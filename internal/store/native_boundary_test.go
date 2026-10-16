package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// nativeBoundaryDriver retains the real driver and production connector initialization.
type nativeBoundaryDriver struct {
	driver.Driver
	boundary *deadlineBoundary
	open     func()
}

func (d *nativeBoundaryDriver) Open(path string) (driver.Conn, error) {
	first := d.boundary.opens.Add(1) == 1
	if d.open != nil {
		d.open()
	}
	c, err := d.Driver.Open(path)
	if err != nil {
		return nil, err
	}
	return &deadlineBoundaryConn{Conn: c, boundary: d.boundary, first: first}, nil
}

func installNativeBoundary(t *testing.T, s *Store, path string) (*deadlineBoundary, *nativeBoundaryDriver) {
	t.Helper()
	b := &deadlineBoundary{}
	d := &nativeBoundaryDriver{Driver: s.db.Driver(), boundary: b}
	db := sql.OpenDB(sqliteConnector{driver: d, path: path, busyTimeoutMs: s.busyTimeoutMs})
	db.SetMaxOpenConns(1)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = db
	t.Cleanup(func() { _ = db.Close() })
	return b, d
}

// nativeBarrier always releases, including assertion failures and old-source overruns.
func nativeBarrier(t *testing.T) (func(), func(), <-chan struct{}) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	timer := time.AfterFunc(3*time.Second, unblock)
	t.Cleanup(func() { unblock(); timer.Stop() })
	return func() { enterOnce.Do(func() { close(entered); <-release }) }, unblock, entered
}

func awaitNativeEvent(t *testing.T, event <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(2 * time.Second):
		t.Fatalf("native boundary not reached: %s", name)
	}
}

