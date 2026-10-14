package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorHostInternalLaunchParsingDoesNotCreateStore(t *testing.T) {
	for _, args := range [][]string{{"cursor-host"}, {"cursor-host", "--runtime", "must-not-run", "--runtime-args", "not-json"}, {"cursor-host", "--runtime", "must-not-run", "--runtime-args", `["--resume=x"]`}, {"cursor-host", "--runtime", "must-not-run", "unexpected"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := t.TempDir()
			_, stderr, code := runCLIWithHome(t, home, args...)
			if code == 0 {
				t.Fatalf("invalid internal command %d %q", code, stderr)
			}
			if _, err := os.Stat(filepath.Join(home, ".config", "agent-director", "state.db")); !os.IsNotExist(err) {
				t.Fatalf("invalid internal launch created store: %v", err)
			}
		})
	}
}
