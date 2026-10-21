package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrStoreBusy marks a bounded-wait store call that could not take what it
// waits for within its wait (b.146 problem 1, decision 9 B): the store's
// write lock, which another process (or another connection) held for longer,
// or the store's one connection, which another call of this process held for
// longer. It also marks such a call that its own context ended before its
// first statement (a cancellation, or a deadline under DefaultLockWait); the
// error then wraps the context's error too (operationBusy). Nothing was
// written. Callers detect it with errors.Is. pkg/api re-exports it as the
// catalogued error decide returns when its max_wait_ms bound is reached before
// the verdict commits.
var ErrStoreBusy = errors.New("ErrStoreBusy")

// DefaultLockWait asks a bounded-wait store call for the store's own waits,
// as every other store call waits: for the store's one connection without a
// bound, and for a lock another connection holds up to the store's busy
// timeout ([store] busy_timeout_ms).
const DefaultLockWait time.Duration = -1

// connGrab is how long a zero-wait call waits for the store's one connection,
// or for a bounded operation's admission (runConnectionOperation), once it
// has to wait because another call of this process holds it. The wait counts
// from then, never from when the call was made, so a call with no wait left
// (a reading verb's hook_gone_at write, b.146 problem 4) takes a free
// connection or slot however long a scheduling stall delays it on its way
// (b.q5r; connGrabContext says what that relies on in database/sql). It is
// too short to amount to a wait for a holder.
const connGrab = time.Millisecond

// A zero lock wait permits an uncontended write, including its commit. It
// cannot mean zero execution time. This fixed budget fits the existing one
// second return allowance and is always cut by an enclosing call deadline.
const immediateWorkBudget = time.Second

// WithContext returns a per-call view of the same pool. It does not own the
// pool and must not be closed independently of the original Store. Existing
// duration-based methods remain available to callers without a context.
func (s *Store) WithContext(ctx context.Context) *Store {
	scoped := *s
	scoped.callContext = ctx
	return &scoped
}

func (s *Store) operationContext(maxWait time.Duration) (context.Context, context.CancelFunc) {
	ctx := s.callContext
	if ctx == nil {
		ctx = context.Background()
	}
	if maxWait < 0 {
		return ctx, func() {}
	}
	if maxWait == 0 {
		return context.WithTimeout(ctx, immediateWorkBudget)
	}
	return context.WithTimeout(ctx, maxWait)
}

// isBusy reports whether err is SQLite's SQLITE_BUSY (any extended code): a
// lock another connection holds was not released within the busy timeout.
func isBusy(err error) bool {
	var serr *sqlite.Error
	return errors.As(err, &serr) && serr.Code()&0xff == sqlite3.SQLITE_BUSY
}

// boundedConnContext takes the store's one connection for a call whose waits are
// bounded by maxWait, and returns it with the busy timeout, in whole
// milliseconds, the call's statements may wait for a lock another connection
// holds.
//
// DefaultLockWait (any negative wait): it waits for the connection as every
// other store call does, without a bound, and returns the store's busy
// timeout. Otherwise both waits share one deadline, maxWait from now: it
// waits for the connection, which the pool's one connection makes a wait for
// any other call of this process using the store, until the deadline, and
// returns what is left of maxWait after it as the busy timeout, cut at the
// store's busy timeout (a bound never waits longer than the store would) and
// never below 0, which makes SQLite answer busy at once. Zero wait takes a
// free connection however late it reaches the pool, waits connGrab for one
// another call holds (connGrabContext, with its limits), and returns a busy
// timeout of 0. A connection not taken in time is an error wrapping
// ErrStoreBusy. When the caller's own context ends the wait in any other way
// (its cancellation, or its deadline under DefaultLockWait), the error this
// returns does not wrap ErrStoreBusy; withConnOperation names it busy.
func (s *Store) boundedConnContext(ctx context.Context, maxWait time.Duration) (*sql.Conn, int, error) {
	// Explicit acquisition carries its own provenance; database/sql's pool
	// replacement opener must never borrow another operation's context.
	acquireCtx := context.WithValue(ctx, connectionInitializationContextKey{}, ctx)
	if maxWait < 0 {
		conn, err := s.db.Conn(acquireCtx)
		if err != nil {
			return nil, 0, fmt.Errorf("store: connection: %w", err)
		}
		return conn, s.busyTimeoutMs, nil
	}
	deadline, _ := ctx.Deadline()
	if maxWait == 0 {
		grab := newConnGrabContext(acquireCtx, s.db)
		defer grab.release()
		acquireCtx = grab
	}
	conn, err := s.db.Conn(acquireCtx)
	if errors.Is(err, context.Canceled) && errors.Is(context.Cause(acquireCtx), errConnGrabElapsed) {
		// connGrab ran out, a refusal as busy. If the caller's context has
		// ended since, its own error decides, as if it had ended first. This
		// helper returns a passed deadline as busy and leaves a cancellation
		// unnamed; withConnOperation then names that cancellation busy.
		err = ctx.Err()
		if err == nil {
			err = errConnGrabElapsed
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, errConnGrabElapsed):
		return nil, 0, fmt.Errorf("%w: the store's connection was not free within %d ms: another call of this process held it", ErrStoreBusy, maxWait.Milliseconds())
	case err != nil:
		return nil, 0, fmt.Errorf("store: connection: %w", err)
	}
	left := max(time.Until(deadline).Milliseconds(), 0)
	if maxWait == 0 {
		left = 0
	}
	return conn, int(min(left, int64(s.busyTimeoutMs))), nil
}

