package store

// Schema v9 (b.ssz, the agent-neutral names): the v8 fixture's rows after the
// hop (every value kept under its new column name, each row's agent claude, no
// end report), a migrated row's reads and resume writes, and no column left
// with a claude_ name. The column specs, re-entry and rollback rows are
// schema_test.go's; the fixture and its checks: migration_fixtures_test.go.
// There is no v9 → v8 recipe: the downgrade guide stops at v8.

import (
	"database/sql"
	"slices"
	"strings"
	"testing"
)

// migrateV8Fixture builds the v8 fixture, authorises its hop and opens the
// migrated store; the store id before the hop is returned with it.
func migrateV8Fixture(t *testing.T) (f v8Fixture, s *Store, storeID string) {
	t.Helper()
	f = makeV8Fixture(t, t.TempDir())
	storeID = assertOneStoreID(t, f.path)
	writeSentinel(t, f.dir, 8, schemaVersion)
	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("open the store to migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return f, s, storeID
}

// TestV9MigrationKeepsV8Rows: after the authorised hop every v8 value of every
// fixture row, request and archived session reads back under its v9 column
// name, each row's agent is claude with no end report, the sentinel is consumed
// and the store id is kept.
func TestV9MigrationKeepsV8Rows(t *testing.T) {
	f, s, storeID := migrateV8Fixture(t)
	if got := s.StoreID(); got != storeID {
		t.Errorf("StoreID() after the hop = %q; want %q kept", got, storeID)
	}
	assertSentinel(t, f.dir, false)
	assertV8ValuesKept(t, f.path, f, nil)
	if got := assertOneStoreID(t, f.path); got != storeID {
		t.Errorf("store_id after the hop = %q; want %q", got, storeID)
	}
}

// TestV9MigratedRowsRead: the store's reads of a migrated row, request and
// archived session fill them from the renamed columns, and the row reads the
// agent claude with no end report.
func TestV9MigratedRowsRead(t *testing.T) {
	f, s, _ := migrateV8Fixture(t)
	for _, id := range f.ids {
		sp := mustGetSpawn(t, s, id)
		if sp.InstanceID != id || sp.Agent != AgentClaude || sp.EndReportedAtMillis != 0 {
			t.Errorf("%s = id %q, agent %q, end report %d; want its id, claude and no end report", id,
				sp.InstanceID, sp.Agent, sp.EndReportedAtMillis)
		}
	}
	live := mustGetSpawn(t, s, v8LiveID)
	if !slices.Equal(live.AgentArgs, []string{"--model", "opus"}) || live.SessionID != "sess-live" {
		t.Errorf("live row = args %v, session %q; want [--model opus] and sess-live", live.AgentArgs, live.SessionID)
	}
	if got := mustGetSpawn(t, s, v8UnsettledID).ParentID; got != v8LiveID {
		t.Errorf("unsettled row's parent = %q; want %q (the renamed key still joins)", got, v8LiveID)
	}
	reused := mustGetSpawn(t, s, v8ReusedID)
	entries, err := s.ListSessionHistory(v8ReusedID, reused.LifeNumber)
	if err != nil || len(entries) != 1 || entries[0].SessionID != "sess-r-2-prior" {
		t.Errorf("ListSessionHistory(reused, life %d) = %+v, %v; want sess-r-2-prior only", reused.LifeNumber, entries, err)
	}
	pr, err := s.GetPermissionRequest(v8RelayID, "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa")
	if err != nil || pr.InstanceID != v8RelayID || pr.ToolUseID != "toolu_01" {
		t.Errorf("open request = %+v, %v; want its row's id and toolu_01", pr, err)
	}
	if rows, err := s.PermissionRequestsForSpawn(v8RelayID); err != nil || len(rows) != 2 {
		t.Errorf("PermissionRequestsForSpawn = %d rows, %v; want the open and the decided request", len(rows), err)
	}
}

// TestV9MigratedRowResumes: a migrated finished row moves to pending and is
// restored by resume's writes (they name the renamed columns); the restore
// writes back an end report the move cleared, the one v9 value a v8 row never
// held.
func TestV9MigratedRowResumes(t *testing.T) {
	f, s, _ := migrateV8Fixture(t)
	const reported = int64(1790000000999)
	withRaw(t, f.path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE spawns SET end_reported_at = ? WHERE instance_id = ?`, reported, v8ReusedID)
	})
	before := mustGetSpawn(t, s, v8ReusedID)
	if before.EndReportedAtMillis != reported {
		t.Fatalf("end report = %d; want %d read back", before.EndReportedAtMillis, reported)
	}

	res, moved, err := s.MoveToPending(v8ReusedID, before.Snapshot, 1790000001000, "0123456789abcdef", "/tmp/ad-v9/sock", "", LaunchOwner{})
	if err != nil || res != CondApplied {
		t.Fatalf("MoveToPending = %v, %v; want applied", res, err)
	}
	got := mustGetSpawn(t, s, v8ReusedID)
	if got.State != StatePending || got.EndReportedAtMillis != 0 || got.Agent != AgentClaude {
		t.Errorf("moved row = state %q, end report %d, agent %q; want pending, none, claude", got.State, got.EndReportedAtMillis, got.Agent)
	}
	// What resume's move does not touch is the row's resume material: the
	// session id, the args, the life and that life's archived sessions.
	if got.SessionID != "sess-r-2" || !slices.Equal(got.AgentArgs, []string{"--resume"}) || got.LifeNumber != before.LifeNumber {
		t.Errorf("moved row = session %q, args %v, life %d; want sess-r-2, [--resume], life %d kept",
			got.SessionID, got.AgentArgs, got.LifeNumber, before.LifeNumber)
	}
	entries, err := s.ListSessionHistory(v8ReusedID, got.LifeNumber)
	if err != nil || len(entries) != 1 || entries[0].SessionID != "sess-r-2-prior" {
		t.Errorf("ListSessionHistory(reused, life %d) after the move = %+v, %v; want sess-r-2-prior kept", got.LifeNumber, entries, err)
	}

	prior := ResumePrior{State: before.State, EndedAtText: before.EndedAtText, EndReportedAtMillis: before.EndReportedAtMillis}
	if res, err := s.RestoreAfterFailedResume(v8ReusedID, moved, prior); err != nil || res != CondApplied {
		t.Fatalf("RestoreAfterFailedResume = %v, %v; want applied", res, err)
	}
	if got := mustGetSpawn(t, s, v8ReusedID); got.State != StateEnded || got.EndReportedAtMillis != reported || got.Agent != AgentClaude {
		t.Errorf("restored row = state %q, end report %d, agent %q; want ended, %d, claude", got.State, got.EndReportedAtMillis, got.Agent, reported)
	}
}

// TestV9LeavesNoClaudeColumn: no table of a new store, nor of a migrated v8
// one (every table sqlite_master lists, so a later table is held to it too),
// has a column named claude_*, and the six renamed columns hold their new
// names (SR-3.1).
func TestV9LeavesNoClaudeColumn(t *testing.T) {
	cases := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"a new store", func(t *testing.T) string { p, _ := newClosedStore(t); return p }},
		{"a migrated v8 store", func(t *testing.T) string {
			f, s, _ := migrateV8Fixture(t)
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			return f.path
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path(t)
			withRaw(t, path, func(db *sql.DB) {
				tables := queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type = 'table'`)
				if len(tables) == 0 {
					t.Fatal("sqlite_master lists no table")
				}
				for _, table := range tables {
					for _, c := range tableColumnNames(t, db, table) {
						if strings.HasPrefix(c, "claude_") {
							t.Errorf("%s has the column %s; want no claude_ column", table, c)
						}
					}
				}
				for _, r := range v9RenameSpecs {
					if !slices.Contains(tableColumnNames(t, db, r.table), r.newName) {
						t.Errorf("%s has no column %s", r.table, r.newName)
					}
				}
			})
		})
	}
}
