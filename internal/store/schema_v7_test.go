package store

// Schema v7 (b.146 steps 2, 2b and 2c): the v6 fixture's requests after the
// hop (no relay hook identity, no settle instant: judged by time, as before)
// and the v7 → v6 downgrade. The guide match of its recipe is
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
	if got := readRawSpawn(t, f.path, f.id, f.spawnCols); !reflect.DeepEqual(got, f.spawn) {
		t.Errorf("row's v6 values changed:\n got  %v\n want %v", got, f.spawn)
	}
	for _, r := range readQuotedRows(t, f.path, "permission_requests", f.id, "request_id", f.requestCols) {
		if want := f.requests[unquote(r["request_token"])]; !reflect.DeepEqual(r, want) {
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

// TestDowngradeV7ToV6_KeepsRowsThenRemigrates: the v7 → v6 statements
// (v7ToV6RecipeStatements) leave a v6 store with no v7 column, every other
// value of a row and of a relay request recorded with its hook's identity
// kept, and the store id kept; this binary refuses it until an authorised
// re-migration, after which the request reads as recorded before v7.
func TestDowngradeV7ToV6_KeepsRowsThenRemigrates(t *testing.T) {
	path, storeID := newClosedStore(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const id = "v7-relay"
	seedSpawnForPerm(t, s, id, "on")
	if _, applied, err := s.InsertRelayRequest(id, agentGate("PermissionRequest", ""), RelayRequest{
		RequestToken: tokenA, ToolName: "Bash", ToolInput: `{}`, ToolUseID: "toolu_01", AgentID: "agent-1",
		Hook: ProcessIdentity{PID: 4321, Starttime: "777", PIDNamespace: "pid:[4026531836]"}, SettledAt: time.UnixMilli(1790000000000),
	}, 0, DefaultLockWait); err != nil || !applied.Applied {
		t.Fatalf("InsertRelayRequest = %+v, %v", applied, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var spawnCols, requestCols []string
	withRaw(t, path, func(db *sql.DB) {
		spawnCols = v6Columns("spawns", tableColumnNames(t, db, "spawns"))
		requestCols = v6Columns("permission_requests", tableColumnNames(t, db, "permission_requests"))
	})
	read := func() any {
		return []any{readRawSpawn(t, path, id, spawnCols), readQuotedRows(t, path, "permission_requests", id, "request_id", requestCols)}
	}
	before := read()

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
	if got := read(); !reflect.DeepEqual(got, before) {
		t.Errorf("values after the downgrade = %v; want %v", got, before)
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
	if got := read(); !reflect.DeepEqual(got, before) {
		t.Errorf("values after re-migration = %v; want %v", got, before)
	}
}
