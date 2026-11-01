package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCursorObligationMigrationPreservesPartialRecords(t *testing.T) {
	for _, partial := range []string{"neither", "cursor_inputs", "cursor_proofs", "both"} {
		t.Run(partial, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 17)
			seedV16PermissionHistory(t, path)
			// The later v19 columns too, so the whole-store comparison below
			// sees only what the v18 hop kept.
			preAddV14Columns(t, path, v19ColumnSpecs)
			preAddV14Columns(t, path, v20ColumnSpecs)
			for _, table := range []string{"cursor_inputs", "cursor_proofs"} {
				if partial == table || partial == "both" {
					seedV18Obligation(t, path, table)
				}
			}
			before, id := dbDump(t, path), assertOneStoreID(t, path)
			snapshot := snapshotDBBytes(t, path)
			if s, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
				if s != nil {
					s.Close()
				}
				t.Fatalf("unauthorized: %v", err)
			}
			assertDBBytesUnchanged(t, path, snapshot)
			writeSentinel(t, dir, 17, schemaVersion)
			s, err := openMigrationTestStore(t, path)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _ := openTempStore(t)
			if !reflect.DeepEqual(schemaShape(t, s.db), schemaShape(t, fresh.db)) || s.StoreID() != id || userVersion(t, s.db) != schemaVersion {
				t.Fatal("migration changed identity or diverged from fresh shape")
			}
			assertSentinel(t, dir, false)
			after := dbDump(t, path)
			for key, value := range before {
				if !reflect.DeepEqual(after[key], value) {
					t.Fatalf("changed existing %s", key)
				}
			}
			for _, table := range []string{"cursor_inputs", "cursor_proofs"} {
				var n int
				if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				want := 0
				if partial == table || partial == "both" {
					want = 1
				}
				if n != want {
					t.Fatalf("%s backfilled or lost evidence: %d != %d", table, n, want)
				}
			}
			s.Close()
			withRaw(t, path, func(db *sql.DB) {
				// Re-running the hop stamps v18; the later hop's reentry
				// returns the store to the current version.
				for _, hop := range []func(*sql.DB) error{migrateV17toV18, migrateV18toV19, migrateV19toV20} {
					if err := hop(db); err != nil {
						t.Fatal(err)
					}
				}
			})
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			if !reflect.DeepEqual(after, dbDump(t, path)) {
				t.Fatal("reentry/reopen changed exact evidence")
			}
		})
	}
}

func TestCursorObligationMigrationRefusesAmbiguousEvidence(t *testing.T) {
	for _, table := range []string{"cursor_inputs", "cursor_proofs"} {
		variants := []string{"case name", "wrong shape", "wrong type", "invalid JSON", "unknown field", "duplicate field", "nested Unicode alias", "wrong store", "wrong key", "wrong instance", "invalid state"}
		if table == "cursor_proofs" {
			variants = append(variants, "duplicate source session", "duplicate source result", "Unicode source alias")
		}
		for _, variant := range variants {
			t.Run(table+"/"+variant, func(t *testing.T) {
				dir := t.TempDir()
				path := makeVersionedDB(t, dir, 17)
				seedV16PermissionHistory(t, path)
				seedV18Obligation(t, path, table)
				withRaw(t, path, func(db *sql.DB) {
					switch variant {
					case "case name":
						mustExec(t, db, "ALTER TABLE "+table+" RENAME TO temporary_obligations")
						mustExec(t, db, "ALTER TABLE temporary_obligations RENAME TO "+strings.ToUpper(table))
					case "wrong shape":
						mustExec(t, db, "ALTER TABLE "+table+" ADD COLUMN unknown TEXT")
					case "wrong type":
						mustExec(t, db, "UPDATE "+table+" SET record=CAST(record AS BLOB)")
					case "invalid JSON":
						mustExec(t, db, "UPDATE "+table+" SET record='{' ")
					case "unknown field":
						mustExec(t, db, "UPDATE "+table+` SET record='{"unexpected":true,'||substr(record,2)`)
					case "duplicate field":
						mustExec(t, db, "UPDATE "+table+` SET record='{"Generation":99,'||substr(record,2)`)
					case "nested Unicode alias":
						mustExec(t, db, "UPDATE "+table+` SET record=replace(record,'"SessionID":','"ſessionID":"wrong","SessionID":')`)
					case "duplicate source session":
						mustExec(t, db, `UPDATE cursor_proofs SET raw=CAST(replace(CAST(raw AS TEXT),'"sessionId":"session"','"sessionId":"wrong","sessionId":"session"') AS BLOB)`)
					case "Unicode source alias":
						mustExec(t, db, `UPDATE cursor_proofs SET raw=CAST(replace(CAST(raw AS TEXT),'"sessionId":"session"','"ſessionId":"wrong","sessionId":"session"') AS BLOB)`)
					case "duplicate source result":
						mustExec(t, db, `UPDATE cursor_proofs SET raw=CAST(replace(CAST(raw AS TEXT),'"exitCode":0','"exitCode":null,"exitCode":0') AS BLOB)`)
					case "wrong store":
						mustExec(t, db, "UPDATE "+table+" SET store_id='0000000000000000'")
					case "wrong key":
						mustExec(t, db, "UPDATE "+table+" SET connection_id='other'")
					case "wrong instance":
						mustExec(t, db, "UPDATE "+table+" SET instance_id='missing'")
					case "invalid state":
						if table == "cursor_inputs" {
							mustExec(t, db, `UPDATE cursor_inputs SET record=replace(record,'"State":"unresolved"','"State":"replay"')`)
						} else {
							mustExec(t, db, `UPDATE cursor_proofs SET raw=CAST('{"sessionId":"session","update":{"sessionUpdate":"tool_call_update","toolCallId":"tool","status":"pending","rawOutput":{"success":true}}}' AS BLOB)`)
						}
					}
				})
				before := dbDump(t, path)
				writeSentinel(t, dir, 17, schemaVersion)
				s, err := openMigrationTestStore(t, path)
				if s != nil {
					s.Close()
				}
				if !errors.Is(err, ErrSchemaMismatch) {
					t.Fatalf("ambiguous evidence admitted: %v", err)
				}
				if readUserVersion(t, path) != 17 || !reflect.DeepEqual(before, dbDump(t, path)) {
					t.Fatal("refusal changed DDL/data/version")
				}
				assertSentinel(t, dir, true)
			})
		}
	}
}

