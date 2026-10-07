package apitest

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

// HoldWriteLock takes the write lock of the existing store at dbPath on a
// connection of its own (BEGIN IMMEDIATE), as another agent-director
// process's write holds it, until the returned release is called or d has
// passed, whichever is first. A store connection that then writes waits up to
// its busy timeout for the lock (b.c7f). release returns once the lock is
// released; later calls, and the test's cleanup, do nothing more.
func HoldWriteLock(t testing.TB, dbPath string, d time.Duration) (release func()) {
	t.Helper()
	db, err := openRawStore(dbPath)
	if err != nil {
		t.Fatalf("HoldWriteLock: %v", err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err == nil {
		_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	}
	if err != nil {
		closeHolder(conn, db)
		t.Fatalf("HoldWriteLock: take the write lock of %s: %v", dbPath, err)
	}
	stop, released := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(released)
		select {
		case <-stop:
		case <-time.After(d):
		}
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		closeHolder(conn, db)
	}()
	var once sync.Once
	release = func() {
		once.Do(func() { close(stop) })
		<-released
	}
	t.Cleanup(release)
	return release
}

// closeHolder closes HoldWriteLock's connection, when it has one, and its pool.
func closeHolder(conn *sql.Conn, db *sql.DB) {
	if conn != nil {
		_ = conn.Close()
	}
	_ = db.Close()
}
