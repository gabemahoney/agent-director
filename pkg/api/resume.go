package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// resumeEnvInstanceID is the env-var key resume reads to re-derive
// parent_id (SRD §7.5). Mirrored verbatim from spawn's constant of
// the same name; both refer to the same operational concept.
const resumeEnvInstanceID = "AGENT_DIRECTOR_INSTANCE_ID"

// ResumeStore is the narrow store surface Resume needs (Appendix F.4). The
// parent id is written only by the move to pending: resume makes no other
// parent-id write.
type ResumeStore interface {
	// GetSpawn returns the row, with its snapshot, raw ended_at text and
	// launch identity exactly as stored; ErrSpawnNotFound when no row has
	// the id.
	GetSpawn(instanceID string) (Spawn, error)
	// ListSessionHistory returns the instance's archived sessions of one
	// life only — life is the LifeNumber of the row Resume read — newest
	// first. Session history belongs to a life: each entry is a session that
	// ran while that life's id was current. The read does not apply the
	// current-session rule; Resume does, keeping the visible history (that
	// life's entries minus the entry for the row's current session id).
	// Resume uses the visible history both to try earlier transcripts as
	// resume candidates (b.v2c AC6 — a rotation must not strand history)
	// and to distinguish ErrJsonlNeverWritten from ErrJsonlMissing (AC2).
	ListSessionHistory(instanceID string, life int64) ([]SessionHistoryEntry, error)
	// MoveToPending applies only if the row is ended or missing and its
	// snapshot equals examined (SR-8.3). In one statement it sets state
	// pending; clears pid, proc_starttime, ended_at and both liveness
	// columns; writes launch_started_at (milliseconds), the new launch token,
	// the launch's socket and the parent id ("" = NULL); sets the server and
	// pane identity NULL; and advances row_version. It returns CondApplied
	// with the version it produced, CondChanged when the row no longer meets
	// the condition, CondAbsent when no row has the id (both writing
	// nothing), or a store error (a parent id naming no row included).
	MoveToPending(instanceID string, examined RowSnapshot, launchStartedAtMillis int64, token, socket, parentID string) (res CondResult, movedVersion int64, err error)
	// RestoreAfterFailedResume applies only if the row is pending with
	// row_version equal to movedVersion (SR-8.5). It writes prior back, the
	// launch identity included, sets launch_started_at NULL and advances
	// row_version; the parent id the move wrote stays. It returns
	// CondApplied, CondChanged or CondAbsent (the last two writing nothing),
	// or a store error.
	RestoreAfterFailedResume(instanceID string, movedVersion int64, prior ResumePrior) (CondResult, error)
}

// resumeIdentityWriter is the optional identity write resume makes after a
// labelled create (SR-3.6): the conditional write on the move's version and
// token. It is not part of ResumeStore (Appendix F.4); resume finds it by a
// type assertion on the store it is given, the optional-read pattern Status
// uses (SR-16.1). *store.Store satisfies it; an injected ResumeStore without
// it makes no identity write, and a ResumeStore wrapper must forward
// RecordLaunchIdentity for the identity write to happen.
type resumeIdentityWriter interface {
	RecordLaunchIdentity(instanceID string, launchVersion int64, token string, id LaunchIdentity) (CondResult, error)
}

// Compile-time assertion that *store.Store provides the optional identity
// write, so the production wiring records the launch identity.
var _ resumeIdentityWriter = (*store.Store)(nil)

// SessionHistoryEntry is re-exported from internal/store so external consumers
// can name the type in the ResumeStore interface without importing
// internal/store directly (b.v2c).
type SessionHistoryEntry = store.SessionHistoryEntry

// ResumeTmux is the narrow tmux surface Resume needs (Appendix F.3): the
// session-creating call with its chained labels, the one label by id and the
// kill by id of a session that could not be labelled (SR-3.5), exactly as
// TmuxClient declares them, plus HasSession for the name pre-check. Each
// socket-taking method reports a failure as *TmuxCallError; an error of any
// other type counts as TmuxFailUnrecognized for that call.
type ResumeTmux interface {
	// HasSession reports whether a session whose name begins with name
	// exists (prefix match). It serves only resume's name pre-check, which
	// the pre-launch lookup on the row's socket (SR-8.2) replaces.
	HasSession(name string) (bool, error)
	// NewSession creates the session name on socket with its chained labels,
	// the session label "ad1 <token> <session id> <instance id> <store id>"
	// and the pane label "<token> <pane id>", and returns the create reply.
	NewSession(socket, name, cwd string, envs map[string]string, command []string, token, instanceID, storeID string) (TmuxCreateReply, error)
	// SetLabel labels, in one call, the session sessionID on socket by its id
	// with "ad1 <token> <session id> <instance id> <store id>" and its pane
	// paneID by its id with "<token> <pane id>".
	SetLabel(socket, sessionID, paneID, token, instanceID, storeID string) error
	// KillSessionID kills the session sessionID on socket: a new session that
	// could not be labelled.
	KillSessionID(socket, sessionID string) error
}

