package api

import "github.com/gabemahoney/agent-director/internal/store"

// StatusResult is the typed return shape of Status — exactly the row's
// state value, surfaced as a one-key JSON object on the CLI.
type StatusResult struct {
	State string `json:"state"`
}

// Status returns the current state column for the given claude_instance_id.
// Missing rows surface store.ErrSpawnNotFound; the CLI translates that to
// the canonical err_name envelope.
func Status(s *store.Store, instanceID string) (StatusResult, error) {
	state, err := s.GetSpawnState(instanceID)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{State: state}, nil
}

// Status returns the current state of a tracked Spawn.
func (c *Client) Status(claudeInstanceID string) (StatusResult, error) {
	if err := c.checkClosed(); err != nil {
		return StatusResult{}, err
	}
	return Status(c.st, claudeInstanceID)
}
