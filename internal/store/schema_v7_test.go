package store

// Schema v7 (b.146 steps 2, 2b and 2c): the v6 fixture's requests after the
// hop (no relay hook identity, no settle instant: judged by time, as before;
// read under their v9 names, the chain runs on to the current schema), the
// hop's backfill proving finished rows' requests gone, and the v7 → v6
// downgrade. The guide match of its recipe is
// schema_v5_test.go's; the column specs, re-entry and rollback rows are
// schema_test.go's; fixtures: migration_fixtures_test.go.

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

// v6Columns returns cols without table's v7 columns.
func v6Columns(table string, cols []string) []string {
	var out []string
	for _, c := range cols {
		if !slices.Contains(v7ColumnNames(table), c) {
			out = append(out, c)
		}
	}
	return out
}

// assertV7Defaults checks every request of id and id's row hold the v7
// columns' migrated defaults: NULL, and pane_answer 'none'.
func assertV7Defaults(t *testing.T, path, id string) {
	t.Helper()
	for _, spec := range v7ColumnSpecs {
		var got []map[string]string
		if spec.table == "spawns" {
			got = []map[string]string{readRawSpawn(t, path, id, []string{spec.name})}
		} else {
			got = readQuotedRows(t, path, spec.table, id, "request_id", []string{spec.name})
		}
		for _, r := range got {
			if r[spec.name] != spec.migratedValue() {
				t.Errorf("%s: %s = %s; want %s", id, spec.key(), r[spec.name], spec.migratedValue())
			}
		}
	}
}

// TestV7MigrationKeepsV6Rows: after the authorised hop every v6 value of the
// fixture's row and requests reads back as seeded, the new columns hold their
// defaults and the store id is kept. A request recorded before the hop is
// judged as before it (b.146 rule 5's compatibility clause): it reads as
// recorded before v7, awaits an answer only while undecided, and decide's
// guarded write still refuses it once its created_at is past the cutoff.
func TestV7MigrationKeepsV6Rows(t *testing.T) {
	f := makeV6Fixture(t, t.TempDir())
	storeID := assertOneStoreID(t, f.path)
	writeSentinel(t, f.dir, 6, schemaVersion)
	if got := openStoreID(t, Open, f.path); got != storeID {
		t.Errorf("StoreID() after the hop = %q; want %q kept", got, storeID)
	}
	assertSentinel(t, f.dir, false)
	assertV7Defaults(t, f.path, f.id)
	if got, want := readRawSpawn(t, f.path, f.id, v9Cols(f.spawnCols)), v9Row(f.spawn); !reflect.DeepEqual(got, want) {
		t.Errorf("row's v6 values changed:\n got  %v\n want %v", got, want)
	}
	for _, r := range readQuotedRows(t, f.path, "permission_requests", f.id, "request_id", v9Cols(f.requestCols)) {
		if want := v9Row(f.requests[unquote(r["request_token"])]); !reflect.DeepEqual(r, want) {
			t.Errorf("request's v6 values changed:\n got  %v\n want %v", r, want)
		}
	}

	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer s.Close()
	for _, tok := range []string{f.open, f.decided} {
		pr, err := s.GetPermissionRequest(f.id, tok)
		if err != nil {
			t.Fatalf("GetPermissionRequest(%s): %v", tok, err)
		}
		if !pr.PreV7() || pr.Hook != (ProcessIdentity{}) || !pr.DeliveredAt.IsZero() || pr.PaneAnswer != PaneAnswerNone || pr.ToolUseID != "" {
			t.Errorf("%s = %+v; want a request recorded before v7 (no settle instant, hook, ack or tool_use_id; pane_answer none)", tok, pr)
		}
	}
	if got := openTokens(t, s, f.id); !slices.Equal(got, []string{f.open}) {
		t.Errorf("requests awaiting an answer = %v; want the undecided one only %v", got, []string{f.open})
	}
	openCreated, err := time.Parse("2006-01-02 15:04:05", f.openCreatedAtSQL)
	if err != nil {
		t.Fatalf("parse created_at: %v", err)
	}
	if ok, err := s.DecideRelayRequest(f.id, f.open, "allow", "", WriterProcessDecide, openCreated, DefaultLockWait); err != nil || ok {
		t.Errorf("decide at a cutoff not before created_at = %v, %v; want refused (fallen back by time)", ok, err)
	}
	if ok, err := s.DecideRelayRequest(f.id, f.open, "allow", "", WriterProcessDecide, openCreated.Add(-time.Second), DefaultLockWait); err != nil || !ok {
		t.Errorf("decide at a cutoff before created_at = %v, %v; want written", ok, err)
	}
}