// ResumeParams is the typed parameter shape for the resume verb.
type ResumeParams struct {
	// ClaudeInstanceID identifies the terminated Spawn to resurrect.
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// ResumeResult is the typed return shape: the id and what the launch's
// pre-trust did (pre_trust). The id field is the same id the caller passed
// in; resume preserves the instance id across the resurrection (SRD §8.1).
type ResumeResult struct {
	// ClaudeInstanceID is the id of the resurrected Spawn — identical to the
	// value passed in ResumeParams.ClaudeInstanceID.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// PreTrust is what the launch's folder-trust pre-trust did, always one
	// of three values (SR-22.6):
	//   - "ok": the folder-trust entry was written.
	//   - "skipped": pre-trust was off for this launch because the row
	//     records that the spawn that began its life turned it off
	//     (SpawnParams.NoPreTrust); nothing was attempted.
	//   - "failed": pre-trust was attempted and the entry was not written
	//     (the .claude.json file is missing, or could not be read, parsed or
	//     written); the agent may stop at Claude Code's folder-trust prompt.
	//
	// A pre-trust failure never fails the resume.
	PreTrust string `json:"pre_trust"`
}

// resumeDeps is what resume's launch uses besides the row: the handler's
// store, tmux client, start-time reader, configuration, this store's id, clock
// and logger, carried from resumeImpl to resumeAfterJsonl.
type resumeDeps struct {
	s       ResumeStore
	t       ResumeTmux
	pc      ProcChecker
	cfg     config.Config
	storeID string
	now     func() time.Time
	lg      *log.Logger
}

// resumeImpl is the unexported verb handler called by (c *Client).Resume.
// It takes internal types directly and is not part of the public API surface;
// external consumers use the Client method instead. pc is the start-time
// reader the identity write uses, storeID this store's id, which every label
// the launch writes ends with (SR-3.5; never in a description or trail
// field, SR-15), now the clock the launch start is read from, and lg the
// client logger (nil logs nothing).
//
// Guards (in order; each refusal writes nothing and makes no tmux call
// unless stated):
//
//  1. GetSpawn → ErrSpawnNotFound when the id is unknown.
//  2. State must be `ended` or `missing` → otherwise
//     ErrSpawnNotResumable. The verb does NOT touch a live Spawn. A
//     `pending` row is a launch in progress whose agent has not reported in
//     (SR-8.4) and gets the launch-in-progress description: when the launch
//     began (or that no launch start is recorded), that resume applies only
//     to an ended or missing row, and what happens next.
//  3. `claude_session_id` must be populated → otherwise
//     ErrNoSessionId. A Spawn killed before its first SessionStart
//     hook fired has no rotated session id to point --resume at.
//  4. JSONL transcript file must exist on disk → otherwise
//     ErrJsonlMissing. Pure os.Stat pre-flight; no read. Candidate
//     resolution follows a strict precedence (decision of record,
//     bug b.1ba):
//     a. The persisted jsonl_path is tried first, if non-empty. If its
//     os.Stat succeeds it wins outright — no fallback is computed.
//     b. If jsonl_path is NULL/empty OR its os.Stat fails for ANY
//     reason (not just ENOENT — a permission-broken persisted path
//     must not block an otherwise-resumable row), a fallback path is
//     recomputed from the row's ExtraEnv["CLAUDE_CONFIG_DIR"] (or
//     ~/.claude when that key is absent/empty) + slug(cwd) + session
//     id, and that is os.Stat'd. The CLAUDE_CONFIG_DIR value is used
//     ONLY when it is non-empty AND absolute (filepath.IsAbs): an
//     empty string is treated as absent (mirroring the `dir != ""` check
//     of internal/spawn's pre-trust file resolution, claudeJSONFor),
//     and a non-absolute value (relative, `~`-prefixed, or
//     whitespace-only) is ALSO treated as absent — in every such
//     case the fallback resolves to ~/.claude, since a relative dir
//     would stat against the nondeterministic process cwd. This
//     heals legacy rows written before the SessionStart hook
//     persisted jsonl_path, and rows whose recorded path has rotted.
//     A successful fallback resume re-fires SessionStart, which
//     re-persists the correct path.
//     c. Then each entry of the row's visible history, newest first
//     (b.v2c AC6): its recorded path, then its recomputed fallback.
//     Session history belongs to a life, and the visible history is the
//     current life's history minus the entry for the row's current
//     session id, whose own candidates are a and b; a recorded path on
//     that dropped entry is never tried.
//     When every candidate fails, the verb distinguishes two cases, both
//     decided on the visible history: ErrJsonlNeverWritten when the
//     persisted jsonl_path was NULL and the visible history is empty
//     (nothing was ever written — AC2); ErrJsonlMissing otherwise (a path
//     was once recorded/composed and has rotted). Both messages report
//     each path tried with its source (persisted / fallback / history)
//     and its stat error; every one of them is from the current life.
//
// The winning candidate's session id is the one `claude --resume` names;
// the launch itself (resumeAfterJsonl) always works from the row as read.
// After these guards and resumeAfterJsonl's own pre-launch checks, and just
// before the move to pending, resume pre-trusts the row's folder
// (spawn.PreTrust) unless the spawn that began the row's life turned
// pre-trust off (the row's NoPreTrust). A pre-trust failure never fails the
// launch, and a refusal above the move writes no trust entry (SR-8.1 step 4,
// SR-22.6).
func resumeImpl(s ResumeStore, t ResumeTmux, pc ProcChecker, cfg config.Config, storeID string, now func() time.Time, lg *log.Logger, params ResumeParams) (ResumeResult, error) {
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	d := resumeDeps{s: s, t: t, pc: pc, cfg: cfg, storeID: storeID, now: now, lg: lg}

	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return ResumeResult{}, err
	}

	if row.State == store.StatePending {
		return ResumeResult{}, launchInProgressError(row)
	}
	if row.State != store.StateEnded && row.State != store.StateMissing {
		return ResumeResult{}, fmt.Errorf("%w: spawn %s state=%s",
			ErrSpawnNotResumable, params.ClaudeInstanceID, row.State)
	}

	if row.ClaudeSessionID == "" {
		return ResumeResult{}, fmt.Errorf("%w: spawn %s has no claude_session_id",
			ErrNoSessionId, params.ClaudeInstanceID)
	}

	// Resolve the transcript path against a strict precedence (bug b.1ba):
	// the persisted jsonl_path wins if it stats successfully; otherwise a
	// CLAUDE_CONFIG_DIR-aware fallback is recomputed from the row's
	// ExtraEnv and stat'd. The fallback fires on ANY stat failure of the
	// persisted path (not just ENOENT) and on a NULL/empty jsonl_path.
	// See the guard-order comment above for the decision of record.
	var attempts []jsonlAttempt

	if persisted := row.JSONLPath; persisted != "" {
		if _, err := os.Stat(persisted); err == nil {
			// Persisted path exists — it wins outright.
			return resumeAfterJsonl(d, row, row.ClaudeSessionID)
		} else {
			attempts = append(attempts, jsonlAttempt{
				source: "persisted", path: persisted, statErr: err,
			})
		}
	}

	// Fallback: recompute from the persisted CLAUDE_CONFIG_DIR (bug b.1ba),
	// following the pattern of internal/spawn's pre-trust file resolution
	// (claudeJSONFor) — fall back to ~/.claude when the key is absent/empty
	// via spawn.JsonlPath.
	//
	// CLAUDE_CONFIG_DIR value semantics (decision of record, bug b.1ba):
	// the ExtraEnv value is used ONLY if it is non-empty AND absolute.
	// Empty string is treated as absent (mirrors claudeJSONFor's
	// `dir != ""` check in internal/spawn). A non-empty but non-ABSOLUTE value — relative,
	// `~`-prefixed, or whitespace-only (whitespace-only is non-absolute,
	// so the single filepath.IsAbs check covers it) — is ALSO treated as
	// absent, because a relative dir would stat against the process cwd,
	// which is nondeterministic across callers. In every absent case the
	// fallback resolves to ~/.claude via spawn.JsonlPath. This read-path
	// rule is deliberately stricter than pretrust's write path, which is
	// out of scope for b.1ba.
	var fallback string
	var ferr error
	if dir := row.ExtraEnv["CLAUDE_CONFIG_DIR"]; dir != "" && filepath.IsAbs(dir) {
		fallback, ferr = spawn.JsonlPathIn(dir, row.CWD, row.ClaudeSessionID)
	} else {
		fallback, ferr = spawn.JsonlPath(row.CWD, row.ClaudeSessionID)
	}
	if ferr != nil {
		return ResumeResult{}, fmt.Errorf("resume: resolve jsonl: %w", ferr)
	}
	if _, err := os.Stat(fallback); err == nil {
		return resumeAfterJsonl(d, row, row.ClaudeSessionID)
	} else {
		attempts = append(attempts, jsonlAttempt{
			source: "fallback", path: fallback, statErr: err,
		})
	}

	// b.v2c AC6: a session rotation (a new session id reported for the row)
	// archives the prior session's (session id, jsonl_path) into
	// session_history, as an entry of the row's life. If the current session
	// has no live transcript, fall back to the most recent entry of the
	// visible history whose transcript still exists on disk — this recovers
	// history orphaned by a rotation rather than abandoning it. The visible
	// history is the current life's entries (the read below takes the life of
	// the row read above) minus the entry for the row's current session id,
	// whose candidates were already tried above (SR-8.7). Resume does not persist
	// a session-id change: it points the relaunch at the recovered archived
	// session id (resumeAfterJsonl's sessionID, which spawn.ComposeRelaunch
	// puts in the argv), so `claude --resume` reattaches to the recovered
	// transcript. The row's claude_session_id is not rewritten here (the move
	// keeps it); it updates later, when the resumed process's SessionStart
	// hook fires.
	//
	// Each archived candidate gets the same persisted→fallback two-step the
	// current session gets (finding b.5jm/1): the recorded jsonl_path is tried
	// first, and on ANY stat failure of a non-empty path — the b.1ba rot mode —
	// the config-dir-aware path is recomputed and stat'd for that same session
	// id before advancing to the next, older entry. Without this, a newer entry
	// whose recorded path has rotted would be skipped outright and an older
	// entry could win, silently reattaching resume to older history.
	lifeHistory, herr := s.ListSessionHistory(params.ClaudeInstanceID, row.LifeNumber)
	if herr != nil {
		return ResumeResult{}, fmt.Errorf("resume: list session history: %w", herr)
	}
	history := visibleHistory(row, lifeHistory)
	for _, h := range history {
		// Step 1: try the archived recorded path, if any. It wins outright when
		// it stats; on any stat failure fall through to the recomputed path.
		if h.JSONLPath != "" {
			if _, err := os.Stat(h.JSONLPath); err == nil {
				return resumeAfterJsonl(d, row, h.ClaudeSessionID)
			} else {
				attempts = append(attempts, jsonlAttempt{
					source: "history", path: h.JSONLPath, statErr: err,
				})
			}
		}

		// Step 2: recompute the config-dir-aware path for this session id and
		// stat it — the same fallback the current session gets. This heals both
		// a NULL/empty recorded path and a non-empty-but-rotted one.
		var recomputed string
		var cerr error
		if dir := row.ExtraEnv["CLAUDE_CONFIG_DIR"]; dir != "" && filepath.IsAbs(dir) {
			recomputed, cerr = spawn.JsonlPathIn(dir, row.CWD, h.ClaudeSessionID)
		} else {
			recomputed, cerr = spawn.JsonlPath(row.CWD, h.ClaudeSessionID)
		}
		if cerr != nil {
			// Record the recomposition failure as an attempt so the
			// ErrJsonlMissing message still names this history candidate
			// rather than silently omitting it.
			attempts = append(attempts, jsonlAttempt{
				source: "history", path: recomputed, statErr: cerr,
			})
			continue
		}
		// Avoid a duplicate stat + attempt entry when the recomputed path is
		// identical to a recorded path we already tried and recorded above.
		if recomputed == h.JSONLPath {
			continue
		}
		if _, err := os.Stat(recomputed); err == nil {
			return resumeAfterJsonl(d, row, h.ClaudeSessionID)
		} else {
			attempts = append(attempts, jsonlAttempt{
				source: "history", path: recomputed, statErr: err,
			})
		}
	}

	// AC2: distinguish "no transcript has EVER been written for this session"
	// from "candidates were tried and none matched". The never-written case is
	// narrow and specific: the persisted jsonl_path was NULL/empty (the
	// SessionStart hook found no file), AND the visible history — the current
	// life's entries minus the entry for the row's current session id — is
	// empty, so there is nothing to have lost. A persisted-but-rotted path, or
	// a row whose visible history is non-empty, is the classic ErrJsonlMissing.
	if row.JSONLPath == "" && len(history) == 0 {
		return ResumeResult{}, fmt.Errorf(
			"%w: spawn %s session %s has produced no transcript on disk (tried %s)",
			ErrJsonlNeverWritten, params.ClaudeInstanceID, row.ClaudeSessionID,
			formatJsonlAttempts(attempts))
	}

	return ResumeResult{}, fmt.Errorf("%w: %s", ErrJsonlMissing, formatJsonlAttempts(attempts))
}

