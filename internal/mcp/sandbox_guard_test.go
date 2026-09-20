package mcp_test

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// TestMain refuses to run this package's tests outside the sandbox container.
// internal/mcp's tests construct clients that open the store, which can rewrite
// the real ~/.agent-director on the host (b.8dr). See sandboxguard for the
// rationale; run via `make test-sandbox`.
//
// It also redirects $HOME to a fresh temp dir before any test runs. These tests
// open the store and drive dispatcher Calls that emit trail events (e.g.
// seedMatrixSpawn's ApplyHookTransition with triggering_event_name "test_seed",
// and TestNoMigrationTriggerTool's api.New client). trail.Emit resolves
// <$HOME>/.agent-director/ad-trail.jsonl through a process-wide sync.Once that
// pins its path on the FIRST Emit and never re-resolves it. Some subtests set a
// per-subtest t.Setenv("HOME", …) but that latches too late: whichever test
// emits first would otherwise pin the singleton to the real ~/.agent-director,
// leaking trail lines that the release trail-leak canary catches under
// `go test ./... -race -count=1` (b.93m). Setting $HOME here — before m.Run() —
// is the sole reliable trail-isolation mechanism for this package.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	tmpHome, err := os.MkdirTemp("", "mcp-home-*")
	if err != nil {
		panic("TestMain: MkdirTemp: " + err.Error())
	}
	if err := os.Setenv("HOME", tmpHome); err != nil { //nolint:errcheck — os.Setenv never errors on non-nil key
		panic("TestMain: Setenv HOME: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(tmpHome)
	os.Exit(code)
}
