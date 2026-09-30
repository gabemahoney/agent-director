package tmuxfix_test

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// TestMain refuses to run outside the sandbox and pins $HOME to a temp dir
// before any store write (apitest.SeedSpawn in recorder_pane_label_test.go),
// so the trail singleton never resolves the real home.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	home, err := os.MkdirTemp("", "tmuxfix-home-*")
	if err != nil {
		panic("TestMain: MkdirTemp: " + err.Error())
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic("TestMain: Setenv HOME: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
