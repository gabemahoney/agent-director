package api_test

// readme_store_id_test.go runs the README's "This store's id" sqlite3 command
// against a fresh store under a temp HOME and checks it prints the store's
// own id (Store.StoreID). The command's documented shape and the pointers to
// it are pinned in readme_operator_actions_more_test.go, not here.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// runnableStoreIDLine returns the one sqlite3 line of the README's "This
// store's id" item (storeIDItemCommands). It refuses a line that is not
// read-only, since the test runs it.
func runnableStoreIDLine(t *testing.T) string {
	t.Helper()
	d := readMD(t, mdTopREADME)
	cmds := storeIDItemCommands(t, d)
	if len(cmds) != 1 {
		t.Fatalf("%s %q: want exactly one sqlite3 line in a code block; found %q", d.path, storeIDItemTitle, cmds)
	}
	if !strings.HasPrefix(cmds[0], "sqlite3 -readonly ") {
		t.Fatalf("%s %q: refusing to run a sqlite3 line without -readonly: %s", d.path, storeIDItemTitle, cmds[0])
	}
	return cmds[0]
}

// TestReadmeStoreIDCommandPrintsStoreID runs the README's store-id line with
// HOME at a temp dir holding a fresh store, closed and while held open.
// A missing sqlite3 fails inside the sandbox (its image installs sqlite3, so a
// skip there would hide a broken image) and skips only outside it.
func TestReadmeStoreIDCommandPrintsStoreID(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		if os.Getenv(sandboxguard.EnvVar) != "" {
			t.Fatalf("sqlite3 not on PATH inside the sandbox (%s is set): the sandbox image must install it: %v", sandboxguard.EnvVar, err)
		}
		t.Skip("sqlite3 not on PATH: the README's store-id command cannot be run here (the sandbox image installs it)")
	}
	line := runnableStoreIDLine(t)
	for _, tc := range []struct {
		name     string
		keepOpen bool
	}{
		{"store closed", false},
		{"store held open", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			st, err := store.OpenOrInit(filepath.Join(home, ".agent-director", "state.db"))
			if err != nil {
				t.Fatalf("OpenOrInit: %v", err)
			}
			want := st.StoreID()
			if tc.keepOpen {
				t.Cleanup(func() { st.Close() })
			} else if err := st.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", line)
			cmd.Env = append(os.Environ(), "HOME="+home) // the last HOME wins
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("README line %s: %v; stderr: %s", line, err, stderr.String())
			}
			if got := strings.TrimSpace(string(out)); got != want {
				t.Errorf("README line %s printed %q; Store.StoreID() is %q", line, got, want)
			}
		})
	}
}
