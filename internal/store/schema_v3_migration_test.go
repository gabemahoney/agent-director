package store

// schema_v3_migration_test.go — migration-gate coverage for the schema v3 step
// (t3.93m.fn.va.gh). Reuses Part A's white-box fixtures/helpers verbatim:
// makeV1DB / makeV2DB / makeVersionedDB / stampUserVersion / readUserVersion,
// writeSentinel / assertSentinelPresent / assertSentinelAbsent, and the
// byte-identity pair snapshotDBBytes / assertDBBytesUnchanged (all in
// migration_fixtures_test.go). It invents no new sentinel format or gate
// semantics.
//
// Dispositions covered (SR-5, SR-12.2):
//   (a) v2 DB + {"from":2,"to":3} sentinel → v3: user_version bumps, all five
//       new spawns columns present via PRAGMA table_info with the correct
//       nullability and the extra_env '{}' default, sentinel consumed.
//   (b) v2 DB WITHOUT a sentinel → ErrSchemaMigrationRequired, DB byte-identical
//       after the refused open.
//   (c) v1 DB + {"from":1,"to":3} chains v1→v2→v3 in a SINGLE open — the real
//       end-to-end chain test carried forward from Part A's joint-ownership note
//       (t3.93m.i5.ck.vc): Part A proved loop mechanics at schemaVersion=2 plus
//       a synthetic multi-step white-box test; the true v1→v3 end-to-end lands
//       HERE now that schemaVersion=3 exists.
//   (d) a fresh DB born directly at user_version=3 with all five columns and NO
//       migration step executed.

import (
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

// v3SpawnsColumn describes the expected shape of one of the five columns the
// v2→v3 step adds: its name, whether it is NOT NULL, and (for extra_env) the
// literal default. dfltWant is compared only when checkDflt is true.
type v3SpawnsColumn struct {
	name      string
	notNull   bool
	checkDflt bool
	dfltWant  string
}

// v3NewColumns is the authoritative expectation for the five columns migrateV2toV3
// adds. pid/proc_starttime/liveness_unverified_since/liveness_note are all
// nullable; extra_env is NOT NULL DEFAULT '{}'.
var v3NewColumns = []v3SpawnsColumn{
	{name: "pid", notNull: false},
	{name: "proc_starttime", notNull: false},
	{name: "liveness_unverified_since", notNull: false},
	{name: "liveness_note", notNull: false},
	{name: "extra_env", notNull: true, checkDflt: true, dfltWant: "'{}'"},
}

// tableInfoRow mirrors one PRAGMA table_info() row for the assertions below.
type tableInfoRow struct {
	notNull int
	dflt    sql.NullString
}

// readTableInfo returns a map from column name to its table_info row for the
// given table, draining and closing the result set before returning.
func readTableInfo(t *testing.T, db *sql.DB, table string) map[string]tableInfoRow {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	out := make(map[string]tableInfoRow)
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		out[name] = tableInfoRow{notNull: notnull, dflt: dflt}
	}
	_ = rows.Close()
	return out
}

// assertV3Columns asserts every column in v3NewColumns is present on spawns with
// the expected nullability, and that extra_env carries the '{}' default. It is
// the shared shape check used by dispositions (a), (c), and (d).
func assertV3Columns(t *testing.T, db *sql.DB) {
	t.Helper()
	info := readTableInfo(t, db, "spawns")
	for _, want := range v3NewColumns {
		got, ok := info[want.name]
		if !ok {
			t.Errorf("spawns.%s column missing after v3 schema", want.name)
			continue
		}
		wantNN := 0
		if want.notNull {
			wantNN = 1
		}
		if got.notNull != wantNN {
			t.Errorf("spawns.%s notnull = %d; want %d", want.name, got.notNull, wantNN)
		}
		if want.checkDflt {
			if !got.dflt.Valid {
				t.Errorf("spawns.%s has no default; want %q", want.name, want.dfltWant)
			} else if got.dflt.String != want.dfltWant {
				t.Errorf("spawns.%s default = %q; want %q", want.name, got.dflt.String, want.dfltWant)
			}
		}
	}
}

