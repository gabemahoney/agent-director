package store

// Shared schema and migration fixtures for package store's white-box tests
// (they cannot import storefix, which imports store). A vN fixture has vN's
// real physical schema: testdata/schema_v1.sql migrated through the production
// steps (b.93m), in WAL mode so a refused open touches no bytes. The inline SQL
// of the v4 history fixture and the v4→v5 failure arrangements lives only here
// (SR-20.3).

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
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openRaw opens path through database/sql for the rest of the test.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw sql.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// withRaw runs fn on a raw connection to the existing DB at path and closes it,
// so the store's files are settled before any byte snapshot.
func withRaw(t *testing.T, path string, fn func(db *sql.DB)) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw sql.Open(%q): %v", path, err)
	}
	fn(db)
	if err := db.Close(); err != nil {
		t.Errorf("close raw %q: %v", path, err)
	}
}

// mustExec runs q on db, failing the test on error.
func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// makeVersionedDB builds a WAL state.db under dir at version v (1 to
// schemaVersion): the v1 fixture, then the real registered steps up to v.
func makeVersionedDB(t *testing.T, dir string, v int) string {
	t.Helper()
	v1SQL, err := os.ReadFile("testdata/schema_v1.sql")
	if err != nil {
		t.Fatalf("read v1 fixture: %v", err)
	}
	path := filepath.Join(dir, "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open %q: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	mustExec(t, db, "PRAGMA journal_mode=WAL")
	mustExec(t, db, string(v1SQL))
	for from := 1; from < v; from++ {
		step, ok := migrationStepFrom(from)
		if !ok {
			t.Fatalf("no migration step from %d", from)
		}
		if err := step.apply(db); err != nil {
			t.Fatalf("step %d→%d: %v", from, from+1, err)
		}
	}
	return path
}

// userVersion reads db's PRAGMA user_version.
func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

// readUserVersion reads the user_version of the DB at path.
func readUserVersion(t *testing.T, path string) (v int) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) { v = userVersion(t, db) })
	return v
}

// stampUserVersion sets the DB's user_version to v, a header write only.
func stampUserVersion(t *testing.T, path string, v int) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, fmt.Sprintf("PRAGMA user_version = %d", v)) })
}

// writeSentinel writes a valid {"from","to"} authorization sentinel beside the
// DB in dir, through the struct the production parser reads.
func writeSentinel(t *testing.T, dir string, from, to int) {
	t.Helper()
	data, err := json.Marshal(migrationAuthorization{From: from, To: to})
	if err != nil {
		t.Fatalf("marshal sentinel: %v", err)
	}
	writeSentinelRaw(t, dir, data)
}

// writeSentinelRaw writes data as the sentinel beside the DB in dir.
func writeSentinelRaw(t *testing.T, dir string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, sentinelFilename), data, 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
}

// assertSentinel fails unless the sentinel beside the DB in dir is present
// (want) or absent (!want).
func assertSentinel(t *testing.T, dir string, want bool) {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, sentinelFilename))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat sentinel: %v", err)
	}
	if got := err == nil; got != want {
		t.Errorf("sentinel present = %v; want %v", got, want)
	}
}

// snapshotDBBytes hashes the DB file and its -wal and -shm sidecars, each
// only when present, so a sidecar appearing is a change too.
func snapshotDBBytes(t *testing.T, dbPath string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("read %q: %v", p, err)
		}
		sum := sha256.Sum256(data)
		snap[filepath.Base(p)] = hex.EncodeToString(sum[:])
	}
	return snap
}

// assertDBBytesUnchanged fails unless the DB files hash as in before.
func assertDBBytesUnchanged(t *testing.T, dbPath string, before map[string]string) {
	t.Helper()
	if after := snapshotDBBytes(t, dbPath); !reflect.DeepEqual(after, before) {
		t.Errorf("DB files changed:\n before %v\n after  %v", before, after)
	}
}

// tableColumn is one PRAGMA table_info row.
type tableColumn struct {
	name, typ string
	notNull   int
	dflt      sql.NullString
	pk        int
}

// readTableShape returns a table's PRAGMA table_info rows in column order.
func readTableShape(t *testing.T, db *sql.DB, table string) []tableColumn {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var out []tableColumn
	for rows.Next() {
		var cid int
		var c tableColumn
		if err := rows.Scan(&cid, &c.name, &c.typ, &c.notNull, &c.dflt, &c.pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		out = append(out, c)
	}
	return out
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

// queryStrings returns the first column of q's rows.
func queryStrings(t *testing.T, db *sql.DB, q string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan %q: %v", q, err)
		}
		out = append(out, s)
	}
	return out
}

