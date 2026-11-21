// Package sqlitelockfix holds a store's SQLite file under a real exclusive
// lock, so every other connection's read waits for it with its busy timeout
// and then fails SQLITE_BUSY, as SQLite documents for WAL
// (https://sqlite.org/wal.html#sometimes_queries_return_sqlite_busy_in_wal_mode).
//
// This is a reusable LEAF test-support package (docs/architecture.md, storefix
// seeders): it imports nothing from agent-director, so the store's own tests
// and pkg/api's import it with no cycle. Tests that need a read-blocking
// SQLite lock MUST use HoldExclusive; reopening the store and observing its
// statements stay with each test.
package sqlitelockfix

import (
	"database/sql"
	"sync"
	"testing"

	_ "modernc.org/sqlite" // the "sqlite" driver
)

// HoldExclusive takes the SQLite store at path in exclusive locking mode on a
// connection of its own until release, which the test's cleanup calls too.
// No other connection may have the store open when it is taken: a WAL
// connection keeps a shared lock while open, so callers first close or drop
// theirs. The store must have a store_meta table, which it reads to take the
// lock.
func HoldExclusive(t testing.TB, path string) (release func()) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the lock holder: %v", err)
	}
	db.SetMaxOpenConns(1)
	var mode string
	var n int
	if err = db.QueryRow("PRAGMA locking_mode=EXCLUSIVE").Scan(&mode); err == nil {
		if _, err = db.Exec("BEGIN EXCLUSIVE"); err == nil {
			err = db.QueryRow("SELECT count(*) FROM store_meta").Scan(&n)
		}
	}
	if err != nil {
		_ = db.Close()
		t.Fatalf("take the store's exclusive lock: %v", err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_, _ = db.Exec("ROLLBACK")
			_ = db.Close()
		})
	}
	t.Cleanup(release)
	return release
}
