package api

import (
	"context"

	internalapi "github.com/gabemahoney/agent-director/internal/api"
)

// Pause politely shuts down a waiting Spawn by sending `/exit` and waiting up
// to pause.timeout_seconds for the row to reach `ended`. ctx is the first
// argument per SRD §12 (preserving CLI ctx semantics).
func (c *Client) Pause(ctx context.Context, params PauseParams) (PauseResult, error) {
	if err := c.checkClosed(); err != nil {
		return PauseResult{}, err
	}
	return internalapi.Pause(ctx, c.st, c.tmuxClient, c.cfg.Pause, params)
}
