package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	_ "modernc.org/sqlite"
)

// This file hosts the shared migration-gate test fixtures for package store.
//
// Architecture note (SR-12.3): these are white-box `package store` helpers.
// The canonical public fixture helper lives in internal/testsupport/storefix,
// but this package cannot import it (storefix imports store → circular import,
// documented in schema_test.go). Migration-gate fixtures therefore live here
// as _test.go helpers and are consumed by the refusal-path and authorized-path
// sibling test files.
//
// Fixture-shape contract (b.93m fix): a vN fixture must have vN's ACTUAL
// physical schema — the columns/indexes that version really had on disk — not
// merely the current DDL with user_version stamped down. The older "build
// current, stamp down" strategy broke once schemaVersion advanced past the
// lowest gated version: a "v2" fixture physically carried v3's five spawns
// columns, so re-migrating it (ALTER TABLE ADD COLUMN pid …) failed with
// "duplicate column name: pid". Each fixture is therefore built at its true
// physical shape:
//
//   - v1: load the canonical testdata/schema_v1.sql script (the pre-migration
//     v1 DDL), which stamps user_version=1 itself. This is the authoritative
//     v1 shape and never changes as schemaVersion advances.
//   - vN (1 < N ≤ schemaVersion): start from the v1 fixture, then run the REAL
//     registered migration steps 1→2→…→N through the engine (migrationStepFrom
//     + step.apply). Every intermediate schema is thus produced by the same
//     production code path the gate exercises, so a vN fixture is physically
//     identical to a genuinely-migrated vN DB — no manual ALTER/DROP guesswork.
//
// All fixtures stay WAL-format (loadV1Fixture sets journal_mode=WAL before any
// write). This matters for byte-identity assertions (PM Q4): ensureJournalModeWAL
// runs before ensureSchema and would append a WAL header on a non-WAL fixture
// even on an open that is ultimately refused. Because these fixtures are already
// genuine WAL DBs, a refused open touches no bytes, so byte-identity is
// well-defined. See makeVersionedDB.

// makeVersionedDB creates a state.db under dir at the given targetVersion and
// returns its resolved path, built at that version's TRUE physical schema (see
// the file-level fixture-shape contract). It loads the v1 fixture, then drives
// the real registered migration steps forward until user_version == targetVersion.
//
// targetVersion must be in [1, schemaVersion]: 1 yields the raw v1 fixture, and
// any higher value replays the production 1→…→targetVersion chain. Callers that
// need user_version==0 (fresh-DB re-arm) or a NEWER-than-binary stamp should
// build the base fixture here and stamp separately with stampUserVersion — a
// stamp is a pure header write and does not change physical shape, which is
// exactly what those synthetic/newer-than-binary tests want.
func makeVersionedDB(t *testing.T, dir string, targetVersion int) string {
	t.Helper()
	if targetVersion < 1 || targetVersion > schemaVersion {
		t.Fatalf("makeVersionedDB: targetVersion=%d out of range [1,%d]; stamp separately for synthetic/newer versions",
			targetVersion, schemaVersion)
	}
	path := filepath.Join(dir, "state.db")
	loadV1Fixture(t, path)
	if targetVersion == 1 {
		return path
	}
	migrateFixtureTo(t, path, targetVersion)
	return path
}

// loadV1Fixture loads the canonical testdata/schema_v1.sql script into a fresh
// WAL-format SQLite file at path (the script stamps user_version=1). It is the
// physical-shape source of truth for v1 and the base every higher fixture is
// migrated up from.
func loadV1Fixture(t *testing.T, path string) {
	t.Helper()
	v1SQL, err := os.ReadFile("testdata/schema_v1.sql")
	if err != nil {
		t.Fatalf("loadV1Fixture: read v1 fixture: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("loadV1Fixture: raw open %q: %v", path, err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("loadV1Fixture: close raw db: %v", cerr)
		}
	}()
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("loadV1Fixture: set WAL: %v", err)
	}
	if _, err := db.Exec(string(v1SQL)); err != nil {
		t.Fatalf("loadV1Fixture: apply v1 DDL: %v", err)
	}
}

