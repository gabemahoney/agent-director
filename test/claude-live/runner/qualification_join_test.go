package runner_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/sqlitelockfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// t3.xis.w3.an.yx: a failed second force must cancel the outstanding first
// caller, rather than hiding the error in a deferred receive until timeout.
func TestQualificationForceEarlyErrorJoin(t *testing.T) {
	kit := replayUnitKit(t)
	s := newSetupReplay(t, kit, replayCase{name: "force-early-error", phase: "force-states"})
	w := &workloadReplay{s: s, tc: workloadCase{phase: "force-states", kind: "tail-control"}, clock: tmuxfix.NewClock(s.r.Manifest.Started), starts: map[string]int64{}, journal: map[string][]runner.Row{}, blobs: map[string]bool{}, messages: map[string]workloadMessage{}, calls: map[string][]string{}, typed: map[string]string{}}
	s.workload = w
	s.r.Now, s.r.Commands.Now = w.clock.Now, w.clock.Now
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.cancel = cancel
	execute := s.r.Commands.Execute
	early := errors.New("synthetic second-force error")
	var calls atomic.Int32
	failed := make(chan time.Time, 1)
	stopped := make(chan struct{})
	s.r.Commands.Execute = func(ctx context.Context, argv, environment []string, out, errout io.Writer) (int, error) {
		if len(argv) > 1 && argv[1] == "force-delivery" {
			switch calls.Add(1) {
			case 2:
				code, err := execute(ctx, argv, environment, out, errout)
				if err != nil {
					return code, err
				}
				// Bound the broken implementation's red to three seconds. The
				// repaired producer cancels this command as soon as force #3 fails.
				timer := time.AfterFunc(3*time.Second, cancel)
				defer timer.Stop()
				<-ctx.Done()
				close(stopped)
				return -1, ctx.Err()
			case 3:
				failed <- time.Now()
				return -1, early
			}
		}
		return execute(ctx, argv, environment, out, errout)
	}
	var phaseErr error
	var elapsed time.Duration
	_, err := q.Execute(parent, s.r, func(ctx context.Context, r *runner.Run) error {
		phaseErr = q.Dispatch(ctx, r, s.options)
		select {
		case at := <-failed:
			elapsed = time.Since(at)
		default:
			t.Error("replay did not reach the second force error")
		}
		s.cleaning = true
		return phaseErr
	}, func(string) runner.Verdict { return q.PhaseResult(s.r.Manifest, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(phaseErr, early) {
		t.Fatalf("original second-force error lost: %v", phaseErr)
	}
	if elapsed >= time.Second {
		t.Errorf("early error blocked for %s waiting for force-delivery; want under one second", elapsed)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("outstanding force command was not cancelled")
	}
}

// The replay opens raw connections while the producer exports the same store.
// t3.xis.w3.an.yx: reads and writes must tolerate a bounded SQLite lock rather
// than calling Fatal from the outstanding command's goroutine (SQLITE_BUSY).
func TestQualificationReplaySQLiteContention(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			s := &setupReplay{t: t, r: &runner.Run{Home: t.TempDir()}}
			if err := os.MkdirAll(filepath.Dir(s.db()), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := apitest.InitStore(s.db()); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", s.db())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE replay_lock_probe (value TEXT); INSERT INTO replay_lock_probe VALUES ('before')"); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			base := db.Driver()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			release := sqlitelockfix.HoldExclusive(t, s.db())
			attempted, done := make(chan struct{}), make(chan struct{})
			var once sync.Once
			statement := "SELECT value FROM replay_lock_probe"
			if operation == "write" {
				statement = "UPDATE replay_lock_probe SET value='after'"
			}
			observed := sqliteobserve.Wrap(base, sqliteobserve.Hooks{Before: func(_ context.Context, query string) error {
				if query == statement {
					once.Do(func() { close(attempted) })
				}
				return nil
			}})
			s.openRaw = func(dsn string) (*sql.DB, error) {
				return sql.OpenDB(replayRawConnector{observed, dsn}), nil
			}
			// Unlock and collect even when an assertion or the helper fails.
			t.Cleanup(func() {
				release()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("database worker did not stop after lock release")
				}
			})
			var got string
			go func() {
				defer close(done)
				w := &workloadReplay{s: s}
				if operation == "write" {
					s.sql(statement)
				}
				got = w.query("SELECT value FROM replay_lock_probe")
			}()
			// Observe entry into the real driver with the lock still held;
			// scheduling a goroutine alone is not evidence of an attempted read.
			select {
			case <-attempted:
			case <-done:
				t.Fatal("database helper ended before attempting its statement")
			case <-time.After(5 * time.Second):
				t.Fatal("database helper did not attempt its statement under lock")
			}
			select {
			case <-done:
				t.Fatal("database helper completed while its exclusive lock was held")
			default:
			}
			release()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("database helper did not finish after lock release")
			}
			want := "before"
			if operation == "write" {
				want = "after"
			}
			if got != want {
				t.Fatalf("%s after contention = %q; want %q", operation, got, want)
			}
		})
	}
}

// Use the real SQLite driver with the shared statement observer, without a
// global driver registration or any replacement for actual SQLite results.
type replayRawConnector struct {
	base driver.Driver
	dsn  string
}

func (c replayRawConnector) Driver() driver.Driver { return c.base }
func (c replayRawConnector) Connect(context.Context) (driver.Conn, error) {
	return c.base.Open(c.dsn)
}