// jsonlAttempt records one candidate transcript path resume tried to
// stat, its provenance (persisted jsonl_path vs CLAUDE_CONFIG_DIR-aware
// fallback), and the stat error that ruled it out. Collected so the
// ErrJsonlMissing message can name every path tried (bug b.1ba scope 2).
type jsonlAttempt struct {
	source  string // "persisted" | "fallback" | "history"
	path    string
	statErr error
}

// formatJsonlAttempts renders every failed candidate for the
// ErrJsonlMissing message, each as `<source> <path> (<stat error>)`,
// joined by "; ". Callers map ErrJsonlMissing by name (unchanged); this
// is the human/log detail that tells them which paths were tried.
func formatJsonlAttempts(attempts []jsonlAttempt) string {
	parts := make([]string, 0, len(attempts))
	for _, a := range attempts {
		parts = append(parts, fmt.Sprintf("%s %s (%v)", a.source, a.path, a.statErr))
	}
	return strings.Join(parts, "; ")
}

// resumeAfterJsonl launches the resume once a transcript has been found:
// row is the row exactly as resumeImpl read it, never re-read and never
// altered, and sessionID the session `claude --resume` names (the row's own,
// or an earlier session of the visible history whose transcript still
// exists). In order (SR-8.1, SR-8.3, SR-8.5):
//
//  1. An instance id containing a control character → ErrInternal (SR-3.13):
//     its session could never be labelled. No tmux call, nothing written.
//  2. The launch's socket (spawn.ResolveRowLaunchSocket): the row's recorded
//     socket, its vanished per-user directory re-created, or, when the row
//     records none, the one a plain spawn would resolve. A refusal →
//     ErrTmuxNotAvailable, no tmux call, nothing written.
//  3. The name pre-check (HasSession): a session with the recorded name →
//     ErrTmuxSessionCreate, nothing written. Resume does NOT auto-kill a
//     stale session. The pre-launch lookup (SR-8.2) replaces this check.
//  4. A new launch token (a failure → ErrInternal, nothing written), then
//     the environment, settings and argv (spawn.ComposeRelaunch), all before
//     the move, so a failure writes nothing.
//  5. Pre-trust (spawn.PreTrust, SR-8.1 step 4, SR-22.6): after every check
//     above that can refuse without a write and immediately before the move,
//     for the row's cwd and extra env (so CLAUDE_CONFIG_DIR resolves the file
//     a spawn of the row used), off when the row records the opt-out of the
//     spawn that began its life (row.NoPreTrust, which the move and the
//     restore keep, so a retry after a restored failure follows it too). Best
//     effort: its outcome never changes resume's control flow or error, and a
//     failure prints the "pre-trust failed" line and the launch proceeds.
//     The outcome is carried to resumeLaunchOutcome, which reports it as
//     ResumeResult.PreTrust on success.
//  6. The move to pending (MoveToPending): one conditional write with the
//     snapshot of the row as read, the launch start from one read of now in
//     milliseconds, the token, the socket and the parent id re-derived from
//     the caller's AGENT_DIRECTOR_INSTANCE_ID ("" = NULL); the only parent-id
//     write. Row changed → ErrSpawnNotResumable; row removed →
//     ErrSpawnNotFound; store error → ErrInternal. Each writes nothing and
//     launches nothing.
//  7. The create (spawn.Relaunch), the first step after the move, with no
//     store, file or network I/O between them: the recorded name on the
//     launch's socket, labelled "ad1 <token> <session id> <instance id>
//     <store id>". Once it returns, ad.resume.moved_to_pending is emitted.
//  8. Its outcome (SR-8.5), mapped in resumeLaunchOutcome: a labelled
//     session → the identity write with the move's version and token (when
//     the store provides it), success; a lost reply → success with no
//     identity; a timeout or a non-zero-exit unparseable reply →
//     ErrTmuxUnresponsive, nothing written, the row stays pending. Every
//     other failure (tmux unavailable, "duplicate session", a session that
//     could not be labelled, any other launch failure) → the restore, then
//     the launch error.
//
// On success the row stays pending until the resumed agent's first
// SessionStart hook moves it and rotates its claude_session_id.
func resumeAfterJsonl(d resumeDeps, row Spawn, sessionID string) (ResumeResult, error) {
	id := row.ClaudeInstanceID
	if hasControlChar(id) {
		return ResumeResult{}, errors.New(`resume: the instance id contains a control character, so its session cannot be labelled; removing the row is a human's decision, see "Operator actions" in the agent-director README; no tmux call was made and nothing was written`)
	}

	socket, err := spawn.ResolveRowLaunchSocket(row.Identity.Socket)
	if err != nil {
		return ResumeResult{}, err
	}

	exists, err := d.t.HasSession(row.TmuxSessionName)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("resume: probe tmux: %w", err)
	}
	if exists {
		return ResumeResult{}, fmt.Errorf("%w: tmux session %s already exists",
			tmux.ErrTmuxSessionCreate, row.TmuxSessionName)
	}

	token, err := spawn.NewLaunchToken()
	if err != nil {
		return ResumeResult{}, fmt.Errorf("resume of instance %s: the launch could not be recorded (%v), so nothing was written and nothing was launched", id, err)
	}
	req, err := spawn.ComposeRelaunch(spawn.RelaunchInput{
		Row:       row,
		SessionID: sessionID,
		Socket:    socket,
		Token:     token,
		StoreID:   d.storeID,
	}, d.cfg)
	if err != nil {
		return ResumeResult{}, err
	}

	// A failure never fails the launch; the outcome goes into the result.
	preTrust := spawn.PreTrust(row.CWD, row.ExtraEnv, row.NoPreTrust)

	// What the move clears, from the row as read: the restore writes it back
	// byte for byte (SR-8.5).
	prior := ResumePrior{
		State:                   row.State,
		EndedAtText:             row.EndedAtText,
		PID:                     row.PID,
		ProcStarttime:           row.ProcStarttime,
		LivenessUnverifiedSince: row.LivenessUnverifiedSince,
		LivenessNote:            row.LivenessNote,
		Identity:                row.Identity,
	}
	parent := os.Getenv(resumeEnvInstanceID)
	res, movedVersion, err := d.s.MoveToPending(id, row.Snapshot, d.now().UnixMilli(), token, socket, parent)
	if err := resumeMoveError(id, res, err); err != nil {
		return ResumeResult{}, err
	}

	out := spawn.Relaunch(d.t, req)
	_ = trail.Emit(context.Background(), "ad.resume.moved_to_pending", map[string]any{
		"claude_instance_id": id,
		"prior_state":        row.State,
		"claude_session_id":  row.ClaudeSessionID,
		"source":             "ad_resume",
	})
	return resumeLaunchOutcome(d, out, req, movedVersion, prior, preTrust)
}

