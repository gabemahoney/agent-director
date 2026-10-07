package mcp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// mcpTrailPath is the trail file every Emit in this test binary writes to.
var mcpTrailPath string

// TestMain refuses to run outside the sandbox container (these tests open
// stores, b.8dr) and, before m.Run, points $HOME at a fresh temp dir and pins
// the trail singleton there: trail.Emit resolves its file once, on the first
// call, so a per-test t.Setenv("HOME") is too late and the first emitting test
// would otherwise pin it to the real ~/.agent-director (b.93m).
func TestMain(m *testing.M) {
	sandboxguard.Require()
	tmpHome, err := os.MkdirTemp("", "mcp-home-*")
	if err != nil {
		panic("TestMain: MkdirTemp: " + err.Error())
	}
	if err := os.Setenv("HOME", tmpHome); err != nil {
		panic("TestMain: Setenv HOME: " + err.Error())
	}
	trail.Default()
	mcpTrailPath = filepath.Join(tmpHome, ".agent-director", "ad-trail.jsonl")
	code := m.Run()
	_ = os.RemoveAll(tmpHome)
	os.Exit(code)
}
