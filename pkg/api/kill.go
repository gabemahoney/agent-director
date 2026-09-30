package api

import "github.com/gabemahoney/agent-director/internal/store"

// KillStore is the narrow store surface Kill needs. *store.Store
// satisfies it; tests pass the real store or a recording fake.
type KillStore interface {
	GetSpawn(instanceID string) (Spawn, error)
}

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
	// ClaudeInstanceID identifies the Spawn whose tmux session will be killed.
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
func Kill(s KillStore, t KillTmux, lg KillLogger, params KillParams) (KillResult, error) {
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

// Kill terminates the Spawn's tmux session. Idempotent on terminal states
// (ended/missing) — calling Kill on an already-gone Spawn is a no-op success.
// Kill does NOT update the row's state column; the row transitions to missing
// on the next find-missing reconciliation pass. Tmux failures are swallowed
// at the verb surface and logged at WARN level to c.logger.
//
// CLI: agent-director kill
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrTmuxNotAvailable]: the tmux binary could not be run, the socket is
//     not accessible to this user, or this is not the tmux server the agent
//     was launched on. When the lookup or the pane listing hit it, no kill
//     was sent; when the follow-up lookup after a sent kill hit it, the kill
//     may or may not have taken effect.
//   - [ErrTmuxKillFailed]: the agent process still runs after kill: a kill
//     was sent and the agent process (or another process of a pane of its
//     session) outlived the kill exit wait, or the process cannot be checked
//     and its labelled session is still there, or no session or pane of this
//     launch was found while the process runs and no kill was sent.
//   - [ErrTmuxUnresponsive]: tmux did not answer usably (the lookup could not
//     be read, or a kill was sent but its follow-up could not answer).
//   - [ErrTmuxSessionConflict]: the session found is not this launch's
//     session, or tmux holds conflicting labels; no kill was sent.
//
// Nondeterminism: none.
func (c *Client) Kill(params KillParams) (KillResult, error) {
	if err := c.checkClosed(); err != nil {
		return KillResult{}, err
	}
	return Kill(c.st, c.tmuxClient, c.logger, params)
}