// resumeMoveError maps the move to pending's outcome to resume's error
// (SR-8.3, SR-5.8), nil when it applied. Not applied, and a store error,
// wrote nothing and launched nothing. The store error's text is kept but not
// wrapped, so the ErrInternal matches no catalogued sentinel (SR-1.5).
func resumeMoveError(id string, res CondResult, err error) error {
	switch {
	case err != nil:
		return fmt.Errorf("resume of instance %s: recording the launch failed and nothing was launched: %v", id, err)
	case res == CondApplied:
		return nil
	case res == CondAbsent:
		return fmt.Errorf("%w: spawn %s was removed after resume examined it; nothing was written and nothing was launched",
			ErrSpawnNotFound, id)
	}
	return fmt.Errorf("%w: spawn %s: the row changed after resume examined it and nothing was written; nothing was launched",
		ErrSpawnNotResumable, id)
}

// resumeLaunchOutcome maps resume's create-and-label outcome to its result
// (SR-8.5), the one place this mapping lives (SR-1.8), using internal/spawn's
// shared description builders. A labelled session gets the identity write
// with the move's version and token when the store provides it; a lost reply
// is a success with no identity; both successes report preTrust, the outcome
// of the pre-trust resumeAfterJsonl ran, as PreTrust. A timeout or a non-zero-exit unparseable
// reply is ErrTmuxUnresponsive with the launch-timeout description and no
// write, the row staying pending. Every other outcome restores the row
// (resumeRestore) and returns its launch error, whose row sentence is the
// restore's result: ErrTmuxNotAvailable for tmux unavailable, and
// ErrTmuxSessionCreate for a session that could not be labelled (after its
// kill by id, or saying it may still run), for "duplicate session" (whose
// re-lookup comes with the pre-launch lookup) and for any other launch
// failure. Every error matches exactly one catalogued sentinel (SR-1.5).
func resumeLaunchOutcome(d resumeDeps, out spawn.CreateOutcome, req spawn.CreateRequest, movedVersion int64, prior ResumePrior, preTrust spawn.PreTrustOutcome) (ResumeResult, error) {
	switch out.Kind {
	case spawn.CreateLabelled:
		if w, ok := d.s.(resumeIdentityWriter); ok {
			spawn.RecordLaunchIdentity(w, d.pc, d.lg, req.InstanceID, movedVersion, req.Token, out.Reply)
		}
		return ResumeResult{ClaudeInstanceID: req.InstanceID, PreTrust: string(preTrust)}, nil
	case spawn.CreateLostReply:
		return ResumeResult{ClaudeInstanceID: req.InstanceID, PreTrust: string(preTrust)}, nil
	case spawn.CreateUnresponsive:
		return ResumeResult{}, spawn.LaunchTimeoutError(out.Cause, "resume", req.InstanceID, spawn.RowStaysPending)
	}

	return ResumeResult{}, resumeRestore(d, req.InstanceID, movedVersion, prior, func(restored string) error {
		switch out.Kind {
		case spawn.CreateUnavailable:
			return spawn.TmuxUnavailableError(out.Cause, req.Socket, restored)
		case spawn.CreateUnlabelledEnded, spawn.CreateUnlabelledRunning:
			return spawn.UnlabelledSessionError(out, req.Name, restored)
		}
		// CreateDuplicate and CreateFailed.
		return spawn.CreateFailedError(out.Cause, req.Name, restored)
	})
}

