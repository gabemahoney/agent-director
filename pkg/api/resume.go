package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// resumeEnvInstanceID is the env-var key resume reads to re-derive
// parent_id (SRD §7.5). Mirrored verbatim from spawn's constant of
// the same name; both refer to the same operational concept.
const resumeEnvInstanceID = "AGENT_DIRECTOR_INSTANCE_ID"

// ResumeStore is the narrow store surface Resume needs.
type ResumeStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	SetParentID(instanceID, parentID string) error
	// ListSessionHistory returns the instance's archived prior sessions
	// (newest first). Used both to try earlier transcripts as resume
	// candidates (b.v2c AC6 — a rotation must not strand history) and to
	// distinguish ErrJsonlNeverWritten from ErrJsonlMissing (AC2).
	ListSessionHistory(instanceID string) ([]SessionHistoryEntry, error)
}

// SessionHistoryEntry is re-exported from internal/store so external consumers
// can name the type in the ResumeStore interface without importing
// internal/store directly (b.v2c).
type SessionHistoryEntry = store.SessionHistoryEntry

// ResumeTmux is the narrow tmux surface Resume needs.
type ResumeTmux interface {
	HasSession(name string) (bool, error)
	NewSession(name, cwd string, envs map[string]string, command []string) error
}

// ResumeParams is the typed parameter shape for the resume verb.
type ResumeParams struct {
	// ClaudeInstanceID identifies the terminated Spawn to resurrect.
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// ResumeResult is the typed return shape. The id field is the same
// id the caller passed in; resume preserves the instance id across
// the resurrection (SRD §8.1).
type ResumeResult struct {
	// ClaudeInstanceID is the id of the resurrected Spawn — identical to the
	// value passed in ResumeParams.ClaudeInstanceID.
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// resumeImpl is the unexported verb handler called by (c *Client).Resume.
// It takes internal types directly and is not part of the public API surface;
// external consumers use the Client method instead.
//
// Guards (in order; each error path is side-effect-free — no DB
// mutation, no half-created tmux session):
//
//  1. GetSpawn → ErrSpawnNotFound when the id is unknown.
//  2. State must be `ended` or `missing` → otherwise
//     ErrSpawnNotResumable. The verb does NOT touch a live Spawn.
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
//     empty string is treated as absent (mirroring pretrust.go:58),
//     and a non-absolute value (relative, `~`-prefixed, or
//     whitespace-only) is ALSO treated as absent — in every such
//     case the fallback resolves to ~/.claude, since a relative dir
//     would stat against the nondeterministic process cwd. This
//     heals legacy rows written before the SessionStart hook
//     persisted jsonl_path, and rows whose recorded path has rotted.
//     A successful fallback resume re-fires SessionStart, which
//     re-persists the correct path.
//     When every candidate (including archived session-history
//     transcripts, b.v2c AC6) fails, the verb distinguishes two cases:
//     ErrJsonlNeverWritten when the persisted jsonl_path was NULL and the
//     instance has no session history (nothing was ever written — AC2);
//     ErrJsonlMissing otherwise (a path was once recorded/composed and has
//     rotted). Both messages report each path tried with its source
//     (persisted / fallback / history) and its stat error.
//  5. Canonical tmux session name must NOT already exist → otherwise
//     the tmux.NewSession at step 7 would surface ErrTmuxSessionCreate
//     anyway, and we'd rather error out cleanly here than after a
//     parent_id mutation. Resume does NOT auto-kill a stale session;
//     the operator cleans up manually.
//  6. Re-derive parent_id from caller env (SRD §7.5). Empty env →
//     NULL parent. The DB write happens BEFORE the tmux launch — if
//     the launch fails, the parent_id update is a harmless stale
//     value that'll be overwritten on the next resume.
//  7. spawn.Relaunch composes env + synthesized settings + tmux argv,
//     fires tmux.NewSession. Fire-and-forget — the first SessionStart
//     hook is what flips state back to `waiting` and rotates
//     `claude_session_id`.
//
// On launch failure (tmux refuses, claude binary missing, etc.) the
// row's state stays `ended` / `missing` — the caller sees the error
// and can retry.
func resumeImpl(s ResumeStore, t ResumeTmux, cfg config.Config, params ResumeParams) (ResumeResult, error) {
	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return ResumeResult{}, err
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
			return resumeAfterJsonl(s, t, cfg, row, params)
		} else {
			attempts = append(attempts, jsonlAttempt{
				source: "persisted", path: persisted, statErr: err,
			})
		}
	}

	// Fallback: recompute from the persisted CLAUDE_CONFIG_DIR (bug b.1ba),
	// following the internal/spawn/pretrust.go pattern — fall back to
	// ~/.claude when the key is absent/empty via spawn.JsonlPath.
	//
	// CLAUDE_CONFIG_DIR value semantics (decision of record, bug b.1ba):
	// the ExtraEnv value is used ONLY if it is non-empty AND absolute.
	// Empty string is treated as absent (mirrors pretrust.go:58's
	// `dir != ""` check). A non-empty but non-ABSOLUTE value — relative,
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
		return resumeAfterJsonl(s, t, cfg, row, params)
	} else {
		attempts = append(attempts, jsonlAttempt{
			source: "fallback", path: fallback, statErr: err,
		})
	}

	// b.v2c AC6: a session rotation (CSCB fleet restart) archives the prior
	// session's (session id, jsonl_path) into session_history. If the current
	// session has no live transcript, fall back to the most recent archived
	// session whose transcript still exists on disk — this recovers history
	// orphaned by a rotation rather than abandoning it. Resume does not persist
	// a session-id change: it points the relaunch at the recovered archived
	// session id (via row.ClaudeSessionID, which spawn.Relaunch reads), so
	// `claude --resume` reattaches to the recovered transcript. The row's
	// claude_session_id is not rewritten here; it updates later, when the
	// resumed process's SessionStart hook fires.
	//
	// Each archived candidate gets the same persisted→fallback two-step the
	// current session gets (finding b.5jm/1): the recorded jsonl_path is tried
	// first, and on ANY stat failure of a non-empty path — the b.1ba rot mode —
	// the config-dir-aware path is recomputed and stat'd for that same session
	// id before advancing to the next, older entry. Without this, a newer entry
	// whose recorded path has rotted would be skipped outright and an older
	// entry could win, silently reattaching resume to older history.
	history, herr := s.ListSessionHistory(params.ClaudeInstanceID)
	if herr != nil {
		return ResumeResult{}, fmt.Errorf("resume: list session history: %w", herr)
	}
	for _, h := range history {
		// Step 1: try the archived recorded path, if any. It wins outright when
		// it stats; on any stat failure fall through to the recomputed path.
		if h.JSONLPath != "" {
			if _, err := os.Stat(h.JSONLPath); err == nil {
				return resumeAfterArchivedJsonl(s, t, cfg, row, h.ClaudeSessionID, h.JSONLPath, params)
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
			return resumeAfterArchivedJsonl(s, t, cfg, row, h.ClaudeSessionID, recomputed, params)
		} else {
			attempts = append(attempts, jsonlAttempt{
				source: "history", path: recomputed, statErr: err,
			})
		}
	}

	// AC2: distinguish "no transcript has EVER been written for this session"
	// from "candidates were tried and none matched". The never-written case is
	// narrow and specific: the persisted jsonl_path was NULL/empty (the
	// SessionStart hook found no file), AND the instance has no archived session
	// history to have lost. A persisted-but-rotted path, or a row that HAS
	// history, is the classic ErrJsonlMissing.
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

// resumeAfterArchivedJsonl points the relaunch at a recovered archived session.
// It sets the in-memory row's ClaudeSessionID + JSONLPath to the archived
// candidate (spawn.Relaunch reads row.ClaudeSessionID) and hands off to
// resumeAfterJsonl. This is an in-memory mutation only — no DB write rotates the
// row's session id here; that happens later when the resumed process's
// SessionStart hook fires.
func resumeAfterArchivedJsonl(s ResumeStore, t ResumeTmux, cfg config.Config, row Spawn, sessionID, jsonlPath string, params ResumeParams) (ResumeResult, error) {
	row.ClaudeSessionID = sessionID
	row.JSONLPath = jsonlPath
	return resumeAfterJsonl(s, t, cfg, row, params)
}

// resumeAfterJsonl runs the remaining resume guards (tmux collision,
// parent re-derivation) and fires the relaunch once a transcript path
// has been verified on disk. Split out so the two-candidate resolution
// above has a single continuation regardless of which candidate won.
func resumeAfterJsonl(s ResumeStore, t ResumeTmux, cfg config.Config, row Spawn, params ResumeParams) (ResumeResult, error) {
	exists, err := t.HasSession(row.TmuxSessionName)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("resume: probe tmux: %w", err)
	}
	if exists {
		return ResumeResult{}, fmt.Errorf("%w: tmux session %s already exists",
			tmux.ErrTmuxSessionCreate, row.TmuxSessionName)
	}

	parent := os.Getenv(resumeEnvInstanceID)
	if err := s.SetParentID(params.ClaudeInstanceID, parent); err != nil {
		return ResumeResult{}, fmt.Errorf("resume: set parent: %w", err)
	}

	if err := spawn.Relaunch(spawn.RelaunchInput{
		Row:       row,
		Parent:    parent,
		SessionID: row.ClaudeSessionID,
	}, t, cfg); err != nil {
		return ResumeResult{}, err
	}

	return ResumeResult{ClaudeInstanceID: params.ClaudeInstanceID}, nil
}

