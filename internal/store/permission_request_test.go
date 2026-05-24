package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// TestGetPermissionRequestReturnsErrNoRows pins the contract the api.Decide
// verb depends on: GetPermissionRequest must return sql.ErrNoRows literally
// when the row is absent, so callers can use errors.Is for disambiguation.
func TestGetPermissionRequestReturnsErrNoRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.GetPermissionRequest("absent")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v; want sql.ErrNoRows", err)
	}
}