// resumeRestore makes the one restore attempt after a failed launch (SR-8.5):
// RestoreAfterFailedResume with the move's version and the prior values of
// the row as read. It returns the launch error launchErr builds from the
// restore's row sentence: restored to the prior state; left as it is because
// the row changed or was removed (nothing written); or, on a store error,
// that the row could not be restored and stays pending, with one WARN line on
// the client logger naming the instance id (no token, label or environment
// value). It then emits ad.resume.restored, fail-open.
func resumeRestore(d resumeDeps, id string, movedVersion int64, prior ResumePrior, launchErr func(restored string) error) error {
	res, rerr := d.s.RestoreAfterFailedResume(id, movedVersion, prior)
	var restored string
	switch {
	case rerr != nil:
		d.lg.Printf("WARN: resume: restoring instance %s to its prior state after a failed launch failed: %v", id, rerr)
		restored = "the row could not be restored and stays pending"
	case res == CondApplied:
		restored = "the row was restored to its prior state, " + prior.State
	case res == CondAbsent:
		restored = "the row was removed after resume moved it to pending, so nothing was restored"
	default:
		restored = "the row changed after resume moved it to pending and was left as it is"
	}
	err := launchErr(restored)

	var restoreError any
	if rerr != nil {
		restoreError = rerr.Error()
	}
	_ = trail.Emit(context.Background(), "ad.resume.restored", map[string]any{
		"claude_instance_id": id,
		"applied":            rerr == nil && res == CondApplied,
		"launch_error":       resumeLaunchErrorName(err),
		"restore_error":      restoreError,
		"source":             "ad_resume",
	})
	return err
}

