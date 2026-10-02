package driverscripts_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// holdFor is how long a test keeps the lock held after starting a reader that
// should wait it out.
const holdFor = 300 * time.Millisecond

// requireSqlite3 fails inside the sandbox (its image installs sqlite3) and
// skips elsewhere when sqlite3 is not on PATH.
func requireSqlite3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		if os.Getenv(sandboxguard.EnvVar) != "" {
			t.Fatalf("sqlite3 not on PATH inside the sandbox: the sandbox image must install it: %v", err)
		}
		t.Skip("sqlite3 not on PATH")
	}
}

// newStore is a WAL-mode db in a temp dir with t(v) = 'tok-abc' and a spawns
// row id-x that records no pane.
func newStore(t *testing.T) string {
	t.Helper()
	requireSqlite3(t)
	db := filepath.Join(t.TempDir(), "state.db")
	r := run(t, nil, "", "sqlite3", db, `PRAGMA journal_mode=WAL;
CREATE TABLE t(v TEXT); INSERT INTO t VALUES ('tok-abc');
CREATE TABLE spawns(claude_instance_id TEXT, pane_id TEXT, tmux_socket TEXT);
INSERT INTO spawns VALUES ('id-x', NULL, NULL);`)
	if r.code != 0 {
		t.Fatalf("create store: %s", r)
	}
	return db
}

// holdLock returns once a sqlite3 shell holds db's exclusive lock (in WAL mode
// BEGIN EXCLUSIVE alone does not block readers); release, also run at cleanup, ends it.
func holdLock(t *testing.T, db string) (release func()) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "locked")
	cmd := exec.Command("sqlite3", db)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = stdin.Close()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
	}
	t.Cleanup(release)
	fmt.Fprintf(stdin, "PRAGMA locking_mode=EXCLUSIVE;\nBEGIN EXCLUSIVE;\nSELECT 1;\n.system touch \"%s\"\n", marker)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			return release
		}
		select {
		case <-done:
			t.Fatalf("lock holder exited before taking the lock: %s", stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("lock holder did not take the lock within 10s")
		}
	}
}

// result is one finished command.
type result struct {
	code           int
	stdout, stderr string
	elapsed        time.Duration
}

func (r result) String() string {
	return fmt.Sprintf("exit %d after %v, stdout %q, stderr %q", r.code, r.elapsed, r.stdout, r.stderr)
}

// proc is a started command; wait returns its result.
type proc struct {
	done chan struct{}
	res  result
}

// start runs name args in a temp dir with env added to ours (minus any
// inherited SQL_BUSY_TIMEOUT_MS), stdin as its input, bounded at 30s.
func start(t *testing.T, env []string, stdin, name string, args ...string) *proc {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = t.TempDir()
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SQL_BUSY_TIMEOUT_MS=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	p := &proc{done: make(chan struct{})}
	began := time.Now()
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start %s: %v", name, err)
	}
	go func() {
		defer cancel()
		err := cmd.Wait()
		p.res = result{code: cmd.ProcessState.ExitCode(), stdout: out.String(), stderr: errb.String(), elapsed: time.Since(began)}
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			p.res.stderr += fmt.Sprintf("[wait: %v]", err)
		}
		close(p.done)
	}()
	return p
}

func (p *proc) wait() result {
	<-p.done
	return p.res
}

func (p *proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func run(t *testing.T, env []string, stdin, name string, args ...string) result {
	t.Helper()
	return start(t, env, stdin, name, args...).wait()
}

func driverScript(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "test", "driver", name)
}

// TestHeldLockFailsReadsWithoutAWait: under a held lock a bare sqlite3 read,
// or sql.sh with no wait, fails with "database is locked"; sql.sh with a
// wait gives up only after it, and well before the 5000 ms default.
func TestHeldLockFailsReadsWithoutAWait(t *testing.T) {
	db := newStore(t)
	sqlSh := driverScript(t, "sql.sh")
	holdLock(t, db)
	const maxWaited = 3 * time.Second // a sql.sh that ignored SQL_BUSY_TIMEOUT_MS would wait 5 s
	for _, tc := range []struct {
		name      string
		env       []string
		cmd       string
		minWaited time.Duration
	}{
		{"bare sqlite3", nil, "sqlite3", 0},
		{"sql.sh with SQL_BUSY_TIMEOUT_MS=0", []string{"SQL_BUSY_TIMEOUT_MS=0"}, sqlSh, 0},
		{"sql.sh with SQL_BUSY_TIMEOUT_MS=300", []string{"SQL_BUSY_TIMEOUT_MS=300"}, sqlSh, 300 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, tc.env, "", tc.cmd, "-readonly", db, "SELECT v FROM t;")
			if r.code != 5 || !strings.Contains(r.stderr, "database is locked") {
				t.Fatalf("want exit 5 and \"database is locked\": %s", r)
			}
			if r.elapsed < tc.minWaited || r.elapsed >= maxWaited {
				t.Errorf("gave up after %v, want at least %v and under %v: %s", r.elapsed, tc.minWaited, maxWaited, r)
			}
		})
	}
}

