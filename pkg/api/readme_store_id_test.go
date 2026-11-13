package api_test

// readme_store_id_test.go runs docs/operator-actions.md's "This store's id" sqlite3 command
// against a fresh store under a temp HOME and checks it prints the store's
// own id (Store.StoreID). The command's documented shape and the pointers to
// it are pinned in readme_operator_actions_more_test.go, not here.

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// holdStoreLock keeps a separate sqlite3 process's exclusive lock until cleanup.
// b.dg1: snapshotTrees closes database handles, which would release a lock
// held in this process even if its SQLite connection remained open.
func holdStoreLock(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "sqlite3", "-batch", "-bail", "-init", "/dev/null", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("lock holder stdin: %v", err)
	}
	t.Cleanup(func() { _ = in.Close() })
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("lock holder stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("lock holder: %v; stderr: %s", err, stderr.String())
		}
	})
	if _, err := io.WriteString(in, "PRAGMA locking_mode=EXCLUSIVE;\nBEGIN EXCLUSIVE;\nSELECT count(*) FROM store_meta;\nSELECT 'ready';\n"); err != nil {
		t.Fatalf("lock holder input: %v", err)
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if sc.Text() == "ready" {
			return
		}
	}
	t.Fatalf("lock holder exited before ready: %v", sc.Err())
}

// operatorSQLiteRC has every sqlite3 started with the returned env entry run
// rc as its ~/.sqliterc until t ends (b.hk7). sqlite3 looks in the passwd home
// before $HOME, so this writes that file, gated on the entry so the sqlite3 of
// other test packages sees no change, under the lock test/install-sh/retry.sh
// takes too. Sandbox only; it never replaces an existing file.
func operatorSQLiteRC(t *testing.T, rc string) string {
	t.Helper()
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("writes the passwd home's ~/.sqliterc: sandbox only (%s=1)", sandboxguard.EnvVar)
	}
	// The passwd entry alone, as sqlite3 reads it: user.Current can fall back
	// to $HOME, and an empty home would put the file in the working directory.
	uid := strconv.Itoa(os.Getuid())
	u, err := user.LookupId(uid)
	if err != nil || u.HomeDir == "" {
		t.Fatalf("passwd entry of uid %s (%+v): %v; sqlite3 reads ~/.sqliterc from its home directory", uid, u, err)
	}
	lock, err := os.OpenFile(filepath.Join(os.TempDir(), "agent-director-sqliterc.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		t.Cleanup(func() { lock.Close() }) // releases the lock, after the file's removal below
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
	}
	if err != nil {
		t.Fatalf("~/.sqliterc lock: %v", err)
	}
	path := filepath.Join(u.HomeDir, ".sqliterc")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("cannot create %s (it exists, which this never replaces, or cannot be written): %v", path, err)
	}
	t.Cleanup(func() { os.Remove(path) })
	_, err = f.WriteString(`.read '|printf "%s\n" "$AGENT_DIRECTOR_TEST_SQLITERC"'` + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return "AGENT_DIRECTOR_TEST_SQLITERC=" + rc
}

// b.dg1: the whole documented block must print Store.StoreID without changing
// any store-folder entry or bytes, including idle WAL and read-only folders.
// The locked-store case proves a plain read is busy after the snapshot;
// b.hk7 retains the ~/.sqliterc coverage.
func TestReadmeStoreIDCommandPrintsStoreID(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		if os.Getenv(sandboxguard.EnvVar) != "" {
			t.Fatalf("sqlite3 not on PATH inside the sandbox (%s is set): the sandbox image must install it: %v", sandboxguard.EnvVar, err)
		}
		t.Skip("sqlite3 not on PATH: the operator-actions store-id command cannot be run here (the sandbox image installs it)")
	}
	blocks := storeIDItemCommands(t, readMD(t, mdOperatorActions))
	if len(blocks) != 1 {
		t.Fatalf("store-id command: want one fenced block, got %d", len(blocks))
	}
	block := blocks[0]
	for _, tc := range []struct {
		name     string
		keepOpen bool
		locked   bool
		rc       string // ~/.sqliterc; "": none
		readOnly bool
	}{
		{name: "store closed"},
		{name: "store held open", keepOpen: true},
		{name: "store locked", locked: true},
		{name: "sqliterc headers on", rc: ".headers on"},
		{name: "read-only store folder", readOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := os.Environ()
			if tc.rc != "" {
				env = append(env, operatorSQLiteRC(t, tc.rc))
			}
			home := t.TempDir()
			env = append(env, "HOME="+home) // the last HOME wins
			dbPath := filepath.Join(adConfigDir(home), "state.db")
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
			if tc.locked {
				holdStoreLock(t, dbPath)
			}
			if tc.rc != "" { // a plain read shows the rc file takes effect
				plain := exec.Command("sqlite3", ":memory:", "SELECT 1")
				plain.Env = env
				if out, err := plain.Output(); err != nil || strings.TrimSpace(string(out)) == "1" {
					t.Fatalf("plain sqlite3 read under ~/.sqliterc %q printed %q (err %v); want it changed by the rc file", tc.rc, out, err)
				}
			}

			if tc.readOnly {
				if err := os.Chmod(filepath.Dir(dbPath), 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(dbPath), 0o700) })
			}
			roots := []string{filepath.Dir(dbPath)}
			before := snapshotTrees(t, roots)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if tc.locked {
				// No busy timeout: prove the holder still owns the lock after
				// snapshotTrees has opened and closed the original database.
				plain := exec.CommandContext(ctx, "sqlite3", "-batch", "-init", "/dev/null", dbPath,
					"SELECT value FROM store_meta WHERE key = 'store_id'")
				plain.Env = env
				out, err := plain.CombinedOutput()
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 5 || !strings.Contains(string(out), "database is locked (5)") {
					t.Fatalf("plain store read after snapshot: %v; output %q; want SQLITE_BUSY (5)", err, out)
				}
				t.Logf("plain store read after snapshot, before documented block: exit 5; %s", strings.TrimSpace(string(out)))
			}
			cmd := exec.CommandContext(ctx, "sh", "-c", block)
			cmd.Env = env
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if diff := before.diff(snapshotTrees(t, roots)); len(diff) != 0 {
				t.Errorf("store-id read changed the store folder: %s", strings.Join(diff, "; "))
			}
			if err != nil {
				t.Fatalf("operator-actions block %s: %v; stderr: %s", block, err, stderr.String())
			}
			if got := strings.TrimSpace(string(out)); got != want {
				t.Errorf("operator-actions block %s printed %q; Store.StoreID() is %q", block, got, want)
			}
		})
	}
}
