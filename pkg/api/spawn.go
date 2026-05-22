package api

import (
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
)

// SpawnResult is the typed return shape of Spawn. The CLI marshals this
// directly to its single-JSON-object stdout (SRD §12.3); the MCP server
// returns it as the tool result.
type SpawnResult struct {
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// runSpawn is the unexported verb handler called by (c *Client).Spawn.
// It takes internal types directly and is not part of the public API surface;
// external consumers use the Client method instead.
func runSpawn(s *store.Store, tmuxClient spawn.TmuxClient, cfg config.Config, params spawn.SpawnParams) (SpawnResult, error) {
	r, err := spawn.Resolve(params, cfg)
	if err != nil {
		return SpawnResult{}, err
	}
	if err := spawn.Validate(&r); err != nil {
		return SpawnResult{}, err
	}
	if err := spawn.ApplyDefaults(&r, cfg, s); err != nil {
		return SpawnResult{}, err
	}
	id, err := spawn.Launch(s, tmuxClient, r, cfg)
	if err != nil {
		return SpawnResult{}, err
	}
	return SpawnResult{ClaudeInstanceID: id}, nil
}

// Spawn launches a tracked Claude Code instance inside a new tmux session.
// It is fire-and-forget: the call returns the claude_instance_id immediately;
// state transitions from pending to waiting on the first SessionStart hook.
func (c *Client) Spawn(params SpawnParams) (SpawnResult, error) {
	if err := c.checkClosed(); err != nil {
		return SpawnResult{}, err
	}
	return runSpawn(c.st, c.tmuxClient, c.cfg, params)
}
