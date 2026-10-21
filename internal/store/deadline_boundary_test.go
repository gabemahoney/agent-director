package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// deadlineBoundary wraps the existing SQLite observation connector at the driver boundary.
type deadlineBoundary struct {
	execHookConnector
	before      func(context.Context, string) error
	afterNext   func(context.Context, string, error)
	afterCommit func(context.Context) error
	afterExec   func(context.Context, string, error)
	closeFirst  func()
	opens       atomic.Int32
}

func (b *deadlineBoundary) Connect(ctx context.Context) (driver.Conn, error) {
	c, err := b.execHookConnector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &deadlineBoundaryConn{Conn: c, boundary: b, first: b.opens.Add(1) == 1}, nil
}

type deadlineBoundaryConn struct {
	driver.Conn
	boundary *deadlineBoundary
	first    bool
}

func (c *deadlineBoundaryConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if c.boundary.before != nil {
		if err := c.boundary.before(ctx, q); err != nil {
			return nil, err
		}
	}
	r, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if c.boundary.afterExec != nil {
		c.boundary.afterExec(ctx, q, err)
	}
	if err == nil && q == "COMMIT" && c.boundary.afterCommit != nil {
		err = c.boundary.afterCommit(ctx)
	}
	return r, err
}

func (c *deadlineBoundaryConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if c.boundary.before != nil {
		if err := c.boundary.before(ctx, q); err != nil {
			return nil, err
		}
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err == nil && c.boundary.afterNext != nil {
		return &deadlineBoundaryRows{Rows: rows, afterNext: func(err error) { c.boundary.afterNext(ctx, q, err) }}, nil
	}
	return rows, err
}

type deadlineBoundaryRows struct {
	driver.Rows
	afterNext func(error)
}

func (r *deadlineBoundaryRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	r.afterNext(err)
	return err
}

func (c *deadlineBoundaryConn) Close() error {
	if c.first && c.boundary.closeFirst != nil {
		c.boundary.closeFirst()
	}
	return c.Conn.Close()
}

func installDeadlineBoundary(t *testing.T, s *Store, path string) *deadlineBoundary {
	t.Helper()
	b := &deadlineBoundary{execHookConnector: execHookConnector{
		drv: s.db.Driver(), dsn: fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)", path, s.busyTimeoutMs), hook: func(string) {},
	}}
	db := sql.OpenDB(b)
	db.SetMaxOpenConns(1)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.db = db
	t.Cleanup(func() { _ = db.Close() })
	return b
}

// ObserveDecisionBoundaryForTest shares the real SQLite seam with external public recovery tests.
func ObserveDecisionBoundaryForTest(t *testing.T, s *Store, path string, before func(context.Context, string) error, afterCommit func(context.Context) error) {
	t.Helper()
	b := installDeadlineBoundary(t, s, path)
	b.before, b.afterCommit = before, afterCommit
}

// ObserveReturningRowsForTest shares actual native rows with this package's external tests.
func ObserveReturningRowsForTest(t *testing.T, s *Store, path string, afterNext func(context.Context, string, error)) func() {
	t.Helper()
	b, _ := installNativeBoundary(t, s, path)
	lease, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = lease.Close()
	b.afterNext = afterNext
	return func() { b.stopObservingAfterCleanup(t, s) }
}

func boundedDecision(s *Store, ctx context.Context, wait time.Duration) (bool, error) {
	return s.WithContext(ctx).DecideRelayRequest(relayID, tokenA, "allow", "", "test", time.Time{}, wait)
}

func (b *deadlineBoundary) stopObservingAfterCleanup(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.before, b.afterCommit, b.afterNext = nil, nil, nil
	_ = conn.Close()
}

