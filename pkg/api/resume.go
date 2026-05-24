package api

import (
	"fmt"
	"os"

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
//     ErrJsonlMissing. Pure os.Stat pre-flight; no read.
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

	jsonl, err := spawn.JsonlPath(row.CWD, row.ClaudeSessionID)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("resume: resolve jsonl: %w", err)
	}
	if _, err := os.Stat(jsonl); err != nil {
		if os.IsNotExist(err) {
			return ResumeResult{}, fmt.Errorf("%w: %s", ErrJsonlMissing, jsonl)
		}
		return ResumeResult{}, fmt.Errorf("resume: stat jsonl: %w", err)
	}

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
//   - [ErrJsonlMissing]: the JSONL transcript file does not exist on disk.
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
