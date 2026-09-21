package store

// schema_v4_migration_test.go — migration-gate coverage for the schema v4 step
// (b.v2c). v4 adds the session_history table and its index so a session rotation
// no longer orphans the prior transcript. Reuses the Part A white-box
// fixtures/helpers verbatim (makeVersionedDB / makeV1DB / stampUserVersion /
// readUserVersion / writeSentinel / assertSentinel* / snapshotDBBytes /
// assertDBBytesUnchanged / openRaw / objectExists) — it invents no new sentinel
// format or gate semantics.
//
// Dispositions covered (mirroring schema_v3_migration_test.go):
//   (a) v3 DB + {"from":3,"to":4} sentinel → v4: user_version bumps, the
//       session_history table + index appear, sentinel consumed.
//   (b) v3 DB WITHOUT a sentinel → ErrSchemaMigrationRequired, DB byte-identical
//       after the refused open.
//   (c) idempotent re-entry: migrateV3toV4 run twice on the same DB succeeds
//       (CREATE ... IF NOT EXISTS) and leaves the table intact at v4.
//   (d) a fresh DB born directly at schemaVersion has session_history already,
//       with NO migration step executed.
//   (e) convergence: a freshly-created DB and a v1→…→v4 migrated DB expose the
//       same session_history shape.

import (
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

// makeV3DB creates a WAL-format v3 (user_version=3) fixture DB under dir. Guarded
// so it only builds when schemaVersion is at least 4 (a v3 fixture below the
// current version is always re-migratable through the real chain).
func makeV3DB(t *testing.T, dir string) string {
	t.Helper()
	return makeVersionedDB(t, dir, 3)
}

// assertSessionHistoryShape asserts the session_history table exists with its
// key columns and the instance index. It is the shared shape check across the
// v4 dispositions.
func assertSessionHistoryShape(t *testing.T, s *Store) {
	t.Helper()
	if !objectExists(t, s.db, "table", "session_history") {
		t.Fatalf("session_history table missing after v4 schema")
	}
	cols := columnNames(t, s.db, "session_history")
	want := map[string]bool{
		"history_id": false, "claude_instance_id": false,
		"claude_session_id": false, "jsonl_path": false, "recorded_at": false,
	}
	for _, c := range cols {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("session_history.%s column missing; cols: %v", name, cols)
		}
	}
	if !objectExists(t, s.db, "index", "idx_session_history_instance") {
		t.Errorf("idx_session_history_instance index missing after v4 schema")
	}
}

// TestV4Migration_AuthorizedFromV3 covers disposition (a).
func TestV4Migration_AuthorizedFromV3(t *testing.T) {
	if schemaVersion < 4 {
		t.Skip("schemaVersion < 4; v4 step not present")
	}
	dir := t.TempDir()
	dbPath := makeV3DB(t, dir)

	// Precondition: a true v3 fixture has NO session_history table yet.
	rawBefore := openRaw(t, dbPath)
	if objectExists(t, rawBefore, "table", "session_history") {
		t.Fatalf("precondition: v3 fixture already has session_history; not a true v3")
	}

	writeSentinel(t, dir, 3, schemaVersion)
	assertSentinelPresent(t, dir)

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(authorized v3): %v", err)
	}
	defer s.Close()

	if got := readUserVersion(t, dbPath); got != schemaVersion {
		t.Errorf("post-migration user_version = %d; want %d", got, schemaVersion)
	}
	assertSessionHistoryShape(t, s)
	assertSentinelAbsent(t, dir)
}