func assertDeadlineConnectionReusable(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var busy int
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != s.busyTimeoutMs {
		t.Fatalf("reused busy_timeout=%d, %v; want %d", busy, err, s.busyTimeoutMs)
	}
	if _, err := s.db.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("reused connection has an open transaction: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
}

func assertDeadlineDatabaseReusable(t *testing.T, s *Store, path, decision string) {
	t.Helper()
	assertDeadlineConnectionReusable(t, s)
	withRaw(t, path, func(db *sql.DB) {
		var got sql.NullString
		if err := db.QueryRow("SELECT decision FROM permission_requests WHERE instance_id=? AND request_token=?", relayID, tokenA).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got.String != decision {
			t.Errorf("independent decision=%q; want %q", got.String, decision)
		}
	})
}

func TestDecisionSetupPragmaFailureBeforeBegin(t *testing.T) {
	ioFailure := errors.New("fixture: setup I/O failure")
	for _, tc := range []struct {
		name    string
		failure error
		busy    bool
	}{
		{"canceled", context.Canceled, true},
		{"deadline", context.DeadlineExceeded, true},
		{"ordinary error with canceled context", ioFailure, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			b := installDeadlineBoundary(t, s, path)
			// Establish the physical connection before the original setup budget.
			lease, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_ = lease.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			var setup, begins, commits atomic.Int32
			b.before = func(sqlCtx context.Context, q string) error {
				switch q {
				case "BEGIN IMMEDIATE":
					begins.Add(1)
				case "COMMIT":
					commits.Add(1)
				case "PRAGMA busy_timeout = 0":
					setup.Add(1)
					if tc.failure != context.DeadlineExceeded {
						cancel()
					}
					<-sqlCtx.Done()
					return fmt.Errorf("driver setup: %w", tc.failure)
				}
				return nil
			}
			start := time.Now()
			written, err := boundedDecision(s, ctx, 5*time.Second)
			if written || !errors.Is(err, tc.failure) || errors.Is(err, ErrStoreBusy) != tc.busy {
				t.Errorf("setup failure: written=%v err=%v; want cause=%v busy=%v", written, err, tc.failure, tc.busy)
			}
			if elapsed := time.Since(start); elapsed > 1100*time.Millisecond {
				t.Errorf("setup exceeded original deadline allowance: %v", elapsed)
			}
			b.stopObservingAfterCleanup(t, s)
			if setup.Load() != 1 || begins.Load() != 0 || commits.Load() != 0 {
				t.Errorf("setup=%d BEGIN=%d COMMIT=%d; want 1/0/0", setup.Load(), begins.Load(), commits.Load())
			}
			assertDeadlineDatabaseReusable(t, s, path, "")
			if b.opens.Load() != 2 {
				t.Errorf("failed setup connection reused: opens=%d; want replacement connection", b.opens.Load())
			}
		})
	}
}

