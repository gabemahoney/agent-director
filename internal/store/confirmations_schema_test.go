package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// seedV18Store lays the v18 fixture the v18→v19 hop starts from: v16's
// permission and session history, both Cursor obligation tables and a live
// row with a recorded agent process, so every retained fact sits beside the
// columns the hop adds.
func seedV18Store(t *testing.T, dir string) string {
	t.Helper()
	path := makeVersionedDB(t, dir, 18)
	seedV16PermissionHistory(t, path)
	seedV18Obligation(t, path, "cursor_inputs")
	seedV18Obligation(t, path, "cursor_proofs")
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE spawns SET pid=4242, proc_starttime='99', launch_token='0123456789abcdef', life_number=2, permission_epoch=3 WHERE instance_id='pre'`)
	})
	return path
}

// b.x9o: the v18→v19 hop adds the three nullable evidence columns with no
// backfill, keeps a compatible partial reentry's values, converges with a
// fresh store, and leaves every earlier fact, the store id and the native and
// Cursor records exactly as they were.
func TestV19MigrationPreservationAndReentry(t *testing.T) {
	record := `{"v":"working","life":2,"token":"0123456789abcdef","epoch":3,"at":1792411208000,"src":"agent_event","checked":1792411208000}`
	for _, tc := range []struct {
		name    string
		present []columnSpec // the v19 columns a partial run already added
		values  string       // their stored values
	}{
		{"no columns", nil, ""},
		{"process_evidence alone", v19ColumnSpecs[2:], `process_evidence='{"v":"alive"}'`},
		{"all three, one record", v19ColumnSpecs, `state_evidence='` + record + `'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := seedV18Store(t, dir)
			preAddV14Columns(t, path, tc.present)
			if tc.values != "" {
				withRaw(t, path, func(db *sql.DB) { mustExec(t, db, `UPDATE spawns SET `+tc.values) })
			}
			before, id := dbDump(t, path), assertOneStoreID(t, path)
			var cols []string
			withRaw(t, path, func(db *sql.DB) { cols = tableColumnNames(t, db, "spawns") })
			prior := readRawSpawn(t, path, "pre", cols)
			writeSentinel(t, dir, 18, schemaVersion)
			s, err := openMigrationTestStore(t, path)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _ := openTempStore(t)
			got, want := schemaShape(t, s.db), schemaShape(t, fresh.db)
			if len(tc.present) == 1 {
				// The partial run's column stays where it was added.
				got, want = columnsByName(got), columnsByName(want)
			}
			if !reflect.DeepEqual(got, want) || s.StoreID() != id || userVersion(t, s.db) != schemaVersion {
				t.Fatal("migration diverged from the fresh schema, replaced the store id or did not stamp the current version")
			}
			assertSentinel(t, dir, false)
			if !reflect.DeepEqual(prior, readRawSpawn(t, path, "pre", cols)) {
				t.Fatal("migration changed existing spawn data or a retained evidence value")
			}
			for c, v := range readRawSpawn(t, path, "pre", v19ColumnNames()) {
				if v != "NULL" && !slices.Contains(cols, c) {
					t.Errorf("added %s = %s; want NULL (no backfill)", c, v)
				}
			}
			after := dbDump(t, path)
			for key, value := range before {
				if strings.HasPrefix(key, "rows ") && key != "rows spawns" && !reflect.DeepEqual(after[key], value) {
					t.Errorf("migration changed %s", key)
				}
			}
			// Unproven history reads honestly unknown, never confirmed at the
			// upgrade's time; a retained record that names its own value and
			// owner still applies.
			row, err := s.GetSpawn("pre")
			if err != nil {
				t.Fatal(err)
			}
			wantState := UnknownConfirmation(ReasonNoEvidence)
			if tc.name == "all three, one record" {
				wantState = Confirmation{Status: ConfirmationConfirmed, ConfirmedAtMillis: 1792411208000, Source: EvidenceAgentEvent, CheckedAtMillis: 1792411208000}
			}
			if c := row.StateConfirmation(SettleRead{}); c != wantState {
				t.Errorf("state record after migration = %+v; want %+v", c, wantState)
			}
			if v, c := row.ProcessConfirmation(); v != ProcessUnknown || c != UnknownConfirmation(ReasonNoEvidence) {
				t.Errorf("process record after migration = %s %+v; want unknown with no evidence", v, c)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			// Reentry of the hop and a plain reopen change nothing.
			withRaw(t, path, func(db *sql.DB) {
				for _, hop := range []func(*sql.DB) error{migrateV18toV19, migrateV19toV20} {
					if err := hop(db); err != nil {
						t.Fatal(err)
					}
				}
			})
			if s, err = Open(path); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if !reflect.DeepEqual(after, dbDump(t, path)) {
				t.Fatal("reentry or reopen changed the migrated store")
			}
		})
	}
}

