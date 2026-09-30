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
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/testsupport/writefailfix"
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
//
// The v4 history fixture (makeV4HistoryFixture) follows the same contract: it
// is makeVersionedDB(t, dir, 4) — built on the real chain, never a current DB
// stamped down — with rows and session history seeded on top through a raw
// connection. Per SR-20.3 the inline SQL for that fixture, and for the v4→v5
// failure/pre-add arrangements, lives only in this file.
//
// injectWriteFailure is the white-box twin of storefix.InjectWriteFailure
// (SR-20.3); its trigger SQL comes only from internal/testsupport/writefailfix.

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

// ---------------------------------------------------------------------------
// Schema v5 (v4→v5 hop) fixtures and read helpers (b.fmk, SR-5.1/SR-5.4,
// SR-20.3). The only inline SQL for these fixtures lives here.
// ---------------------------------------------------------------------------

// v5ColumnSpec is the SR-5.1 expectation for one column the v4→v5 hop adds.
// dflt is the PRAGMA table_info dflt_value text; "" means no default (NULL).
type v5ColumnSpec struct {
	table, name, typ string
	notNull          bool
	dflt             string
}

// v5ColumnSpecs lists the thirteen v5 columns in SR-5.1 order.
var v5ColumnSpecs = []v5ColumnSpec{
	{"spawns", "row_version", "INTEGER", true, "0"},
	{"spawns", "launch_started_at", "INTEGER", false, ""},
	{"spawns", "life_number", "INTEGER", true, "0"},
	{"spawns", "no_pre_trust", "INTEGER", true, "0"},
	{"spawns", "launch_token", "TEXT", false, ""},
	{"spawns", "tmux_socket", "TEXT", false, ""},
	{"spawns", "tmux_server_pid", "INTEGER", false, ""},
	{"spawns", "tmux_server_started", "INTEGER", false, ""},
	{"spawns", "tmux_server_starttime", "TEXT", false, ""},
	{"spawns", "pane_id", "TEXT", false, ""},
	{"spawns", "pane_pid", "INTEGER", false, ""},
	{"spawns", "pane_starttime", "TEXT", false, ""},
	{"session_history", "life_number", "INTEGER", true, "0"},
}

// key returns "table.column", the name tests use to pick a v5 column.
func (c v5ColumnSpec) key() string { return c.table + "." + c.name }

// migratedValue is the quote() literal an existing row gets for this column
// from ADD COLUMN: the default, or NULL when the column has none.
func (c v5ColumnSpec) migratedValue() string {
	if c.dflt == "" {
		return "NULL"
	}
	return c.dflt
}

// tableColumn is one full PRAGMA table_info row (column order is slice order).
type tableColumn struct {
	name, typ string
	notNull   int
	dflt      sql.NullString
	pk        int
}