func TestDecisionAbsoluteDeadlineAtSQLiteBoundaries(t *testing.T) {
	for _, phase := range []string{"precommit SQL", "BEGIN phase parking", "rollback", "restore", "commit response"} {
		t.Run(phase, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			b := installDeadlineBoundary(t, s, path)
			if phase == "commit response" {
				// Establish the replacement physical connection before the original SQL budget.
				lease, err := s.db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			deadline, _ := ctx.Deadline()
			var reached, commits atomic.Int32
			forced := errors.New("fixture: fail update before rollback")
			b.before = func(sqlCtx context.Context, q string) error {
				if got, ok := sqlCtx.Deadline(); !ok || got.After(deadline) {
					t.Errorf("%s received renewed SQL deadline %v, want <= %v", q, got, deadline)
				}
				if q == "COMMIT" {
					commits.Add(1)
				}
				update := strings.Contains(q, "UPDATE permission_requests")
				if phase == "rollback" && update {
					return forced
				}
				blocked := phase == "precommit SQL" && update || phase == "BEGIN phase parking" && q == "BEGIN IMMEDIATE" || phase == "rollback" && q == "ROLLBACK" || phase == "restore" && q == fmt.Sprintf("PRAGMA busy_timeout = %d", s.busyTimeoutMs)
				if blocked {
					reached.Add(1)
					<-sqlCtx.Done()
					return sqlCtx.Err()
				}
				return nil
			}
			if phase == "commit response" {
				b.afterCommit = func(sqlCtx context.Context) error { reached.Add(1); <-sqlCtx.Done(); return sqlCtx.Err() }
			}
			start := time.Now()
			written, err := boundedDecision(s, ctx, 5*time.Second)
			if elapsed := time.Since(start); elapsed > 150*time.Millisecond+time.Second {
				t.Errorf("deadline overrun: %v", elapsed)
			}
			if reached.Load() != 1 {
				t.Fatalf("actual %s boundary reached %d times", phase, reached.Load())
			}
			wantDecision := ""
			switch phase {
			case "restore":
				wantDecision = "allow"
				if !written || err != nil {
					t.Errorf("committed before canceled cleanup: %v, %v", written, err)
				}
			case "commit response":
				wantDecision = "allow"
				if written || err == nil || errors.Is(err, ErrStoreBusy) || !strings.Contains(err.Error(), "outcome unconfirmed") {
					t.Errorf("uncertain commit=%v, %v; want internal unconfirmed, never busy", written, err)
				}
			case "rollback":
				if written || !errors.Is(err, forced) {
					t.Errorf("precommit failure=%v, %v", written, err)
				}
			default:
				if written || !errors.Is(err, ErrStoreBusy) {
					t.Errorf("expired precommit=%v, %v", written, err)
				}
				if commits.Load() != 0 {
					t.Error("COMMIT attempted after precommit expiry")
				}
			}
			// Await cleanup through an ordinary pool lease before switching off the boundary.
			b.stopObservingAfterCleanup(t, s)
			assertDeadlineDatabaseReusable(t, s, path, wantDecision)
		})
	}
}

