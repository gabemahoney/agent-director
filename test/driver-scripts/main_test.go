// Package driverscripts_test tests the Docker harness's driver scripts under
// test/driver/ and the shell the testplans give them (b.ai5): every store
// read or write goes through test/driver/sql.sh, the sqlite3 shell with a busy
// timeout; sql.sh and pane-hook.sh wait out a held lock; run-testplan.sh's
// diagnostic rerun starts from a fresh db-reset; and db-reset.sh refuses to
// run outside the harness container (b.8yq). The tests run the scripts
// against temp-dir stores and plans, with a real sqlite3, a fake db-reset and
// fake tmux; nothing touches ~/.agent-director, tmux or Docker.
package driverscripts_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}

// repoRoot is the directory holding go.mod above the test's working dir.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