// migrateFixtureTo drives the REAL registered migration steps against the DB at
// path until user_version == target, producing that version's genuine physical
// schema through the production code path (migrationStepFrom + step.apply). It
// deliberately bypasses the authorization gate — a fixture builder is not an
// operator open — so no sentinel is required.
func migrateFixtureTo(t *testing.T, path string, target int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("migrateFixtureTo: raw open %q: %v", path, err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("migrateFixtureTo: close raw db: %v", cerr)
		}
	}()
	for v := readUserVersionDB(t, db); v < target; v = readUserVersionDB(t, db) {
		step, ok := migrationStepFrom(v)
		if !ok {
			t.Fatalf("migrateFixtureTo: no registered migration step from user_version=%d toward %d", v, target)
		}
		if err := step.apply(db); err != nil {
			t.Fatalf("migrateFixtureTo: step %d→%d: %v", v, v+1, err)
		}
	}
}

// readUserVersionDB reads PRAGMA user_version off an already-open *sql.DB.
// Companion to readUserVersion (which opens by path); used inside the fixture
// migration loop to avoid reopening the file between steps.
func readUserVersionDB(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("readUserVersionDB: scan: %v", err)
	}
	return v
}

// makeV1DB creates a WAL-format v1 (user_version=1) fixture DB under dir and
// returns its path. This is the raw canonical v1 fixture — the true v1 physical
// shape, unchanged as schemaVersion advances.
func makeV1DB(t *testing.T, dir string) string {
	t.Helper()
	return makeVersionedDB(t, dir, 1)
}

// makeV2DB creates a WAL-format v2 (user_version=2) fixture DB under dir and
// returns its path, built by migrating a v1 fixture through the real v1→v2 step
// so it carries v2's ACTUAL physical schema (request_token column, composite
// UNIQUE, v2 indexes — and crucially NOT v3's five spawns columns). A prior
// version pinned this to schemaVersion; the b.93m fix makes it a true v2 so the
// v2→v3 gate tests have a re-migratable fixture.
func makeV2DB(t *testing.T, dir string) string {
	t.Helper()
	return makeVersionedDB(t, dir, 2)
}

// stampUserVersion opens the DB raw and stamps PRAGMA user_version to v.
// PRAGMA values cannot be parameterized, so v is interpolated directly — safe
// because callers pass only test-controlled integers. This is the generalized
// replacement for the older setUserVersion (schema_mismatch_test.go) — it does
// the same work under a name that reads well next to makeVersionedDB.
func stampUserVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("stampUserVersion: raw open %q: %v", path, err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("stampUserVersion: close raw db: %v", cerr)
		}
	}()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatalf("stampUserVersion: set user_version=%d: %v", v, err)
	}
}

// readUserVersion reads back the on-disk user_version of the DB at path.
// Convenience for gate assertions ("refused open left version unchanged").
func readUserVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("readUserVersion: raw open %q: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("readUserVersion: scan: %v", err)
	}
	return v
}

// ---------------------------------------------------------------------------
// Sentinel-file helpers.
//
// The authorization sentinel is a sibling of the DB file named
// sentinelFilename, containing strict JSON {"from":N,"to":M}. These helpers
// write valid and deliberately-broken variants, read them back, and assert
// presence/absence — all parameterized by the directory the DB/sentinel lives
// under (SR-2.2: never hard-coded to ~/.agent-director).
// ---------------------------------------------------------------------------

// writeSentinel writes a valid {"from":from,"to":to} authorization sentinel
// beside the DB in dir and returns the sentinel path. The JSON is produced via
// the same struct the production parser consumes, so a "valid" fixture stays in
// lock-step with parseAuthorization.
func writeSentinel(t *testing.T, dir string, from, to int) string {
	t.Helper()
	data, err := json.Marshal(migrationAuthorization{From: from, To: to})
	if err != nil {
		t.Fatalf("writeSentinel: marshal: %v", err)
	}
	return writeSentinelRaw(t, dir, data)
}

// writeSentinelRaw writes arbitrary bytes as the sentinel beside the DB in dir
// and returns the sentinel path. It is the escape hatch the malformed-variant
// helpers build on; tests generally prefer the named variants below.
func writeSentinelRaw(t *testing.T, dir string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, sentinelFilename)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writeSentinelRaw: write %q: %v", path, err)
	}
	return path
}

// writeSentinelMalformed writes a sentinel whose contents are not valid JSON at
// all (so parseAuthorization fails at the decode step). Returns the path.
func writeSentinelMalformed(t *testing.T, dir string) string {
	t.Helper()
	return writeSentinelRaw(t, dir, []byte("{not valid json"))
}