func TestDecisionCleanupQuarantinesNoncancelableClose(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	b := installDeadlineBoundary(t, s, path)
	closing, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	b.closeFirst = func() { close(closing); <-release }
	b.before = func(ctx context.Context, q string) error {
		if strings.Contains(q, "UPDATE permission_requests") {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	written, err := boundedDecision(s, ctx, time.Second)
	if written || !errors.Is(err, ErrStoreBusy) || time.Since(start) > 1100*time.Millisecond {
		t.Fatalf("decision=%v, %v after %v", written, err, time.Since(start))
	}
	select {
	case <-closing:
	case <-time.After(2 * time.Second):
		t.Fatal("discard never reached actual driver Close")
	}
	blocked, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if _, err := s.WithContext(blocked).GetPermissionRequestWithin(relayID, tokenA, time.Second); !errors.Is(err, ErrStoreBusy) {
		t.Errorf("quarantined lease escaped: %v", err)
	}
	if b.opens.Load() != 1 {
		t.Errorf("opened %d connections while disposal was blocked", b.opens.Load())
	}
	unblock()
	assertDeadlineDatabaseReusable(t, s, path, "")
	if b.opens.Load() != 2 {
		t.Errorf("unsafe connection reused after discard: opens=%d", b.opens.Load())
	}
}

func TestDecisionExpiredContextCannotRenewAcquisitionOrZeroWait(t *testing.T) {
	for _, wait := range []time.Duration{0, 5 * time.Second} {
		t.Run(wait.String(), func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			b := installDeadlineBoundary(t, s, path)
			var statements atomic.Int32
			b.before = func(context.Context, string) error { statements.Add(1); return nil }
			lease, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			<-ctx.Done()
			_ = lease.Close()
			written, err := boundedDecision(s, ctx, wait)
			if written || !errors.Is(err, ErrStoreBusy) || statements.Load() != 0 {
				t.Errorf("expired original context ran SQL: written=%v err=%v statements=%d", written, err, statements.Load())
			}
			assertDeadlineDatabaseReusable(t, s, path, "")
		})
	}
}

func TestDecisionLateAcquisitionRetainsOriginalSQLDeadline(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	b := installDeadlineBoundary(t, s, path)
	lease, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var reached atomic.Bool
	b.before = func(sqlCtx context.Context, q string) error {
		if got, ok := sqlCtx.Deadline(); !ok || got.After(deadline) {
			t.Errorf("late acquisition renewed SQL deadline: %v", got)
		}
		if strings.Contains(q, "UPDATE permission_requests") {
			reached.Store(true)
			<-sqlCtx.Done()
			return sqlCtx.Err()
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := boundedDecision(s, ctx, 5*time.Second); done <- err }()
	// Pool statistics prove the actual caller is waiting before acquisition is released.
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for s.db.Stats().WaitCount == 0 {
		select {
		case <-poll.C:
		case <-ctx.Done():
			t.Fatal("caller did not reach pool acquisition")
		}
	}
	late := time.NewTimer(time.Until(deadline.Add(-75 * time.Millisecond)))
	defer late.Stop()
	<-late.C
	_ = lease.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStoreBusy) || !reached.Load() {
			t.Errorf("late acquired SQL=%v, reached=%v", err, reached.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("late acquisition renewed total wait")
	}
	b.stopObservingAfterCleanup(t, s)
	assertDeadlineDatabaseReusable(t, s, path, "")
}

func TestDecisionZeroWaitAllowsSQLBeyondAcquisitionAllowance(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	b := installDeadlineBoundary(t, s, path)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var delayed bool
	b.before = func(sqlCtx context.Context, q string) error {
		if strings.Contains(q, "UPDATE permission_requests") {
			delayed = true
			got, ok := sqlCtx.Deadline()
			if !ok || got.After(deadline) || time.Until(got) <= 30*time.Millisecond {
				t.Errorf("zero-wait SQL inherited acquisition budget: %v", got)
			}
			timer := time.NewTimer(30 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-sqlCtx.Done():
				return sqlCtx.Err()
			}
		}
		return nil
	}
	written, err := boundedDecision(s, ctx, 0)
	if !written || err != nil || !delayed {
		t.Fatalf("zero-wait uncontended SQL=%v, %v, delayed=%v", written, err, delayed)
	}
	assertDeadlineDatabaseReusable(t, s, path, "allow")
	if b.opens.Load() != 1 {
		t.Errorf("timely cleanup unnecessarily discarded connection: opens=%d", b.opens.Load())
	}
}

// stalledDone delays each Done() past connGrab, as b.q5r's CPU throttling did:
// a deadline derived from it has passed by the time Done returns.
type stalledDone struct{ context.Context }

func (c stalledDone) Done() <-chan struct{} {
	// The defect needs real elapsed time. Sleep lasts at least this long, so
	// the derived deadline has passed however goroutines are scheduled.
	time.Sleep(2 * connGrab)
	return c.Context.Done()
}

// cancelOnErr ends the caller with cause the first time the store asks for its
// error, which a zero-wait call does only once refused a held connection.
type cancelOnErr struct {
	context.Context
	cancel context.CancelCauseFunc
	cause  error
}

func (c cancelOnErr) Err() error {
	c.cancel(c.cause)
	return context.Cause(c.Context)
}

// TestZeroWaitAdmissionAfterStall (b.q5r): a zero-wait call that reaches the
// free connection or operation slot only after its connGrab has elapsed still
// takes it, and a returned operation call has freed its slot. A connection
// another lease holds is refused at once after the call queues for it:
// ErrStoreBusy, also when the caller's deadline has passed by then, or the
// caller's own error if it was canceled.
func TestZeroWaitAdmissionAfterStall(t *testing.T) {
	const refused = "ErrStoreBusy: the store's connection was not free within 0 ms: another call of this process held it"
	grabConn := func(s *Store, ctx context.Context) error {
		conn, _, err := s.boundedConnContext(ctx, 0)
		if err == nil {
			_ = conn.Close()
		}
		return err
	}
	callerEnds := func(cause error) func(*Store, context.Context) error {
		return func(s *Store, ctx context.Context) error {
			ctx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)
			return grabConn(s, cancelOnErr{ctx, cancel, cause})
		}
	}
	runOperation := func(s *Store, ctx context.Context) error {
		// Hold the worker in its last step until the slot is checked: the
		// slot must already be free when the call returns, not soon after.
		hold := make(chan struct{})
		defer close(hold)
		s.cleanup.hookAfterOperationForTest = func() { <-hold }
		if err := s.runConnectionOperation(ctx, 0, func(*connectionOperation) error { return nil }); err != nil {
			return err
		}
		select {
		case s.cleanup.operationGate <- struct{}{}:
			<-s.cleanup.operationGate
			return nil
		default:
			return errors.New("a returned operation call still held the slot")
		}
	}
	for _, tc := range []struct {
		name    string
		held    bool   // another lease holds the store's one connection
		wantErr string // "" for none
		// An admission that arms connGrab before taking a free slot fails
		// only 1 time in 2 (random select), so repeat.
		attempts int
		admit    func(*Store, context.Context) error
	}{
		{"free connection", false, "", 1, grabConn},
		{"connection held by another lease", true, refused, 1, grabConn},
		{"caller canceled once refused a held connection", true, "store: connection: context canceled", 1, callerEnds(context.Canceled)},
		{"caller's deadline passed once refused a held connection", true, refused, 1, callerEnds(context.DeadlineExceeded)},
		{"free operation slot", false, "", 20, runOperation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTempStore(t)
			if tc.held {
				defer holdConnection(t, s)()
			}
			if st := s.db.Stats(); st.OpenConnections != 1 || (st.InUse == 1) != tc.held {
				t.Fatalf("pool before the call: %+v; want its one connection held=%v", st, tc.held)
			}
			for i := 1; i <= tc.attempts; i++ {
				ctx, cancel := s.operationContext(0)
				waits, start := s.db.Stats().WaitCount, time.Now()
				err := tc.admit(s, stalledDone{ctx})
				elapsed := time.Since(start)
				cancel()
				got := ""
				if err != nil {
					got = err.Error()
				}
				if got != tc.wantErr {
					t.Fatalf("attempt %d after a stall = %q; want %q", i, got, tc.wantErr)
				}
				// Refused only once it queued behind the holder, and at once.
				if queued := s.db.Stats().WaitCount - waits; tc.held && (queued != 1 || elapsed > 250*time.Millisecond) {
					t.Fatalf("refused after queuing %d times, %v; want once, at once", queued, elapsed)
				}
			}
		})
	}
}