// schemaShape is db's every table (its table_info) and index (its columns),
// keyed "table <name>" and "index <name>".
func schemaShape(t *testing.T, db *sql.DB) map[string]any {
	t.Helper()
	shape := map[string]any{}
	for _, name := range queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type = 'table'`) {
		shape["table "+name] = readTableShape(t, db, name)
	}
	for _, name := range queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type = 'index'`) {
		shape["index "+name] = queryStrings(t, db, `SELECT name FROM pragma_index_info(?)`, name)
	}
	return shape
}

// dbDump is the schema shape of the DB at path plus every table's rows (each
// value as %#v, in rowid order), keyed "rows <table>".
func dbDump(t *testing.T, path string) map[string]any {
	t.Helper()
	var out map[string]any
	withRaw(t, path, func(db *sql.DB) {
		out = schemaShape(t, db)
		for _, table := range queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type = 'table'`) {
			rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
			if err != nil {
				t.Fatalf("dump %s: %v", table, err)
			}
			cols, _ := rows.Columns()
			var dump []string
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					t.Fatalf("scan %s: %v", table, err)
				}
				dump = append(dump, fmt.Sprintf("%#v", vals))
			}
			_ = rows.Close()
			out["rows "+table] = dump
		}
	})
	return out
}

// columnSpec is one column a migration step adds; dflt is table_info's
// dflt_value, "" for none (NULL).
type columnSpec struct {
	table, name, typ string
	notNull          bool
	dflt             string
}

// key returns "table.column".
func (c columnSpec) key() string { return c.table + "." + c.name }

// migratedValue is the quote() literal ADD COLUMN gives an existing row.
func (c columnSpec) migratedValue() string {
	if c.dflt == "" {
		return "NULL"
	}
	return c.dflt
}

// v3ColumnSpecs are the five spawns columns the v2→v3 step adds (SR-5).
var v3ColumnSpecs = []columnSpec{
	{"spawns", "pid", "INTEGER", false, ""},
	{"spawns", "proc_starttime", "TEXT", false, ""},
	{"spawns", "liveness_unverified_since", "TEXT", false, ""},
	{"spawns", "liveness_note", "TEXT", false, ""},
	{"spawns", "extra_env", "TEXT", true, "'{}'"},
}

// v5ColumnSpecs are the thirteen columns the v4→v5 step adds, in SR-5.1 order.
var v5ColumnSpecs = []columnSpec{
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

// v5ColumnSpecByKey looks up a v5 column by "table.column".
func v5ColumnSpecByKey(key string) (columnSpec, bool) {
	for _, c := range v5ColumnSpecs {
		if c.key() == key {
			return c, true
		}
	}
	return columnSpec{}, false
}

// assertColumnSpecs checks each spec's column is present exactly once with its
// type, NOT NULL flag and default.
func assertColumnSpecs(t *testing.T, db *sql.DB, specs []columnSpec) {
	t.Helper()
	for _, want := range specs {
		var found []tableColumn
		for _, c := range readTableShape(t, db, want.table) {
			if c.name == want.name {
				found = append(found, c)
			}
		}
		wantNN := 0
		if want.notNull {
			wantNN = 1
		}
		if len(found) != 1 || found[0].typ != want.typ || found[0].notNull != wantNN ||
			found[0].dflt != (sql.NullString{String: want.dflt, Valid: want.dflt != ""}) {
			t.Errorf("%s = %+v; want one {type %s notnull %d dflt %q}", want.key(), found, want.typ, wantNN, want.dflt)
		}
	}
}

// assertNoV5Columns fails if any v5 column is present.
func assertNoV5Columns(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, want := range v5ColumnSpecs {
			for _, c := range readTableShape(t, db, want.table) {
				if c.name == want.name {
					t.Errorf("%s present; want absent", want.key())
				}
			}
		}
	})
}

// v4HistoryFixture describes what makeV4HistoryFixture seeded. rows and
// history hold quote() literals read before migration, so NULL, 0 and ""
// stay distinct.
type v4HistoryFixture struct {
	dir, path string

	rotated      string // archived prior session + a different current session
	neverWritten string // ended row whose current session has no jsonl_path
	holdsCurrent string // history already holds the row's current session id
	interleavedA string // history interleaves in time with interleavedB
	interleavedB string
	pending      string // pending row, no session yet

	ids         []string
	spawnsCols  []string                       // the v4 spawns columns
	historyCols []string                       // the v4 session_history columns
	rows        map[string]map[string]string   // id → column → literal
	history     map[string][]map[string]string // id → entries (read order) → column → literal
}

// makeV4HistoryFixture builds a genuine v4 store under dir holding the SR-20.3
// scenario set, closed, and returns what it seeded.
func makeV4HistoryFixture(t *testing.T, dir string) v4HistoryFixture {
	t.Helper()
	f := v4HistoryFixture{dir: dir, path: makeVersionedDB(t, dir, 4),
		rotated: "v4-rotated", neverWritten: "v4-never-written", holdsCurrent: "v4-holds-current",
		interleavedA: "v4-interleaved-a", interleavedB: "v4-interleaved-b", pending: "v4-pending"}
	f.ids = []string{f.rotated, f.neverWritten, f.holdsCurrent, f.interleavedA, f.interleavedB, f.pending}
	// id, parent, state, cwd, tmux name, args, relay, jsonl, session, labels,
	// started, last seen, ended, pid, proc start, liveness since/note, extra env.
	spawnRows := [][]any{
		{f.rotated, nil, "waiting", "/work/rotated", "ad-rotated", `["--model","opus"]`, "off",
			"/t/rot-current.jsonl", "sess-rot-current", `{"team":"red"}`,
			"2026-01-01 10:00:00", "2026-01-01 10:05:00", nil, 4242, "98765", nil, nil, `{"K":"V"}`},
		{f.neverWritten, nil, "ended", "/work/never", "ad-never", "[]", "off", nil, "sess-nw-current", "{}",
			"2026-01-02 10:00:00", "2026-01-02 11:00:00", "2026-01-02 11:00:00", nil, nil, nil, nil, "{}"},
		{f.holdsCurrent, nil, "working", "/work/holds", "ad-holds", "[]", "off", "/t/hc-current.jsonl", "sess-hc-current", "{}",
			"2026-01-03 10:00:00", "2026-01-03 10:01:00", nil, 5151, "11111", nil, nil, "{}"},
		{f.interleavedA, nil, "waiting", "/work/a", "ad-a", "[]", "off", "/t/a-current.jsonl", "sess-a-current", "{}",
			"2026-01-04 09:00:00", "2026-01-04 09:30:00", nil, nil, nil, nil, nil, "{}"},
		{f.interleavedB, f.interleavedA, "ask_user", "/work/b", "ad-b", "[]", "off", "/t/b-current.jsonl", "sess-b-current",
			`{"role":"child"}`, "2026-01-04 09:00:30", "2026-01-04 09:31:00", nil, 6262, "22222", "2026-01-04 09:31:00", "pid gone", "{}"},
		{f.pending, nil, "pending", "/work/pending", "ad-pending", "[]", "off", nil, nil, "{}",
			"2026-01-05 12:00:00", "2026-01-05 12:00:00", nil, nil, nil, nil, nil, "{}"},
	}
	// instance id, session id, jsonl path, recorded_at.
	historyRows := [][]any{
		{f.rotated, "sess-rot-prior", "/t/rot-prior.jsonl", "2026-01-01 10:02:00"},
		{f.neverWritten, "sess-nw-prior", "/t/nw-prior.jsonl", "2026-01-02 10:30:00"},
		{f.holdsCurrent, "sess-hc-current", "/t/hc-current.jsonl", "2026-01-03 10:00:30"},
		{f.interleavedA, "sess-a-1", "/t/a-1.jsonl", "2026-01-04 09:10:00"},
		{f.interleavedB, "sess-b-1", "/t/b-1.jsonl", "2026-01-04 09:11:00"},
		{f.interleavedA, "sess-a-2", nil, "2026-01-04 09:12:00"},
		{f.interleavedB, "sess-b-2", "/t/b-2.jsonl", "2026-01-04 09:13:00"},
	}
	withRaw(t, f.path, func(db *sql.DB) {
		for _, r := range spawnRows {
			mustExec(t, db, `INSERT INTO spawns (claude_instance_id, parent_id, state, cwd, tmux_session_name,
				claude_args, relay_mode, jsonl_path, claude_session_id, labels, started_at, last_seen_at, ended_at,
				pid, proc_starttime, liveness_unverified_since, liveness_note, extra_env)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r...)
		}
		for _, h := range historyRows {
			mustExec(t, db, `INSERT INTO session_history (claude_instance_id, claude_session_id, jsonl_path, recorded_at)
				VALUES (?,?,?,?)`, h...)
		}
		f.spawnsCols, f.historyCols = tableColumnNames(t, db, "spawns"), tableColumnNames(t, db, "session_history")
	})
	f.rows = map[string]map[string]string{}
	f.history = map[string][]map[string]string{}
	for _, id := range f.ids {
		f.rows[id] = readRawSpawn(t, f.path, id, f.spawnsCols)
		f.history[id] = readRawHistory(t, f.path, id, f.historyCols)
	}
	return f
}