// TestV4Migration_IdempotentReentry covers disposition (c): migrateV3toV4 uses
// CREATE ... IF NOT EXISTS, so a second run against a DB that already carries
// session_history succeeds and leaves the table intact at v4.
func TestV4Migration_IdempotentReentry(t *testing.T) {
	if schemaVersion < 4 {
		t.Skip("schemaVersion < 4; v4 step not present")
	}
	dir := t.TempDir()
	dbPath := makeV3DB(t, dir)
	db := openRaw(t, dbPath)

	if err := migrateV3toV4(db); err != nil {
		t.Fatalf("first migrateV3toV4: %v", err)
	}
	if v := readUserVersionDB(t, db); v != 4 {
		t.Fatalf("after first migration user_version = %d; want 4", v)
	}
	if !objectExists(t, db, "table", "session_history") {
		t.Fatalf("session_history missing after first migration")
	}

	// Second run on the SAME DB — table already present. Must be a clean no-op.
	if err := migrateV3toV4(db); err != nil {
		t.Fatalf("second (idempotent) migrateV3toV4: %v", err)
	}
	if v := readUserVersionDB(t, db); v != 4 {
		t.Errorf("after idempotent re-run user_version = %d; want 4", v)
	}
	if !objectExists(t, db, "table", "session_history") {
		t.Errorf("session_history disappeared after idempotent re-run")
	}
}

// TestV4Migration_RefusedWithoutSentinel covers disposition (b): a v3 fixture
// with NO sentinel refuses with ErrSchemaMigrationRequired and leaves the DB
// byte-identical (rollback leaves user_version=3 intact).
func TestV4Migration_RefusedWithoutSentinel(t *testing.T) {
	if schemaVersion < 4 {
		t.Skip("schemaVersion < 4; v4 step not present")
	}
	dir := t.TempDir()
	dbPath := makeV3DB(t, dir)

	before := snapshotDBBytes(t, dbPath)
	beforeVersion := readUserVersion(t, dbPath)

	_, err := Open(dbPath)
	if !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("Open(v3, no sentinel) err = %v; want errors.Is ErrSchemaMigrationRequired", err)
	}

	assertDBBytesUnchanged(t, dbPath, before)
	if v := readUserVersion(t, dbPath); v != beforeVersion {
		t.Errorf("user_version = %d after refused open; want %d (unchanged)", v, beforeVersion)
	}
	assertSentinelAbsent(t, dir)
}

// TestV4FreshCreate_And_MigratedConverge covers dispositions (d) and (e): a
// fresh DB is born at schemaVersion with session_history already present and NO
// migration step run, and a v1→…→v4 migrated DB exposes the identical
// session_history shape (fresh-create DDL and the migration path agree — the
// two-places rule).
func TestV4FreshCreate_And_MigratedConverge(t *testing.T) {
	if schemaVersion < 4 {
		t.Skip("schemaVersion < 4; v4 step not present")
	}

	// (d) Fresh create.
	freshDir := t.TempDir()
	freshPath := freshDir + "/state.db"
	fresh, err := OpenOrInit(freshPath)
	if err != nil {
		t.Fatalf("OpenOrInit(fresh): %v", err)
	}
	defer fresh.Close()
	if v := readUserVersion(t, freshPath); v != schemaVersion {
		t.Errorf("fresh DB user_version = %d; want %d", v, schemaVersion)
	}
	assertSessionHistoryShape(t, fresh)
	assertSentinelAbsent(t, freshDir)
	freshCols := columnNames(t, fresh.db, "session_history")

	// (e) v1→…→v4 migrated via the authorized chain.
	migDir := t.TempDir()
	migPath := makeV1DB(t, migDir)
	writeSentinel(t, migDir, 1, schemaVersion)
	migrated, err := Open(migPath)
	if err != nil {
		t.Fatalf("Open(v1→v4 chain): %v", err)
	}
	defer migrated.Close()
	if v := readUserVersion(t, migPath); v != schemaVersion {
		t.Errorf("migrated DB user_version = %d; want %d", v, schemaVersion)
	}
	assertSessionHistoryShape(t, migrated)
	migCols := columnNames(t, migrated.db, "session_history")

	// Convergence: identical column set (order and names).
	if len(freshCols) != len(migCols) {
		t.Fatalf("session_history column count diverges: fresh %v vs migrated %v", freshCols, migCols)
	}
	for i := range freshCols {
		if freshCols[i] != migCols[i] {
			t.Errorf("session_history column %d diverges: fresh %q vs migrated %q", i, freshCols[i], migCols[i])
		}
	}
}
