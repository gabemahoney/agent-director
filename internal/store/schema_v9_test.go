package store

// Schema v9 (b.ssz, the agent-neutral names): the v8 fixture's rows after the
// hop (every value kept under its new column name, each row's agent claude, no
// end report), a migrated row's reads and resume writes, and no column left
// with a claude_ name. b.xjn folded its subagent storage into the same hop: the
// migrated rows read untracked (subagent_tracking 0) beside empty subagent
// tables whose foreign keys work. The column specs, re-entry and rollback rows
// are schema_test.go's; the fixture and its checks: migration_fixtures_test.go.
// There is no v9 → v8 recipe: the downgrade guide stops at v8, and the subagent
// objects have no recipe of their own (v9 was never released).

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
// name, each row's agent is claude with no end report, every row reads
// subagent_tracking 0 (untracked) and no subagent entry or claimable launch
// exists, the sentinel is consumed and the store id is kept.
func TestV9MigrationKeepsV8Rows(t *testing.T) {
	f, s, storeID := migrateV8Fixture(t)
	if got := s.StoreID(); got != storeID {
		t.Errorf("StoreID() after the hop = %q; want %q kept", got, storeID)
	}
	assertSentinel(t, f.dir, false)
	assertV8ValuesKept(t, f.path, f, nil)
	for _, id := range f.ids {
		assertV9Defaults(t, f.path, id)
		if sp := mustGetSpawn(t, s, id); sp.SubagentTracking {
			t.Errorf("%s reads tracked after the hop; want a pre-upgrade row untracked", id)
		}
	}
	if got := assertOneStoreID(t, f.path); got != storeID {
		t.Errorf("store_id after the hop = %q; want %q", got, storeID)
	}
}

// TestV9SubagentConstraints checks required life identity, uniqueness and
// foreign keys with cascade deletion on fresh and migrated stores.
func TestV9SubagentConstraints(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		name := "fresh"
		if migrated {
			name = "v8-to-v9"
		}
		t.Run(name, func(t *testing.T) {
			var s *Store
			if migrated {
				_, s, _ = migrateV8Fixture(t)
			} else {
				s, _ = openTempStore(t)
				for _, id := range []string{v8OptedOutID, v8LiveID} {
					insertRow(t, s.db, "spawns", map[string]any{
						"instance_id": id, "state": "waiting", "cwd": "/tmp",
						"tmux_session_name": id, "relay_mode": "off", "agent": "claude",
					})
				}
			}
			const entry = `INSERT INTO subagents (instance_id, subagent_id, launch_token, confirmed, state, opened_at, activity_at, life_number)
				VALUES (?, 'agent-1', ?, 1, 'running', 1, 1, ?)`
			const launch = `INSERT INTO subagent_launches (instance_id, launch_token, tool_use_id, subagent_type, opened_at)
				VALUES (?, 'tok', 'toolu_1', 'general-purpose', 1)`
			for _, id := range []string{v8OptedOutID, v8LiveID} {
				for _, life := range []int{0, 2} {
					mustExec(t, s.db, entry, id, "tok", life)
				}
				mustExec(t, s.db, launch, id)
			}
			for _, life := range []int{0, 2} {
				if _, err := s.db.Exec(entry, v8LiveID, "another-launch", life); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
					t.Errorf("duplicate subagent in life %d = %v; want unique constraint refusal across launches", life, err)
				}
			}
			for _, tc := range []struct {
				name    string
				query   string
				args    []any
				refusal string
			}{
				{"omitted life", `INSERT INTO subagents (instance_id, subagent_id, launch_token, confirmed, state, opened_at, activity_at)
					VALUES (?, 'no-life', 'tok', 1, 'running', 1, 1)`, []any{v8LiveID}, "NOT NULL constraint failed: subagents.life_number"},
				{"null life", entry, []any{v8LiveID, "tok", nil}, "NOT NULL constraint failed: subagents.life_number"},
				{"entry without spawn", entry, []any{"no-such-row", "tok", 2}, "FOREIGN KEY constraint failed"},
				{"launch without spawn", launch, []any{"no-such-row"}, "FOREIGN KEY constraint failed"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if _, err := s.db.Exec(tc.query, tc.args...); err == nil || !strings.Contains(err.Error(), tc.refusal) {
						t.Errorf("insert = %v; want %s", err, tc.refusal)
					}
				})
			}
			mustExec(t, s.db, `DELETE FROM spawns WHERE instance_id = ?`, v8OptedOutID)
			for table, want := range map[string]int{"subagents": 2, "subagent_launches": 1} {
				var n int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE instance_id = ?`, v8OptedOutID).Scan(&n); err != nil || n != 0 {
					t.Errorf("%s rows of the deleted row = %d, %v; want 0 (cascade across lives)", table, n, err)
				}
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE instance_id = ?`, v8LiveID).Scan(&n); err != nil || n != want {
					t.Errorf("%s rows of the kept row = %d, %v; want %d", table, n, err, want)
				}
			}
		})
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

// TestV9LeavesNoClaudeColumn rejects legacy claude_* columns in fresh and
// migrated stores, allowing the capacity metadata introduced in v12.
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
						if strings.HasPrefix(c, "claude_") && !(table == "spawns" && c == "claude_capacity") {
							t.Errorf("%s has the legacy column %s", table, c)
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
