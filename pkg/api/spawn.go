package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// Spawn launches a tracked Claude Code instance inside a new tmux session.
// It is fire-and-forget: the call returns the claude_instance_id immediately;
// state transitions from pending to waiting on the first SessionStart hook.
func (c *Client) Spawn(params SpawnParams) (SpawnResult, error) {
	if err := c.checkClosed(); err != nil {
		return SpawnResult{}, err
	}
	return internalapi.Spawn(c.st, c.tmuxClient, c.cfg, params)
}
