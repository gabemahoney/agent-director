package store

// Schema v6 (b.kdf, b.146 rule 11): the v5 fixture's rows after the hop (no
// launch owner, every v5 value kept, read under their v9 names: the chain runs
// on to the current schema) and the v6 → v5 downgrade. The guide
// match of its recipe is schema_v5_test.go's. The column specs, re-entry and
// rollback rows are schema_test.go's; fixtures: migration_fixtures_test.go.
// How find-missing judges a migrated row is v5_migrated_find_missing_test.go's.

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// TestV6MigrationKeepsV5Rows: after the authorised hop every v5 value of the
// fixture's rows reads back exactly as seeded, the pending row's
// launch_started_at included (CSCB), each row records no launch owner (read
// as the zero LaunchOwner by GetSpawn and ListLiveSpawnIdentities), and the
// store id is kept.
func TestV6MigrationKeepsV5Rows(t *testing.T) {
	f := makeV5Fixture(t, t.TempDir())
	id := assertOneStoreID(t, f.path)
	writeSentinel(t, f.dir, 5, schemaVersion)
	if got := openStoreID(t, Open, f.path); got != id {
		t.Errorf("StoreID() after the hop = %q; want %q kept", got, id)
	}
	assertSentinel(t, f.dir, false)
	for _, rowID := range f.ids {
		assertV6Defaults(t, f.path, rowID)
		if got, want := readRawSpawn(t, f.path, rowID, v9Cols(f.spawnsCols)), v9Row(f.rows[rowID]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s v5 values changed:\n got  %v\n want %v", rowID, got, want)
		}
	}
	if got := readRawSpawn(t, f.path, f.pending, []string{"launch_started_at"})["launch_started_at"]; got != "1772359200123" {
		t.Errorf("pending row's launch_started_at = %s; want 1772359200123 kept", got)
	}

	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer s.Close()
	for _, rowID := range f.ids {
		if sp := mustGetSpawn(t, s, rowID); sp.LaunchOwner != (LaunchOwner{}) {
			t.Errorf("%s: GetSpawn LaunchOwner = %+v; want none", rowID, sp.LaunchOwner)
		}
	}
	live, err := s.ListLiveSpawnIdentities()
	if err != nil || len(live) != 2 {
		t.Fatalf("ListLiveSpawnIdentities = %d rows, %v; want the pending and waiting rows", len(live), err)
	}
	for _, it := range live {
		sp := mustGetSpawn(t, s, it.InstanceID)
		if it.LaunchOwner != (LaunchOwner{}) || it.Snapshot != sp.Snapshot || it.Identity != sp.Identity {
			t.Errorf("%s live read = owner %+v, snapshot %+v, identity %+v; want no owner and GetSpawn's %+v, %+v",
				it.InstanceID, it.LaunchOwner, it.Snapshot, it.Identity, sp.Snapshot, sp.Identity)
		}
	}
}

// TestDowngradeV6ToV5_KeepsRowsThenRemigrates: on the v8 fixture taken back to
// v6 first (the v8 → v7 and v7 → v6 recipes), the v6 → v5 statements
// (v6ToV5RecipeStatements) leave a v5 store with no v6 column, every other
// value of a pending row that recorded an owner kept and the store id kept;
// this binary refuses it until an authorised re-migration, which gives the
// row no owner (its other values read under their v9 names).
func TestDowngradeV6ToV5_KeepsRowsThenRemigrates(t *testing.T) {
	f := makeV8Fixture(t, t.TempDir())
	path, storeID := f.path, assertOneStoreID(t, f.path)
	const id = v8PendingID // pending with its launch owner recorded
	if f.rows[id]["launch_owner_pid"] != "4300" {
		t.Fatalf("fixture row's launch_owner_pid = %s; want 4300 recorded", f.rows[id]["launch_owner_pid"])
	}
	applyRecipe(t, path, v8ToV7RecipeStatements) // the v8 fixture is past v6: back to v6 first
	applyRecipe(t, path, v7ToV6RecipeStatements)
	var v5Cols []string
	withRaw(t, path, func(db *sql.DB) {
		for _, c := range tableColumnNames(t, db, "spawns") {
			if !slices.Contains(v6ColumnNames(), c) {
				v5Cols = append(v5Cols, c)
			}
		}
	})
	before := readRawSpawn(t, path, id, v5Cols)

	applyRecipe(t, path, v6ToV5RecipeStatements)

	if v := readUserVersion(t, path); v != 5 {
		t.Errorf("user_version = %d; want 5", v)
	}
	assertNoV6Columns(t, path)
	if got := readRawSpawn(t, path, id, v5Cols); !reflect.DeepEqual(got, before) {
		t.Errorf("row after the downgrade = %v; want %v", got, before)
	}
	if got := assertOneStoreID(t, path); got != storeID {
		t.Errorf("store_id after the downgrade = %q; want %q kept", got, storeID)
	}
	if _, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Errorf("Open(v5) err = %v; want ErrSchemaMigrationRequired", err)
	}

	writeSentinel(t, filepath.Dir(path), 5, schemaVersion)
	if got := openStoreID(t, Open, path); got != storeID {
		t.Errorf("StoreID() after re-migration = %q; want %q", got, storeID)
	}
	assertV6Defaults(t, path, id)
	assertV9Defaults(t, path, id)
	if got, want := readRawSpawn(t, path, id, v9Cols(v5Cols)), v9Row(before); !reflect.DeepEqual(got, want) {
		t.Errorf("row after re-migration = %v; want %v", got, want)
	}
}
