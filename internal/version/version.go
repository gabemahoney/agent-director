// Package version exposes the version stamp embedded at build time via
// -ldflags. Defaults below are used when the binary is built without
// the stamp (plain `go build`); the Makefile and the /release skill override
// both via `-X github.com/gabemahoney/agent-director/internal/version.Version=...`.
package version

// Version is the human-readable stamp from `git describe --tags --always --dirty`
// at build time. Empty / "dev" means the binary was built without ldflags.
var Version = "dev"

// Commit is the full git SHA the binary was built from. "unknown" means
// the binary was built without ldflags (e.g. plain `go build`).
var Commit = "unknown"