// TestV3Migration_AuthorizedFromV2 covers disposition (a): a genuine v2 fixture
// beside an exact-match {"from":2,"to":schemaVersion} sentinel opens
// successfully, lands at schemaVersion, gains all five v3 spawns columns with
// correct nullability and the extra_env '{}' default, and has its sentinel
// consumed.
func TestV3Migration_AuthorizedFromV2(t *testing.T) {
	dir := t.TempDir()
	dbPath := makeV2DB(t, dir)

	// Precondition: the fixture is a TRUE v2 — the five v3 columns must be
	// absent before the step runs (this is what the b.93m fixture-shape fix
	// guarantees; a stamped-down v3 fixture would already carry them).
	rawBefore := openRaw(t, dbPath)
	beforeInfo := readTableInfo(t, rawBefore, "spawns")
	for _, c := range v3NewColumns {
		if _, present := beforeInfo[c.name]; present {
			t.Fatalf("precondition: v2 fixture already has spawns.%s; fixture is not a true v2", c.name)
		}
	}

	writeSentinel(t, dir, 2, schemaVersion)
	assertSentinelPresent(t, dir)

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(authorized v2): %v", err)
	}
	defer s.Close()

	if got := readUserVersion(t, dbPath); got != schemaVersion {
		t.Errorf("post-migration user_version = %d; want %d", got, schemaVersion)
	}
	assertV3Columns(t, s.db)
	assertSentinelAbsent(t, dir)
}

// TestV3Migration_IdempotentReentry covers the b.93m idempotency fix: the v2→v3
// step guards each ALTER TABLE ... ADD COLUMN with a pragma_table_info probe so
// re-running the hop against a DB whose spawns columns are ALREADY present
// succeeds instead of failing with SQLite's "duplicate column name" error.
//
// This white-box test (package store) drives migrateV2toV3 DIRECTLY, twice, on
// its own connection:
//
//   - First call: a true v2 fixture (no v3 columns) → all five columns added,
//     user_version stamped to 3.
//   - Second call: the SAME DB, now already carrying every v3 column → must
//     return nil (each guarded ALTER is skipped), leave the columns intact, and
//     re-stamp user_version=3.
//
// Without the pragma_table_info guard the second call's unguarded
// `ALTER TABLE spawns ADD COLUMN pid ...` would error, so a green run proves the
// guard, not merely that the columns exist. A partial-completion variant (only
// the first column present) exercises the mixed present/absent branch.
func TestV3Migration_IdempotentReentry(t *testing.T) {
	t.Run("full re-entry after complete migration", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := makeV2DB(t, dir)
		db := openRaw(t, dbPath)

		// First migration: true v2 → v3.
		if err := migrateV2toV3(db); err != nil {
			t.Fatalf("first migrateV2toV3: %v", err)
		}
		assertV3Columns(t, db)
		if v := readUserVersionDB(t, db); v != 3 {
			t.Fatalf("after first migration user_version = %d; want 3", v)
		}

		// Second migration on the SAME DB — every column already present. The
		// guarded ALTERs must all be skipped; an unguarded ADD COLUMN would fail
		// here with "duplicate column name".
		if err := migrateV2toV3(db); err != nil {
			t.Fatalf("second (idempotent) migrateV2toV3: %v", err)
		}
		assertV3Columns(t, db)
		if v := readUserVersionDB(t, db); v != 3 {
			t.Errorf("after idempotent re-run user_version = %d; want 3", v)
		}
	})

	t.Run("re-entry after partial migration adds only the missing columns", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := makeV2DB(t, dir)
		db := openRaw(t, dbPath)

		// Simulate a migration that crashed after adding only the first column:
		// add spawns.pid out of band, leaving the other four absent.
		if _, err := db.Exec("ALTER TABLE spawns ADD COLUMN pid INTEGER"); err != nil {
			t.Fatalf("seed partial migration (add pid): %v", err)
		}
		before := readTableInfo(t, db, "spawns")
		if _, ok := before["pid"]; !ok {
			t.Fatalf("precondition: spawns.pid not present after seeding")
		}
		if _, ok := before["extra_env"]; ok {
			t.Fatalf("precondition: spawns.extra_env unexpectedly present before migration")
		}

		// Re-entry must add the four missing columns without tripping over the
		// pre-existing pid column.
		if err := migrateV2toV3(db); err != nil {
			t.Fatalf("migrateV2toV3 over partial DB: %v", err)
		}
		assertV3Columns(t, db)
		if v := readUserVersionDB(t, db); v != 3 {
			t.Errorf("after partial re-entry user_version = %d; want 3", v)
		}
	})
}