// resumeLaunchErrorName names a resume launch error as errnames.Classify
// would, for ad.resume.restored's launch_error (SR-14); pkg/api cannot import
// pkg/api/errnames. It lists every name a launch followed by a restore can
// return, and is the one place to extend when a launch gains another.
func resumeLaunchErrorName(err error) string {
	switch {
	case errors.Is(err, tmux.ErrTmuxNotAvailable):
		return "ErrTmuxNotAvailable"
	case errors.Is(err, tmux.ErrTmuxSessionCreate):
		return "ErrTmuxSessionCreate"
	case errors.Is(err, tmux.ErrTmuxSessionConflict):
		return "ErrTmuxSessionConflict"
	case errors.Is(err, tmux.ErrTmuxUnresponsive):
		return "ErrTmuxUnresponsive"
	}
	return "ErrInternal"
}

// launchInProgressError is resume's refusal of a pending row (SR-8.4,
// SR-1.4): a launch in progress whose agent has not reported in. The launch
// start is the row's decoded LaunchStartedAtMillis (0 = none recorded),
// formatted as RFC3339 UTC exactly as get, status and list show
// launch_started_at. It names no session-ending command.
func launchInProgressError(row Spawn) error {
	began := "no launch start is recorded"
	if at := launchStartedAt(row.State, row.LaunchStartedAtMillis); at != nil {
		began = "a launch of this row began at " + at.Format(time.RFC3339Nano)
	}
	return fmt.Errorf("%w: spawn %s is pending: %s and its agent has not reported in; resume applies only to an ended or missing row; if the agent reports in, the row becomes live, and if the launch was abandoned or failed, find-missing marks the row missing once the pending grace period has passed since its launch start; nothing was written",
		ErrSpawnNotResumable, row.ClaudeInstanceID, began)
}

