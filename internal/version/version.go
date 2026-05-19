// Package version exposes the version stamp embedded at build time via
// -ldflags. Defaults below are used when the binary is built without
// the stamp (plain `go build`); the Makefile and release.sh override
// both via `-X github.com/gabemahoney/claude-director/internal/version.Version=...`.
package version

// Version is the human-readable stamp from `git describe --tags --always --dirty`
// at build time. Empty / "dev" means the binary was built without ldflags.
var Version = "dev"

// Commit is the full git SHA the binary was built from. "unknown" means
// the binary was built without ldflags (e.g. plain `go build`).
var Commit = "unknown"