// TestV3Migration_RefusedWithoutSentinel covers disposition (b): a v2 fixture
// with NO sentinel refuses with ErrSchemaMigrationRequired and leaves the DB
// byte-identical (main + WAL/SHM sidecars) after the refused open.
func TestV3Migration_RefusedWithoutSentinel(t *testing.T) {
	dir := t.TempDir()
	dbPath := makeV2DB(t, dir)

	before := snapshotDBBytes(t, dbPath)
	beforeVersion := readUserVersion(t, dbPath)

	_, err := Open(dbPath)
	if !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("Open(v2, no sentinel) err = %v; want errors.Is ErrSchemaMigrationRequired", err)
	}

	assertDBBytesUnchanged(t, dbPath, before)
	if v := readUserVersion(t, dbPath); v != beforeVersion {
		t.Errorf("user_version = %d after refused open; want %d (unchanged)", v, beforeVersion)
	}
	assertSentinelAbsent(t, dir)
}

// TestSchemaChain_V1toV3_EndToEnd is the JOINTLY-OWNED end-to-end chain test
// carried forward from Part A's joint-ownership note (t3.93m.i5.ck.vc): Part A
// proved chain-loop mechanics at schemaVersion=2 plus a synthetic multi-step
// white-box test and explicitly deferred the REAL v1→v3 end-to-end here, since
// it could not execute until Epic t1.93m.fn landed schemaVersion=3 and the
// v2→v3 step. It covers disposition (c): a v1 fixture beside a
// {"from":1,"to":schemaVersion} sentinel chains v1→v2→v3 in a SINGLE open —
// asserting the final version, the v2 artifacts (request_token column + the two
// permission_requests indexes) persist, and the v3 spawns columns are present.
func TestSchemaChain_V1toV3_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	dbPath := makeV1DB(t, dir)
	writeSentinel(t, dir, 1, schemaVersion)
	assertSentinelPresent(t, dir)

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(v1→v3 chain): %v", err)
	}
	defer s.Close()

	// Single open climbed the whole chain to current.
	if got := readUserVersion(t, dbPath); got != schemaVersion {
		t.Errorf("chain landed user_version = %d; want %d", got, schemaVersion)
	}

	// v2 artifacts survive at v3: request_token column present, both
	// permission_requests indexes present.
	pcols := columnNames(t, s.db, "permission_requests")
	foundToken := false
	for _, c := range pcols {
		if c == "request_token" {
			foundToken = true
		}
	}
	if !foundToken {
		t.Errorf("request_token column missing after v1→v3 chain; cols: %v", pcols)
	}
	for _, idx := range []string{
		"idx_permission_requests_instance_decision",
		"idx_permission_requests_decision_decided_at",
	} {
		if !objectExists(t, s.db, "index", idx) {
			t.Errorf("v2 index %q missing after v1→v3 chain", idx)
		}
	}

	// v3 columns present with correct shape.
	assertV3Columns(t, s.db)

	// Sentinel consumed as part of the same logical operation.
	assertSentinelAbsent(t, dir)
}

// TestV3FreshCreate_BornAtV3 covers disposition (d): a store created on a fresh
// path is stamped directly at schemaVersion (3) with all five v3 spawns columns
// already present — NO migration step runs (a fresh create takes the
// version==0 branch, never runMigrationChain) and no sentinel is created.
func TestV3FreshCreate_BornAtV3(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.db"

	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("OpenOrInit(fresh) err = %v; want nil", err)
	}
	defer s.Close()

	if v := readUserVersion(t, path); v != schemaVersion {
		t.Errorf("fresh DB user_version = %d; want %d", v, schemaVersion)
	}
	assertV3Columns(t, s.db)
	assertSentinelAbsent(t, dir)
}
