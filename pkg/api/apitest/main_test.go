package apitest

import (
	"os"
	"testing"
)

// TestMain redirects $HOME to a fresh temp dir before any test in this package
// runs.
//
// The apitest seed helpers exercised by seeds_test.go drive in-process store
// mutations (SeedSpawn → InsertPending + state transitions, and the decide
// fixtures → DecidePermissionRequest). Those store paths emit audit events
// through the trail package. trail.Default() is a process-wide sync.Once
// singleton that pins its file path from $HOME on the FIRST Emit and never
// re-resolves it; the individual tests here use per-test temp stores but do NOT
// redirect $HOME, so absent this redirect the singleton would pin — and this
// package would append to — the real ~/.agent-director/ad-trail.jsonl.
//
// That un-redirected write is exactly what the test/smoke/go canary guards
// against, and it tripped that canary intermittently when `go test ./...` ran
// apitest and smoke in parallel and the write landed inside the canary's
// before/after snapshot window. Pinning $HOME to a temp dir here — before
// m.Run() — makes the singleton resolve under a throwaway dir, so this package
// can never touch the real ~/.agent-director.
func TestMain(m *testing.M) {
	tmpHome, err := os.MkdirTemp("", "apitest-home-*")
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
