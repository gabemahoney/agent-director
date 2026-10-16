//go:build linux

package cursorhost

import (
	"database/sql"
	"testing"
	"time"
)

// Synthetic proof writes retain the fixture's SQL bound and preserve first failure.
func (c *cursorSession) rotationFixtureSQL(t *testing.T, barrier *recordBarrier, want int64, query string, args ...any) {
	t.Helper()
	started := time.Now()
	db, err := sql.Open("sqlite", c.e.path+"?_pragma=busy_timeout(1000)")
	var affected int64
	if err == nil {
		defer db.Close()
		var result sql.Result
		result, err = db.Exec(query, args...)
		if err == nil {
			affected, err = result.RowsAffected()
		}
	}
	if err != nil {
		t.Errorf("rotation fixture SQL first error after %s: %v", time.Since(started), err)
		rotationFailureDiagnostics(t, c, barrier, []string{"spawns", "cursor_runtimes", "permission_requests"})
		t.Fatalf("rotation fixture SQL failed: %v", err)
	}
	if affected != want {
		t.Fatalf("rotation fixture SQL affected %d rows, want %d", affected, want)
	}
}