// readTableShape returns a table's full PRAGMA table_info shape in column order.
func readTableShape(t *testing.T, db *sql.DB, table string) []tableColumn {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("readTableShape: PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var out []tableColumn
	for rows.Next() {
		var cid int
		var c tableColumn
		if err := rows.Scan(&cid, &c.name, &c.typ, &c.notNull, &c.dflt, &c.pk); err != nil {
			t.Fatalf("readTableShape: scan table_info(%s): %v", table, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("readTableShape: iterate table_info(%s): %v", table, err)
	}
	return out
}

// v4HistoryFixture describes what makeV4HistoryFixture seeded. rows and
// history hold SQLite quote() literals ("NULL", "0", "'text'") read back
// before migration, so NULL, 0 and the empty string stay distinct and
// comparison is exact.
type v4HistoryFixture struct {
	dir, path string

	rotated      string // archived prior session + a different current session
	neverWritten string // ended row whose current session has no jsonl_path
	holdsCurrent string // history already holds the row's current session id
	interleavedA string // history interleaves in time with interleavedB
	interleavedB string
	pending      string // pending row, no session yet

	ids         []string                       // every seeded instance id
	spawnsCols  []string                       // the v4 spawns column names
	historyCols []string                       // the v4 session_history column names
	rows        map[string]map[string]string   // id → v4 spawns column → literal
	history     map[string][]map[string]string // id → entries (read order) → column → literal
}

// makeV4HistoryFixture builds a genuine v4 WAL store under dir holding the
// SR-20.3 scenario set, closes it, and returns what it seeded.
func makeV4HistoryFixture(t *testing.T, dir string) v4HistoryFixture {
	t.Helper()
	f := v4HistoryFixture{
		dir:          dir,
		path:         makeVersionedDB(t, dir, 4),
		rotated:      "v4-rotated",
		neverWritten: "v4-never-written",
		holdsCurrent: "v4-holds-current",
		interleavedA: "v4-interleaved-a",
		interleavedB: "v4-interleaved-b",
		pending:      "v4-pending",
	}
	f.ids = []string{f.rotated, f.neverWritten, f.holdsCurrent, f.interleavedA, f.interleavedB, f.pending}

	// Columns: id, parent, state, cwd, tmux name, args, relay, jsonl, session,
	// labels, started, last seen, ended, pid, proc start, liveness since/note, extra env.
	spawnRows := [][]any{
		{f.rotated, nil, "waiting", "/work/rotated", "ad-rotated", `["--model","opus"]`, "off",
			"/t/rot-current.jsonl", "sess-rot-current", `{"team":"red"}`,
			"2026-01-01 10:00:00", "2026-01-01 10:05:00", nil,
			4242, "98765", nil, nil, `{"K":"V"}`},
		{f.neverWritten, nil, "ended", "/work/never", "ad-never", "[]", "off",
			nil, "sess-nw-current", "{}",
			"2026-01-02 10:00:00", "2026-01-02 11:00:00", "2026-01-02 11:00:00",
			nil, nil, nil, nil, "{}"},
		{f.holdsCurrent, nil, "working", "/work/holds", "ad-holds", "[]", "off",
			"/t/hc-current.jsonl", "sess-hc-current", "{}",
			"2026-01-03 10:00:00", "2026-01-03 10:01:00", nil,
			5151, "11111", nil, nil, "{}"},
		{f.interleavedA, nil, "waiting", "/work/a", "ad-a", "[]", "off",
			"/t/a-current.jsonl", "sess-a-current", "{}",
			"2026-01-04 09:00:00", "2026-01-04 09:30:00", nil,
			nil, nil, nil, nil, "{}"},
		{f.interleavedB, f.interleavedA, "ask_user", "/work/b", "ad-b", "[]", "off",
			"/t/b-current.jsonl", "sess-b-current", `{"role":"child"}`,
			"2026-01-04 09:00:30", "2026-01-04 09:31:00", nil,
			6262, "22222", "2026-01-04 09:31:00", "pid gone", "{}"},
		{f.pending, nil, "pending", "/work/pending", "ad-pending", "[]", "off",
			nil, nil, "{}",
			"2026-01-05 12:00:00", "2026-01-05 12:00:00", nil,
			nil, nil, nil, nil, "{}"},
	}
	// Columns: instance id, session id, jsonl path, recorded_at.
	historyRows := [][]any{
		{f.rotated, "sess-rot-prior", "/t/rot-prior.jsonl", "2026-01-01 10:02:00"},
		{f.neverWritten, "sess-nw-prior", "/t/nw-prior.jsonl", "2026-01-02 10:30:00"},
		{f.holdsCurrent, "sess-hc-current", "/t/hc-current.jsonl", "2026-01-03 10:00:30"},
		{f.interleavedA, "sess-a-1", "/t/a-1.jsonl", "2026-01-04 09:10:00"},
		{f.interleavedB, "sess-b-1", "/t/b-1.jsonl", "2026-01-04 09:11:00"},
		{f.interleavedA, "sess-a-2", nil, "2026-01-04 09:12:00"},
		{f.interleavedB, "sess-b-2", "/t/b-2.jsonl", "2026-01-04 09:13:00"},
	}

	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatalf("makeV4HistoryFixture: raw open: %v", err)
	}
	for _, r := range spawnRows {
		if _, err := db.Exec(`INSERT INTO spawns (claude_instance_id, parent_id, state, cwd,
			tmux_session_name, claude_args, relay_mode, jsonl_path, claude_session_id, labels,
			started_at, last_seen_at, ended_at, pid, proc_starttime,
			liveness_unverified_since, liveness_note, extra_env)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r...); err != nil {
			t.Fatalf("makeV4HistoryFixture: insert spawn %v: %v", r[0], err)
		}
	}
	for _, h := range historyRows {
		if _, err := db.Exec(`INSERT INTO session_history
			(claude_instance_id, claude_session_id, jsonl_path, recorded_at) VALUES (?,?,?,?)`, h...); err != nil {
			t.Fatalf("makeV4HistoryFixture: insert history %v: %v", h, err)
		}
	}
	f.spawnsCols = tableColumnNames(t, db, "spawns")
	f.historyCols = tableColumnNames(t, db, "session_history")
	if err := db.Close(); err != nil {
		t.Fatalf("makeV4HistoryFixture: close raw db: %v", err)
	}

	f.rows = make(map[string]map[string]string, len(f.ids))
	f.history = make(map[string][]map[string]string, len(f.ids))
	for _, id := range f.ids {
		f.rows[id] = readRawSpawn(t, f.path, id, f.spawnsCols)
		f.history[id] = readRawHistory(t, f.path, id, f.historyCols)
	}
	return f
}

// tableColumnNames returns a table's column names in order.
func tableColumnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	var names []string
	for _, c := range readTableShape(t, db, table) {
		names = append(names, c.name)
	}
	return names
}

// readQuotedRows returns quote() literals of cols for every row of table
// belonging to instance id, in orderBy order. cols come from table_info, never
// from input.
func readQuotedRows(t *testing.T, path, table, id, orderBy string, cols []string) []map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("readQuotedRows: raw open %q: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = "quote(" + c + ")"
	}
	q := "SELECT " + strings.Join(sel, ", ") + " FROM " + table + " WHERE claude_instance_id = ?"
	if orderBy != "" {
		q += " ORDER BY " + orderBy
	}
	rows, err := db.Query(q, id)
	if err != nil {
		t.Fatalf("readQuotedRows: query %s for %q: %v", table, id, err)
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		vals := make([]string, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("readQuotedRows: scan %s for %q: %v", table, id, err)
		}
		row := make(map[string]string, len(cols))
		for i, c := range cols {
			row[c] = vals[i]
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("readQuotedRows: iterate %s for %q: %v", table, id, err)
	}
	return out
}

// readRawSpawn returns quote() literals of cols for one spawns row; it fails
// the test if the row is absent.
func readRawSpawn(t *testing.T, path, id string, cols []string) map[string]string {
	t.Helper()
	rows := readQuotedRows(t, path, "spawns", id, "", cols)
	if len(rows) != 1 {
		t.Fatalf("readRawSpawn: %d spawns rows for %q; want 1", len(rows), id)
	}
	return rows[0]
}

// readRawHistory returns quote() literals of cols for every session_history
// entry of id, in ListSessionHistory's order (newest recorded first).
func readRawHistory(t *testing.T, path, id string, cols []string) []map[string]string {
	t.Helper()
	return readQuotedRows(t, path, "session_history", id, "recorded_at DESC, history_id DESC", cols)
}

// v5ColumnValues holds one id's v5 values as quote() literals: the twelve
// spawns columns by name, and the life_number of each history entry in
// readRawHistory order.
type v5ColumnValues struct {
	spawns      map[string]string
	historyLife []string
}

// readV5Columns reads the thirteen v5 column values for instance id.
func readV5Columns(t *testing.T, path, id string) v5ColumnValues {
	t.Helper()
	var cols []string
	for _, c := range v5ColumnSpecs {
		if c.table == "spawns" {
			cols = append(cols, c.name)
		}
	}
	out := v5ColumnValues{spawns: readRawSpawn(t, path, id, cols)}
	for _, h := range readRawHistory(t, path, id, []string{"life_number"}) {
		out.historyLife = append(out.historyLife, h["life_number"])
	}
	return out
}

// preAddV5Columns adds the named v5 columns ("table.column") to the DB at path
// with their SR-5.1 definitions, simulating a v4→v5 hop that stopped part-way.
func preAddV5Columns(t *testing.T, path string, keys ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("preAddV5Columns: raw open %q: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	for _, k := range keys {
		spec, ok := v5ColumnSpecByKey(k)
		if !ok {
			t.Fatalf("preAddV5Columns: unknown v5 column %q", k)
		}
		ddl := "ALTER TABLE " + spec.table + " ADD COLUMN " + spec.name + " " + spec.typ
		if spec.notNull {
			ddl += " NOT NULL"
		}
		if spec.dflt != "" {
			ddl += " DEFAULT " + spec.dflt
		}
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("preAddV5Columns: add %s: %v", k, err)
		}
	}
}

// v5ColumnSpecByKey looks up a v5 column spec by "table.column".
func v5ColumnSpecByKey(key string) (v5ColumnSpec, bool) {
	for _, c := range v5ColumnSpecs {
		if c.key() == key {
			return c, true
		}
	}
	return v5ColumnSpec{}, false
}

// breakV5SessionHistoryHop makes session_history a view over the renamed v4
// table, so the hop fails adding session_history.life_number after it has
// added every spawns column. History entries stay readable through the view.
func breakV5SessionHistoryHop(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("breakV5SessionHistoryHop: raw open %q: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		"ALTER TABLE session_history RENAME TO session_history_v4",
		"CREATE VIEW session_history AS SELECT * FROM session_history_v4",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("breakV5SessionHistoryHop: %s: %v", stmt, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Store-id fixtures (Epic 4b; SR-5.1, SR-5.4, SR-20.6 "The store id"). Each
// helper opens its own raw connection and closes it before returning, so the
// store is closed afterwards and byte-identity snapshots are well defined.
// ---------------------------------------------------------------------------

// v5ShapeTables lists the tables whose fresh and migrated shapes must match
// (SR-20.6 shape case); store_meta is a table, so it is here, not in v5ColumnSpecs.
var v5ShapeTables = []string{"spawns", "session_history", "store_meta"}

// storeMetaShape is store_meta's expected PRAGMA table_info (SR-5.1; SR-20.6 shape case).
var storeMetaShape = []tableColumn{
	{name: "key", typ: "TEXT", notNull: 0, pk: 1},
	{name: "value", typ: "TEXT", notNull: 1, pk: 0},
}

// storeMetaRow is one raw store_meta row; typ is SQLite's typeof(value).
type storeMetaRow struct{ key, value, typ string }

// storeMetaRaw is store_meta read raw; present is false when the table is absent.
type storeMetaRaw struct {
	present bool
	rows    []storeMetaRow // ordered by key; empty when absent or empty
}

// storeID returns the store_id row's value and whether that row exists.
func (m storeMetaRaw) storeID() (string, bool) {
	for _, r := range m.rows {
		if r.key == "store_id" {
			return r.value, true
		}
	}
	return "", false
}

// openExistingRaw opens a raw connection to an existing DB file; it fails the
// test rather than let sql.Open create a missing file. The caller closes it.
func openExistingRaw(t *testing.T, who, path string) *sql.DB {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s: stat %q: %v", who, path, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("%s: raw open %q: %v", who, path, err)
	}
	return db
}

// readStoreMetaRaw reads every store_meta row raw, telling "table absent" from
// "table empty" (SR-20.6 one-id, keep-the-id, rollback and recipe cases).
func readStoreMetaRaw(t *testing.T, path string) storeMetaRaw {
	t.Helper()
	db := openExistingRaw(t, "readStoreMetaRaw", path)
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'`,
	).Scan(&n); err != nil {
		t.Fatalf("readStoreMetaRaw: probe sqlite_master: %v", err)
	}
	if n == 0 {
		return storeMetaRaw{}
	}
	out := storeMetaRaw{present: true}
	rows, err := db.Query(`SELECT key, CAST(value AS TEXT), typeof(value) FROM store_meta ORDER BY key`)
	if err != nil {
		t.Fatalf("readStoreMetaRaw: query store_meta: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r storeMetaRow
		if err := rows.Scan(&r.key, &r.value, &r.typ); err != nil {
			t.Fatalf("readStoreMetaRaw: scan store_meta: %v", err)
		}
		out.rows = append(out.rows, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("readStoreMetaRaw: iterate store_meta: %v", err)
	}
	return out
}

// execStoreIDRow runs one store_id-row statement on a closed store and fails
// unless it changed exactly one row.
func execStoreIDRow(t *testing.T, who, path, stmt string, args ...any) {
	t.Helper()
	db := openExistingRaw(t, who, path)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("%s: close raw db: %v", who, err)
		}
	}()
	res, err := db.Exec(stmt, args...)
	if err != nil {
		t.Fatalf("%s: %v", who, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("%s: changed %d store_id rows (err %v); want 1", who, n, err)
	}
}

// deleteStoreIDRow hand-edits a closed v5 store to remove its store_id row
// (SR-20.6 missing-row case: the open fails with ErrSchemaMismatch).
func deleteStoreIDRow(t *testing.T, path string) {
	t.Helper()
	execStoreIDRow(t, "deleteStoreIDRow", path, `DELETE FROM store_meta WHERE key = 'store_id'`)
}

// setStoreIDRaw hand-edits a closed v5 store's store_id to value, unvalidated;
// a []byte stores a BLOB (SR-20.6 malformed-value case).
func setStoreIDRaw(t *testing.T, path string, value any) {
	t.Helper()
	execStoreIDRow(t, "setStoreIDRaw", path, `UPDATE store_meta SET value = ? WHERE key = 'store_id'`, value)
}

// dropStoreMetaTable hand-edits a closed v5 store to drop store_meta, failing
// unless the table existed (SR-20.6 missing-row case, table-absent variant:
// the open fails with ErrSchemaMismatch).
func dropStoreMetaTable(t *testing.T, path string) {
	t.Helper()
	db := openExistingRaw(t, "dropStoreMetaTable", path)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("dropStoreMetaTable: close raw db: %v", err)
		}
	}()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'`,
	).Scan(&n); err != nil {
		t.Fatalf("dropStoreMetaTable: probe sqlite_master: %v", err)
	}
	if n != 1 {
		t.Fatalf("dropStoreMetaTable: store_meta tables = %d; want 1", n)
	}
	if _, err := db.Exec(`DROP TABLE store_meta`); err != nil {
		t.Fatalf("dropStoreMetaTable: drop store_meta: %v", err)
	}
}

// preAddStoreMeta creates store_meta on a v4 fixture before the hop, holding id,
// or no row when id is "" (SR-20.6 partial re-entry: the hop keeps an existing id).
func preAddStoreMeta(t *testing.T, path, id string) {
	t.Helper()
	db := openExistingRaw(t, "preAddStoreMeta", path)
	defer func() { _ = db.Close() }()
	// Same text as schemaDDL and migrateV4toV5 (the two-places rule).
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`); err != nil {
		t.Fatalf("preAddStoreMeta: create store_meta: %v", err)
	}
	if id == "" {
		return
	}
	if _, err := db.Exec(`INSERT INTO store_meta(key, value) VALUES ('store_id', ?)`, id); err != nil {
		t.Fatalf("preAddStoreMeta: insert store_id: %v", err)
	}
}

// breakV5StoreMetaStep pre-creates an empty store_meta whose value CHECK always
// fails, so the hop's guarded insert fails after all thirteen ADD COLUMNs (SR-20.6
// rollback case). The table survives the rollback, present and empty.
func breakV5StoreMetaStep(t *testing.T, path string) {
	t.Helper()
	db := openExistingRaw(t, "breakV5StoreMetaStep", path)
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL CHECK (0))`); err != nil {
		t.Fatalf("breakV5StoreMetaStep: create store_meta: %v", err)
	}
}