// migrateV4Fixture opens f with a {4→schemaVersion} sentinel and closes it.
func migrateV4Fixture(t *testing.T, f v4HistoryFixture) {
	t.Helper()
	writeSentinel(t, f.dir, 4, schemaVersion)
	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("Open(authorised v4): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// unquote turns a quote() text literal back into its value ("NULL" → "").
func unquote(lit string) string {
	if lit == "NULL" {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(lit, "'"), "'"), "''", "'")
}

// readQuotedRows returns quote() literals of cols (from table_info, never
// input) for table's rows of instance id, in orderBy order.
func readQuotedRows(t *testing.T, path, table, id, orderBy string, cols []string) []map[string]string {
	t.Helper()
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = "quote(" + c + ")"
	}
	q := "SELECT " + strings.Join(sel, ", ") + " FROM " + table + " WHERE claude_instance_id = ?"
	if orderBy != "" {
		q += " ORDER BY " + orderBy
	}
	var out []map[string]string
	withRaw(t, path, func(db *sql.DB) {
		rows, err := db.Query(q, id)
		if err != nil {
			t.Fatalf("query %s for %q: %v", table, id, err)
		}
		defer rows.Close()
		for rows.Next() {
			vals := make([]string, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s for %q: %v", table, id, err)
			}
			row := map[string]string{}
			for i, c := range cols {
				row[c] = vals[i]
			}
			out = append(out, row)
		}
	})
	return out
}

