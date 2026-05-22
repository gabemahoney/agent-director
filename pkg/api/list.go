package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// List enumerates Spawn rows matching the supplied filters. All filters AND
// together. When no rows match, ListResult.Spawns is a non-nil empty slice
// (JSON-stability invariant preserved by internal/api.List).
func (c *Client) List(params ListParams) (ListResult, error) {
	if err := c.checkClosed(); err != nil {
		return ListResult{}, err
	}
	return internalapi.List(c.st, params)
}
