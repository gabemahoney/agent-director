package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// apiTrailDir is the temp HOME for this test binary. Set by TestMain before
// any test function runs so the trail singleton writes to a known location
// (apiTrailDir/.agent-director/ad-trail.jsonl). Trail tests
// (find_missing_trail_test.go) read this to locate the trail file.
var apiTrailDir string

// apiTmuxTmpdir is this test binary's TMUX_TMPDIR, set by TestMain before any
// test runs, with its per-user socket directory made (mode 0700): the base a
// row with no recorded socket resolves its default socket under. The shared
// fixtures (newKillEnv, newResumeEnv) use it rather than a per-test
// t.Setenv, so their tests can run in parallel; a test that changes that
// directory on disk takes its own first (useOwnTmuxTmpdir).
var apiTmuxTmpdir string

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
//   - Unsets TMUX and points TMUX_TMPDIR at a fresh temp directory
//     (apiTmuxTmpdir), so a default socket resolves under this binary's own
//     directory, never the caller's tmux or /tmp/tmux-<uid>.
//
// The trail singleton pins to HOME, so HOME MUST be set here (via os.Setenv)
// before m.Run() — a per-test t.Setenv would be too late. The environment is
// process-wide: a test that needs other values sets them with t.Setenv, which
// the testing package refuses in a parallel test, so such a test runs alone.
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
	// Pin the trail path now: tests that move HOME (e.g. TestResumeDelegation)
	// would otherwise fix it to their own HOME if they emit first.
	trail.Default()

	// ── Clear AGENT_DIRECTOR_INSTANCE_ID ─────────────────────────────────────
	// spawn.Launch reads this env var and uses it as parent_id for the new
	// store row. When tests run inside an active agent-director session the
	// var is set to a real UUID that does not exist in the freshly-created
	// test database, causing an FK constraint failure. Clearing it ensures
	// all test spawns are created as roots (NULL parent_id).
	os.Unsetenv("AGENT_DIRECTOR_INSTANCE_ID") // nolint:errcheck

	// ── Unset TMUX, point TMUX_TMPDIR at this binary's own directory ─────────
	// tmux.ResolveSocket reads both; the real path is taken as tmux takes it.
	tmuxTmp, err := os.MkdirTemp("", "pkg-api-tmux-*")
	if err == nil {
		tmuxTmp, err = filepath.EvalSymlinks(tmuxTmp)
	}
	if err == nil {
		err = os.MkdirAll(userSocketDir(tmuxTmp), 0o700)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: TMUX_TMPDIR: %v\n", err)
		os.Exit(2)
	}
	os.Setenv("TMUX_TMPDIR", tmuxTmp) // nolint:errcheck
	os.Unsetenv("TMUX")               // nolint:errcheck
	apiTmuxTmpdir = tmuxTmp

	// ── Run all tests and examples ────────────────────────────────────────────
	code := m.Run()

	// ── Clean up the fake home and TMUX_TMPDIR ────────────────────────────────
	_ = os.RemoveAll(tmpHome)
	_ = os.RemoveAll(tmuxTmp)

	os.Exit(code)
}