// readRawSpawn returns quote() literals of cols for id's spawns row.
func readRawSpawn(t *testing.T, path, id string, cols []string) map[string]string {
	t.Helper()
	rows := readQuotedRows(t, path, "spawns", id, "", cols)
	if len(rows) != 1 {
		t.Fatalf("%d spawns rows for %q; want 1", len(rows), id)
	}
	return rows[0]
}

// readRawHistory returns quote() literals of cols for id's history entries, in
// ListSessionHistory's order (newest recorded first).
func readRawHistory(t *testing.T, path, id string, cols []string) []map[string]string {
	t.Helper()
	return readQuotedRows(t, path, "session_history", id, "recorded_at DESC, history_id DESC", cols)
}

// assertV5Defaults checks id's v5 values are the ordinary defaults: every
// spawns column at its DDL default (or NULL) and historyLen entries at life 0.
func assertV5Defaults(t *testing.T, path, id string, historyLen int) {
	t.Helper()
	var cols []string
	for _, c := range v5ColumnSpecs {
		if c.table == "spawns" {
			cols = append(cols, c.name)
		}
	}
	got := readRawSpawn(t, path, id, cols)
	for _, spec := range v5ColumnSpecs[:len(cols)] {
		if v := got[spec.name]; v != spec.migratedValue() {
			t.Errorf("%s: %s = %s; want %s", id, spec.key(), v, spec.migratedValue())
		}
	}
	h := readRawHistory(t, path, id, []string{"life_number"})
	if len(h) != historyLen {
		t.Errorf("%s: %d history entries; want %d", id, len(h), historyLen)
	}
	for i, e := range h {
		if e["life_number"] != "0" {
			t.Errorf("%s: history entry %d life_number = %s; want 0", id, i, e["life_number"])
		}
	}
}

// preAddV5Columns adds the named v5 columns ("table.column") with their SR-5.1
// definitions: a v4→v5 hop that stopped part-way.
func preAddV5Columns(t *testing.T, path string, keys ...string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, k := range keys {
			spec, ok := v5ColumnSpecByKey(k)
			if !ok {
				t.Fatalf("unknown v5 column %q", k)
			}
			ddl := "ALTER TABLE " + spec.table + " ADD COLUMN " + spec.name + " " + spec.typ
			if spec.notNull {
				ddl += " NOT NULL"
			}
			if spec.dflt != "" {
				ddl += " DEFAULT " + spec.dflt
			}
			mustExec(t, db, ddl)
		}
	})
}

// breakV5SessionHistoryHop makes session_history a view over the renamed v4
// table, so the hop fails at session_history.life_number after every spawns column.
func breakV5SessionHistoryHop(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, "ALTER TABLE session_history RENAME TO session_history_v4")
		mustExec(t, db, "CREATE VIEW session_history AS SELECT * FROM session_history_v4")
	})
}

// breakV5StoreMetaStep pre-creates an empty store_meta whose CHECK always
// fails, so the hop's store_id insert fails after all thirteen columns.
func breakV5StoreMetaStep(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `CREATE TABLE store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL CHECK (0))`)
	})
}

