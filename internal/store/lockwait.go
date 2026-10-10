package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrStoreBusy marks a bounded-wait store call that could not take what it
// waits for within its wait (b.146 problem 1, decision 9 B): the store's
// write lock, which another process (or another connection) held for longer,
// or the store's one connection, which another call of this process held for
// longer. Nothing was written. Callers detect it with errors.Is. pkg/api
// re-exports it as the catalogued error decide returns when its max_wait_ms
// bound is reached before the verdict commits.
var ErrStoreBusy = errors.New("ErrStoreBusy")

// DefaultLockWait asks a bounded-wait store call for the store's own waits,
// as every other store call waits: for the store's one connection without a
// bound, and for a lock another connection holds up to the store's busy
// timeout ([store] busy_timeout_ms).
const DefaultLockWait time.Duration = -1

// connGrab is the least time a bounded call allows itself to take the store's
// one connection. database/sql refuses a context that has already expired
// even when the connection is free, so a call with no wait left (a reading
// verb's hook_gone_at write, b.146 problem 4) still gets this long to take a
// free connection; it is too short to amount to a wait for a connection
// another call of this process holds.
const connGrab = time.Millisecond

// isBusy reports whether err is SQLite's SQLITE_BUSY (any extended code): a
// lock another connection holds was not released within the busy timeout.
func isBusy(err error) bool {
	var serr *sqlite.Error
	return errors.As(err, &serr) && serr.Code()&0xff == sqlite3.SQLITE_BUSY
}

// boundedConn takes the store's one connection for a call whose waits are
// bounded by maxWait, and returns it with the busy timeout, in whole
// milliseconds, the call's statements may wait for a lock another connection
// holds.
//
// DefaultLockWait (any negative wait): it waits for the connection as every
// other store call does, without a bound, and returns the store's busy
// timeout. Otherwise both waits share one deadline, maxWait from now: it
// waits for the connection, which the pool's one connection makes a wait for
// any other call of this process using the store, until the deadline (at
// least connGrab), and returns what is left of maxWait after it as the busy
// timeout, cut at the store's busy timeout (a bound never waits longer than
// the store would) and never below 0, which makes SQLite answer busy at once.
// A connection not taken by the deadline is an error wrapping ErrStoreBusy.
func (s *Store) boundedConn(maxWait time.Duration) (*sql.Conn, int, error) {
	if maxWait < 0 {
		conn, err := s.db.Conn(context.Background())
		if err != nil {
			return nil, 0, fmt.Errorf("store: connection: %w", err)
		}
		return conn, s.busyTimeoutMs, nil
	}
	start := time.Now()
	deadline := start.Add(maxWait)
	ctx, cancel := context.WithDeadline(context.Background(), start.Add(max(maxWait, connGrab)))
	defer cancel()
	conn, err := s.db.Conn(ctx)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return nil, 0, fmt.Errorf("%w: the store's connection was not free within %d ms: another call of this process held it", ErrStoreBusy, maxWait.Milliseconds())
	case err != nil:
		return nil, 0, fmt.Errorf("store: connection: %w", err)
	}
	left := max(time.Until(deadline).Milliseconds(), 0)
	return conn, int(min(left, int64(s.busyTimeoutMs))), nil
}

// withConn runs fn on the store's one connection, its waits bounded by
// maxWait (boundedConn): the wait for the connection, and, through that
// connection's busy timeout for fn's statements only, the wait for a lock
// another connection holds. A connection not taken in time returns an error
// wrapping ErrStoreBusy, having run nothing. The store's own busy timeout is
// put back before the connection returns to the pool; if that fails the
// connection is discarded, so no later statement inherits the shorter wait.
// fn must use conn alone: the pool has one connection, so a statement
// through s.db inside fn would wait for conn forever. busyMs is the busy
// timeout fn's statements run with.
func (s *Store) withConn(maxWait time.Duration, fn func(ctx context.Context, conn *sql.Conn, busyMs int) error) error {
	ctx := context.Background()
	conn, busyMs, err := s.boundedConn(maxWait)
	if err != nil {
		return err
	}
	defer conn.Close()
	if busyMs != s.busyTimeoutMs {
		// PRAGMA busy_timeout takes no bound parameter; busyMs is an int the
		// store computed (boundedConn), never input.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", busyMs)); err != nil {
			return fmt.Errorf("store: set busy timeout: %w", err)
		}
		defer func() {
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", s.busyTimeoutMs)); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}()
	}
	return fn(ctx, conn, busyMs)
}

// readWithin runs the read fn on the store's one connection with its waits
// bounded by maxWait (withConn) and names its error: nil and sql.ErrNoRows
// (anywhere in the chain) are returned as they are; a wait cut by the bound,
// for the connection or for a lock another connection holds (SQLITE_BUSY),
// is an error wrapping ErrStoreBusy; any other error is wrapped with
// errPrefix, or returned as it is when errPrefix is "" (fn names its own). fn
// must use conn alone (see withConn).
func (s *Store) readWithin(maxWait time.Duration, errPrefix string, fn func(ctx context.Context, conn *sql.Conn) error) error {
	err := s.withConn(maxWait, func(ctx context.Context, conn *sql.Conn, _ int) error { return fn(ctx, conn) })
	switch {
	case err == nil, errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrStoreBusy):
		return err
	case isBusy(err) && errPrefix == "":
		return fmt.Errorf("%w: %w", ErrStoreBusy, err)
	case isBusy(err):
		return fmt.Errorf("%w: %s: %w", ErrStoreBusy, errPrefix, err)
	case errPrefix == "":
		return err
	}
	return fmt.Errorf("%s: %w", errPrefix, err)
}

// inWriteTx runs fn inside one BEGIN IMMEDIATE transaction on the store's
// connection, waiting at most maxWait in all for the connection and the write
// lock (DefaultLockWait: as every other write waits; boundedConn). The
// transaction holds the write lock from its first statement, so nothing
// inside it waits for the lock again.
//
// When the connection or the lock is not taken within the wait, it returns an
// error wrapping ErrStoreBusy (and, for the lock, the driver's error), having
// run nothing. When fn returns an error, or the commit fails, the transaction
// is rolled back and nothing is written; fn's error is returned as it is. fn
// must use conn alone (see withConn).
func (s *Store) inWriteTx(maxWait time.Duration, fn func(ctx context.Context, conn *sql.Conn) error) error {
	return s.withConn(maxWait, func(ctx context.Context, conn *sql.Conn, busyMs int) error {
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			if isBusy(err) {
				return fmt.Errorf("%w: the write lock was not free within %d ms: %w", ErrStoreBusy, busyMs, err)
			}
			return fmt.Errorf("store: begin: %w", err)
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
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return fmt.Errorf("store: commit: %w", err)
		}
		committed = true
		return nil
	})
}

// inImmediateTx runs fn inside one BEGIN IMMEDIATE transaction on the store's
// connection, waiting for the connection and the write lock as every other
// write does (the store's busy timeout), as MarkMissingIfSameLife and
// ResetForReuse begin theirs. Unlike inWriteTx, a lock not taken in that time
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