// errConnGrabElapsed is the cause a connGrabContext ends with when its
// connGrab runs out.
var errConnGrabElapsed = errors.New("store: connection grab elapsed")

// connGrabContext bounds a zero-wait call's wait for the store's one
// connection to connGrab, counted from when database/sql starts to wait for
// it, so a call a scheduling stall delays on its way still takes a free
// connection however late it arrives (b.q5r). Zero-wait connection
// acquisition must use it; never pre-arm a connGrab timeout.
//
// It relies on how database/sql's DB.conn (Go 1.22.10, as in 1.26.1) uses
// the context. Each attempt first looks at Done once, holding db.mu, and
// refuses an ended context; it then takes a free connection without looking
// again (the store's SessionResetters never look at Done), or looks at Done
// again to wait for one. So the first Done never arms the timer and every
// later one does: a free connection never arms it, and a queued call arms it
// as its wait starts. A later Done can also be a retry's first look (after
// driver.ErrBadConn), made holding db.mu, so the timer takes db.mu (through
// Stats) before it cancels, by when that look is over. Opening a new
// connection also arms it (callerOwnedConnector looks at Done), harmlessly:
// the native open runs under the operation's context, not this one.
//
// One contended case stays. A queued call that stalls for connGrab after its
// wait look, while the holder releases within connGrab, finds both the
// connection and the ended grab ready, and database/sql's wait select picks
// either at random: the call can get ErrStoreBusy after a hold shorter than
// connGrab. That is accepted, because only a retry could close it and a
// zero-wait call makes none.
//
// If database/sql stopped looking at Done before taking a free connection,
// a queued call's wait would be its first look and would last the
// operation's immediateWorkBudget instead of connGrab. If it looked at Done
// again, outside db.mu, before taking a free connection, a stall of connGrab
// after that look could again refuse a free connection.
type connGrabContext struct {
	context.Context // a cancelable child of the call's acquisition context
	cancel          context.CancelCauseFunc
	db              *sql.DB
	mu              sync.Mutex
	looks           int
	timer           *time.Timer
	released        bool
}

func newConnGrabContext(parent context.Context, db *sql.DB) *connGrabContext {
	ctx, cancel := context.WithCancelCause(parent)
	ctx.Done() // made now, so no look allocates it
	return &connGrabContext{Context: ctx, cancel: cancel, db: db}
}

func (c *connGrabContext) Done() <-chan struct{} {
	c.mu.Lock()
	c.looks++
	if c.looks > 1 && c.timer == nil && !c.released {
		c.timer = time.AfterFunc(connGrab, func() {
			c.db.Stats() // takes db.mu: a look that armed the timer under it has finished
			c.cancel(errConnGrabElapsed)
		})
	}
	c.mu.Unlock()
	return c.Context.Done()
}

// release ends the grab once acquisition has returned, leaving no timer or
// registration on the parent behind.
func (c *connGrabContext) release() {
	c.mu.Lock()
	c.released = true // a later Done arms nothing
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
	c.cancel(nil)
}

// withConn runs fn on the store's one connection, its waits bounded by
// maxWait (boundedConnContext): the wait for the connection, and, through
// that context-aware retries, the wait for a lock another connection holds.
// All SQL and cleanup retain the same context. A connection not taken in time,
// or not taken because the caller's context ended, returns an error wrapping
// ErrStoreBusy, having run nothing. The store's own busy timeout is
// put back before the connection returns to the pool; if that fails the
// connection is discarded, so no later statement inherits the shorter wait.
// Expired cleanup quarantines the leased connection until disposal finishes.
// fn must use conn alone: the pool has one connection, so a statement
// through s.db inside fn would wait for conn forever. busyMs is the busy
// timeout fn's statements run with.
func (s *Store) withConn(maxWait time.Duration, fn func(ctx context.Context, conn *sql.Conn, busyMs int, cleanup *connectionCleanup) error) error {
	ctx, cancel := s.operationContext(maxWait)
	defer cancel()
	return s.withConnContext(ctx, maxWait, fn)
}