func TestDecisionUncooperativeNativeBoundaries(t *testing.T) {
	for _, phase := range []string{"open", "initialization", "zero initialization", "setup", "rows next", "precommit", "commit response", "close"} {
		t.Run(phase, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			before := mustRequest(t, s, tokenA)
			b, drv := installNativeBoundary(t, s, path)
			if phase != "open" && phase != "initialization" && phase != "zero initialization" {
				lease, err := s.db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				_ = lease.Close()
			}
			block, release, entered := nativeBarrier(t)
			var statements, commits, callbacks atomic.Int32
			type contextKey struct{}
			parent := context.WithValue(context.Background(), contextKey{}, phase)
			ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
			defer cancel()
			deadline, _ := ctx.Deadline()
			b.before = func(sqlCtx context.Context, q string) error {
				if b.opens.Load() > 1 {
					return nil
				}
				statements.Add(1)
				got, ok := sqlCtx.Deadline()
				if !ok || !got.Equal(deadline) || sqlCtx.Value(contextKey{}) != phase {
					t.Errorf("%s lost original context: deadline=%v marker=%v", q, got, sqlCtx.Value(contextKey{}))
				}
				if q == "COMMIT" {
					commits.Add(1)
				}
				if (phase == "initialization" || phase == "zero initialization") && strings.HasPrefix(q, "PRAGMA busy_timeout = ") || phase == "setup" && q == "PRAGMA busy_timeout = 0" || phase == "precommit" && strings.Contains(q, "UPDATE permission_requests") {
					callbacks.Add(1)
					block()
				}
				if phase == "close" && strings.Contains(q, "UPDATE permission_requests") {
					<-sqlCtx.Done()
					return sqlCtx.Err()
				}
				return nil
			}
			if phase == "open" {
				drv.open = func() { callbacks.Add(1); block() }
			}
			if phase == "rows next" {
				b.afterNext = func(context.Context, string, error) { callbacks.Add(1); block() }
			}
			if phase == "commit response" {
				b.afterCommit = func(context.Context) error { callbacks.Add(1); block(); return nil }
			}
			if phase == "close" {
				b.closeFirst = func() { callbacks.Add(1); block() }
			}
			type result struct {
				written bool
				err     error
			}
			done := make(chan result, 1)
			start := time.Now()
			go func() {
				if phase == "rows next" {
					_, err := s.WithContext(ctx).GetPermissionRequestWithin(relayID, tokenA, 5*time.Second)
					done <- result{err: err}
					return
				}
				wait := 5 * time.Second
				if phase == "zero initialization" {
					wait = 0
				}
				written, err := boundedDecision(s, ctx, wait)
				done <- result{written, err}
			}()
			awaitNativeEvent(t, entered, phase)
			var got result
			select {
			case got = <-done:
			case <-time.After(1500*time.Millisecond - time.Since(start)):
				release()
				got = <-done
				t.Errorf("native %s blocked caller past original 500ms + 1s", phase)
			}
			if phase == "commit response" {
				if got.written || got.err == nil || errors.Is(got.err, ErrStoreBusy) || !strings.Contains(got.err.Error(), "outcome unconfirmed") {
					t.Errorf("matched COMMIT=%+v; want unconfirmed, never busy", got)
				}
			} else if got.written || !errors.Is(got.err, ErrStoreBusy) {
				t.Errorf("precommit/native result=%+v; want Busy and no confirmed write", got)
			}
			initialStatements, initialCallbacks := statements.Load(), callbacks.Load()
			for i := 0; i < 3; i++ {
				retryCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
				_, err := s.WithContext(retryCtx).GetPermissionRequestWithin(relayID, tokenA, time.Second)
				stop()
				if !errors.Is(err, ErrStoreBusy) {
					t.Errorf("call %d bypassed quarantined owner: %v", i, err)
				}
			}
			zeroStart := time.Now()
			if _, err := boundedDecision(s, context.Background(), 0); !errors.Is(err, ErrStoreBusy) || time.Since(zeroStart) > time.Second {
				t.Errorf("zero-wait admission bypassed quarantine or inherited 1s work wait: %v after %v", err, time.Since(zeroStart))
			}
			if b.opens.Load() != 1 || statements.Load() != initialStatements || callbacks.Load() != initialCallbacks {
				t.Errorf("native work accumulated: opens=%d SQL=%d/%d callbacks=%d/%d", b.opens.Load(), statements.Load(), initialStatements, callbacks.Load(), initialCallbacks)
			}
			release()
			b.stopObservingAfterCleanup(t, s)
			if phase == "open" && statements.Load() != 0 {
				t.Error("expired native opening initiated initialization SQL")
			}
			wantDecision := ""
			if phase == "commit response" {
				wantDecision = "allow"
			} else if commits.Load() != 0 {
				t.Errorf("precommit expiry initiated late COMMIT: %d", commits.Load())
			}
			assertDeadlineDatabaseReusable(t, s, path, wantDecision)
			after := mustRequest(t, s, tokenA)
			if wantDecision != "" {
				if after.DecidedAt.IsZero() {
					t.Error("durable COMMIT lacks decided_at")
				}
				expected := before
				expected.Decision, expected.DecidedAt = "allow", after.DecidedAt
				if !reflect.DeepEqual(after, expected) {
					t.Errorf("uncertain COMMIT changed unrelated request facts: before=%+v after=%+v", before, after)
				}
			}
			if wantDecision == "" && !reflect.DeepEqual(after, before) {
				t.Errorf("abandoned operation changed request: before=%+v after=%+v", before, after)
			}
			if b.opens.Load() != 2 {
				t.Errorf("unsafe connection returned instead of replaced: opens=%d", b.opens.Load())
			}
		})
	}
}

func TestProductionSQLiteConnectorInitializesEveryReplacement(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	configured, err := OpenWithBusyTimeout(path, 137)
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	s = configured
	b, _ := installNativeBoundary(t, s, path)
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, err := s.db.Conn(ctx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var foreign, busy int
		if err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreign); err != nil {
			t.Fatal(err)
		}
		if err = conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if foreign != 1 || busy != s.busyTimeoutMs {
			t.Errorf("connection %d foreign_keys=%d busy=%d want 1/%d", i, foreign, busy, s.busyTimeoutMs)
		}
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
		cancel()
	}
	if b.opens.Load() != 3 {
		t.Errorf("physical connections=%d want 3", b.opens.Load())
	}
}