// b.x9o: an evidence column of another shape, or a present value that is not
// a JSON object, refuses the hop before any DDL: v18, its data and the
// administrator's sentinel stay exactly as they were.
func TestV19MigrationRefusesIncompatibleEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, ddl, update, msg string
		bare                   bool // no seeded rows, so a NOT NULL column with no default can be added
	}{
		{"case conflict", `State_Evidence TEXT`, "", "v18→v19 incompatible spawns.state_evidence", false},
		{"wrong type", `settled_evidence BLOB`, "", "v18→v19 incompatible spawns.settled_evidence", false},
		{"not null", `process_evidence TEXT NOT NULL`, "", "v18→v19 incompatible spawns.process_evidence", true},
		{"default", `state_evidence TEXT DEFAULT '{}'`, "", "v18→v19 incompatible spawns.state_evidence", false},
		{"non-text value", `process_evidence TEXT`, `UPDATE spawns SET process_evidence=x'7b7d'`, "v18→v19 invalid spawns.process_evidence", false},
		{"malformed JSON", `settled_evidence TEXT`, `UPDATE spawns SET settled_evidence='{'`, "v18→v19 invalid spawns.settled_evidence", false},
		{"not an object", `state_evidence TEXT`, `UPDATE spawns SET state_evidence='["working"]'`, "v18→v19 invalid spawns.state_evidence", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 18)
			if !tc.bare {
				seedV16PermissionHistory(t, path)
			}
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `ALTER TABLE spawns ADD COLUMN `+tc.ddl)
				if tc.update != "" {
					mustExec(t, db, tc.update)
				}
			})
			before := dbDump(t, path)
			writeSentinel(t, dir, 18, schemaVersion)
			s, err := openMigrationTestStore(t, path)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, ErrSchemaMismatch) || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("Open=%v; want a schema mismatch naming %q", err, tc.msg)
			}
			if readUserVersion(t, path) != 18 || !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("refusal changed schema, data or version")
			}
			assertSentinel(t, dir, true)
		})
	}
}

// b.x9o: a hop whose COMMIT fails leaves v18 exactly (no column, no stamp)
// and the authorized retry completes; a stopped-writer v18 backup restored
// after the upgrade has none of the evidence written since, needs
// authorization again and remigrates to empty columns under the same id.
func TestV19MigrationRollbackRetryAndRestore(t *testing.T) {
	dir := t.TempDir()
	path := seedV18Store(t, dir)
	before, id := dbDump(t, path), assertOneStoreID(t, path)
	backup := readDBFileSet(t, path)
	db := openRaw(t, path)
	mustExec(t, db, "PRAGMA journal_mode=DELETE")
	mustExec(t, db, "PRAGMA busy_timeout=1")
	reader := openRaw(t, path)
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM spawns").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := migrateV18toV19(db); err == nil {
		t.Fatal("commit succeeded with a blocking reader")
	}
	_ = tx.Rollback()
	db.Close()
	reader.Close()
	if readUserVersion(t, path) != 18 || !reflect.DeepEqual(before, dbDump(t, path)) {
		t.Fatal("failed commit retained DDL, data or the version stamp")
	}
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "PRAGMA journal_mode=WAL") })
	writeSentinel(t, dir, 18, schemaVersion)
	s, err := openMigrationTestStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPending(Spawn{InstanceID: "post", Agent: AgentClaude, CWD: "/tmp", TmuxSessionName: "post", RelayMode: "off"}); err != nil {
		t.Fatal(err)
	}
	if row, err := s.GetSpawn("post"); err != nil || row.StateConfirmation(SettleRead{}).Source != EvidenceCallerCommand {
		t.Fatalf("upgraded store wrote no admission record: %+v %v", row, err)
	}
	s.Close()

	restoredDir := t.TempDir()
	restored := filepath.Join(restoredDir, "state.db")
	for name, data := range backup {
		if err := os.WriteFile(restored+strings.TrimPrefix(name, path), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if opened, err := Open(restored); !errors.Is(err, ErrSchemaMigrationRequired) {
		if opened != nil {
			opened.Close()
		}
		t.Fatalf("restored v18 opened without authorization: %v", err)
	}
	writeSentinel(t, restoredDir, 18, schemaVersion)
	reopened, err := openMigrationTestStore(t, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.StoreID() != id {
		t.Fatal("restore and remigration replaced the store id")
	}
	if _, err := reopened.GetSpawn("post"); !errors.Is(err, ErrSpawnNotFound) {
		t.Fatalf("restore kept a row written after the upgrade: %v", err)
	}
	for c, v := range readRawSpawn(t, restored, "pre", v19ColumnNames()) {
		if v != "NULL" {
			t.Errorf("remigrated %s = %s; want NULL", c, v)
		}
	}
}

// b.x9o: v20 adds no proof to historical displays; compatible partial
// reentry retains exact evidence and the fresh column order.
func TestV20DisplayMigration(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical", true: "partial reentry"}[present], func(t *testing.T) {
			dir := t.TempDir()
			path := seedV18Store(t, dir)
			withRaw(t, path, func(db *sql.DB) {
				if err := migrateV18toV19(db); err != nil {
					t.Fatal(err)
				}
				mustExec(t, db, `UPDATE spawns SET state='working', settled_state='working', display_observation='{"state":"idle_screen","since_ms":1792411208000,"unchanged_minutes":20,"candidate":"idle_screen","candidate_since_ms":1792411208000,"observed_at_ms":1792411208000,"permission_epoch":3}',pane_since_ms=1792410008000,pane_sha256='abc',pane_epoch=3`)
			})
			const record = `{"v":"idle_screen","life":2,"token":"0123456789abcdef","since":1792411208000,"at":1792411208000,"src":"pane","checked":1792411210000,"reason":null}`
			if present {
				preAddV14Columns(t, path, v20ColumnSpecs)
				withRaw(t, path, func(db *sql.DB) { mustExec(t, db, `UPDATE spawns SET display_evidence=?`, record) })
			}
			var cols []string
			withRaw(t, path, func(db *sql.DB) { cols = tableColumnNames(t, db, "spawns") })
			before, id := readRawSpawn(t, path, "pre", cols), assertOneStoreID(t, path)
			if opened, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
				if opened != nil {
					opened.Close()
				}
				t.Fatalf("v19 opened without authorization: %v", err)
			}
			writeSentinel(t, dir, 19, schemaVersion)
			s, err := openMigrationTestStore(t, path)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _ := openTempStore(t)
			if !reflect.DeepEqual(schemaShape(t, s.db), schemaShape(t, fresh.db)) || s.StoreID() != id || userVersion(t, s.db) != 20 {
				t.Fatal("v20 migration changed identity, column shape/order or version")
			}
			if !reflect.DeepEqual(before, readRawSpawn(t, path, "pre", cols)) {
				t.Fatal("v20 changed historical facts")
			}
			row, err := s.GetSpawn("pre")
			if err != nil {
				t.Fatal(err)
			}
			want := UnknownConfirmation(ReasonNoEvidence)
			if present {
				want = Confirmation{Status: ConfirmationConfirmed, ConfirmedAtMillis: 1792411208000, Source: EvidencePane, CheckedAtMillis: 1792411210000}
			}
			if got := row.DisplayConfirmation(row.CommittedDisplay(), row.DisplayEvidence); got != want {
				t.Fatalf("migrated display record %+v; want %+v", got, want)
			}
			if !present && row.DisplayEvidence != "" {
				t.Fatal("migration backfilled display evidence")
			}
			s.Close()
			after := dbDump(t, path)
			withRaw(t, path, func(db *sql.DB) {
				if err := migrateV19toV20(db); err != nil {
					t.Fatal(err)
				}
			})
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			reopened.Close()
			if !reflect.DeepEqual(after, dbDump(t, path)) {
				t.Fatal("reentry/reopen changed display evidence")
			}
		})
	}
}

