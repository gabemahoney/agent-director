package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// setUserVersion opens the DB raw and stamps PRAGMA user_version to v.
// PRAGMA values cannot be parameterized, so v is interpolated directly —
// safe because callers only pass test-controlled integers.
func setUserVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open for version stamp: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close raw db: %v", cerr)
		}
	}()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatalf("set user_version=%d: %v", v, err)
	}
}

// TestOpenReturnsErrSchemaMismatch covers the three "wrong version" cases
// at once: any user_version that's non-zero and not the current schema must
// surface ErrSchemaMismatch through errors.Is.
func TestOpenReturnsErrSchemaMismatch(t *testing.T) {
	for _, badVersion := range []int{2, 99, 1000} {
		badVersion := badVersion
		t.Run(fmt.Sprintf("version=%d", badVersion), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")

			// Bootstrap a real v1 DB, close it, then corrupt user_version.
			s, err := Open(path)
			if err != nil {
				t.Fatalf("initial Open: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			setUserVersion(t, path, badVersion)

			_, err = Open(path)
			if !errors.Is(err, ErrSchemaMismatch) {
				t.Fatalf("Open with user_version=%d returned %v, want errors.Is ErrSchemaMismatch",
					badVersion, err)
			}
		})
	}
}