// TestV7MigrationProvesFinishedRowsRequests (b.146 step 2c; b.kxu): the hop
// proves every request of a row already ended or missing gone, agent_gone at
// one instant within the hop, its v6 values kept (an undecided one stays
// undecided and awaits an answer); the live row's requests stay unproven.
func TestV7MigrationProvesFinishedRowsRequests(t *testing.T) {
	f := makeV6Fixture(t, t.TempDir())
	v6Values := map[string][]map[string]string{}
	for _, id := range f.finished {
		v6Values[id] = readQuotedRows(t, f.path, "permission_requests", id, "request_id", f.requestCols)
	}
	writeSentinel(t, f.dir, 6, schemaVersion)

	from := time.Now().Truncate(time.Millisecond)
	s, err := Open(f.path)
	to := time.Now()

	if err != nil {
		t.Fatalf("Open (the hop): %v", err)
	}
	var at time.Time
	for _, id := range f.finished {
		for _, tok := range []string{f.open, f.decided} {
			pr, err := s.GetPermissionRequest(id, tok)
			if err != nil {
				t.Fatalf("GetPermissionRequest(%s, %s): %v", id, tok, err)
			}
			if at.IsZero() {
				at = pr.ProvenGoneAt
			}
			if pr.ProvenGoneHow != ProvenGoneAgentGone || !pr.ProvenGoneAt.Equal(at) || at.Before(from) || at.After(to) {
				t.Errorf("%s/%s = proven_gone_how %q at %v; want agent_gone at the hop's one instant in [%v, %v]",
					id, tok, pr.ProvenGoneHow, pr.ProvenGoneAt, from, to)
			}
			if tok == f.open && (pr.Decision != "" || !pr.AwaitsAnswer()) {
				t.Errorf("%s/%s = decision %q, awaits an answer %v; want it undecided, still awaiting one", id, tok, pr.Decision, pr.AwaitsAnswer())
			}
		}
		if got, want := readQuotedRows(t, f.path, "permission_requests", id, "request_id", v9Cols(f.requestCols)), v9Rows(v6Values[id]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s's requests' v6 values changed:\n got  %v\n want %v", id, got, want)
		}
	}
	for _, tok := range []string{f.open, f.decided} {
		if pr, err := s.GetPermissionRequest(f.id, tok); err != nil || pr.ProvenGone() {
			t.Errorf("live row's %s = %+v, %v; want it unproven", tok, pr, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestV7BackfillRunsOnce: the hop run again over a v6 store it has migrated
// rewrites no proven request (the second run's own instant would show), and
// changes nothing else. It runs the step itself, on a v6 fixture: the backfill
// names the v6 columns, so it cannot run over a store the chain took to v9.
func TestV7BackfillRunsOnce(t *testing.T) {
	f := makeV6Fixture(t, t.TempDir())
	step, _ := migrationStepFrom(6)
	if err := step.apply(openRaw(t, f.path)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	withRaw(t, f.path, func(db *sql.DB) { // a rewrite would stamp the second run's own instant
		mustExec(t, db, `UPDATE permission_requests SET proven_gone_at = 1 WHERE proven_gone_at IS NOT NULL`)
	})
	before := dbDump(t, f.path)
	if err := step.apply(openRaw(t, f.path)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if after := dbDump(t, f.path); !reflect.DeepEqual(after, before) {
		t.Errorf("the second run changed the store:\n before %v\n after  %v", before, after)
	}
}

// TestDowngradeV7ToV6_KeepsRowsThenRemigrates: on the v8 fixture taken back to
// v7 first (the v8 → v7 recipe), the v7 → v6 statements
// (v7ToV6RecipeStatements) leave a v6 store with no v7 column, every other
// value of a row and of a relay request recorded with its hook's identity
// kept, and the store id kept; this binary refuses it until an authorised
// re-migration, after which the request reads as recorded before v7 (its other
// values read under their v9 names).
func TestDowngradeV7ToV6_KeepsRowsThenRemigrates(t *testing.T) {
	f := makeV8Fixture(t, t.TempDir())
	path, storeID := f.path, assertOneStoreID(t, f.path)
	const id = v8RelayID // an open request holding its hook's identity
	if f.requests[id][0]["hook_pid"] != "4321" {
		t.Fatalf("fixture request's hook_pid = %s; want 4321 recorded", f.requests[id][0]["hook_pid"])
	}
	applyRecipe(t, path, v8ToV7RecipeStatements) // the fixture is past v7: back to v7 first
	var spawnCols, requestCols []string
	withRaw(t, path, func(db *sql.DB) {
		spawnCols = v6Columns("spawns", tableColumnNames(t, db, "spawns"))
		requestCols = v6Columns("permission_requests", tableColumnNames(t, db, "permission_requests"))
	})
	// read reads id's v6 columns, under their v9 names once the store is migrated
	read := func(migrated bool) (spawn map[string]string, requests []map[string]string) {
		sc, rc := spawnCols, requestCols
		if migrated {
			sc, rc = v9Cols(sc), v9Cols(rc)
		}
		return readRawSpawn(t, path, id, sc), readQuotedRows(t, path, "permission_requests", id, "request_id", rc)
	}
	beforeSpawn, beforeRequests := read(false)

	applyRecipe(t, path, v7ToV6RecipeStatements)

	if v := readUserVersion(t, path); v != 6 {
		t.Errorf("user_version = %d; want 6", v)
	}
	assertNoV7Columns(t, path)
	withRaw(t, path, func(db *sql.DB) {
		var v6 map[string]any
		withRaw(t, makeVersionedDB(t, t.TempDir(), 6), func(ref *sql.DB) { v6 = schemaShape(t, ref) })
		if got := schemaShape(t, db); !reflect.DeepEqual(columnsByName(got), columnsByName(v6)) {
			t.Errorf("schema after the downgrade:\n got  %v\n want a v6 store's %v", got, v6)
		}
	})
	if spawn, requests := read(false); !reflect.DeepEqual(spawn, beforeSpawn) || !reflect.DeepEqual(requests, beforeRequests) {
		t.Errorf("values after the downgrade = %v, %v; want %v, %v", spawn, requests, beforeSpawn, beforeRequests)
	}
	if got := assertOneStoreID(t, path); got != storeID {
		t.Errorf("store_id after the downgrade = %q; want %q kept", got, storeID)
	}
	if _, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Errorf("Open(v6) err = %v; want ErrSchemaMigrationRequired", err)
	}

	writeSentinel(t, filepath.Dir(path), 6, schemaVersion)
	if got := openStoreID(t, Open, path); got != storeID {
		t.Errorf("StoreID() after re-migration = %q; want %q", got, storeID)
	}
	assertV7Defaults(t, path, id)
	assertV9Defaults(t, path, id)
	if spawn, requests := read(true); !reflect.DeepEqual(spawn, v9Row(beforeSpawn)) || !reflect.DeepEqual(requests, v9Rows(beforeRequests)) {
		t.Errorf("values after re-migration = %v, %v; want %v, %v", spawn, requests, v9Row(beforeSpawn), v9Rows(beforeRequests))
	}
}
