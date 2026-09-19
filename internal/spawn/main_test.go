package spawn

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// TestMain stubs claudeJSONPath to a per-process temp file so existing
// Launch tests (which create real tmpdir cwds) don't leak project
// entries into the operator's actual ~/.claude.json. Tests that exercise
// pre-trust behavior call withStubClaudeJSON(t) to re-stub to their own
// per-test temp file; that override nests cleanly because the helper
// saves and restores the var via t.Cleanup.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	dir, err := os.MkdirTemp("", "agent-director-spawn-tests-*")
	if err != nil {
		panic("setup: mkdtemp: " + err.Error())
	}
	defer os.RemoveAll(dir)

	stubPath := filepath.Join(dir, ".claude.json")
	claudeJSONPath = func() (string, error) { return stubPath, nil }

	os.Exit(m.Run())
}
