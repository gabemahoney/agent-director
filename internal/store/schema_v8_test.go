package store

// Schema v8 (b.3ie, the settle time): the v7 fixture's rows after the hop
// (every value kept, read under its v9 name: the chain runs on to the current
// schema; each row settled at once whatever the settle time), the
// hop's backfill run twice, and the v8 → v7 downgrade. The guide match of its
// recipe is schema_v5_test.go's; the column specs, re-entry and rollback rows
// are schema_test.go's; fixtures: migration_fixtures_test.go.

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
)

// withoutV8Columns returns cols without the v8 columns.
func withoutV8Columns(cols []string) []string {
	return slices.DeleteFunc(slices.Clone(cols), func(c string) bool { return slices.Contains(v8ColumnNames(), c) })
}

// assertReadsRaw fails unless every read surface's store read reports each
// of ids' raw state with the largest settle time at every one of nows.
func assertReadsRaw(t *testing.T, s *Store, ids []string, nows ...int64) {
	t.Helper()
	for _, now := range nows {
		r := SettleRead{SettleMs: config.MaxStateSettleMs, NowMs: now}
		rows, err := s.ListSpawns(ListFilters{Settle: r})
		if err != nil {
			t.Fatalf("ListSpawns: %v", err)
		}
		listed := map[string]string{}
		for _, row := range rows {
			listed[row.InstanceID] = row.ReportedState
		}
		for _, id := range ids {
			reported, raw, _, err := s.SpawnStatusSettled(id, r)
			if err != nil {
				t.Fatalf("SpawnStatusSettled(%s): %v", id, err)
			}
			sp, err := s.GetSpawnSettled(id, r)
			if err != nil {
				t.Fatalf("GetSpawnSettled(%s): %v", id, err)
			}
			if reported != raw || sp.ReportedState != raw || listed[id] != raw {
				t.Errorf("%s at %d: status %q, get %q, list %q; want the raw state %q", id, now, reported, sp.ReportedState,
					listed[id], raw)
			}
		}
	}
}

// TestV8MigrationKeepsV7Rows: after the authorised hop every v7 value of each
// fixture row and of the open request reads back as seeded, the store id is
// kept, and every row is settled (settled_state its state, state_changed_ms 0):
// status, get and list report its raw state at once with the largest settle
// time, read at any clock.
func TestV8MigrationKeepsV7Rows(t *testing.T) {
	f := makeV7Fixture(t, t.TempDir())
	storeID := assertOneStoreID(t, f.path)
	writeSentinel(t, f.dir, 7, schemaVersion)
	if got := openStoreID(t, Open, f.path); got != storeID {
		t.Errorf("StoreID() after the hop = %q; want %q kept", got, storeID)
	}
	assertSentinel(t, f.dir, false)
	var ids []string
	for _, st := range v7States {
		id := v7ID(st)
		ids = append(ids, id)
		assertV8Defaults(t, f.path, id)
		if got, want := readRawSpawn(t, f.path, id, v9Cols(f.spawnCols)), v9Row(f.rows[id]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s's v7 values changed:\n got  %v\n want %v", id, got, want)
		}
	}
	if got, want := readQuotedRows(t, f.path, "permission_requests", v7ID(StateCheckPermission), "request_id", v9Cols(f.requestCols)), v9Rows(f.requests); !reflect.DeepEqual(got, want) {
		t.Errorf("request's v7 values changed:\n got  %v\n want %v", got, want)
	}

	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer s.Close()
	assertReadsRaw(t, s, ids, 0, time.Now().UnixMilli(), int64(config.MaxStateSettleMs)-1)
}

// TestV8BackfillRunsOnce: a hop stopped after adding both columns leaves every
// row's settled_state empty; the hop run again backfills each with its state
// and leaves state_changed_ms 0, and a further run changes nothing.
func TestV8BackfillRunsOnce(t *testing.T) {
	f := makeV7Fixture(t, t.TempDir())
	preAddV8Columns(t, f.path, v8ColumnNames()...)
	step, _ := migrationStepFrom(7)
	if err := step.apply(openRaw(t, f.path)); err != nil {
		t.Fatalf("hop after the columns: %v", err)
	}
	for _, st := range v7States {
		assertV8Defaults(t, f.path, v7ID(st))
	}
	before := dbDump(t, f.path)
	if err := step.apply(openRaw(t, f.path)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if after := dbDump(t, f.path); !reflect.DeepEqual(after, before) {
		t.Errorf("the second run changed the store:\n before %v\n after  %v", before, after)
	}
}

// TestDowngradeV8ToV7_KeepsRowsThenRemigrates: the v8 → v7 statements
// (v8ToV7RecipeStatements), run on the v8 fixture, leave a v7 store with no v8
// column, every other value of a row with an unsettled change kept, and the
// store id kept; this binary refuses it until an authorised re-migration, after
// which the row reads settled (its unsettled change is lost with the columns)
// and its other values read under their v9 names.
func TestDowngradeV8ToV7_KeepsRowsThenRemigrates(t *testing.T) {
	f := makeV8Fixture(t, t.TempDir())
	path, storeID := f.path, assertOneStoreID(t, f.path)
	const id = v8UnsettledID
	got := readRawSpawn(t, path, id, []string{"state", "settled_state"})
	if got["state"] != "'working'" || got["settled_state"] != "'waiting'" {
		t.Fatalf("fixture row = %v; want raw working with waiting settled (an unsettled change)", got)
	}
	cols := withoutV8Columns(f.spawnCols)
	before := readRawSpawn(t, path, id, cols)

	applyRecipe(t, path, v8ToV7RecipeStatements)

	if v := readUserVersion(t, path); v != 7 {
		t.Errorf("user_version = %d; want 7", v)
	}
	assertNoV8Columns(t, path)
	withRaw(t, path, func(db *sql.DB) {
		var v7 map[string]any
		withRaw(t, makeVersionedDB(t, t.TempDir(), 7), func(ref *sql.DB) { v7 = schemaShape(t, ref) })
		if got := schemaShape(t, db); !reflect.DeepEqual(columnsByName(got), columnsByName(v7)) {
			t.Errorf("schema after the downgrade:\n got  %v\n want a v7 store's %v", got, v7)
		}
	})
	if got := readRawSpawn(t, path, id, cols); !reflect.DeepEqual(got, before) {
		t.Errorf("values after the downgrade = %v; want %v", got, before)
	}
	if got := assertOneStoreID(t, path); got != storeID {
		t.Errorf("store_id after the downgrade = %q; want %q kept", got, storeID)
	}
	if _, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Errorf("Open(v7) err = %v; want ErrSchemaMigrationRequired", err)
	}

	writeSentinel(t, filepath.Dir(path), 7, schemaVersion)
	if got := openStoreID(t, Open, path); got != storeID {
		t.Errorf("StoreID() after re-migration = %q; want %q", got, storeID)
	}
	assertV8Defaults(t, path, id)
	assertV9Defaults(t, path, id)
	if got, want := readRawSpawn(t, path, id, v9Cols(cols)), v9Row(before); !reflect.DeepEqual(got, want) {
		t.Errorf("values after re-migration = %v; want %v", got, want)
	}
}
