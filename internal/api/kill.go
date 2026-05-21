package api

import (
	"github.com/gabemahoney/agent-director/internal/store"
)

// KillTmux is the narrow tmux surface Kill needs. *tmux.Client
// satisfies it; tests pass a recording fake that captures the kill argv.
type KillTmux interface {
	KillSession(name string) error
}

// KillLogger is the narrow log surface Kill uses to surface
// swallowed tmux failures at WARN level. *log.Logger satisfies it;
// tests inject a recording fake to inspect the message. nil is
// accepted (Kill stays silent) so callers that don't care still
// compile against the previous interface.
type KillLogger interface {
	Printf(format string, v ...any)
}

// KillParams is the typed parameter shape for the kill verb.
type KillParams struct {
	ClaudeInstanceID string `json:"claude_instance_id"`
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
//     swallowed AT THE VERB SURFACE (post-condition "session gone" is
//     satisfied either way, and find-missing reconciles the row), but
//     the error is emitted at WARN level via lg so an operator running
//     `agent-director kill` interactively can see permission /
//     stale-TMUX_TMPDIR / etc. diagnostics without having to wait for
//     the next reconciliation pass.
//
// Note: kill does NOT promise state cleanup — the row stays in its
// pre-kill state until find-missing (Epic 8) reconciles it. SRD §5
// pins this intentionally so a hung tmux session and a freshly killed
// one are reconciled by the same audit path.
func Kill(s *store.Store, t KillTmux, lg KillLogger, params KillParams) (KillResult, error) {
	row, err := s.GetSpawn(params.ClaudeInstanceID)
	if err != nil {
		return KillResult{}, err
	}

	if row.State == store.StateEnded || row.State == store.StateMissing {
		return KillResult{}, nil
	}

	// Swallow tmux errors at the verb surface (the post-condition is
	// "session gone"; find-missing will reconcile the row regardless),
	// but log them so an operator running kill interactively can see
	// the underlying tmux failure.
	if err := t.KillSession(row.TmuxSessionName); err != nil {
		if lg != nil {
			lg.Printf("WARN: kill: tmux kill-session for spawn %s failed: %v (find-missing will reconcile)",
				params.ClaudeInstanceID, err)
		}
	}
	return KillResult{}, nil
}