// withConnContext retains an operation's deadline through SQL and cleanup.
func (s *Store) withConnContext(ctx context.Context, maxWait time.Duration, fn func(context.Context, *sql.Conn, int, *connectionCleanup) error) error {
	if ctx.Done() != nil {
		return s.runConnectionOperation(ctx, maxWait, func(op *connectionOperation) error {
			return s.withConnOperation(ctx, maxWait, op, fn)
		})
	}
	return s.withConnOperation(ctx, maxWait, nil, fn)
}

// withConnOperation takes the connection and runs fn on it. When the caller's
// context ends the operation in the wait for the connection or just after
// taking it, it returns operationBusy's error, as operationBusy's rule
// requires, except that a bounded call whose deadline ends the connection
// wait keeps boundedConnContext's refusal (a case that rule names).
func (s *Store) withConnOperation(ctx context.Context, maxWait time.Duration, op *connectionOperation, fn func(context.Context, *sql.Conn, int, *connectionCleanup) error) (result error) {
	conn, busyMs, err := s.boundedConnContext(ctx, maxWait)
	if err != nil {
		if cause := ctx.Err(); cause != nil && errors.Is(err, cause) {
			return operationBusy(cause)
		}
		return err
	}
	cleanup := &connectionCleanup{conn: conn, busyTimeoutMs: s.busyTimeoutMs, operation: op}
	if op == nil {
		defer s.releaseConn(ctx, cleanup)
	} else {
		// The operation slot stays owned through native disposal, even when
		// the caller has already returned. Publish SQL's result first so a
		// confirmed COMMIT cannot become uncertain during cleanup.
		defer cleanup.finish(ctx)
		defer func() {
			op.mu.Lock()
			op.err, op.completed = result, true
			op.mu.Unlock()
		}()
	}
	if err := ctx.Err(); err != nil {
		cleanup.discard = true
		return operationBusy(err)
	}
	sqliteBusyMs := busyMs
	if _, bounded := ctx.Deadline(); bounded {
		// SQLite's busy handler can otherwise consume a stale allowance after
		// time spent preparing/running a statement. Retry BEGIN in Go instead.
		sqliteBusyMs = 0
	}
	if sqliteBusyMs != s.busyTimeoutMs {
		// PRAGMA busy_timeout takes no bound parameter; busyMs is an int the
		// store computed (boundedConnContext), never input.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", sqliteBusyMs)); err != nil {
			cleanup.discard = true
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// Setup precedes BEGIN, so cancellation cannot hide a commit.
				return fmt.Errorf("%w: store: set busy timeout: %w", ErrStoreBusy, err)
			}
			return fmt.Errorf("store: set busy timeout: %w", err)
		}
		cleanup.restore = true
	}
	return fn(ctx, conn, busyMs, cleanup)
}