func TestRelayNativeReturningRowsCannotPublishLateOutputs(t *testing.T) {
	for _, ack := range []bool{false, true} {
		name := "timeout deny"
		if ack {
			name = "ack"
		}
		t.Run(name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			if ack {
				decideA(t, s, "allow")
			}
			before := mustRequest(t, s, tokenA)
			block, release, entered := nativeBarrier(t)
			stopObserving := ObserveReturningRowsForTest(t, s, path, func(_ context.Context, q string, _ error) {
				if strings.Contains(q, "UPDATE permission_requests") {
					block()
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			type result struct {
				decision, reason string
				written          bool
				err              error
			}
			done := make(chan result, 1)
			start := time.Now()
			go func() {
				if ack {
					d, r, w, e := s.WithContext(ctx).AckRelayDecision(relayID, tokenA, time.Now(), 5*time.Second, nil)
					done <- result{d, r, w, e}
					return
				}
				w, e := s.WithContext(ctx).DenyRelayTimeout(relayID, tokenA, time.Now(), 5*time.Second, nil)
				done <- result{written: w, err: e}
			}()
			awaitNativeEvent(t, entered, name)
			select {
			case got := <-done:
				if got.decision != "" || got.reason != "" || got.written || !errors.Is(got.err, ErrStoreBusy) {
					t.Errorf("late native row leaked outputs: %+v", got)
				}
			case <-time.After(1500*time.Millisecond - time.Since(start)):
				t.Error("late native row blocked caller past 500ms + 1s")
			}
			release()
			stopObserving()
			if after := mustRequest(t, s, tokenA); !reflect.DeepEqual(before, after) {
				t.Errorf("uncommitted late row changed request: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestProductionSQLiteConnectorCancellationBetweenPragmas(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	b, _ := installNativeBoundary(t, s, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var init atomic.Int32
	b.before = func(_ context.Context, q string) error {
		if strings.HasPrefix(q, "PRAGMA ") {
			init.Add(1)
		}
		return nil
	}
	b.afterExec = func(_ context.Context, q string, err error) {
		if strings.HasPrefix(q, "PRAGMA busy_timeout = ") {
			if err != nil {
				t.Errorf("first pragma failed before cancellation: %v", err)
			}
			cancel()
		}
	}
	if conn, err := s.db.Conn(ctx); err == nil {
		_ = conn.Close()
		t.Fatal("partially initialized connection was borrowed")
	}
	if init.Load() != 1 {
		t.Errorf("initialization statements=%d want only first pragma", init.Load())
	}
	b.before = nil
	b.afterExec = nil
	assertDeadlineDatabaseReusable(t, s, path, "")
	if b.opens.Load() != 2 {
		t.Errorf("partial connection not replaced: %d", b.opens.Load())
	}
}

func TestProductionSQLiteConnectorDefaultAndZeroWait(t *testing.T) {
	for _, wait := range []time.Duration{DefaultLockWait, 0} {
		t.Run(fmt.Sprint(wait), func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			b, _ := installNativeBoundary(t, s, path)
			var delayed atomic.Bool
			b.before = func(ctx context.Context, q string) error {
				if strings.HasPrefix(q, "PRAGMA busy_timeout = ") && !delayed.Swap(true) {
					if wait == 0 {
						if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < 100*time.Millisecond || time.Until(deadline) > time.Second {
							t.Errorf("initialization got admission instead of original work budget: %v", deadline)
						}
					} else if _, ok := ctx.Deadline(); ok {
						t.Error("default wait unexpectedly bounded")
					}
					time.Sleep(30 * time.Millisecond)
				}
				return nil
			}
			written, err := boundedDecision(s, context.Background(), wait)
			if !written || err != nil || !delayed.Load() {
				t.Fatalf("uncontended initialization=%v,%v delayed=%v", written, err, delayed.Load())
			}
			assertDeadlineDatabaseReusable(t, s, path, "allow")
		})
	}
}
