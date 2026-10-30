package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
)

// ObserveSQLiteForTest lets internal cross-package fixtures reuse their real
// driver observer without exposing a raw pool or installing a global hook.
// Install only on a quiescent owning Store, before creating context views.
// The wrapper must preserve the driver's behavior except at observed boundaries.
// Normal Store opens never use this seam.
//
// Before stop, release observer barriers and stop issuing calls on the Store
// and its views. Stop waits for native work and cleanup under ctx, then restores
// the original driver. A context failure retains the observed pool (possibly
// already closed) until a later stop attempt; issue no Store calls meanwhile.
// Successful stop is idempotent. Views made while observing must not be reused
// after stop; callbacks must be immutable or synchronized.
func ObserveSQLiteForTest(s *Store, wrap func(driver.Driver) driver.Driver) (stop func(context.Context) error, err error) {
	if wrap == nil {
		return nil, fmt.Errorf("store: missing SQLite observer wrapper")
	}
	original := s.db.Driver()
	observed := wrap(original)
	if observed == nil {
		return nil, fmt.Errorf("store: SQLite observer returned no driver")
	}
	pool := func(d driver.Driver) *sql.DB {
		db := sql.OpenDB(callerOwnedConnector{sqliteConnector{driver: d, path: s.filePath, busyTimeoutMs: s.busyTimeoutMs}})
		db.SetMaxOpenConns(1)
		return db
	}
	if err := s.db.Close(); err != nil {
		return nil, err
	}
	db := pool(observed)
	s.db = db
	var retired, stopped bool
	var stopErr error
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The existing worker owns native acquire/init/close past the caller's
		// deadline. Its slot also serializes retries and the state below.
		return s.runConnectionOperation(ctx, DefaultLockWait, func(op *connectionOperation) error {
			if stopped {
				return stopErr
			}
			if s.db != db {
				return fmt.Errorf("store: SQLite observer no longer owns the pool")
			}
			if !retired {
				// The sole lease waits out separately dispatched cleanup too.
				conn, _, err := s.boundedConnContext(ctx, DefaultLockWait)
				if err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					cleanup := connectionCleanup{conn: conn, discard: true}
					cleanup.finish(ctx)
					return err
				}
				stopErr = errors.Join(conn.Close(), db.Close())
				// Close cannot be canceled. A retry must not reacquire this
				// closed pool or repeat native disposal after it finishes.
				retired = true
			}
			op.mu.Lock()
			defer op.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			if op.abandoned {
				return context.DeadlineExceeded
			}
			s.db = pool(original)
			stopped = true
			// Publish restoration before abandonment can return a timeout.
			op.err, op.completed = stopErr, true
			return stopErr
		})
	}, nil
}
