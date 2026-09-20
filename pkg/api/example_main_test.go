package api_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// apiTrailDir is the temp HOME for this test binary. Set by TestMain before
// any test function runs so the trail singleton writes to a known location
// (apiTrailDir/.agent-director/ad-trail.jsonl). Trail tests
// (find_missing_trail_test.go) read this to locate the trail file.
var apiTrailDir string

// TestMain is the package-level entry point for pkg/api tests.
//
// It provides environment-isolation behaviors before any test runs:
//   - Redirects $HOME to a fresh temp directory so paths that use
//     os.Getenv("HOME") or os.UserHomeDir() (e.g. internal/spawn/pretrust.go,
//     and the trail resolver) land under the temp dir rather than the real
//     home directory. This HOME redirection is now the SOLE trail-isolation
//     mechanism: the trail singleton lands at
//     apiTrailDir/.agent-director/ad-trail.jsonl.
//   - Clears AGENT_DIRECTOR_INSTANCE_ID so test spawns are created as roots
//     (NULL parent_id) and don't hit FK failures from a real UUID pointing
//     at a non-existent row in the test database.
//
// The trail singleton pins to HOME, so HOME MUST be set here (via os.Setenv)
// before m.Run() — a per-test t.Setenv would be too late.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	// ── Redirect $HOME and pin the trail singleton ───────────────────────────
	// The trail writer resolves ~/.agent-director/ad-trail.jsonl from HOME via
	// os.UserHomeDir() and uses sync.Once to capture the path on first Emit. By
	// setting HOME before m.Run() we ensure the singleton lands at
	// tmpHome/.agent-director/ad-trail.jsonl regardless of the developer's
	// shell env. Trail-reading helpers in find_missing_trail_test.go use
	// apiTrailDir to locate the file.
	tmpHome, err := os.MkdirTemp("", "pkg-api-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: MkdirTemp: %v\n", err)
		os.Exit(2)
	}
	os.Setenv("HOME", tmpHome) // nolint:errcheck — os.Setenv never errors on non-nil key
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
