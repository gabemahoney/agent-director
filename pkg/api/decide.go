package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// Decide writes the orchestrator's allow/deny verdict on an open
// PermissionRequest. Only callable on Spawns with relay_mode=on.
func (c *Client) Decide(params DecideParams) (DecideResult, error) {
	if err := c.checkClosed(); err != nil {
		return DecideResult{}, err
	}
	return internalapi.Decide(c.st, params)
}
