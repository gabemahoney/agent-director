package store

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// envFreshInitHelper is the env var that flips this test binary into
// helper-process mode for TestConcurrentFreshInit_NoSqliteBusyRace. The
// helper does a single OpenOrInit against the path the parent passes via
// the same env var (value = DB path), then exits 0 on success / 1 with the
// error printed to stderr on failure. See go-style subprocess test pattern
// (Go stdlib uses the same trick in os/exec, syscall, etc.).
const envFreshInitHelper = "AD_STORE_TEST_FRESH_INIT_HELPER"

// TestMain dispatches to the helper-process code path before the testing
// framework starts when the helper env var is set. This keeps the helper
// invisible to `go test -list`/normal test runs while letting the
// concurrent-fresh-init test fork the same binary as N subprocesses.
//
// For non-helper runs, TestMain also fixes AGENT_DIRECTOR_STATE_DIR to a
// temp dir so the trail singleton writes to a known location rather than
// ~/.agent-director/. Trail tests (trail_emit_test.go) read storeTrailDir
// to locate the file.
func TestMain(m *testing.M) {
	if dbPath := os.Getenv(envFreshInitHelper); dbPath != "" {
		freshInitHelperMain(dbPath)
		return
	}
	d, err := os.MkdirTemp("", "ad-store-trail-*")
	if err != nil {
		panic("TestMain: MkdirTemp: " + err.Error())
	}
	defer os.RemoveAll(d)
	storeTrailDir = d
	if err := os.Setenv("AGENT_DIRECTOR_STATE_DIR", d); err != nil {
		panic("TestMain: Setenv: " + err.Error())
	}
	os.Exit(m.Run())
}

// freshInitHelperMain runs a single OpenOrInit against dbPath and exits
// 0 on success / 1 with the error on stderr. The parent test interprets a
// non-zero exit (especially one mentioning SQLITE_BUSY) as a repro of b.ng9.
func freshInitHelperMain(dbPath string) {
	s, err := OpenOrInit(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	if err := s.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

// TestConcurrentOpenOrInitWithWrites is the regression gate for b.x2m
// (burst-spawned hooks fail with SQLITE_BUSY because every new connection
// re-ran PRAGMA journal_mode=WAL and the DSN had no busy_timeout).
//
// N goroutines simultaneously OpenOrInit the same DB file, then each
// performs an insert + state transition (the exact pattern a SessionStart
// hook executes). All must succeed; any SQLITE_BUSY surfacing here means
// the fix has regressed.
func TestConcurrentOpenOrInitWithWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	const workers = 20

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(workers)
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer done.Done()
			start.Wait()

			s, err := OpenOrInit(path)
			if err != nil {
				errs <- fmt.Errorf("worker %d: OpenOrInit: %w", i, err)
				return
			}
			defer s.Close()

			sp := Spawn{
				ClaudeInstanceID: fmt.Sprintf("burst-worker-%02d", i),
				State:            StatePending,
				CWD:              "/tmp",
				TmuxSessionName:  fmt.Sprintf("cd-burst-%02d", i),
				RelayMode:        "off",
			}
			if err := s.InsertPending(sp); err != nil {
				errs <- fmt.Errorf("worker %d: InsertPending: %w", i, err)
				return
			}
			if err := s.ApplyHookTransition(sp.ClaudeInstanceID, StateWaiting, false, "test_seed"); err != nil {
				errs <- fmt.Errorf("worker %d: ApplyHookTransition: %w", i, err)
				return
			}
		}()
	}

	start.Done()
	done.Wait()
	close(errs)

	var failed int
	for err := range errs {
		t.Error(err)
		failed++
	}
	if failed > 0 {
		t.Fatalf("%d/%d concurrent OpenOrInit+write attempts failed", failed, workers)
	}

	// Sanity check: every worker's row landed in the waiting state.
	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("post-burst OpenOrInit: %v", err)
	}
	defer s.Close()
	for i := 0; i < workers; i++ {
		id := fmt.Sprintf("burst-worker-%02d", i)
		state, err := s.GetSpawnState(id)
		if err != nil {
			t.Errorf("GetSpawnState(%s): %v", id, err)
			continue
		}
		if state != StateWaiting {
			t.Errorf("%s: state = %q, want %q", id, state, StateWaiting)
		}
	}
}

// TestConcurrentFreshInit_NoSqliteBusyRace is the regression gate for b.ng9
// (cross-process SQLITE_BUSY race on PRAGMA journal_mode=WAL during fresh-DB
// init). N subprocesses simultaneously OpenOrInit the same nonexistent path;
// without the flock serialization in ensureJournalModeWAL one or more losers
// crash with "store: set journal_mode=wal: ... SQLITE_BUSY" because SQLite's
// busy_timeout does not retry the EXCLUSIVE-lock acquisition that the
// WAL-mode transition performs.
//
// In-process goroutine concurrency (see TestConcurrentOpenOrInitWithWrites)
// is insufficient: every goroutine shares the same *sql.DB pool and lock,
// so the journal-mode write only happens once. The race only manifests when
// each caller has its own pool — i.e. across processes.
func TestConcurrentFreshInit_NoSqliteBusyRace(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess fan-out is slow under -short")
	}
	dbPath := filepath.Join(t.TempDir(), "state.db")
	const procs = 8

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(procs)

	type result struct {
		i      int
		err    error
		stderr string
	}
	results := make(chan result, procs)

	for i := 0; i < procs; i++ {
		i := i
		go func() {
			defer done.Done()
			start.Wait()

			cmd := exec.Command(os.Args[0], "-test.run=^$") //nolint:gosec // path is the test binary itself
			cmd.Env = append(os.Environ(), envFreshInitHelper+"="+dbPath)
			out, err := cmd.CombinedOutput()
			results <- result{i: i, err: err, stderr: string(out)}
		}()
	}

	start.Done()
	done.Wait()
	close(results)

	var failed int
	for r := range results {
		if r.err != nil {
			failed++
			t.Errorf("helper proc %d: %v\noutput:\n%s", r.i, r.err, r.stderr)
			if strings.Contains(r.stderr, "SQLITE_BUSY") {
				t.Logf("helper proc %d hit SQLITE_BUSY (b.ng9 repro)", r.i)
			}
		}
	}
	if failed > 0 {
		t.Fatalf("%d/%d concurrent fresh-init subprocesses failed", failed, procs)
	}

	// Final state: DB file exists and reports journal_mode=wal.
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("post-race Open: %v", err)
	}
	defer s.Close()
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("post-race journal_mode = %q, want wal", mode)
	}
}
