package api_test

// readme_store_id_test.go runs the README's "This store's id" sqlite3 command
// against a fresh store under a temp HOME and checks it prints the store's
// own id (Store.StoreID). The command's documented shape and the pointers to
// it are pinned in readme_operator_actions_more_test.go, not here.

import (
	"context"
	"database/sql"
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

// holdStoreLock has a second connection take path's exclusive lock now and
// release it after d, as a starting or exiting agent-director briefly does.
func holdStoreLock(t *testing.T, path string, d time.Duration) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open lock holder: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("lock holder conn: %v", err)
	}
	for _, q := range []string{"PRAGMA locking_mode=EXCLUSIVE", "BEGIN EXCLUSIVE"} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("lock holder %s: %v", q, err)
		}
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM store_meta").Scan(&n); err != nil {
		t.Fatalf("lock holder read: %v", err)
	}
	released := make(chan struct{})
	time.AfterFunc(d, func() {
		_, _ = conn.ExecContext(ctx, "COMMIT")
		_ = conn.Close()
		_ = db.Close()
		close(released)
	})
	t.Cleanup(func() { <-released })
}

// TestReadmeStoreIDCommandPrintsStoreID runs the README's store-id line with
// HOME at a temp dir holding a fresh store: closed, held open, and while
// another connection briefly holds its exclusive lock (b.ady).
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
		lockFor  time.Duration // 0: no lock held
	}{
		{"store closed", false, 0},
		{"store held open", true, 0},
		{"store briefly locked", false, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dbPath := filepath.Join(home, ".agent-director", "state.db")
			st, err := store.OpenOrInit(dbPath)
			if err != nil {
				t.Fatalf("OpenOrInit: %v", err)
			}
			want := st.StoreID()
			if tc.keepOpen {
				t.Cleanup(func() { st.Close() })
			} else if err := st.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if tc.lockFor > 0 {
				holdStoreLock(t, dbPath, tc.lockFor)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-c", line)
			cmd.Env = append(os.Environ(), "HOME="+home) // the last HOME wins
			var stderr strings.Builder
			cmd.Stderr = &stderr
			start := time.Now()
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("README line %s: %v; stderr: %s", line, err, stderr.String())
			}
			if got := strings.TrimSpace(string(out)); got != want {
				t.Errorf("README line %s printed %q; Store.StoreID() is %q", line, got, want)
			}
			if waited := time.Since(start); waited < tc.lockFor/2 {
				t.Errorf("README line returned after %v, well before the %v lock was released: the lock never blocked it", waited, tc.lockFor)
			}
		})
	}
}
