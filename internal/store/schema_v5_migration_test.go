package store

// schema_v5_migration_test.go — migration-gate coverage for the schema v5 step
// (b.fmk, SR-5.1/SR-5.4). v5 adds twelve spawns columns and
// session_history.life_number. Template: schema_v4_migration_test.go. Fixtures
// and v5 read helpers live in migration_fixtures_test.go (SR-20.3); this file
// adds no inline SQL beyond PRAGMA reads.
//
// Dispositions: authorised migration; refusal without sentinel (byte-identical,
// ErrSchemaMigrationRequired); idempotent and partial re-entry; fresh vs
// migrated convergence on both tables; rollback on an injected failure; history
// fixture at life 0 (AC-REUSE-25, AC-RES-20); pending row with no launch start
// (AC-FM-14).

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// assertV5Columns checks every v5 column is present exactly once with its
// SR-5.1 type, NOT NULL flag and default.
func assertV5Columns(t *testing.T, path string) {
	t.Helper()
	db := openRaw(t, path)
	for _, table := range []string{"spawns", "session_history"} {
		shape := readTableShape(t, db, table)
		for _, want := range v5ColumnSpecs {
			if want.table != table {
				continue
			}
			var found []tableColumn
			for _, c := range shape {
				if c.name == want.name {
					found = append(found, c)
				}
			}
			if len(found) != 1 {
				t.Errorf("%s: found %d columns; want exactly 1", want.key(), len(found))
				continue
			}
			got := found[0]
			wantNN := 0
			if want.notNull {
				wantNN = 1
			}
			if got.typ != want.typ || got.notNull != wantNN || got.dflt.String != want.dflt || got.dflt.Valid != (want.dflt != "") {
				t.Errorf("%s = {type %q notnull %d dflt %v}; want {type %q notnull %d dflt %q}",
					want.key(), got.typ, got.notNull, got.dflt, want.typ, wantNN, want.dflt)
			}
		}
	}
}

// assertNoV5Columns fails if any v5 column is present on either table.
func assertNoV5Columns(t *testing.T, path string) {
	t.Helper()
	db := openRaw(t, path)
	for _, want := range v5ColumnSpecs {
		for _, c := range readTableShape(t, db, want.table) {
			if c.name == want.name {
				t.Errorf("%s present; want absent", want.key())
			}
		}
	}
}

// assertV5Defaults checks id's v5 values are the ordinary defaults: every
// spawns column at its DDL default (or NULL), every history entry at life 0.
func assertV5Defaults(t *testing.T, path, id string, historyLen int) {
	t.Helper()
	got := readV5Columns(t, path, id)
	for _, spec := range v5ColumnSpecs {
		if spec.table != "spawns" {
			continue
		}
		if v := got.spawns[spec.name]; v != spec.migratedValue() {
			t.Errorf("%s: %s = %s; want %s", id, spec.key(), v, spec.migratedValue())
		}
	}
	if len(got.historyLife) != historyLen {
		t.Errorf("%s: %d history entries; want %d", id, len(got.historyLife), historyLen)
	}
	for i, life := range got.historyLife {
		if life != "0" {
			t.Errorf("%s: history entry %d life_number = %s; want 0", id, i, life)
		}
	}
}

// unquote turns a quote() text literal back into its value ("NULL" → "").
func unquote(lit string) string {
	if lit == "NULL" {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(lit, "'"), "'"), "''", "'")
}

