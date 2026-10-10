package store

// The schema and the administrator-gated migration chain (SR-1, SR-2, SR-5,
// SR-12): a fresh store, an authorized open from every released version,
// refusals, step re-entry (b.93m), rollback and the consume. Fixtures:
// migration_fixtures_test.go; v5 data, store id and downgrade cases:
// schema_v5_test.go; v6 data and downgrade cases: schema_v6_test.go; v7's:
// schema_v7_test.go.

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openTempStore opens a store under t.TempDir() for the test and returns it
// with its path (storefix.OpenTempStore's white-box twin: storefix imports store).
func openTempStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("OpenOrInit(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// columnsByName returns shape with each table's columns sorted by name, to
// compare schemas whose columns were added in another order.
func columnsByName(shape map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range shape {
		if cols, ok := v.([]tableColumn); ok {
			cols = slices.Clone(cols)
			slices.SortFunc(cols, func(a, b tableColumn) int { return strings.Compare(a.name, b.name) })
			v = cols
		}
		out[k] = v
	}
	return out
}

// TestFreshStoreSchema checks a new store and a reopen of it: user_version at
// schemaVersion, every table and index, the v3, v5, v6 and v7 columns (in
// order, last on their tables), store_meta, WAL and foreign keys on, no
// sentinel, and a 0700 parent and 0600 file that a second open does not widen.
func TestFreshStoreSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent-director") // OpenOrInit creates the parent
	path := filepath.Join(dir, "state.db")
	for i, open := range []func(string) (*Store, error){OpenOrInit, Open} {
		s, err := open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		var fk int
		if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
			t.Errorf("open #%d: foreign_keys = %d, %v; want 1", i, fk, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatalf("stat %s: %v", p, err)
			}
			if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != want {
				t.Errorf("open #%d: mode of %s = %o; want %o", i, p, got, want)
			}
		}
	}
	db := openRaw(t, path)
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", journal, err)
	}
	if v := userVersion(t, db); v != schemaVersion {
		t.Errorf("user_version = %d; want %d", v, schemaVersion)
	}
	shape := schemaShape(t, db)
	for _, name := range []string{"table spawns", "table permission_requests", "table session_history",
		"index idx_spawns_state", "index idx_spawns_last_seen", "index idx_spawns_parent",
		"index idx_permission_requests_instance_decision", "index idx_permission_requests_decision_decided_at",
		"index idx_session_history_instance"} {
		if _, ok := shape[name]; !ok {
			t.Errorf("%s missing", name)
		}
	}
	// v2's composite UNIQUE(claude_instance_id, request_token).
	if got := shape["index sqlite_autoindex_permission_requests_1"]; !reflect.DeepEqual(got, []string{"claude_instance_id", "request_token"}) {
		t.Errorf("permission_requests unique index = %v; want (claude_instance_id, request_token)", got)
	}
	if got := shape["table store_meta"]; !reflect.DeepEqual(got, storeMetaShape) {
		t.Errorf("store_meta = %+v; want %+v", got, storeMetaShape)
	}
	if got, want := tableColumnNames(t, db, "session_history"), []string{"history_id", "claude_instance_id",
		"claude_session_id", "jsonl_path", "recorded_at", "life_number"}; !slices.Equal(got, want) {
		t.Errorf("session_history columns = %v; want %v", got, want)
	}
	assertColumnSpecs(t, db, slices.Concat(v3ColumnSpecs, v5ColumnSpecs, v6ColumnSpecs, v7ColumnSpecs))
	// spawns ends with the v6 columns then v7's idle_since; permission_requests
	// with the v7 columns after created_at (the two-places rule's order).
	wantSpawnsTail := append(v6ColumnNames(), v7ColumnNames("spawns")...)
	if cols := tableColumnNames(t, db, "spawns"); len(cols) < len(wantSpawnsTail) || !slices.Equal(cols[len(cols)-len(wantSpawnsTail):], wantSpawnsTail) {
		t.Errorf("spawns columns = %v; want the v6 then v7 columns last, in order %v", cols, wantSpawnsTail)
	}
	wantRequestsTail := append([]string{"created_at"}, v7ColumnNames("permission_requests")...)
	if cols := tableColumnNames(t, db, "permission_requests"); len(cols) < len(wantRequestsTail) ||
		!slices.Equal(cols[len(cols)-len(wantRequestsTail):], wantRequestsTail) {
		t.Errorf("permission_requests columns = %v; want created_at then the v7 columns last, in order %v", cols, wantRequestsTail)
	}
	assertSentinel(t, dir, false)
}

