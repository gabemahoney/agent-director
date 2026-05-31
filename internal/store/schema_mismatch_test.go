package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrationFailurePreservesV1State verifies SR-2.4: a failed v1→v2
// migration leaves the database at user_version=1 with the v1 table shape
// intact. We inject a failure by pre-creating a conflicting index name on
// spawns before the migration runs; migrateV1toV2 rolls back the entire tx.
func TestMigrationFailurePreservesV1State(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	openV1DB(t, path)

	// Pre-create a conflicting index name so the v2 CREATE INDEX fails
	// mid-transaction, triggering rollback.
	rawDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open to inject conflict: %v", err)
	}
	if _, err := rawDB.Exec(`CREATE INDEX idx_permission_requests_instance_decision ON spawns(state)`); err != nil {
		_ = rawDB.Close()
		t.Fatalf("pre-create conflicting index: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw after inject: %v", err)
	}

	// Open must fail — migration cannot complete.
	_, err = Open(path)
	if err == nil {
		t.Fatal("Open returned nil, expected migration error")
	}

	// Database must still be at user_version=1 — rollback preserved state.
	checkDB := openRaw(t, path)
	var version int
	if err := checkDB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 1 {
		t.Fatalf("user_version = %d after failed migration, want 1", version)
	}

	// V1 table shape: request_token absent, updated_at present.
	cols := columnNames(t, checkDB, "permission_requests")
	for _, col := range cols {
		if col == "request_token" {
			t.Error("request_token found after rollback — v1 state not preserved")
		}
	}
	found := false
	for _, col := range cols {
		if col == "updated_at" {
			found = true
		}
	}
	if !found {
		t.Error("updated_at missing — v1 schema was not preserved after failed migration")
	}
}

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
	for _, badVersion := range []int{3, 99, 1000} {
		badVersion := badVersion
		t.Run(fmt.Sprintf("version=%d", badVersion), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")

			// Bootstrap a real v1 DB, close it, then corrupt user_version.
			s, err := OpenOrInit(path)
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
