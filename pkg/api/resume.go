package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// resumeRestoredEvent is the trail event of each restore attempt after a
// failed resume launch (SR-8.5, SR-14; finishedLaunch.restore).
const resumeRestoredEvent = "ad.resume.restored"

// ResumeStore is the narrow store surface Resume needs (Appendix F.4). The
// parent id is written only by the move to pending: resume makes no other
// parent-id write.
type ResumeStore interface {
	// GetSpawn returns the row, with its snapshot, raw ended_at text and
	// launch identity exactly as stored; ErrSpawnNotFound when no row has
	// the id. Resume reads the row once at entry and, only when its
	// pre-launch check refuses a Leftover, once more to compare snapshots
	// (resumeLostRace).
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
	// A reuse starts a new life, so after a reuse no earlier life's entry is
	// read and the earlier conversation cannot be resumed; a failed reuse's
	// restore returns the row to its pre-reuse life, whose history counts
	// again.
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
// lookup of its pre-launch check (SR-8.2) and of its re-lookup after
// "duplicate session" (SR-8.5), the session-creating call with its
// chained labels, the one label by id and the kill by id of a session that
// could not be labelled (SR-3.5), exactly as TmuxClient declares them. It has
// no pane listing, because resume writes no adoption (SR-3.6), and no
// name-based method. Every method takes a socket and reports a failure as
// *TmuxCallError; an error of any other type counts as TmuxFailUnrecognized
// for that call. TmuxClient, *tmux.Client and tmuxfix.Recorder satisfy it.
type ResumeTmux interface {
	TmuxLookup
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

// The production tmux client satisfies ResumeTmux.
var _ ResumeTmux = TmuxClient(nil)

// ResumeParams is the typed parameter shape for the resume verb.
type ResumeParams struct {
	// ClaudeInstanceID identifies the finished (ended or missing) row to resume.
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// ResumeResult is the typed return shape: the id and what the launch's
// pre-trust did (pre_trust). The id field is the same id the caller passed
// in; resume preserves the instance id (SRD §8.1).
type ResumeResult struct {
	// ClaudeInstanceID is the id of the resumed row — identical to the
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
// and logger, and the caller identity collected at entry, carried from
// resumeImpl to resumeAfterJsonl.
type resumeDeps struct {
	s       ResumeStore
	t       ResumeTmux
	pc      ProcChecker
	cfg     config.Config
	storeID string
	now     func() time.Time
	lg      *log.Logger
	who     caller
}

// launchOnto returns resume's launch after its move applied (finishedLaunch,
// shared with reuse): row is the row exactly as read, never re-read (its
// ended_at, pid and session-id presence, launch token and server identity,
// and the prior values the restore writes back); disagreeWritten the
// ad.provenance.disagree reasons the pre-launch check already wrote, so the
// re-lookup after "duplicate session" writes no reason twice; movedVersion
// the version the move produced. The identity write is the store's when it
// provides one (resumeIdentityWriter); the restore is
// RestoreAfterFailedResume with movedVersion and the row's prior values
// (resumePriorOf).
func (d resumeDeps) launchOnto(row Spawn, disagreeWritten []string, movedVersion int64) finishedLaunch {
	var identity spawn.IdentityWriter
	if w, ok := d.s.(resumeIdentityWriter); ok {
		identity = w
	}
	return finishedLaunch{
		v:        resumeLaunchVerb,
		t:        d.t,
		pc:       d.pc,
		cfg:      d.cfg,
		storeID:  d.storeID,
		now:      d.now,
		lg:       d.lg,
		who:      d.who,
		identity: identity,
		restoreWrite: func() (CondResult, error) {
			return d.s.RestoreAfterFailedResume(row.ClaudeInstanceID, movedVersion, resumePriorOf(row))
		},
		row:             row,
		disagreeWritten: disagreeWritten,
		version:         movedVersion,
	}
}

// resumeImpl is the unexported verb handler called by (c *Client).Resume.
// It takes internal types directly and is not part of the public API surface;
// external consumers use the Client method instead. pc is the start-time
// reader the identity write uses, storeID this store's id, which every label
// the launch writes ends with (SR-3.5; never in a description or trail
// field, SR-15), now the clock the launch start and the pre-launch check's
// starting-session rule read, and lg the client logger (nil logs nothing).
// The caller identity of the trail records is collected once, here.
//
// Guards (SR-8.1 step 1, in order; each refusal writes nothing and makes no
// tmux call):
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
//     ONLY when it is usable (spawn.ConfigDirUsable: non-empty AND
//     absolute), the one rule pre-trust's file resolution shares: an
//     empty string is treated as absent, and a non-absolute value
//     (relative, `~`-prefixed, or whitespace-only) is ALSO treated as
//     absent — in every such case the fallback resolves to ~/.claude,
//     since a relative dir would stat against the nondeterministic
//     process cwd. This heals legacy rows written before the
//     SessionStart hook persisted jsonl_path, and rows whose recorded
//     path has rotted. A successful fallback resume re-fires
//     SessionStart, which re-persists the correct path.
//     c. Then each entry of the row's visible history, newest first
//     (b.v2c AC6): its recorded path, then its recomputed fallback.
//     Session history belongs to a life, and the visible history is the
//     current life's history minus the entry for the row's current
//     session id, whose own candidates are a and b; a recorded path on
//     that dropped entry is never tried. After a reuse, which starts a new
//     life, no earlier life's transcript is a candidate.
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
// After these guards come resumeAfterJsonl's steps (SR-8.1 steps 2 to 7):
// the unusable recorded-name refusal (SR-3.2), then the control-character id
// refusal, the launch socket, the one pre-launch
// lookup on it (SR-8.2), pre-trust, the move to pending, the launch and,
// after a failed launch, the restore (after "duplicate session", preceded by
// one re-lookup that classifies the name's holder against the row as
// examined, and followed by one ad.launch.name_held). Every refusal before
// the move writes nothing: no move, no parent id, no trust entry, no
// ad.resume.* event.
func resumeImpl(s ResumeStore, t ResumeTmux, pc ProcChecker, cfg config.Config, storeID string, now func() time.Time, lg *log.Logger, params ResumeParams) (ResumeResult, error) {
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	d := resumeDeps{s: s, t: t, pc: pc, cfg: cfg, storeID: storeID, now: now, lg: lg, who: callerIdentity()}

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

	// Fallback: recompute from the persisted CLAUDE_CONFIG_DIR (bug b.1ba)
	// — fall back to ~/.claude via spawn.JsonlPath when the value is not
	// usable.
	//
	// CLAUDE_CONFIG_DIR value semantics (decision of record, bugs b.1ba and
	// b.nje): the ExtraEnv value is used ONLY if spawn.ConfigDirUsable
	// holds (non-empty AND absolute), the one rule internal/spawn's
	// pre-trust file resolution (claudeJSONFor) also follows. Empty string
	// is treated as absent. A non-empty but non-ABSOLUTE value — relative,
	// `~`-prefixed, or whitespace-only — is ALSO treated as absent, because
	// a relative dir would stat against the process cwd, which is
	// nondeterministic across callers. In every absent case the fallback
	// resolves to ~/.claude via spawn.JsonlPath. Only what each does with
	// an unusable value differs: this read path falls back, while
	// pre-trust, which writes, refuses it and writes nothing.
	var fallback string
	var ferr error
	if dir := row.ExtraEnv["CLAUDE_CONFIG_DIR"]; spawn.ConfigDirUsable(dir) {
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
		if dir := row.ExtraEnv["CLAUDE_CONFIG_DIR"]; spawn.ConfigDirUsable(dir) {
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
//  1. SR-8.1 step 2, the recorded name first, then the id: a recorded name
//     that is unusable (SR-3.2) → unusableNameError's ErrInternal, wrapped
//     with "resume: " so an id is never printed; then an instance id
//     containing a control character → ErrInternal (SR-3.13): its session
//     could never be labelled. No tmux call, nothing written.
//  2. The launch's socket (spawn.ResolveRowLaunchSocket): the row's recorded
//     socket, its vanished per-user directory re-created, or, when the row
//     records none, the one a plain spawn would resolve. A refusal →
//     ErrTmuxNotAvailable, no tmux call, nothing written.
//  3. The pre-launch check (SR-8.2): exactly one tmux.Lookup on that socket
//     for the row's launch identity as read (instance id, launch token,
//     recorded server identity, this store's id) with the recorded name as
//     the holder name, decided by decidePreLaunch: proceed, or a refusal
//     (Can't tell by its kind; Ours, or Gone while the agent process runs, by
//     the starting-session rule with the configured bound and window;
//     Leftover; a name held on Gone, by the holder's class). Its
//     ad.provenance.disagree records, one per distinct reason, are written
//     right after the decision, before any write, fail-open. A refusal makes
//     no further tmux call and writes nothing else; no blocking session is
//     touched, and nothing is adopted. A Leftover refusal alone re-reads the
//     row once (resumeLostRace): if its snapshot changed since it was read
//     (a competing resume moved it), the move's lost-race
//     ErrSpawnNotResumable is returned instead, and if the row was removed,
//     the move's ErrSpawnNotFound (SR-8.6).
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
//     The outcome is reported as ResumeResult.PreTrust on success.
//  6. The move to pending (MoveToPending): one conditional write with the
//     snapshot of the row as read, the launch start from one read of now in
//     milliseconds, the token, the socket and the parent id re-derived from
//     the caller's AGENT_DIRECTOR_INSTANCE_ID (spawn.ParentIDFromEnv, "" =
//     NULL); the only parent-id write. Row changed → ErrSpawnNotResumable; row removed →
//     ErrSpawnNotFound; store error → ErrInternal. Each writes nothing and
//     launches nothing.
//  7. The create (spawn.Relaunch), the first step after the move, with no
//     store, file or network I/O between them: the recorded name on the
//     launch's socket, labelled "ad1 <token> <session id> <instance id>
//     <store id>". Once it returns, ad.resume.moved_to_pending is emitted.
//  8. Its outcome (SR-8.5), mapped by finishedLaunch.outcome, which reuse
//     shares (resumeDeps.launchOnto, verb values resumeLaunchVerb): a labelled
//     session → the identity write with the move's version and token (when
//     the store provides it), success; a lost reply → success with no
//     identity; a timeout or a non-zero-exit unparseable reply →
//     ErrTmuxUnresponsive, nothing written, the row stays pending.
//     "duplicate session" → finishedLaunch.heldName: exactly one re-lookup of the
//     recorded name on the launch socket for the row as examined (its id,
//     earlier token and recorded server identity), the restore, the holder's
//     classified error carrying the restore's sentence (the holder class; the
//     row's own session by the starting-session rule with the examined
//     ended_at; ErrTmuxSessionCreate only when no session holds the name any
//     more), the re-lookup's disagree reasons not already written, and one
//     ad.launch.name_held. Every other failure (tmux unavailable, a session
//     that could not be labelled, any other launch failure) → the restore,
//     then the launch error.
//
// On success the row stays pending until the resumed agent's first
// SessionStart hook moves it and rotates its claude_session_id.
func resumeAfterJsonl(d resumeDeps, row Spawn, sessionID string) (ResumeResult, error) {
	if err := unusableNameError(row.TmuxSessionName); err != nil {
		return ResumeResult{}, fmt.Errorf("resume: %w", err)
	}
	id := row.ClaudeInstanceID
	if hasControlChar(id) {
		return ResumeResult{}, errors.New(`resume: the instance id contains a control character, so its session cannot be labelled; removing the row is a human's decision, see "Operator actions" in the agent-director README; no tmux call was made and nothing was written`)
	}

	socket, err := spawn.ResolveRowLaunchSocket(row.Identity.Socket)
	if err != nil {
		return ResumeResult{}, err
	}

	lookup := tmux.Lookup(d.t, d.pc, rowLaunch(id, row.Identity, d.storeID, socket), row.TmuxSessionName)
	pre := decidePreLaunch(lookup, preLaunchRowOf(row, row.TmuxSessionName, socket), d.pc,
		startingSessionLimitsOf(d.cfg.Tmux), d.now)
	emitFinishedRowDisagree(resumeLaunchVerb, row, socket, d.who, preLaunchActionOf(pre.Err), pre)
	if pre.Err != nil {
		if lookup.Verdict == tmux.Leftover {
			if lost := resumeLostRace(d.s, row); lost != nil {
				return ResumeResult{}, lost
			}
		}
		return ResumeResult{}, pre.Err
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

	parent := spawn.ParentIDFromEnv()
	res, movedVersion, err := d.s.MoveToPending(id, row.Snapshot, d.now().UnixMilli(), token, socket, parent)
	if err := resumeMoveError(id, res, err); err != nil {
		return ResumeResult{}, err
	}

	out := spawn.Relaunch(d.t, req)
	_ = trail.Emit(context.Background(), "ad.resume.moved_to_pending", map[string]any{
		"claude_instance_id": id,
		"prior_state":        row.State,
		"claude_session_id":  row.ClaudeSessionID,
		"source":             nameHeldSourceResume,
	})
	if err := d.launchOnto(row, pre.Reasons, movedVersion).outcome(out, req); err != nil {
		return ResumeResult{}, err
	}
	return ResumeResult{ClaudeInstanceID: id, PreTrust: string(preTrust)}, nil
}

// resumePriorOf returns what the move to pending clears, from the row as
// read: the restore writes it back byte for byte (SR-8.5).
func resumePriorOf(row Spawn) ResumePrior {
	return ResumePrior{
		State:                   row.State,
		EndedAtText:             row.EndedAtText,
		PID:                     row.PID,
		ProcStarttime:           row.ProcStarttime,
		LivenessUnverifiedSince: row.LivenessUnverifiedSince,
		LivenessNote:            row.LivenessNote,
		Identity:                row.Identity,
	}
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

// resumeLostRace is the one re-read after the pre-launch check refused a
// Leftover (SR-8.6, AC-RES-09): a competing resume whose move followed this
// call's read leaves a session whose label carries a token this call did not
// examine, which the lookup sees as left over. One GetSpawn and no write,
// decided by leftoverLostRace (which reuse shares) and mapped as the move
// would map its outcome (resumeMoveError): the row removed (ErrSpawnNotFound)
// gives the move's ErrSpawnNotFound; a snapshot that is no longer the one row
// holds gives the move's lost-race ErrSpawnNotResumable; an unchanged
// snapshot, or a re-read that fails otherwise, gives nil, and the Leftover
// refusal stands. Nothing was written either way.
func resumeLostRace(s ResumeStore, row Spawn) error {
	again, err := s.GetSpawn(row.ClaudeInstanceID)
	found := true
	if errors.Is(err, ErrSpawnNotFound) {
		found, err = false, nil
	}
	if res := leftoverLostRace(row.Snapshot, again.Snapshot, found, err); res != 0 {
		return resumeMoveError(row.ClaudeInstanceID, res, nil)
	}
	return nil
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

// Resume relaunches a finished (ended/missing) row by launching
// `claude --resume` in a fresh tmux session pointed at the same JSONL
// transcript. The claude_instance_id is preserved; the result returns it and
// pre_trust (what the launch's pre-trust did: ok, skipped or failed). A
// finished row is not proof that its agent is dead: missing is the sweep's
// judgement on the evidence available to it, not proof that the agent has
// exited.
//
// Before anything is written, Resume looks the row up once on its recorded
// tmux socket (SR-8.2) and refuses when a session is in the way, touching no
// session: the row's own session, or its agent process still running with
// no session of its launch, gives ErrTmuxUnresponsive (UNAVAILABLE,
// transient) while it appears to still be stopping (the row ended less than
// the stopping window ago) or starting (younger than the starting-session
// bound), and past both ErrTmuxSessionConflict ("this row's own id"); a
// session left over from an earlier life, a session holding the recorded
// name (another row's, another agent-director store's, which must not be
// ended, or one with no valid label) and conflicting labels give
// ErrTmuxSessionConflict. ErrTmuxSessionConflict is CONFLICT and lasts until
// a human looks (see "Operator actions" in the agent-director README). A
// tmux server other than the one the agent was launched on, or tmux that
// cannot be run, gives ErrTmuxNotAvailable (ENVIRONMENT), and an answer that
// cannot be read ErrTmuxUnresponsive. A refusal before the move to pending
// writes nothing, so re-issuing resume later is safe. If a resume that read
// the row before a competing resume's move finds that resume's session left
// over, and the row has changed since it was read, it returns
// ErrSpawnNotResumable instead (ErrSpawnNotFound if the row was removed), as
// its move would have. None of these errors
// means that the agent is dead.
//
// If the session-creating call answers "duplicate session" (a session took
// the recorded name after the lookup), Resume looks the name up once more on
// the launch socket, judges its holder against the row as it was read before
// the move, exactly as the pre-launch check does (the row's own session by
// the starting-session rule with the ended_at it read), restores the row, and
// returns the holder's classified error, whose description says what the
// restore did instead of "nothing was done". The holding session is never
// touched. Only a holder that vanished before that lookup gives
// ErrTmuxSessionCreate.
//
// Before its launch, Resume pre-trusts the row's working directory (marks it
// trusted in the .claude.json file of the row's CLAUDE_CONFIG_DIR, or
// ~/.claude.json) so the agent skips Claude Code's folder-trust prompt, as a
// spawn does, unless the spawn that began the row's life turned pre-trust off
// (SpawnParams.NoPreTrust); then nothing is pre-trusted, on every resume of
// that life, and pre_trust is skipped. A pre-trust failure never fails the
// launch. A resume refused before its move to pending writes no trust entry.
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
// A finished row whose recorded tmux session name cannot be used (it is
// empty, contains a control character, or contains a character tmux stores
// differently) gets ErrInternal (an error matching no catalogued sentinel)
// with no tmux call, and nothing is written; removing the row is a human's
// decision (see "Operator actions" in the agent-director README).
//
// CLI: agent-director resume
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id, or the row was
//     removed during the resume (found by the move, or by the one re-read
//     after the pre-launch check found a left-over session; nothing
//     launched).
//   - [ErrSpawnNotResumable]: state is not ended or missing (a live Spawn
//     must be paused, or killed and then marked by find-missing, before it
//     can be resumed: follow the live-row sequence in kill's description); a
//     pending row is a launch in progress whose agent has not reported in.
//     Also a lost race: the row changed after resume examined it (found by
//     the move, or by the one re-read after the pre-launch check found a
//     left-over session), and nothing was written.
//   - [ErrNoSessionId]: claude_session_id is empty — the Spawn was killed
//     before its first SessionStart hook. Recourse: spawn again with the
//     same id, opting in to reuse (SpawnParams.ReuseFinished); the reused id
//     starts a new life with no memory of the earlier one.
//   - [ErrJsonlMissing]: no candidate JSONL transcript exists on disk —
//     neither the persisted jsonl_path, the CLAUDE_CONFIG_DIR-aware
//     fallback, nor any transcript of the visible history (the message
//     names every path tried and its source, all from the current life).
//     Meaning: history existed but the file is gone. Session history
//     belongs to a life; the visible history is the current life's
//     history minus the entry for the row's current session id. Recourse:
//     spawn again with the same id, opting in to reuse
//     (SpawnParams.ReuseFinished); the reused id starts a new life with no
//     memory of the earlier one, so the earlier conversation cannot be
//     resumed through agent-director afterwards.
//   - [ErrJsonlNeverWritten]: the row has a session id but no transcript was
//     ever written (persisted jsonl_path NULL and the visible history,
//     the current life's, empty) — the b.v2c case of a freshly restarted
//     agent that has not been messaged. Recourse: spawn again with the same
//     id, opting in to reuse (SpawnParams.ReuseFinished); the reused id
//     starts a new life with no memory of the earlier one.
//   - ErrTmuxNotAvailable: ENVIRONMENT. At the pre-launch lookup, before
//     anything is written, this is not the tmux server the agent was launched
//     on, or the tmux binary cannot be run or the socket is not accessible to
//     this user; the socket's directory is unusable, also before anything is
//     written; or, at session creation or at the re-lookup after "duplicate
//     session", the binary cannot be run or the socket is not accessible (or,
//     at the re-lookup, this is not the agent's tmux server), followed by the
//     restore.
//   - [ErrTmuxSessionConflict]: CONFLICT, until a human looks (see
//     "Operator actions" in the agent-director README). At the pre-launch
//     lookup, before anything is written and with no session touched, a
//     session holds the recorded name (another row's agent, another
//     agent-director store's agent, which must not be ended, or a session with
//     no valid instance id); a session is left over from an earlier life of
//     this row; the row's own session, or its agent process still running
//     with no session, is past the stopping window and the starting-session
//     bound ("this row's own id"); or tmux holds conflicting labels. After
//     "duplicate session" at the create, the same cases for the session
//     holding the name (its own session judged with the ended_at read before
//     the move), followed by the restore.
//   - [ErrTmuxSessionCreate]: LAUNCH FAILURE. Session creation failed, a
//     created session could not be labelled, or the create answered
//     "duplicate session" and no session held the name when it was looked up
//     again, each followed by the restore. A name found held is never this
//     error.
//   - ErrTmuxUnresponsive: UNAVAILABLE, transient. At the pre-launch lookup,
//     before anything is written, the row's own session or agent appears to
//     still be stopping (the row ended less than the stopping window ago) or
//     still starting (younger than the starting-session bound), tmux's answer
//     could not be read, or more than one session's name matches the recorded
//     name; retry later. After "duplicate session" at the create, the same
//     cases for the re-lookup and the session holding the name, followed by
//     the restore. Or the session-creating call timed out or gave a
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
