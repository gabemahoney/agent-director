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
// Fixtures are built THROUGH the store (create a current-version DB, then stamp
// user_version DOWN) rather than replayed from a raw SQL script. This matters
// for byte-identity assertions (PM Q4): ensureJournalModeWAL runs before
// ensureSchema and would write a WAL header on a non-WAL fixture even on an
// open that is ultimately refused. Because these fixtures are already genuine
// store-created WAL DBs, a refused open touches no bytes, so byte-identity is
// well-defined. See makeVersionedDB.

// makeVersionedDB creates a state.db under dir at the given target user_version
// and returns its resolved path. The DB is constructed THROUGH the store
// (OpenOrInit creates a current, WAL-format schemaVersion DB) and then stamped
// DOWN to targetVersion with a raw PRAGMA. The result is a genuine WAL-format
// SQLite file whose only difference from a freshly-created current DB is the
// user_version stamp — exactly the shape an older-than-binary DB has on disk.
//
// targetVersion may be any value; passing schemaVersion leaves the DB current,
// and passing 0 re-arms the fresh-DB branch. Callers that want the canonical
// v1/v2 fixtures should use makeV1DB / makeV2DB.
func makeVersionedDB(t *testing.T, dir string, targetVersion int) string {
	t.Helper()
	path := filepath.Join(dir, "state.db")
	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("makeVersionedDB: OpenOrInit(%q): %v", path, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("makeVersionedDB: Close: %v", err)
	}
	if targetVersion != schemaVersion {
		stampUserVersion(t, path, targetVersion)
	}
	return path
}

// makeV1DB creates a WAL-format v1 (user_version=1) fixture DB under dir and
// returns its path. Built through the store so it is a real WAL DB (see the
// file-level note on byte-identity).
func makeV1DB(t *testing.T, dir string) string {
	t.Helper()
	return makeVersionedDB(t, dir, 1)
}

// makeV2DB creates a WAL-format v2 (current) fixture DB under dir and returns
// its path. Equivalent to a plain OpenOrInit; provided for symmetry with
// makeV1DB so gate tests read uniformly.
func makeV2DB(t *testing.T, dir string) string {
	t.Helper()
	return makeVersionedDB(t, dir, schemaVersion)
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
