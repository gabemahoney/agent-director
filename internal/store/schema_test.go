package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openTempStore opens a Store under t.TempDir and registers cleanup.
// Returns the resolved DB path so tests can re-open it raw if they need to.
func openTempStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// openRaw is a test helper that opens the same DB through database/sql
// directly so tests can poke PRAGMAs and sqlite_master without going through
// the Store API (which deliberately exposes no SQL surface).
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw sql.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenCreatesSchemaV1(t *testing.T) {
	s, path := openTempStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openRaw(t, path)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d, want 1", version)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	for i := 0; i < 2; i++ {
		s, err := OpenOrInit(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}

	db := openRaw(t, path)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d after two opens, want 1", version)
	}
}

func TestSchemaObjectsExist(t *testing.T) {
	s, path := openTempStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db := openRaw(t, path)

	wantTables := []string{"spawns", "permission_requests"}
	for _, name := range wantTables {
		if !objectExists(t, db, "table", name) {
			t.Errorf("table %q missing from sqlite_master", name)
		}
	}

	wantIndexes := []string{
		"idx_spawns_state",
		"idx_spawns_last_seen",
		"idx_spawns_parent",
	}
	for _, name := range wantIndexes {
		if !objectExists(t, db, "index", name) {
			t.Errorf("index %q missing from sqlite_master", name)
		}
	}
}

func TestPragmasApplied(t *testing.T) {
	s, path := openTempStore(t)

	// journal_mode persists in the DB header, so a fresh raw connection
	// observes the same value the Store set.
	rawDB := openRaw(t, path)
	var journal string
	if err := rawDB.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want %q", journal, "wal")
	}

	// foreign_keys is per-connection, so verify on the Store's own conn.
	var fk int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

// TestSingleWriterSerializesExec exercises SetMaxOpenConns(1): two writes
// back-to-back through the Store's *sql.DB must both succeed without
// SQLite's "database is locked" error.
func TestSingleWriterSerializesExec(t *testing.T) {
	s, _ := openTempStore(t)

	insert := `INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode)
	           VALUES (?, 'idle', '/tmp', 'sess', 'mirror')`
	if _, err := s.db.Exec(insert, "a"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.db.Exec(insert, "b"); err != nil {
		t.Fatalf("second insert: %v", err)
	}
}

// objectExists returns true if sqlite_master contains a row of the given
// type and name. Centralizing the query keeps individual tests readable.
func objectExists(t *testing.T, db *sql.DB, kind, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type = ? AND name = ?",
		kind, name,
	).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("sqlite_master lookup %s %q: %v", kind, name, err)
	}
	return got == name
}
