package store

// Export-for-test shim: hands the v5 fixture (migration_fixtures_test.go) to
// the external v5_migrated_find_missing_test.go, migrated through the real
// {5→schemaVersion} sentinel flow. It seeds and migrates nothing itself.

import (
	"strconv"
	"testing"
)

// MigratedV5 is the migrated v5 fixture store and its pending row: its launch
// start and recorded pane process, as seeded before the hop.
type MigratedV5 struct {
	Path, Pending            string
	PendingLaunchStartMillis int64
	PendingPanePID           int
	PendingPaneStarttime     string
}

// MigrateV5Fixture builds the v5 fixture in a temp dir, migrates it via
// writeSentinel(5, schemaVersion) + Open, and returns it closed.
func MigrateV5Fixture(t *testing.T) MigratedV5 {
	t.Helper()
	f := makeV5Fixture(t, t.TempDir())
	writeSentinel(t, f.dir, 5, schemaVersion)
	openStoreID(t, Open, f.path)
	if v := readUserVersion(t, f.path); v != schemaVersion {
		t.Fatalf("MigrateV5Fixture: user_version = %d; want %d", v, schemaVersion)
	}
	row := f.rows[f.pending]
	atoi := func(col string) int64 {
		n, err := strconv.ParseInt(row[col], 10, 64)
		if err != nil {
			t.Fatalf("MigrateV5Fixture: %s = %s: %v", col, row[col], err)
		}
		return n
	}
	return MigratedV5{Path: f.path, Pending: f.pending, PendingLaunchStartMillis: atoi("launch_started_at"),
		PendingPanePID: int(atoi("pane_pid")), PendingPaneStarttime: unquote(row["pane_starttime"])}
}
