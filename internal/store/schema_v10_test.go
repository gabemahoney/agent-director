package store

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// v10 adds only runtime binding; existing v9 agent and lifecycle data remain intact.
func TestV10MigrationReentryAndPreservation(t *testing.T) {
	for _, pre := range []string{"none", "complete table", "complete table twice"} {
		t.Run(pre, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 9)
			seedPreMigrationRow(t, path, 9)
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `UPDATE spawns SET agent='codex', end_reported_at=123456, life_number=3 WHERE instance_id='pre'`)
				if pre != "none" {
					mustExec(t, db, cursorRuntimeDDL)
				}
			})
			id := assertOneStoreID(t, path)
			writeSentinel(t, dir, 9, schemaVersion)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			sp, err := s.GetSpawn("pre")
			if err != nil || sp.Agent != AgentCodex || sp.EndReportedAtMillis != 123456 || sp.LifeNumber != 3 {
				t.Fatalf("v9 data lost: %+v %v", sp, err)
			}
			if s.StoreID() != id {
				t.Fatal("store id replaced")
			}
			assertSentinel(t, dir, false)
			fresh, _ := openTempStore(t)
			if !reflect.DeepEqual(schemaShape(t, s.db), schemaShape(t, fresh.db)) {
				t.Fatal("fresh/migrated schemas differ")
			}
			if pre == "complete table twice" {
				if err := migrateV9toV10(s.db); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if s.StoreID() != id {
				t.Fatal("reopen changed store")
			}
		})
	}
}
func TestV10MigrationRefusalRollback(t *testing.T) {
	for _, tc := range []struct{ name, ddl, needle string }{
		{"incomplete", `CREATE TABLE cursor_runtimes(bound_instance_id TEXT PRIMARY KEY)`, "incomplete"},
		{"case conflict", `CREATE TABLE Cursor_Runtimes(bound_instance_id TEXT PRIMARY KEY)`, "conflicting"},
		{"bad type", strings.Replace(cursorRuntimeDDL, "bound_runtime_pid INTEGER", "bound_runtime_pid TEXT", 1), "incompatible"},
		{"bad protection", strings.Replace(cursorRuntimeDDL, "bound_connection_id TEXT NOT NULL", "bound_connection_id TEXT", 1), "incompatible"},
		{"bad default", strings.Replace(cursorRuntimeDDL, "bound_terminated INTEGER NOT NULL DEFAULT 0", "bound_terminated INTEGER NOT NULL DEFAULT 1", 1), "incompatible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 9)
			seedPreMigrationRow(t, path, 9)
			withRaw(t, path, func(db *sql.DB) { mustExec(t, db, tc.ddl) })
			before := dbDump(t, path)
			writeSentinel(t, dir, 9, schemaVersion)
			s, err := Open(path)
			if err == nil {
				s.Close()
				t.Fatal("incompatible migration succeeded")
			}
			if errors.Is(err, ErrSchemaMigrationRequired) || !strings.Contains(err.Error(), tc.needle) {
				t.Fatalf("migration err %v", err)
			}
			if after := dbDump(t, path); !reflect.DeepEqual(before, after) {
				t.Fatal("migration failure changed database")
			}
			if readUserVersion(t, path) != 9 {
				t.Fatal("failed migration stamped v10")
			}
			assertSentinel(t, dir, true)
		})
	}
}
