package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// Status returns the current state of a tracked Spawn.
func (c *Client) Status(claudeInstanceID string) (StatusResult, error) {
	if err := c.checkClosed(); err != nil {
		return StatusResult{}, err
	}
	return internalapi.Status(c.st, claudeInstanceID)
}

// Get returns the full DB row for a tracked Spawn.
func (c *Client) Get(claudeInstanceID string) (SpawnRow, error) {
	if err := c.checkClosed(); err != nil {
		return SpawnRow{}, err
	}
	return internalapi.Get(c.st, claudeInstanceID)
}
