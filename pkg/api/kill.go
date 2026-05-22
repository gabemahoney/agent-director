// Package api: Kill verb method.
//
// Logger contract: c.logger is the constructor-injected *log.Logger. Kill
// passes it verbatim to internal/api.Kill, which uses it to surface swallowed
// tmux failures at WARN level. The Client method layer is logger-agnostic —
// it only owns the wire-through. CLI callers inject a recovery logger;
// MCP callers inject nil (discard), intentionally swallowing the warning in
// that context (see serveHandlerWith in Task 4).

package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// Kill terminates the Spawn's tmux session. Idempotent on terminal states.
// Swallowed tmux failures are logged to c.logger at WARN level.
func (c *Client) Kill(params KillParams) (KillResult, error) {
	if err := c.checkClosed(); err != nil {
		return KillResult{}, err
	}
	return internalapi.Kill(c.st, c.tmuxClient, c.logger, params)
}