// Go's JSON decoder folds long s to S even though strings.ToLower does not.
// Refusing a live read must preserve the conflicting nested metadata verbatim.
func TestCursorObligationLiveReadRefusesNestedUnicodeAlias(t *testing.T) {
	for _, table := range []string{"cursor_inputs", "cursor_proofs"} {
		t.Run(table, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 17)
			seedV16PermissionHistory(t, path)
			seedV18Obligation(t, path, table)
			writeSentinel(t, dir, 17, schemaVersion)
			s, err := openMigrationTestStore(t, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			mustExec(t, s.db, "UPDATE "+table+` SET record=replace(record,'"SessionID":','"ſessionID":"wrong","SessionID":')`)
			before := dbDump(t, path)
			if table == "cursor_inputs" {
				_, err = s.ReadCursorInput("connection", "input")
			} else {
				_, err = s.ReadCursorProof("connection", "proof")
			}
			if err == nil {
				t.Fatal("ambiguous nested Unicode alias accepted")
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("refusal rewrote original evidence")
			}
		})
	}
}

func TestCursorObligationMigrationCommitRollbackAndRetry(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 17)
	seedV16PermissionHistory(t, path)
	before := dbDump(t, path)
	writeSentinel(t, dir, 17, schemaVersion)
	db := openRaw(t, path)
	mustExec(t, db, "PRAGMA journal_mode=DELETE")
	mustExec(t, db, "PRAGMA busy_timeout=1")
	reader := openRaw(t, path)
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM spawns").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := migrateV17toV18(db); err == nil {
		t.Fatal("commit succeeded with blocking reader")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	reader.Close()
	if readUserVersion(t, path) != 17 || !reflect.DeepEqual(before, dbDump(t, path)) {
		t.Fatal("failed commit retained DDL/data/version")
	}
	assertSentinel(t, dir, true)
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "PRAGMA journal_mode=WAL") })
	s, err := openMigrationTestStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if userVersion(t, s.db) != schemaVersion {
		t.Fatal("authorized retry failed")
	}
	assertSentinel(t, dir, false)
}

// v18 has no reverse SQL. Restoring a stopped-writer v17 backup loses every
// later obligation and requires authorization again before the current binary opens it.
func TestCursorObligationRestoreOnlyRetainsBackupIdentity(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 17)
	seedV16PermissionHistory(t, path)
	before, id := dbDump(t, path), assertOneStoreID(t, path)
	backup := readDBFileSet(t, path)
	writeSentinel(t, dir, 17, schemaVersion)
	s, err := openMigrationTestStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	seedV18Obligation(t, path, "cursor_inputs")
	seedV18Obligation(t, path, "cursor_proofs")
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "UPDATE spawns SET labels='post-upgrade' WHERE instance_id='pre'") })
	// The backup was captured before any v18-only data existed. Inspect and
	// restore a separate stopped copy; never overwrite the upgraded evidence.
	restoredDir := t.TempDir()
	restored := filepath.Join(restoredDir, "state.db")
	for name, data := range backup {
		if err := os.WriteFile(restored+strings.TrimPrefix(name, path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, dbDump(t, restored)) || assertOneStoreID(t, restored) != id {
		t.Fatal("restore lost pre-upgrade data/identity")
	}
	oldBytes := snapshotDBBytes(t, restored)
	if opened, err := Open(restored); !errors.Is(err, ErrSchemaMigrationRequired) {
		if opened != nil {
			opened.Close()
		}
		t.Fatalf("restore bypassed administrator: %v", err)
	}
	assertDBBytesUnchanged(t, restored, oldBytes)
	writeSentinel(t, restoredDir, 17, schemaVersion)
	reopened, err := openMigrationTestStore(t, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, table := range []string{"cursor_inputs", "cursor_proofs"} {
		var n int
		if err := reopened.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("restore invented later %s: %d %v", table, n, err)
		}
	}
	if reopened.StoreID() != id {
		t.Fatal("restore/remigration replaced store identity")
	}
}