// TestDriverReadsWaitOutAHeldLock: sql.sh with its default wait, and
// pane-hook.sh's row lookup through it, are still waiting while the lock is
// held and finish normally once it is released (b.ai5).
func TestDriverReadsWaitOutAHeldLock(t *testing.T) {
	db := newStore(t)
	for _, tc := range []struct {
		name     string
		env      []string
		script   string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"sql.sh read", nil, "sql.sh", []string{"-readonly", db, "SELECT v FROM t;"}, 0, "tok-abc\n", ""},
		{"pane-hook.sh row lookup", []string{"PANE_HOOK_DB=" + db}, "pane-hook.sh", []string{"id-x", "{}"}, 3, "", "row id-x records no pane"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := holdLock(t, db)
			p := start(t, tc.env, "", driverScript(t, tc.script), tc.args...)
			time.Sleep(holdFor)
			if !p.running() {
				t.Fatalf("exited while the lock was held, want it to wait: %s", p.wait())
			}
			release()
			r := p.wait()
			if r.code != tc.wantCode || r.stdout != tc.wantOut || !strings.Contains(r.stderr, tc.wantErr) {
				t.Fatalf("want exit %d, stdout %q, stderr containing %q: %s", tc.wantCode, tc.wantOut, tc.wantErr, r)
			}
		})
	}
}

// TestSQLShPassesArgumentsThrough: sql.sh's flags, SQL and stdin reach sqlite3
// unchanged, so its exit status and output match a bare sqlite3's.
func TestSQLShPassesArgumentsThrough(t *testing.T) {
	db := newStore(t)
	sqlSh := driverScript(t, "sql.sh")
	for _, tc := range []struct {
		name    string
		args    []string
		stdin   string
		wantOut string
		wantErr string // substring; "" wants exit 0
	}{
		{"-separator", []string{"-readonly", "-separator", "|", db, "SELECT rowid, v FROM t;"}, "", "1|tok-abc\n", ""},
		{"-readonly rejects a write", []string{"-readonly", db, "INSERT INTO t VALUES ('w');"}, "", "", "readonly"},
		{"SQL on stdin", []string{db}, "SELECT v FROM t;\n", "tok-abc\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, nil, tc.stdin, sqlSh, tc.args...)
			bare := run(t, nil, tc.stdin, "sqlite3", tc.args...)
			if got.code != bare.code || got.stdout != bare.stdout || got.stderr != bare.stderr {
				t.Errorf("sql.sh: %s\nsqlite3: %s\nwant the same exit status and output", got, bare)
			}
			if got.stdout != tc.wantOut || (tc.wantErr == "") != (got.code == 0) || !strings.Contains(got.stderr, tc.wantErr) {
				t.Errorf("want stdout %q and stderr containing %q (exit 0 iff none): %s", tc.wantOut, tc.wantErr, got)
			}
		})
	}
	if r := run(t, nil, "", "sqlite3", "-readonly", db, "SELECT count(*) FROM t;"); r.stdout != "1\n" {
		t.Errorf("t has %q rows after the -readonly write, want 1", r.stdout)
	}
}

// TestSQLShRejectsANonIntegerTimeout: a SQL_BUSY_TIMEOUT_MS that is not whole
// milliseconds exits 2 before sqlite3 runs.
func TestSQLShRejectsANonIntegerTimeout(t *testing.T) {
	db := newStore(t)
	sqlSh := driverScript(t, "sql.sh")
	for _, v := range []string{"abc", "-1", "1.5", "5s"} {
		t.Run(v, func(t *testing.T) {
			r := run(t, []string{"SQL_BUSY_TIMEOUT_MS=" + v}, "", sqlSh, db, "INSERT INTO t VALUES ('w');")
			want := "SQL_BUSY_TIMEOUT_MS=" + v + ": want whole milliseconds"
			if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, want) {
				t.Errorf("want exit 2 and %q: %s", want, r)
			}
		})
	}
	if r := run(t, nil, "", "sqlite3", "-readonly", db, "SELECT count(*) FROM t;"); r.stdout != "1\n" {
		t.Errorf("t has %q rows, want 1: sqlite3 ran despite the bad timeout", r.stdout)
	}
}
