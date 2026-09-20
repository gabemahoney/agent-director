package envelope_diff

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// TestMain refuses to run this package's tests outside the sandbox container.
// The envelope-diff tests build and exec the agent-director binary, which can
// open (and, on a schema-bumping branch, migrate) the real ~/.agent-director on
// the host (b.8dr). See sandboxguard for the rationale; run via
// `make test-sandbox`.
//
// It also redirects $HOME to a fresh temp dir before any test runs. Several
// subtests drive verbs in-process (runClient → c.Decide / c.FindMissing),
// which emit audit events through the trail package. trail.Default() is a
// process-wide sync.Once singleton that pins its file path from $HOME on the
// FIRST Emit and never re-resolves it. The per-subtest t.Setenv("HOME", …) in
// success_cases_test.go / error_cases_test.go only isolates whichever subtest
// happens to emit first; any earlier in-process Emit would pin the singleton
// to the real ~/.agent-director. Redirecting $HOME here — before m.Run() —
// guarantees the singleton can only ever resolve under a temp dir, so this
// package never writes to the real ~/.agent-director. (This is what the
// test/smoke/go canary guards against, and why an un-redirected Emit here
// tripped it intermittently when `go test ./...` ran the two packages in
// parallel.)
func TestMain(m *testing.M) {
	sandboxguard.Require()
	tmpHome, err := os.MkdirTemp("", "envelope-diff-home-*")
	if err != nil {
		panic("TestMain: MkdirTemp: " + err.Error())
	}
	if err := os.Setenv("HOME", tmpHome); err != nil {
		panic("TestMain: Setenv HOME: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(tmpHome)
	os.Exit(code)
}
