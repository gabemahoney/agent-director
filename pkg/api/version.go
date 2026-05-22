package api

import "github.com/gabemahoney/agent-director/internal/version"

// VersionResult is the JSON shape returned by the `version` verb.
type VersionResult struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Version returns the ldflags-stamped build identity. Pure read of
// internal/version package vars; no I/O.
func Version() (VersionResult, error) {
	return VersionResult{
		Version: version.Version,
		Commit:  version.Commit,
	}, nil
}

// Version returns the ldflags-stamped build identity (version string + commit
// SHA). It is a handle-free verb: no store, tmux, or config is consulted.
func (c *Client) Version() (VersionResult, error) {
	if err := c.checkClosed(); err != nil {
		return VersionResult{}, err
	}
	return Version()
}