// preAddStoreMeta creates store_meta on a v4 fixture before the hop, holding
// id, or no row when id is "" (partial re-entry).
func preAddStoreMeta(t *testing.T, path, id string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		// Same text as schemaDDL and migrateV4toV5 (the two-places rule).
		mustExec(t, db, `CREATE TABLE IF NOT EXISTS store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);`)
		if id != "" {
			mustExec(t, db, `INSERT INTO store_meta(key, value) VALUES ('store_id', ?)`, id)
		}
	})
}

// storeMetaShape is store_meta's expected table_info (SR-5.1).
var storeMetaShape = []tableColumn{
	{name: "key", typ: "TEXT", notNull: 0, pk: 1},
	{name: "value", typ: "TEXT", notNull: 1, pk: 0},
}

// storeMetaRow is one raw store_meta row; typ is typeof(value).
type storeMetaRow struct{ key, value, typ string }

// readStoreMeta reads store_meta raw; present is false when the table is absent.
func readStoreMeta(t *testing.T, path string) (rows []storeMetaRow, present bool) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		if len(queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'store_meta'`)) == 0 {
			return
		}
		present = true
		r, err := db.Query(`SELECT key, CAST(value AS TEXT), typeof(value) FROM store_meta ORDER BY key`)
		if err != nil {
			t.Fatalf("query store_meta: %v", err)
		}
		defer r.Close()
		for r.Next() {
			var m storeMetaRow
			if err := r.Scan(&m.key, &m.value, &m.typ); err != nil {
				t.Fatalf("scan store_meta: %v", err)
			}
			rows = append(rows, m)
		}
	})
	return rows, present
}

// v5ToV4RecipeStatements is the v5 → v4 recipe in docs/migration-guide.md
// ("#### v5 → v4"), statement for statement.
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

// applyV5ToV4Recipe runs the recipe on a closed v5 store in one transaction.
func applyV5ToV4Recipe(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		for _, stmt := range v5ToV4RecipeStatements {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	})
}

// seedV5DowngradeRows seeds a closed v5 store with a row reused twice (life 2,
// history in lives 0 to 2, a launch identity) and a row that opted out of
// pre-trust, returning their ids (SR-5.4).
func seedV5DowngradeRows(t *testing.T, path string) (reused, optedOut string) {
	t.Helper()
	reused, optedOut = "v5-reused", "v5-opted-out"
	withRaw(t, path, func(db *sql.DB) {
		// id, state, cwd, tmux name, relay, jsonl, session, started, ended, row
		// version, launch start, life, no_pre_trust, token, socket, server pid /
		// started / starttime, pane id / pid / starttime.
		for _, s := range [][]any{
			{reused, "ended", "/work/reused", "ad-reused", "off", "/t/r-2.jsonl", "sess-r-2", "2026-02-01 10:00:00",
				"2026-02-03 10:00:00", 7, nil, 2, 0, "0123456789abcdef", "/tmp/ad-sock", 4100, 1767225600, "123", "%3", 4200, "456"},
			{optedOut, "waiting", "/work/opted-out", "ad-opted-out", "on", "/t/o.jsonl", "sess-o", "2026-02-04 10:00:00",
				nil, 3, nil, 0, 1, "fedcba9876543210", "/tmp/ad-sock", 4100, 1767225600, "123", "%4", 4300, "789"},
		} {
			mustExec(t, db, `INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode,
				jsonl_path, claude_session_id, started_at, ended_at, row_version, launch_started_at, life_number,
				no_pre_trust, launch_token, tmux_socket, tmux_server_pid, tmux_server_started, tmux_server_starttime,
				pane_id, pane_pid, pane_starttime) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, s...)
		}
		// instance id, session id, jsonl path, recorded_at, life.
		for _, h := range [][]any{
			{reused, "sess-r-0a", "/t/r-0a.jsonl", "2026-02-01 11:00:00", 0},
			{reused, "sess-r-0b", nil, "2026-02-01 12:00:00", 0},
			{reused, "sess-r-1", "/t/r-1.jsonl", "2026-02-02 11:00:00", 1},
			{reused, "sess-r-2-prior", "/t/r-2p.jsonl", "2026-02-03 09:00:00", 2},
			{optedOut, "sess-o-prior", "/t/o-prior.jsonl", "2026-02-04 11:00:00", 0},
		} {
			mustExec(t, db, `INSERT INTO session_history (claude_instance_id, claude_session_id, jsonl_path,
				recorded_at, life_number) VALUES (?,?,?,?,?)`, h...)
		}
	})
	return reused, optedOut
}
