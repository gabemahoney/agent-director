package api

import (
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/version"
)

// VersionResult is the JSON shape returned by the `version` verb.
type VersionResult struct {
	// Version is the human-readable version stamp from `git describe --tags
	// --always --dirty` at build time. "dev" for unstamped builds.
	// Nondeterministic: varies per build.
	Version string `json:"version"`
	// Commit is the full git SHA the binary was built from.
	// "unknown" for unstamped builds. Nondeterministic: varies per build.
	Commit string `json:"commit"`
	// ConfigDir is the directory this binary keeps agent-director's files in,
	// as the literal "~/.config/agent-director" (the "~" unexpanded): the same
	// for every HOME. install.sh refuses a binary pair whose version result
	// lacks it or differs.
	ConfigDir string `json:"config_dir"`
}

// Version returns the ldflags-stamped build identity. Pure read of
// internal/version package vars; no I/O.
func Version() (VersionResult, error) {
	return VersionResult{
		Version:   version.Version,
		Commit:    version.Commit,
		ConfigDir: layout.Display,
	}, nil
}

// Version returns the ldflags-stamped build identity (version string and commit
// SHA) and config_dir, the directory the binary keeps its files in. It is handle-free: no store, tmux, or config is consulted; the call
// succeeds even if the store is not initialized.
//
// CLI: agent-director version
//
// Errors: none.
//
// Nondeterminism: .version and .commit — these are build-time stamps
// (from `git describe` and the full git SHA); they vary between builds and
// read "dev" / "unknown" for unstamped development builds. .config_dir is
// the same for every build and every HOME.
func (c *Client) Version() (VersionResult, error) {
	if err := c.checkClosed(); err != nil {
		return VersionResult{}, err
	}
	return Version()
}
