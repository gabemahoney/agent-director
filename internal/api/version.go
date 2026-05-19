package api

import "github.com/gabemahoney/claude-director/internal/version"

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