// b.x9o: validate before adding/stamping; incompatible schema or values
// leave the v19 store and its authorization intact.
func TestV20DisplayMigrationRefusesInvalidEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, ddl, value, kind string
		bare                   bool
	}{
		{"case", "Display_Evidence TEXT", "", "incompatible", false},
		{"type", "display_evidence BLOB", "", "incompatible", false},
		{"not null", "display_evidence TEXT NOT NULL", "", "incompatible", true},
		{"default", "display_evidence TEXT DEFAULT '{}'", "", "incompatible", false},
		{"blob", "display_evidence TEXT", "x'7b7d'", "invalid", false},
		{"malformed", "display_evidence TEXT", "'{'", "invalid", false},
		{"array", "display_evidence TEXT", "'[]'", "invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 19)
			if !tc.bare {
				seedV16PermissionHistory(t, path)
			}
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `ALTER TABLE spawns ADD COLUMN `+tc.ddl)
				if tc.value != "" {
					mustExec(t, db, `UPDATE spawns SET display_evidence=`+tc.value)
				}
			})
			before := dbDump(t, path)
			writeSentinel(t, dir, 19, schemaVersion)
			s, err := openMigrationTestStore(t, path)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, ErrSchemaMismatch) || !strings.Contains(err.Error(), "v19→v20 "+tc.kind+" spawns.display_evidence") {
				t.Fatalf("refusal = %v", err)
			}
			if readUserVersion(t, path) != 19 || !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("refused v20 changed schema/data/stamp")
			}
			assertSentinel(t, dir, true)
		})
	}
	// SQLite cannot ALTER ADD a primary key, so this shape-only refusal
	// starts from the minimal partial table the hop itself validates.
	t.Run("primary key", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		db := openRaw(t, path)
		mustExec(t, db, `CREATE TABLE spawns (display_evidence TEXT PRIMARY KEY)`)
		mustExec(t, db, `PRAGMA user_version=19`)
		if err := migrateV19toV20(db); !errors.Is(err, ErrSchemaMismatch) {
			t.Fatalf("primary key accepted: %v", err)
		}
		if userVersion(t, db) != 19 {
			t.Fatal("refusal changed version")
		}
	})
}