// readWithin runs the read fn on the store's one connection with its waits
// bounded by maxWait (withConn) and names its error: nil and sql.ErrNoRows
// (anywhere in the chain) are returned as they are; a wait cut by the bound,
// for the connection or for a lock another connection holds (SQLITE_BUSY),
// is an error wrapping ErrStoreBusy; any other error is wrapped with
// errPrefix, or returned as it is when errPrefix is "" (fn names its own). fn
// must use conn alone (see withConn).
func (s *Store) readWithin(maxWait time.Duration, errPrefix string, fn func(ctx context.Context, conn *sql.Conn) error) error {
	err := s.withConn(maxWait, func(ctx context.Context, conn *sql.Conn, busyMs int, _ *connectionCleanup) error {
		return retryBusy(ctx, busyMs, func() error { return fn(ctx, conn) })
	})
	switch {
	case err == nil, errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrStoreBusy):
		return err
	case (isBusy(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) && errPrefix == "":
		return fmt.Errorf("%w: %w", ErrStoreBusy, err)
	case isBusy(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %s: %w", ErrStoreBusy, errPrefix, err)
	case errPrefix == "":
		return err
	}
	return fmt.Errorf("%s: %w", errPrefix, err)
}

// inWriteTx runs fn inside one BEGIN IMMEDIATE transaction on the store's
// connection, waiting at most maxWait in all for the connection and the write
// lock (DefaultLockWait: as every other write waits; boundedConnContext).
// The transaction holds the write lock from its first statement, so nothing
// inside it waits for the lock again.
//
// When the connection or the lock is not taken within the wait, it returns an
// error wrapping ErrStoreBusy (and, for the lock, the driver's error), having
// run nothing. Before commit, a failed callback is rolled back or its
// connection discarded. A COMMIT error is unconfirmed, never ErrStoreBusy:
// read the durable request rather than assume that nothing was written. fn
// must use conn alone (see withConn).
func (s *Store) inWriteTx(maxWait time.Duration, fn func(ctx context.Context, conn *sql.Conn) error) error {
	ctx, cancel := s.operationContext(maxWait)
	defer cancel()
	return s.inWriteTxContext(ctx, maxWait, fn)
}

// inWriteTxContext allows post-commit work to share the transaction's deadline.
func (s *Store) inWriteTxContext(ctx context.Context, maxWait time.Duration, fn func(context.Context, *sql.Conn) error) error {
	return s.withConnContext(ctx, maxWait, func(ctx context.Context, conn *sql.Conn, busyMs int, cleanup *connectionCleanup) error {
		if err := beginWithin(ctx, conn, busyMs); err != nil {
			if isBusy(err) || ctx.Err() != nil {
				return fmt.Errorf("%w: the write lock was not free within %d ms: %w", ErrStoreBusy, busyMs, err)
			}
			return fmt.Errorf("store: begin: %w", err)
		}
		cleanup.rollback = true
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: before callback: %w", ErrStoreBusy, err)
		}
		if err := fn(ctx, conn); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("%w: before commit: %w", ErrStoreBusy, err)
			}
			return err
		}
		if cleanup.operation != nil {
			cleanup.operation.mu.Lock()
			if cleanup.operation.abandoned {
				cleanup.operation.mu.Unlock()
				return fmt.Errorf("%w: before commit: %w", ErrStoreBusy, context.DeadlineExceeded)
			}
			if err := ctx.Err(); err != nil {
				cleanup.operation.mu.Unlock()
				return fmt.Errorf("%w: before commit: %w", ErrStoreBusy, err)
			}
			cleanup.operation.commitStarted = true
			cleanup.operation.mu.Unlock()
		} else if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: before commit: %w", ErrStoreBusy, err)
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return fmt.Errorf("store: commit outcome unconfirmed; read the request before retrying (branch=commit_exec): %w", err)
		}
		cleanup.rollback = false
		return nil
	})
}

// beginWithin never gives SQLite a relative busy allowance that can outlive
// the operation. The connection's busy handler is disabled for bounded calls.
func beginWithin(ctx context.Context, conn *sql.Conn, busyMs int) error {
	return retryBusy(ctx, busyMs, func() error {
		_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		return err
	})
}

func retryBusy(ctx context.Context, busyMs int, fn func() error) error {
	if _, bounded := ctx.Deadline(); !bounded {
		return fn()
	}
	end := time.Now().Add(time.Duration(busyMs) * time.Millisecond)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn()
		if !isBusy(err) || busyMs == 0 {
			return err
		}
		left := time.Until(end)
		if left <= 0 {
			return err
		}
		timer := time.NewTimer(min(time.Millisecond, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// inImmediateTx runs fn inside one BEGIN IMMEDIATE transaction on the store's
// connection, waiting for the connection and the write lock as every other
// write does (the store's busy timeout), as MarkMissingIfSameLife,
// MarkEndedIfSameLife and ResetForReuse begin theirs. Unlike inWriteTx, a lock not taken in that time
// is returned as the driver's busy error, an unnamed store failure, never
// ErrStoreBusy: it is for a write whose verb names no ErrStoreBusy (resume's
// move to pending, a hook's ended transition). When fn returns an error, or
// the commit fails, the transaction is rolled back and nothing is written;
// fn's error is returned as it is. fn must use conn alone (see withConn).
func (s *Store) inImmediateTx(fn func(ctx context.Context, conn *sql.Conn) error) error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackConn(ctx, conn)
		}
	}()
	if err := fn(ctx, conn); err != nil {
		return err
	}
	if err := s.refreshFamilies(connQuerier{ctx, conn}); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}

// millisArg returns t as a bound argument of an integer-milliseconds column
// (the v7 instants: delivered_at, settled_at, hook_gone_at, attempted_at,
// pane_intent_at, closed_at, proven_gone_at), or nil (SQL NULL) for the zero
// time.
func millisArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

// millisTime maps an integer-milliseconds column to a UTC time; the zero time
// for NULL.
func millisTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
}
