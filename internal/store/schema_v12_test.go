package store

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

var v12ToV11RecipeStatements = []string{
	`ALTER TABLE spawns DROP COLUMN family_changed_ms`,
	`ALTER TABLE spawns DROP COLUMN family_settled_state`,
	`ALTER TABLE spawns DROP COLUMN family_state`,
	`PRAGMA user_version=11`,
}

func TestV12MigrationInitializesRawFamiliesAndPreservesRows(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 11)
	seedPreMigrationRow(t, path, 11)
	var cols []string
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE spawns SET state='waiting',settled_state='working',state_changed_ms=123 WHERE instance_id='pre'`)
		for _, row := range []struct{ id, parent, state string }{{"bridge", "pre", "ended"}, {"leaf", "bridge", "working"}, {"empty", "", "missing"}} {
			insertRow(t, db, "spawns", map[string]any{"instance_id": row.id, "parent_id": nullableStringArg(row.parent), "state": row.state, "cwd": "/tmp", "tmux_session_name": row.id, "relay_mode": "off"})
		}
		cols = tableColumnNames(t, db, "spawns")
	})
	before := readRawSpawn(t, path, "pre", cols)
	id := assertOneStoreID(t, path)
	writeSentinel(t, dir, 11, schemaVersion)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct{ id, want string }{{"pre", "working"}, {"bridge", "working"}, {"leaf", "working"}, {"empty", "waiting"}} {
		for _, now := range []int64{-1, 0, 1000} {
			row, err := s.GetSpawnSettled(tc.id, SettleRead{SettleMs: 300000, NowMs: now})
			if err != nil || row.FamilyState != tc.want {
				t.Fatalf("%s initialized family=%q err=%v; want %q", tc.id, row.FamilyState, err, tc.want)
			}
		}
	}
	if s.StoreID() != id {
		t.Fatal("migration changed store identity")
	}
	if got := readRawSpawn(t, path, "pre", cols); !reflect.DeepEqual(got, before) {
		t.Fatal("preexisting spawn columns changed")
	}
	fresh, _ := openTempStore(t)
	if !reflect.DeepEqual(schemaShape(t, s.db), schemaShape(t, fresh.db)) {
		t.Fatal("fresh/migrated schemas differ")
	}
	assertSentinel(t, dir, false)
}

func TestV12MigrationCompatibleReentry(t *testing.T) {
	for _, prefix := range []int{0, 1, 2, 3} {
		t.Run(string(rune('0'+prefix))+" columns", func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 11)
			seedPreMigrationRow(t, path, 11)
			withRaw(t, path, func(db *sql.DB) {
				for _, col := range familyColumns[:prefix] {
					mustExec(t, db, "ALTER TABLE spawns ADD COLUMN "+col.name+" "+col.kind+" NOT NULL DEFAULT "+col.def)
				}
				if prefix == 3 {
					mustExec(t, db, `UPDATE spawns SET family_state='working',family_settled_state='waiting',family_changed_ms=123456`)
				}
			})
			writeSentinel(t, dir, 11, schemaVersion)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			before := dbDump(t, path)
			if err := migrateV11toV12(s.db); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("reentry changed initialized history")
			}
			if prefix == 3 {
				row, err := s.GetSpawnSettled("pre", SettleRead{SettleMs: 1000, NowMs: 123457})
				if err != nil || row.FamilyState != "waiting" {
					t.Fatalf("retained family history=%q %v", row.FamilyState, err)
				}
			}
		})
	}
}

func TestV12MigrationRefusalAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name, ddl, update string
		injected          bool
	}{
		{"case conflict", `ALTER TABLE spawns ADD COLUMN Family_State TEXT NOT NULL DEFAULT ''`, "", false},
		{"wrong type", `ALTER TABLE spawns ADD COLUMN family_state INTEGER NOT NULL DEFAULT ''`, "", false},
		{"nullable", `ALTER TABLE spawns ADD COLUMN family_state TEXT DEFAULT ''`, "", false},
		{"wrong default", `ALTER TABLE spawns ADD COLUMN family_state TEXT NOT NULL DEFAULT 'waiting'`, "", false},
		{"invalid aggregate", "", `UPDATE spawns SET family_state='ended'`, false},
		{"invalid retained value", "", `UPDATE spawns SET family_settled_state='missing'`, false},
		{"noninteger clock", "", `UPDATE spawns SET family_changed_ms=0.5`, false},
		{"backfill abort", `CREATE TRIGGER fail_family BEFORE UPDATE ON spawns BEGIN SELECT RAISE(ABORT,'family failure'); END`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 11)
			seedPreMigrationRow(t, path, 11)
			withRaw(t, path, func(db *sql.DB) {
				if tc.ddl != "" {
					mustExec(t, db, tc.ddl)
				}
				if tc.update != "" {
					for _, col := range familyColumns {
						mustExec(t, db, "ALTER TABLE spawns ADD COLUMN "+col.name+" "+col.kind+" NOT NULL DEFAULT "+col.def)
					}
					mustExec(t, db, tc.update)
				}
			})
			before := dbDump(t, path)
			writeSentinel(t, dir, 11, schemaVersion)
			s, err := Open(path)
			if s != nil {
				s.Close()
			}
			if err == nil || (!tc.injected && !errors.Is(err, ErrSchemaMismatch)) {
				t.Fatalf("Open=%v; want refusal", err)
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("failed migration changed schema/data/version")
			}
			if readUserVersion(t, path) != 11 {
				t.Fatal("failed migration stamped version")
			}
			assertSentinel(t, dir, true)
			if tc.injected {
				withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "DROP TRIGGER fail_family") })
				s, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
				s.Close()
				assertSentinel(t, dir, false)
			}
		})
	}
}

func TestV12RecoveryRecipePreservesOwnHistoryAndRemigrates(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 12)
	seedPreMigrationRow(t, path, 12)
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE spawns SET family_state='working',family_settled_state='waiting',family_changed_ms=99,settled_state='waiting',state_changed_ms=55`)
	})
	cols := []string{"instance_id", "state", "parent_id", "settled_state", "state_changed_ms", "row_version", "life_number"}
	before := readRawSpawn(t, path, "pre", cols)
	id := assertOneStoreID(t, path)
	withRaw(t, path, func(db *sql.DB) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for _, q := range v12ToV11RecipeStatements {
			if _, err := tx.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		ref := openRaw(t, makeVersionedDB(t, t.TempDir(), 11))
		if !reflect.DeepEqual(schemaShape(t, db), schemaShape(t, ref)) {
			t.Fatal("recovery did not restore v11 shape")
		}
	})
	if _, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("unauthorized recovery open=%v", err)
	}
	writeSentinel(t, dir, 11, schemaVersion)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.StoreID() != id || !reflect.DeepEqual(before, readRawSpawn(t, path, "pre", cols)) {
		t.Fatal("recovery changed own history or identity")
	}
	row, err := s.GetSpawnSettled("pre", SettleRead{SettleMs: 1000, NowMs: 0})
	if err != nil || row.FamilyState != "working" {
		t.Fatalf("remigration family=%q %v", row.FamilyState, err)
	}
}
