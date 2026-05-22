package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// Version returns the ldflags-stamped build identity (version string + commit
// SHA). It is a handle-free verb: no store, tmux, or config is consulted.
func (c *Client) Version() (VersionResult, error) {
	if err := c.checkClosed(); err != nil {
		return VersionResult{}, err
	}
	return internalapi.Version()
}
