package api_test

import (
	"fmt"
	"os"
	"testing"
)

// TestMain is the package-level entry point for pkg/api tests.
//
// It provides two environment-isolation behaviors before any test runs:
//   - Redirects $HOME to a fresh temp directory so paths that use
//     os.Getenv("HOME") or os.UserHomeDir() (e.g. internal/spawn/pretrust.go)
//     land under the temp dir rather than the real home directory.
//   - Clears AGENT_DIRECTOR_INSTANCE_ID so test spawns are created as roots
//     (NULL parent_id) and don't hit FK failures from a real UUID pointing
//     at a non-existent row in the test database.
//
// Per-test isolation is the primary guarantee; this is defense-in-depth.
func TestMain(m *testing.M) {
	// ── Redirect $HOME (defense-in-depth) ────────────────────────────────────
	// Catches paths that use os.Getenv("HOME") / os.UserHomeDir().
	tmpHome, err := os.MkdirTemp("", "pkg-api-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: MkdirTemp: %v\n", err)
		os.Exit(2)
	}
	os.Setenv("HOME", tmpHome) // nolint:errcheck — os.Setenv never errors on non-nil key

	// ── Clear AGENT_DIRECTOR_INSTANCE_ID ─────────────────────────────────────
	// spawn.Launch reads this env var and uses it as parent_id for the new
	// store row. When tests run inside an active agent-director session the
	// var is set to a real UUID that does not exist in the freshly-created
	// test database, causing an FK constraint failure. Clearing it ensures
	// all test spawns are created as roots (NULL parent_id).
	os.Unsetenv("AGENT_DIRECTOR_INSTANCE_ID") // nolint:errcheck

	// ── Run all tests and examples ────────────────────────────────────────────
	code := m.Run()

	// ── Clean up the fake home ────────────────────────────────────────────────
	_ = os.RemoveAll(tmpHome)

	os.Exit(code)
}
