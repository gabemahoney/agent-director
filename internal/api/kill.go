package api

import (
	"github.com/gabemahoney/claude-director/internal/store"
)

// KillTmux is the narrow tmux surface Kill needs. *tmux.Client
// satisfies it; tests pass a recording fake that captures the kill argv.
type KillTmux interface {
	KillSession(name string) error
}

// KillParams is the typed parameter shape for the kill verb.
type KillParams struct {
	ClaudeInstanceID string
}

// KillResult is the typed return shape — empty today, reserved so
// future fields (e.g. session_already_gone) can be added without
// breaking the wire shape.
type KillResult struct{}

// Kill terminates the Spawn's tmux session and returns. Behavior
// (SRD §5, §12):
//
//   - Unknown id → ErrSpawnNotFound (the only surface error).
//   - Terminal state (ended / missing) → no-op success: the session is
//     either already gone or we never tracked it as live.
//   - Otherwise → tmux.KillSession is invoked; any tmux failure is
//     swallowed. The intent is "make sure that session is gone";
//     "session was already gone" satisfies that intent, so a
//     non-zero tmux exit (e.g. another orchestrator killed it first)
//     is indistinguishable from success at this layer.
//
// Note: kill does NOT promise state cleanup — the row stays in its
// pre-kill state until find-missing (Epic 8) reconciles it. SRD §5
// pins this intentionally so a hung tmux session and a freshly killed
// one are reconciled by the same audit path.
func Kill(s *store.Store, t KillTmux, params KillParams) (KillResult, error) {
	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return KillResult{}, err
	}

	if row.State == store.StateEnded || row.State == store.StateMissing {
		return KillResult{}, nil
	}

	// Swallow tmux errors: a missing/gone session is the desired
	// post-condition, and reporting the failure here would force
	// callers to distinguish "already dead" from "really failed",
	// which they can't usefully act on at this layer.
	_ = t.KillSession(row.TmuxSessionName)
	return KillResult{}, nil
}