// Resume brings a finished (ended/missing) Spawn back to life by launching
// `claude --resume` in a fresh tmux session pointed at the same JSONL
// transcript. The claude_instance_id is preserved; the result returns it and
// pre_trust (what the launch's pre-trust did: ok, skipped or failed).
//
// Before its launch, Resume pre-trusts the row's working directory (marks it
// trusted in the .claude.json file of the row's CLAUDE_CONFIG_DIR, or
// ~/.claude.json) so the agent skips Claude Code's folder-trust prompt, as a
// spawn does, unless the spawn that began the row's life turned pre-trust off
// (SpawnParams.NoPreTrust); then nothing is pre-trusted, on every resume of
// that life, and pre_trust is skipped. A pre-trust failure never fails the
// launch. A resume refused
// before its move to pending writes no trust entry.
//
// Before it creates the session, Resume moves the row to pending in one
// conditional write, keeping its session id and history and writing the
// parent id (the caller's AGENT_DIRECTOR_INSTANCE_ID). The row stays pending
// until the agent reports in (Claude Code's SessionStart), then becomes
// waiting. If the launch fails other than by timing out, Resume restores the
// row to its prior ended or missing state; if the restore cannot be applied,
// the error says so. The session-creating call is bounded by the create
// timeout. If it times out, Resume returns ErrTmuxUnresponsive (UNAVAILABLE,
// transient): the session may have been created and the row stays pending;
// do not retry until get shows the row ended or missing, since a retried
// resume of the pending row is refused and changes nothing.
//
// CLI: agent-director resume
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id, or the row was
//     removed during the resume (nothing launched).
//   - [ErrSpawnNotResumable]: state is not ended or missing (a live Spawn
//     must be killed or paused before it can be resumed); a pending row is a
//     launch in progress whose agent has not reported in. Also a lost race:
//     the row changed after resume examined it, and nothing was written.
//   - [ErrNoSessionId]: claude_session_id is empty — the Spawn was killed
//     before its first SessionStart hook; delete and re-spawn instead.
//   - [ErrJsonlMissing]: no candidate JSONL transcript exists on disk —
//     neither the persisted jsonl_path, the CLAUDE_CONFIG_DIR-aware
//     fallback, nor any transcript of the visible history (the message
//     names every path tried and its source, all from the current life).
//     Meaning: history existed but the file is gone. Session history
//     belongs to a life; the visible history is the current life's
//     history minus the entry for the row's current session id.
//   - [ErrJsonlNeverWritten]: the row has a session id but no transcript was
//     ever written (persisted jsonl_path NULL and the visible history
//     empty) — the b.v2c case of a freshly restarted agent that has not
//     been messaged. Recourse: message it, or delete + re-spawn.
//   - ErrTmuxNotAvailable: the tmux binary cannot be run, or the tmux socket
//     is not accessible to this user (at session creation, followed by the
//     restore); or the socket's directory is unusable, before anything is
//     written (nothing launched).
//   - [ErrTmuxSessionCreate]: a tmux session with the same name already
//     exists; or session creation failed ("duplicate session" included), or
//     a created session could not be labelled, each followed by the restore.
//   - ErrTmuxUnresponsive: the session-creating call timed out or gave a
//     reply that does not parse with a non-zero exit: the session may have
//     been created and the row stays pending; do not retry until get shows
//     the row ended or missing.
//
// Nondeterminism: none.
func (c *Client) Resume(params ResumeParams) (ResumeResult, error) {
	if err := c.checkClosed(); err != nil {
		return ResumeResult{}, err
	}
	return resumeImpl(c.st, c.tmuxClient, c.procChecker, c.cfg, c.st.StoreID(), c.now, c.logger, params)
}
