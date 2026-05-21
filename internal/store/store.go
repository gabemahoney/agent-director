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

// ErrSchemaMismatch is returned by Open when the SQLite user_version is
// non-zero and does not match the schema version this binary understands.
// Callers should use errors.Is to detect it.
var ErrSchemaMismatch = errors.New("store: schema version mismatch")

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

// Open prepares the SQLite database at path, creating its parent directory
// and the file itself when missing, applying file-mode constraints, opening
// a single-connection pool, enabling WAL + foreign keys, and ensuring the
// schema is at the current version.
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

	parent := filepath.Dir(resolved)
	if err := os.MkdirAll(parent, parentDirMode); err != nil {
		return nil, fmt.Errorf("store: create parent dir: %w", err)
	}

	// foreign_keys is a per-connection PRAGMA, so set it via DSN so every
	// connection the pool dials in starts with FKs enforced. journal_mode
	// persists in the DB header, but we set it via DSN too for symmetry
	// and so a freshly-deleted DB file picks up WAL on first contact.
	dsn := resolved + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	// SRD §13.3: serialize all writers through a single connection so WAL
	// readers never observe a half-applied transaction and SQLite's own
	// "database is locked" retry loop is bypassed.
	db.SetMaxOpenConns(1)

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

// verifyPragmas confirms the DSN-supplied PRAGMAs took effect. journal_mode
// is requested as WAL and foreign_keys as 1 (SRD §4.5 / §13.3); if either
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