// migrateV4Fixture opens f with a {4→schemaVersion} sentinel and closes it.
func migrateV4Fixture(t *testing.T, f v4HistoryFixture) {
	t.Helper()
	writeSentinel(t, f.dir, 4, schemaVersion)
	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("Open(authorised v4): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestV5Migration_AuthorizedFromV4 covers the authorised hop: version lands at
// schemaVersion, all thirteen columns appear with SR-5.1 shape, sentinel consumed.
func TestV5Migration_AuthorizedFromV4(t *testing.T) {
	f := makeV4HistoryFixture(t, t.TempDir())
	assertNoV5Columns(t, f.path)

	migrateV4Fixture(t, f)

	if v := readUserVersion(t, f.path); v != schemaVersion {
		t.Errorf("user_version = %d; want %d", v, schemaVersion)
	}
	assertV5Columns(t, f.path)
	assertSentinelAbsent(t, f.dir)
}

// TestV5Migration_RefusedWithoutSentinel: a v4 store opened without a sentinel
// is refused with ErrSchemaMigrationRequired and left byte-identical at v4.
func TestV5Migration_RefusedWithoutSentinel(t *testing.T) {
	f := makeV4HistoryFixture(t, t.TempDir())
	before := snapshotDBBytes(t, f.path)

	_, err := Open(f.path)
	if !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("Open(v4, no sentinel) err = %v; want errors.Is ErrSchemaMigrationRequired", err)
	}

	assertDBBytesUnchanged(t, f.path, before)
	if v := readUserVersion(t, f.path); v != 4 {
		t.Errorf("user_version = %d after refused open; want 4", v)
	}
	assertSentinelAbsent(t, f.dir)
}

// TestV5Migration_IdempotentReentry: migrateV4toV5 succeeds over a DB already
// holding none, some, or all of the v5 columns, leaving each exactly once at v5.
func TestV5Migration_IdempotentReentry(t *testing.T) {
	allKeys := make([]string, 0, len(v5ColumnSpecs))
	for _, c := range v5ColumnSpecs {
		allKeys = append(allKeys, c.key())
	}
	cases := []struct {
		name     string
		preAdded []string
	}{
		{"first spawns column only", []string{"spawns.row_version"}},
		{"session_history column only", []string{"session_history.life_number"}},
		{"mix across both tables", []string{"spawns.life_number", "spawns.pane_id", "session_history.life_number"}},
		{"all thirteen already present", allKeys},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := makeVersionedDB(t, t.TempDir(), 4)
			preAddV5Columns(t, path, tc.preAdded...)
			db := openRaw(t, path)
			if err := migrateV4toV5(db); err != nil {
				t.Fatalf("migrateV4toV5 over partial DB: %v", err)
			}
			assertV5Columns(t, path)
			if v := readUserVersionDB(t, db); v != 5 {
				t.Errorf("user_version = %d; want 5", v)
			}
		})
	}

	t.Run("second run on a migrated DB", func(t *testing.T) {
		path := makeVersionedDB(t, t.TempDir(), 4)
		db := openRaw(t, path)
		for run := 1; run <= 2; run++ {
			if err := migrateV4toV5(db); err != nil {
				t.Fatalf("migrateV4toV5 run %d: %v", run, err)
			}
		}
		assertV5Columns(t, path)
		if v := readUserVersionDB(t, db); v != 5 {
			t.Errorf("user_version = %d; want 5", v)
		}
	})
}

// TestV5FreshCreate_And_MigratedConverge: a fresh store and a v1→current
// migrated store expose identical table_info for spawns and session_history.
func TestV5FreshCreate_And_MigratedConverge(t *testing.T) {
	freshPath := t.TempDir() + "/state.db"
	fresh, err := OpenOrInit(freshPath)
	if err != nil {
		t.Fatalf("OpenOrInit(fresh): %v", err)
	}
	defer fresh.Close()

	migDir := t.TempDir()
	migPath := makeV1DB(t, migDir)
	writeSentinel(t, migDir, 1, schemaVersion)
	migrated, err := Open(migPath)
	if err != nil {
		t.Fatalf("Open(v1→current chain): %v", err)
	}
	defer migrated.Close()

	for _, table := range []string{"spawns", "session_history"} {
		f, m := readTableShape(t, fresh.db, table), readTableShape(t, migrated.db, table)
		if !reflect.DeepEqual(f, m) {
			t.Errorf("%s shape diverges:\n fresh    %+v\n migrated %+v", table, f, m)
		}
	}
	assertV5Columns(t, freshPath)
}

// TestV5Migration_RollbackOnInjectedFailure: a failure after the spawns
// columns are added rolls back the whole hop — v4, no new columns on either
// table, rows unchanged, open fails, sentinel kept.
func TestV5Migration_RollbackOnInjectedFailure(t *testing.T) {
	f := makeV4HistoryFixture(t, t.TempDir())
	breakV5SessionHistoryHop(t, f.path)
	writeSentinel(t, f.dir, 4, schemaVersion)

	s, err := Open(f.path)
	if err == nil {
		_ = s.Close()
		t.Fatalf("Open(v4 with failing hop) succeeded; want error")
	}
	if errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("Open err = %v; want a migration failure, not a refusal", err)
	}
	// The injection point is the last ALTER, so every spawns ALTER ran first.
	if !strings.Contains(err.Error(), "session_history.life_number") {
		t.Errorf("Open err = %v; want the failure at session_history.life_number", err)
	}

	if v := readUserVersion(t, f.path); v != 4 {
		t.Errorf("user_version = %d after failed hop; want 4", v)
	}
	assertNoV5Columns(t, f.path)
	for _, id := range f.ids {
		if got := readRawSpawn(t, f.path, id, f.spawnsCols); !reflect.DeepEqual(got, f.rows[id]) {
			t.Errorf("%s row changed after rollback:\n got  %v\n want %v", id, got, f.rows[id])
		}
	}
	assertSentinelPresent(t, f.dir)
}