// TestConnGrabStartsAtSecondLook (b.q5r): database/sql looks at
// connGrabContext's Done once to take a free connection and twice to wait for
// a held one; only the second look starts the grab, and a look after release
// arms nothing.
func TestConnGrabStartsAtSecondLook(t *testing.T) {
	s, _ := openTempStore(t)
	free := newConnGrabContext(context.Background(), s.db)
	conn, err := s.db.Conn(free)
	if err != nil {
		t.Fatalf("the free connection was refused: %v", err)
	}
	_ = conn.Close()
	if free.looks != 1 || free.timer != nil {
		t.Fatalf("taking the free connection: %d looks, timer armed=%v; want 1 look, unarmed", free.looks, free.timer != nil)
	}
	free.release()
	free.Done()
	if free.timer != nil {
		t.Error("a look after release armed the grab timer")
	}

	defer holdConnection(t, s)()
	held := newConnGrabContext(context.Background(), s.db)
	defer held.release()
	start := time.Now()
	if conn, err := s.db.Conn(held); err == nil {
		_ = conn.Close()
		t.Fatal("the held connection was taken; want it refused once the grab ran out")
	}
	if cause, elapsed := context.Cause(held), time.Since(start); held.looks != 2 || cause != errConnGrabElapsed || elapsed < connGrab {
		t.Errorf("held connection: %d looks, grab ended with %v after %v; want 2 looks, %v after at least %v", held.looks, cause, elapsed, errConnGrabElapsed, connGrab)
	}
}