// Resume brings a terminated (ended/missing) Spawn back to life by launching
// `claude --resume` in a fresh tmux session pointed at the same JSONL
// transcript. The claude_instance_id is preserved across the resurrection;
// state transitions back to waiting when the first SessionStart hook fires.
//
// CLI: agent-director resume
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrSpawnNotResumable]: state is not ended or missing (a live Spawn
//     must be killed or paused before it can be resumed).
//   - [ErrNoSessionId]: claude_session_id is empty — the Spawn was killed
//     before its first SessionStart hook; delete and re-spawn instead.
//   - [ErrJsonlMissing]: no candidate JSONL transcript exists on disk —
//     neither the persisted jsonl_path, the CLAUDE_CONFIG_DIR-aware
//     fallback, nor any archived session-history transcript (the message
//     names every path tried and its source). Meaning: history existed but
//     the file is gone.
//   - [ErrJsonlNeverWritten]: the row has a session id but no transcript was
//     ever written (persisted jsonl_path NULL and no session history) — the
//     b.v2c freshly-restarted, un-messaged case. Recourse: message it, or
//     delete + re-spawn.
//   - ErrTmuxNotAvailable: tmux binary is not on PATH.
//   - [ErrTmuxSessionCreate]: a tmux session with the same name already exists.
//
// Nondeterminism: none.
func (c *Client) Resume(params ResumeParams) (ResumeResult, error) {
	if err := c.checkClosed(); err != nil {
		return ResumeResult{}, err
	}
	return resumeImpl(c.st, c.tmuxClient, c.cfg, params)
}