// v5ToV4RecipeStatements is the v5 → v4 recipe in docs/migration-guide.md
// ("#### v5 → v4"); it must match the guide statement for statement.
var v5ToV4RecipeStatements = []string{
	"ALTER TABLE spawns DROP COLUMN row_version",
	"ALTER TABLE spawns DROP COLUMN launch_started_at",
	"ALTER TABLE spawns DROP COLUMN life_number",
	"ALTER TABLE spawns DROP COLUMN no_pre_trust",
	"ALTER TABLE spawns DROP COLUMN launch_token",
	"ALTER TABLE spawns DROP COLUMN tmux_socket",
	"ALTER TABLE spawns DROP COLUMN tmux_server_pid",
	"ALTER TABLE spawns DROP COLUMN tmux_server_started",
	"ALTER TABLE spawns DROP COLUMN tmux_server_starttime",
	"ALTER TABLE spawns DROP COLUMN pane_id",
	"ALTER TABLE spawns DROP COLUMN pane_pid",
	"ALTER TABLE spawns DROP COLUMN pane_starttime",
	"ALTER TABLE session_history DROP COLUMN life_number",
	"DROP TABLE store_meta",
	"PRAGMA user_version = 4",
}

// applyV5ToV4Recipe runs v5ToV4RecipeStatements on a closed v5 store in one
// transaction (SR-20.6 recipe case); it must match the guide statement for statement.
func applyV5ToV4Recipe(t *testing.T, path string) {
	t.Helper()
	db := openExistingRaw(t, "applyV5ToV4Recipe", path)
	defer func() { _ = db.Close() }()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("applyV5ToV4Recipe: begin: %v", err)
	}
	for _, stmt := range v5ToV4RecipeStatements {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			t.Fatalf("applyV5ToV4Recipe: %s: %v", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("applyV5ToV4Recipe: commit: %v", err)
	}
}

// injectWriteFailure makes kind's writes to instanceID's rows fail on s, via
// writefailfix (the single trigger source); the test's cleanup removes it.
// Seed the row first: several kinds also match seeding writes.
func injectWriteFailure(t *testing.T, s *Store, kind writefailfix.Kind, instanceID string) {
	t.Helper()
	h, err := writefailfix.Install(s.db, kind, instanceID)
	if err != nil {
		t.Fatalf("injectWriteFailure(%v, %q): %v", kind, instanceID, err)
	}
	t.Cleanup(func() {
		if err := h.Remove(s.db); err != nil {
			t.Errorf("injectWriteFailure cleanup (%v, %q): %v", kind, instanceID, err)
		}
	})
}