// writeSentinelWrongFrom writes a well-formed sentinel whose `from` end does
// NOT match wantFrom (it uses wantFrom+1), while `to` is correct. Exercises the
// version-mismatch branch on the from side. Returns the path.
func writeSentinelWrongFrom(t *testing.T, dir string, wantFrom, to int) string {
	t.Helper()
	return writeSentinel(t, dir, wantFrom+1, to)
}

// writeSentinelWrongTo writes a well-formed sentinel whose `to` end does NOT
// match wantTo (it uses wantTo+1), while `from` is correct. Exercises the
// version-mismatch branch on the to side. Returns the path.
func writeSentinelWrongTo(t *testing.T, dir string, from, wantTo int) string {
	t.Helper()
	return writeSentinel(t, dir, from, wantTo+1)
}

// readSentinel reads back the sentinel bytes from dir. It fails the test if the
// sentinel is absent — use sentinelExists for presence checks.
func readSentinel(t *testing.T, dir string) []byte {
	t.Helper()
	path := filepath.Join(dir, sentinelFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readSentinel: read %q: %v", path, err)
	}
	return data
}

// sentinelExists reports whether a sentinel file is present beside the DB in
// dir. It distinguishes a genuine absence (ErrNotExist) from any other stat
// error, which it treats as a test failure.
func sentinelExists(t *testing.T, dir string) bool {
	t.Helper()
	path := filepath.Join(dir, sentinelFilename)
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	t.Fatalf("sentinelExists: stat %q: %v", path, err)
	return false
}

// assertSentinelPresent fails the test unless a sentinel is present in dir.
func assertSentinelPresent(t *testing.T, dir string) {
	t.Helper()
	if !sentinelExists(t, dir) {
		t.Errorf("assertSentinelPresent: no sentinel %q in %q", sentinelFilename, dir)
	}
}

// assertSentinelAbsent fails the test unless the sentinel in dir is gone.
func assertSentinelAbsent(t *testing.T, dir string) {
	t.Helper()
	if sentinelExists(t, dir) {
		t.Errorf("assertSentinelAbsent: sentinel %q still present in %q", sentinelFilename, dir)
	}
}

// ---------------------------------------------------------------------------
// Byte-identity helpers.
//
// A refused open must leave the DB file byte-identical, WAL/SHM sidecars
// included — this is the on-disk proof of "no DB write / no WAL growth". The
// snapshot hashes the main DB file plus its -wal and -shm sidecars (each hashed
// only if present) so a spurious WAL append is caught, not just a main-file
// change.
// ---------------------------------------------------------------------------

// dbSidecars returns the DB path plus its WAL/SHM sidecar paths, in a stable
// order. The sidecars may or may not exist at snapshot time; the caller hashes
// only those present.
func dbSidecars(dbPath string) []string {
	return []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
}

// snapshotDBBytes returns a sha256-based fingerprint of the DB file at dbPath
// INCLUDING any -wal and -shm sidecars. The fingerprint is a map from each
// present file's basename to its hex sha256 (absent sidecars are simply omitted
// so appearance/disappearance of a sidecar is itself a detectable change).
func snapshotDBBytes(t *testing.T, dbPath string) map[string]string {
	t.Helper()
	snap := make(map[string]string)
	for _, p := range dbSidecars(dbPath) {
		data, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			t.Fatalf("snapshotDBBytes: read %q: %v", p, err)
		}
		sum := sha256.Sum256(data)
		snap[filepath.Base(p)] = hex.EncodeToString(sum[:])
	}
	return snap
}

// assertDBBytesUnchanged re-snapshots dbPath and fails the test if any tracked
// file's hash differs from before, or if a sidecar appeared or disappeared.
// This is the "no WAL growth" / byte-identity-after-refused-open assertion.
func assertDBBytesUnchanged(t *testing.T, dbPath string, before map[string]string) {
	t.Helper()
	after := snapshotDBBytes(t, dbPath)

	names := map[string]struct{}{}
	for n := range before {
		names[n] = struct{}{}
	}
	for n := range after {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	for _, n := range sorted {
		b, hadBefore := before[n]
		a, hasAfter := after[n]
		switch {
		case hadBefore && !hasAfter:
			t.Errorf("assertDBBytesUnchanged: %q disappeared after open", n)
		case !hadBefore && hasAfter:
			t.Errorf("assertDBBytesUnchanged: %q appeared after open (unexpected write)", n)
		case b != a:
			t.Errorf("assertDBBytesUnchanged: %q changed after open\n  before %s\n  after  %s", n, b, a)
		}
	}
}
