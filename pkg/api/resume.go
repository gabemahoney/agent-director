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
}

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
//     When every candidate fails, ErrJsonlMissing reports each path
//     tried with its source (persisted vs fallback) and its stat error.
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

	return ResumeResult{}, fmt.Errorf("%w: %s", ErrJsonlMissing, formatJsonlAttempts(attempts))
}

// jsonlAttempt records one candidate transcript path resume tried to
// stat, its provenance (persisted jsonl_path vs CLAUDE_CONFIG_DIR-aware
// fallback), and the stat error that ruled it out. Collected so the
// ErrJsonlMissing message can name every path tried (bug b.1ba scope 2).
type jsonlAttempt struct {
	source  string // "persisted" | "fallback"
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
//     neither the persisted jsonl_path nor the CLAUDE_CONFIG_DIR-aware
//     fallback recomputed from the row's ExtraEnv (the message names
//     every path tried and its source).
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