// TestV5Migration_HistoryFixtureAtLifeZero (AC-REUSE-25, AC-RES-20): every
// migrated row and history entry is at life 0 with ordinary defaults, and
// every v4 value and history entry reads back exactly as seeded.
func TestV5Migration_HistoryFixtureAtLifeZero(t *testing.T) {
	f := makeV4HistoryFixture(t, t.TempDir())
	migrateV4Fixture(t, f)

	for _, id := range f.ids {
		assertV5Defaults(t, f.path, id, len(f.history[id]))
		if got := readRawSpawn(t, f.path, id, f.spawnsCols); !reflect.DeepEqual(got, f.rows[id]) {
			t.Errorf("%s v4 values changed:\n got  %v\n want %v", id, got, f.rows[id])
		}
		if got := readRawHistory(t, f.path, id, f.historyCols); !reflect.DeepEqual(got, f.history[id]) {
			t.Errorf("%s history changed:\n got  %v\n want %v", id, got, f.history[id])
		}
	}

	// Read paths a verb uses see the same values.
	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer s.Close()
	listed, err := s.ListSpawns(ListFilters{})
	if err != nil {
		t.Fatalf("ListSpawns: %v", err)
	}
	if len(listed) != len(f.ids) {
		t.Errorf("ListSpawns returned %d rows; want %d", len(listed), len(f.ids))
	}
	for _, sp := range listed {
		got, err := s.GetSpawn(sp.ClaudeInstanceID)
		if err != nil {
			t.Fatalf("GetSpawn(%s): %v", sp.ClaudeInstanceID, err)
		}
		if !reflect.DeepEqual(got, sp) {
			t.Errorf("%s: GetSpawn %+v differs from ListSpawns %+v", sp.ClaudeInstanceID, got, sp)
		}
	}
	for _, id := range f.ids {
		assertSpawnMatchesRaw(t, s, id, f.rows[id])
		hist, err := s.ListSessionHistory(id)
		if err != nil {
			t.Fatalf("ListSessionHistory(%s): %v", id, err)
		}
		if len(hist) != len(f.history[id]) {
			t.Fatalf("%s: ListSessionHistory has %d entries; want %d", id, len(hist), len(f.history[id]))
		}
		for i, e := range hist {
			want := f.history[id][i]
			if e.ClaudeSessionID != unquote(want["claude_session_id"]) || e.JSONLPath != unquote(want["jsonl_path"]) {
				t.Errorf("%s history[%d] = {%q %q}; want {%s %s}", id, i,
					e.ClaudeSessionID, e.JSONLPath, want["claude_session_id"], want["jsonl_path"])
			}
		}
	}
}

// assertSpawnMatchesRaw checks GetSpawn's view of id against its raw v4 values.
func assertSpawnMatchesRaw(t *testing.T, s *Store, id string, raw map[string]string) {
	t.Helper()
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	pid := ""
	if sp.PID != 0 {
		pid = strconv.Itoa(sp.PID)
	}
	args, _ := json.Marshal(sp.ClaudeArgs)
	got := map[string]string{
		"parent_id":                 sp.ParentID,
		"state":                     sp.State,
		"cwd":                       sp.CWD,
		"tmux_session_name":         sp.TmuxSessionName,
		"relay_mode":                sp.RelayMode,
		"jsonl_path":                sp.JSONLPath,
		"claude_session_id":         sp.ClaudeSessionID,
		"pid":                       pid,
		"proc_starttime":            sp.ProcStarttime,
		"liveness_unverified_since": sp.LivenessUnverifiedSince,
		"liveness_note":             sp.LivenessNote,
		"claude_args":               string(args),
	}
	for col, g := range got {
		if want := unquote(raw[col]); g != want {
			t.Errorf("%s: GetSpawn %s = %q; want %q", id, col, g, want)
		}
	}
	for col, m := range map[string]map[string]string{"labels": sp.Labels, "extra_env": sp.ExtraEnv} {
		var want map[string]string
		if err := json.Unmarshal([]byte(unquote(raw[col])), &want); err != nil {
			t.Fatalf("%s: decode raw %s: %v", id, col, err)
		}
		if !reflect.DeepEqual(m, want) {
			t.Errorf("%s: GetSpawn %s = %v; want %v", id, col, m, want)
		}
	}
	if (sp.EndedAt == nil) != (raw["ended_at"] == "NULL") {
		t.Errorf("%s: GetSpawn EndedAt = %v; raw ended_at %s", id, sp.EndedAt, raw["ended_at"])
	}
}

// TestV5Migration_PendingRowHasNoLaunchStart (AC-FM-14): the migrated pending
// row has NULL launch_started_at, stays pending, and has the ordinary defaults.
func TestV5Migration_PendingRowHasNoLaunchStart(t *testing.T) {
	f := makeV4HistoryFixture(t, t.TempDir())
	migrateV4Fixture(t, f)

	got := readV5Columns(t, f.path, f.pending)
	if v := got.spawns["launch_started_at"]; v != "NULL" {
		t.Errorf("pending launch_started_at = %s; want NULL", v)
	}
	if st := readRawSpawn(t, f.path, f.pending, []string{"state"})["state"]; st != "'pending'" {
		t.Errorf("pending row state = %s; want 'pending'", st)
	}
	assertV5Defaults(t, f.path, f.pending, len(f.history[f.pending]))
}
