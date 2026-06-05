package api_test

import (
	"fmt"
	"os"
	"testing"
)

// apiTrailDir is the AGENT_DIRECTOR_STATE_DIR for this test binary. Set by
// TestMain before any test function runs so the trail singleton writes to a
// known location. Trail tests (find_missing_trail_test.go) read this to
// locate the trail file.
var apiTrailDir string

// TestMain is the package-level entry point for pkg/api tests.
//
// It provides environment-isolation behaviors before any test runs:
//   - Redirects $HOME to a fresh temp directory so paths that use
//     os.Getenv("HOME") or os.UserHomeDir() (e.g. internal/spawn/pretrust.go)
//     land under the temp dir rather than the real home directory.
//   - Fixes AGENT_DIRECTOR_STATE_DIR to the same temp dir so the trail
//     singleton writes to a known location (apiTrailDir/ad-trail.jsonl).
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

	// ── Pin the trail singleton to tmpHome ───────────────────────────────────
	// The trail writer uses sync.Once to capture the path on first Emit. By
	// setting AGENT_DIRECTOR_STATE_DIR before m.Run() we ensure the singleton
	// lands in tmpHome/ad-trail.jsonl regardless of the developer's shell env.
	// Trail-reading helpers in find_missing_trail_test.go use apiTrailDir to
	// locate the file.
	os.Setenv("AGENT_DIRECTOR_STATE_DIR", tmpHome) // nolint:errcheck
	apiTrailDir = tmpHome

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