// TestNoAgentSuppliedIdentifierIsAKey: the hook input's tool_use_id and
// agent_id are recorded on a request (b.146 rule 2) but never identify one
// (b.c26): no index, UNIQUE or primary key covers either; a request stays
// keyed by its row id and (claude_instance_id, request_token), the token the
// relay hook mints.
func TestNoAgentSuppliedIdentifierIsAKey(t *testing.T) {
	s, _ := openTempStore(t)
	for _, idx := range queryStrings(t, s.db, `SELECT name FROM sqlite_master WHERE type = 'index'`) {
		for _, col := range queryStrings(t, s.db, `SELECT name FROM pragma_index_info(?)`, idx) {
			if col == "tool_use_id" || col == "agent_id" {
				t.Errorf("index %s covers %s; want no agent-supplied id in any key", idx, col)
			}
		}
	}
	for _, c := range readTableShape(t, s.db, "permission_requests") {
		if c.pk != 0 && c.name != "request_id" {
			t.Errorf("permission_requests primary key includes %s; want request_id alone", c.name)
		}
	}
}

// TestAuthorizedMigrationFromEveryVersion: each released version beside an
// exact-match sentinel climbs the real chain in one open to the fresh store's
// schema (the two-places rule), consumes the sentinel with one
// ad.schema.migrated line, keeps a pre-existing row with the new columns'
// defaults and one store id, and does not backfill v1's permission rows. A
// reopen is a clean no-op, and a stale sentinel beside the current store is
// left alone (SR-1, SR-2, SR-5.4).
func TestAuthorizedMigrationFromEveryVersion(t *testing.T) {
	fresh, _ := openTempStore(t)
	want := schemaShape(t, fresh.db)
	for from := 1; from < schemaVersion; from++ {
		t.Run(fmt.Sprintf("v%d", from), func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, from)
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode)
					VALUES ('pre', 'working', '/tmp', 'pre-sess', 'on')`)
				if from == 1 { // v1's permission_requests has no request_token
					mustExec(t, db, `INSERT INTO permission_requests (claude_instance_id, tool_name, tool_input) VALUES ('pre', 'Bash', '{}')`)
				}
			})
			writeSentinel(t, dir, from, schemaVersion)
			mark := TrailMark(t)

			s, err := Open(path)
			if err != nil {
				t.Fatalf("Open(authorized v%d): %v", from, err)
			}
			if got := schemaShape(t, s.db); !reflect.DeepEqual(got, want) {
				t.Errorf("migrated schema differs from a fresh store's:\n got  %v\n want %v", got, want)
			}
			if v := userVersion(t, s.db); v != schemaVersion {
				t.Errorf("user_version = %d; want %d", v, schemaVersion)
			}
			var requests int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM permission_requests").Scan(&requests); err != nil || requests != 0 {
				t.Errorf("permission_requests rows = %d, %v; want 0 (no v1 backfill)", requests, err)
			}
			sp, err := s.GetSpawn("pre")
			if err != nil || sp.ExtraEnv == nil || len(sp.ExtraEnv) != 0 || sp.PID != 0 || sp.ProcStarttime != "" ||
				sp.LivenessUnverifiedSince != "" || sp.LivenessNote != "" || sp.LifeNumber != 0 || sp.NoPreTrust ||
				sp.LaunchOwner != (LaunchOwner{}) {
				t.Errorf("pre-migration row = %+v, %v; want it read with the defaults", sp, err)
			}
			id := s.StoreID()
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			assertSentinel(t, dir, false)
			lines := trailEventsSince(t, mark, "ad.schema.migrated")
			if len(lines) != 1 {
				t.Fatalf("ad.schema.migrated lines = %d; want 1", len(lines))
			}
			assertTrailStr(t, lines[0], "source", "ad_store_schema")
			assertTrailStr(t, lines[0], "message", fmt.Sprintf("migrated %d→%d, consumed authorization", from, schemaVersion))
			assertTrailInt(t, lines[0], "from", from)
			assertTrailInt(t, lines[0], "to", schemaVersion)
			assertV5Defaults(t, path, "pre", 0)
			assertV6Defaults(t, path, "pre")
			assertV7Defaults(t, path, "pre")
			if raw := assertOneStoreID(t, path); raw != id {
				t.Errorf("StoreID() = %q; raw store_id = %q", id, raw)
			}

			for _, stale := range []bool{false, true} {
				if stale {
					writeSentinel(t, dir, from, schemaVersion)
				}
				mark := TrailMark(t)
				if got := openStoreID(t, Open, path); got != id {
					t.Errorf("reopen (stale sentinel %v): StoreID() = %q; want %q", stale, got, id)
				}
				assertSentinel(t, dir, stale)
				for _, ev := range []string{"ad.schema.migrated", "ad.schema.authorization_delete_failed"} {
					if n := len(trailEventsSince(t, mark, ev)); n != 0 {
						t.Errorf("reopen (stale sentinel %v) emitted %d %s; want 0", stale, n, ev)
					}
				}
			}
		})
	}
}

// TestMigrationRefusedWithoutSentinel: a store at each older version opened
// with no sentinel gets ErrSchemaMigrationRequired with the SR-1.4 dead-end
// text and no self-service breadcrumb, is left byte-identical at its version,
// and no sentinel or authorization_mismatch line appears.
func TestMigrationRefusedWithoutSentinel(t *testing.T) {
	for from := 1; from < schemaVersion; from++ {
		t.Run(fmt.Sprintf("v%d", from), func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, from)
			before, mark := snapshotDBBytes(t, path), TrailMark(t)
			_, err := Open(path)
			if !errors.Is(err, ErrSchemaMigrationRequired) {
				t.Fatalf("Open err = %v; want ErrSchemaMigrationRequired", err)
			}
			msg := err.Error()
			if want := fmt.Sprintf("state.db is schema v%d; this binary requires v%d. Migration must be performed "+
				"by an administrator via the agent-director install process.", from, schemaVersion); !strings.Contains(msg, want) {
				t.Errorf("error = %q; want it to contain %q", msg, want)
			}
			for _, bad := range []string{"migrate", "--", "AGENT_DIRECTOR", "~/.agent-director", "/", sentinelFilename} {
				if strings.Contains(strings.ToLower(msg), strings.ToLower(bad)) {
					t.Errorf("error %q leaks the self-service breadcrumb %q", msg, bad)
				}
			}
			assertDBBytesUnchanged(t, path, before)
			if v := readUserVersion(t, path); v != from {
				t.Errorf("user_version = %d; want %d", v, from)
			}
			assertSentinel(t, dir, false)
			if n := len(trailEventsSince(t, mark, "ad.schema.authorization_mismatch")); n != 0 {
				t.Errorf("authorization_mismatch lines = %d; want 0", n)
			}
		})
	}
}

// TestGateRefusesBadSentinel: a malformed sentinel, or one with a wrong from or
// to end, refuses like a missing one, is left in place unchanged (admin
// evidence), and emits one authorization_mismatch line reporting both ends
// (-1 when unknown).
func TestGateRefusesBadSentinel(t *testing.T) {
	cases := []struct {
		name, reason       string
		foundFrom, foundTo int // -1: a malformed sentinel
	}{
		{"malformed_json", "malformed_json", -1, -1},
		{"wrong_from", "version_mismatch", 2, schemaVersion},
		{"wrong_to", "version_mismatch", 1, schemaVersion + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 1)
			if tc.foundFrom < 0 {
				writeSentinelRaw(t, dir, []byte("{not valid json"))
			} else {
				writeSentinel(t, dir, tc.foundFrom, tc.foundTo)
			}
			sentinel := filepath.Join(dir, sentinelFilename)
			sentinelBefore, _ := os.ReadFile(sentinel)
			before, mark := snapshotDBBytes(t, path), TrailMark(t)
			if _, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
				t.Fatalf("Open err = %v; want ErrSchemaMigrationRequired", err)
			}
			assertDBBytesUnchanged(t, path, before)
			if v := readUserVersion(t, path); v != 1 {
				t.Errorf("user_version = %d; want 1", v)
			}
			if got, err := os.ReadFile(sentinel); err != nil || !bytes.Equal(got, sentinelBefore) {
				t.Errorf("sentinel after the refusal = %q, %v; want %q left in place", got, err, sentinelBefore)
			}
			lines := trailEventsSince(t, mark, "ad.schema.authorization_mismatch")
			if len(lines) != 1 {
				t.Fatalf("authorization_mismatch lines = %d; want 1", len(lines))
			}
			assertTrailStr(t, lines[0], "source", "ad_store_schema")
			assertTrailStr(t, lines[0], "reason", tc.reason)
			for key, want := range map[string]int{"expected_from": 1, "expected_to": schemaVersion,
				"found_from": tc.foundFrom, "found_to": tc.foundTo} {
				assertTrailInt(t, lines[0], key, want)
			}
		})
	}
}

// TestNewerThanBinaryIsSchemaMismatch: a store stamped past this binary's
// version fails with ErrSchemaMismatch, never ErrSchemaMigrationRequired.
func TestNewerThanBinaryIsSchemaMismatch(t *testing.T) {
	for _, v := range []int{schemaVersion + 1, 1000} {
		path := makeVersionedDB(t, t.TempDir(), schemaVersion)
		stampUserVersion(t, path, v)
		if _, err := Open(path); !errors.Is(err, ErrSchemaMismatch) || errors.Is(err, ErrSchemaMigrationRequired) {
			t.Errorf("Open(user_version %d) err = %v; want ErrSchemaMismatch only", v, err)
		}
	}
}

// TestMigrationStepReentry: a step re-run over a DB it migrated wholly or in
// part succeeds and gives the schema one run gives (b.93m); v4→v5 and every
// later hop never add a second store id or replace one (SR-5.4).
func TestMigrationStepReentry(t *testing.T) {
	const preID = "0123456789abcdef"
	var allV5 []string
	for _, c := range v5ColumnSpecs {
		allV5 = append(allV5, c.key())
	}
	preV5 := func(keys ...string) func(*testing.T, string) {
		return func(t *testing.T, path string) { preAddV5Columns(t, path, keys...) }
	}
	preV6 := func(names ...string) func(*testing.T, string) {
		return func(t *testing.T, path string) { preAddV6Columns(t, path, names...) }
	}
	var allV7 []string
	for _, c := range v7ColumnSpecs {
		allV7 = append(allV7, c.key())
	}
	preV7 := func(keys ...string) func(*testing.T, string) {
		return func(t *testing.T, path string) { preAddV7Columns(t, path, keys...) }
	}
	meta := func(id string) func(*testing.T, string) {
		return func(t *testing.T, path string) { preAddStoreMeta(t, path, id) }
	}
	cases := []struct {
		name   string
		from   int
		pre    func(t *testing.T, path string)
		runs   int
		keepID string // the store id the hop must keep; "" when it creates one
	}{
		{"v2→v3 run twice", 2, nil, 2, ""},
		{"v2→v3 after pid was added", 2, func(t *testing.T, path string) {
			withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "ALTER TABLE spawns ADD COLUMN pid INTEGER") })
		}, 1, ""},
		{"v3→v4 run twice", 3, nil, 2, ""},
		{"v4→v5 run twice", 4, nil, 2, ""},
		{"v4→v5 after the first spawns column", 4, preV5("spawns.row_version"), 1, ""},
		{"v4→v5 after session_history.life_number", 4, preV5("session_history.life_number"), 1, ""},
		{"v4→v5 after a mix across both tables", 4, preV5("spawns.life_number", "spawns.pane_id", "session_history.life_number"), 1, ""},
		{"v4→v5 after all thirteen", 4, preV5(allV5...), 1, ""},
		{"v4→v5 with store_meta holding an id", 4, meta(preID), 1, preID},
		{"v4→v5 with store_meta empty", 4, meta(""), 1, ""},
		{"v5→v6 run twice", 5, nil, 2, ""},
		{"v5→v6 after launch_owner_pid", 5, preV6("launch_owner_pid"), 1, ""},
		{"v5→v6 after launch_owner_starttime alone", 5, preV6("launch_owner_starttime"), 1, ""},
		{"v5→v6 after all three", 5, preV6(v6ColumnNames()...), 1, ""},
		{"v6→v7 run twice", 6, nil, 2, ""},
		{"v6→v7 after hook_pid", 6, preV7("permission_requests.hook_pid"), 1, ""},
		{"v6→v7 after pane_answer and idle_since", 6, preV7("permission_requests.pane_answer", "spawns.idle_since"), 1, ""},
		{"v6→v7 after all twenty", 6, preV7(allV7...), 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var want map[string]any
			withRaw(t, makeVersionedDB(t, t.TempDir(), tc.from+1), func(db *sql.DB) { want = schemaShape(t, db) })
			path := makeVersionedDB(t, t.TempDir(), tc.from)
			if tc.pre != nil {
				tc.pre(t, path)
			}
			step, _ := migrationStepFrom(tc.from)
			db := openRaw(t, path)
			id := tc.keepID
			for run := 1; run <= tc.runs; run++ {
				if err := step.apply(db); err != nil {
					t.Fatalf("run %d: %v", run, err)
				}
				if tc.from >= 4 {
					if got := assertOneStoreID(t, path); id == "" {
						id = got
					} else if got != id {
						t.Errorf("run %d: store_id = %q; want %q kept", run, got, id)
					}
				}
			}
			if v := userVersion(t, db); v != tc.from+1 {
				t.Errorf("user_version = %d; want %d", v, tc.from+1)
			}
			if got := schemaShape(t, db); !reflect.DeepEqual(columnsByName(got), columnsByName(want)) {
				t.Errorf("schema after re-entry:\n got  %v\n want %v", got, want)
			}
		})
	}
}

// TestMigrationRollback: a step failing part-way rolls back its whole hop: the
// open fails as a migration failure (not a refusal), and the store keeps its
// version, schema, rows and sentinel (SR-2.4, SR-5.4, SR-20.6); a v6→v7 hop
// failing at its backfill leaves no column added and no request proven.
func TestMigrationRollback(t *testing.T) {
	cases := []struct {
		name    string
		from    int
		breakIt func(t *testing.T, path string)
		wantErr string // in the open's error
	}{
		{"v1→v2 at a conflicting index", 1, func(t *testing.T, path string) {
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `CREATE INDEX idx_permission_requests_instance_decision ON spawns(state)`)
			})
		}, ""},
		{"v4→v5 at session_history.life_number", 4, breakV5SessionHistoryHop, "session_history.life_number"},
		{"v4→v5 at the store_id insert", 4, breakV5StoreMetaStep, ""},
		{"v5→v6 at launch_owner_pidns, after the first two columns", 5, breakV6PIDNSColumn, "spawns.launch_owner_pidns"},
		{"v6→v7 at idle_since, after every permission_requests column", 6, breakV7IdleSinceColumn, "spawns.idle_since"},
		{"v6→v7 at the backfill, after every column", 6, breakV7Backfill, "prove finished rows' requests gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var path string
			switch tc.from {
			case 4:
				path = makeV4HistoryFixture(t, t.TempDir()).path
			case 5:
				path = makeV5Fixture(t, t.TempDir()).path
			case 6:
				path = makeV6Fixture(t, t.TempDir()).path
			default:
				path = makeVersionedDB(t, t.TempDir(), tc.from)
			}
			dir := filepath.Dir(path)
			tc.breakIt(t, path)
			before := dbDump(t, path)
			writeSentinel(t, dir, tc.from, schemaVersion)
			s, err := Open(path)
			if err == nil {
				_ = s.Close()
				t.Fatal("Open succeeded; want a migration failure")
			}
			if errors.Is(err, ErrSchemaMigrationRequired) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Open err = %v; want a migration failure naming %q", err, tc.wantErr)
			}
			if v := readUserVersion(t, path); v != tc.from {
				t.Errorf("user_version = %d; want %d", v, tc.from)
			}
			if after := dbDump(t, path); !reflect.DeepEqual(after, before) {
				t.Errorf("the failed hop changed the store:\n before %v\n after  %v", before, after)
			}
			assertSentinel(t, dir, true)
		})
	}
}

// TestConsumeDeleteFailure_LoudButOpenSucceeds: when the committed migration's
// sentinel cannot be deleted (a read-only directory), consumeAuthorization
// emits one authorization_delete_failed line and no migrated line, and leaves
// the sentinel as admin evidence. It drives the chain and the consume directly
// to reach the seam between the commit and the delete.
func TestConsumeDeleteFailure_LoudButOpenSucceeds(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode, so the delete cannot be made to fail")
	}
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 1)
	writeSentinel(t, dir, 1, schemaVersion)
	withRaw(t, path, func(db *sql.DB) {
		if err := runMigrationChain(db, 1); err != nil {
			t.Fatalf("runMigrationChain: %v", err)
		}
	})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	mark := TrailMark(t)

	consumeAuthorization(path, 1, schemaVersion)

	assertSentinel(t, dir, true)
	failed := trailEventsSince(t, mark, "ad.schema.authorization_delete_failed")
	if len(failed) != 1 {
		t.Fatalf("authorization_delete_failed lines = %d; want 1", len(failed))
	}
	assertTrailStr(t, failed[0], "source", "ad_store_schema")
	if _, ok := failed[0]["error"].(string); !ok {
		t.Errorf("[error] = %v; want a string", failed[0]["error"])
	}
	if n := len(trailEventsSince(t, mark, "ad.schema.migrated")); n != 0 {
		t.Errorf("ad.schema.migrated lines = %d; want 0", n)
	}
}
