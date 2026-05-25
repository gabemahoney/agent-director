// Package store is the single layer permitted to touch SQL in agent-director.
//
// Per SRD §4.5, no other package may import database/sql or speak SQL directly.
// Callers receive a *Store with typed methods (added in later Tasks); raw
// *sql.DB and SQL strings stay private to this package.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver registered as "sqlite"
)

// ErrSchemaMismatch is returned by Open/OpenOrInit when the SQLite
// user_version is non-zero and does not match the schema version this binary
// understands. Callers should use errors.Is to detect it.
var ErrSchemaMismatch = errors.New("store: schema version mismatch")

// ErrStoreNotInitialized is returned by Open when the database file does not
// exist. It signals that the caller should either run OpenOrInit (which
// creates the file and applies the schema) or report a useful error to the
// user. Callers should use errors.Is to detect it.
var ErrStoreNotInitialized = errors.New("store: database not initialized")

// schemaVersion is the current schema version this package writes and reads.
// Bump (and add a migration) whenever the DDL in schema.go changes.
const schemaVersion = 1

// dbFileMode is the mode the SQLite file itself is forced to on every Open.
// 0600 = owner read/write only.
const dbFileMode os.FileMode = 0o600

// parentDirMode is the mode applied to the parent directory of the DB file
// when it is created. 0700 = owner traverse/read/write only.
const parentDirMode os.FileMode = 0o700

// Store is the opaque handle callers hold for the lifetime of a process.
// The underlying *sql.DB is unexported on purpose — see package doc.
type Store struct {
	db *sql.DB
}

// Open opens an existing SQLite database at path. It does NOT create the
// parent directory or the database file; if the file is absent it returns
// ErrStoreNotInitialized. Use OpenOrInit when create-if-missing behavior is
// required (e.g. CLI first-run).
//
// A leading "~/" in path is expanded against the current user's home dir.
//
// On any error the caller does not need to close anything — Open cleans up
// the partially-opened *sql.DB before returning.
func Open(path string) (*Store, error) {
	resolved, err := expandTilde(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve path: %w", err)
	}

	if _, err := os.Stat(resolved); os.IsNotExist(err) {
		return nil, fmt.Errorf("store: %w", ErrStoreNotInitialized)
	} else if err != nil {
		return nil, fmt.Errorf("store: stat db file: %w", err)
	}

	return openDB(resolved)
}

// OpenOrInit prepares the SQLite database at path, creating its parent
// directory and the file itself when missing, applying file-mode constraints,
// opening a single-connection pool, enabling WAL + foreign keys, and ensuring
// the schema is at the current version.
//
// A leading "~/" in path is expanded against the current user's home dir.
//
// On any error the caller does not need to close anything — OpenOrInit cleans
// up the partially-opened *sql.DB before returning.
func OpenOrInit(path string) (*Store, error) {
	resolved, err := expandTilde(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve path: %w", err)
	}

	parent := filepath.Dir(resolved)
	if err := os.MkdirAll(parent, parentDirMode); err != nil {
		return nil, fmt.Errorf("store: create parent dir: %w", err)
	}

	return openDB(resolved)
}

// openDB dials a SQLite connection at the given (already-resolved, already-
// present-or-newly-created) path, verifies PRAGMAs, enforces file mode, and
// ensures the schema is current. It is the shared backend for Open and
// OpenOrInit.
func openDB(resolved string) (*Store, error) {
	// foreign_keys is a per-connection PRAGMA, so set it via DSN so every
	// connection the pool dials in starts with FKs enforced.
	//
	// busy_timeout tells the driver to retry-with-backoff for up to 10s
	// on SQLITE_BUSY instead of failing on the first lock collision. With
	// N hook processes hitting the same DB file during burst-spawn, this
	// is the floor of correctness for any multi-writer SQLite system.
	//
	// journal_mode is intentionally NOT set here. WAL persists in the DB
	// header; re-running PRAGMA journal_mode=WAL on every connection forces
	// a write-lock acquisition and is what caused the b.x2m burst-spawn
	// bug. We set WAL once at fresh-DB init (see ensureJournalModeWAL) and
	// trust the header thereafter; verifyPragmas keeps reading it
	// (read-only form, no lock) to confirm.
	dsn := resolved + "?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	// SRD §13.3: serialize all writers through a single connection so WAL
	// readers never observe a half-applied transaction and SQLite's own
	// "database is locked" retry loop is bypassed.
	db.SetMaxOpenConns(1)

	if err := ensureJournalModeWAL(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := verifyPragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	// Force the file mode after the driver has created it. chmod is
	// idempotent, so always setting it is simpler than statting first.
	if err := os.Chmod(resolved, dbFileMode); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: chmod db file: %w", err)
	}

	if err := ensureSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close releases the underlying database handle. Safe to call once.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// expandTilde resolves a leading "~/" against the current user's home dir.
// Any other form of path is returned unchanged.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return filepath.Join(u.HomeDir, strings.TrimPrefix(path, "~/")), nil
}

// ensureJournalModeWAL sets journal_mode=WAL exactly once per DB file —
// only when the header doesn't already report WAL. Reading PRAGMA
// journal_mode is a lock-free header read; the write-form PRAGMA only
// runs on a genuinely fresh DB (or one whose header was reset by hand),
// which avoids the per-connection write-lock contention that caused
// b.x2m. busy_timeout (set in the DSN) covers the rare case where two
// processes race to initialize the same fresh DB.
func ensureJournalModeWAL(db *sql.DB) error {
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("store: read journal_mode: %w", err)
	}
	if mode == "wal" {
		return nil
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("store: set journal_mode=wal: %w", err)
	}
	return nil
}

// verifyPragmas confirms the required PRAGMAs are in effect. journal_mode
// is checked as WAL and foreign_keys as 1 (SRD §4.5 / §13.3); if either
// silently downgraded we want a loud failure at Open, not a mysterious
// data-integrity bug later.
func verifyPragmas(db *sql.DB) error {
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		return fmt.Errorf("store: read journal_mode: %w", err)
	}
	if journal != "wal" {
		return fmt.Errorf("store: journal_mode = %q, want wal", journal)
	}
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("store: read foreign_keys: %w", err)
	}
	if fk != 1 {
		return fmt.Errorf("store: foreign_keys = %d, want 1", fk)
	}
	return nil
}
