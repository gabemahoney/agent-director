package store

// Shared schema and migration fixtures for package store's white-box tests
// (they cannot import storefix, which imports store). A vN fixture has vN's
// real physical schema: testdata/schema_v1.sql migrated through the production
// steps (b.93m), in WAL mode so a refused open touches no bytes. The inline SQL
// of the v4 history fixture, the v5 fixture and the v4→v5 and v5→v6 failure
// arrangements lives only here (SR-20.3).

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
	"slices"
	"sort"
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

// schemaShape is db's every table (its table_info), index (its columns) and
// foreign keys (each one's column, target table and column and delete action),
// keyed "table <name>", "index <name>" and "fk <table>".
func schemaShape(t *testing.T, db *sql.DB) map[string]any {
	t.Helper()
	shape := map[string]any{}
	for _, name := range queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type = 'table'`) {
		shape["table "+name] = readTableShape(t, db, name)
		shape["fk "+name] = queryStrings(t, db, `SELECT "from" || ' -> ' || "table" || '(' || COALESCE("to", '') || ') on delete '
			|| on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, name)
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

// addColumnDDL is the ALTER TABLE … ADD COLUMN statement that adds c with its
// type, NOT NULL flag and default.
func (c columnSpec) addColumnDDL() string {
	ddl := "ALTER TABLE " + c.table + " ADD COLUMN " + c.name + " " + c.typ
	if c.notNull {
		ddl += " NOT NULL"
	}
	if c.dflt != "" {
		ddl += " DEFAULT " + c.dflt
	}
	return ddl
}

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

// v6ColumnSpecs are the three launch-owner spawns columns the v5→v6 step adds,
// in schema order (b.kdf).
var v6ColumnSpecs = []columnSpec{
	{"spawns", "launch_owner_pid", "INTEGER", false, ""},
	{"spawns", "launch_owner_starttime", "TEXT", false, ""},
	{"spawns", "launch_owner_pidns", "TEXT", false, ""},
}

// v6ColumnNames are v6ColumnSpecs' column names, in order.
func v6ColumnNames() []string {
	var names []string
	for _, c := range v6ColumnSpecs {
		names = append(names, c.name)
	}
	return names
}

// assertV6Defaults checks id's v6 columns hold their migrated default: no
// owner (NULL).
func assertV6Defaults(t *testing.T, path, id string) {
	t.Helper()
	got := readRawSpawn(t, path, id, v6ColumnNames())
	for _, spec := range v6ColumnSpecs {
		if v := got[spec.name]; v != spec.migratedValue() {
			t.Errorf("%s: %s = %s; want %s", id, spec.key(), v, spec.migratedValue())
		}
	}
}

// assertNoV6Columns fails if any v6 column is present.
func assertNoV6Columns(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, name := range tableColumnNames(t, db, "spawns") {
			for _, want := range v6ColumnSpecs {
				if strings.EqualFold(name, want.name) {
					t.Errorf("%s present; want absent", want.key())
				}
			}
		}
	})
}

// preAddV6Columns adds the named v6 spawns columns with their definitions: a
// v5→v6 hop that stopped part-way.
func preAddV6Columns(t *testing.T, path string, names ...string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, n := range names {
			for _, spec := range v6ColumnSpecs {
				if spec.name == n {
					mustExec(t, db, spec.addColumnDDL())
				}
			}
		}
	})
}

// breakV6PIDNSColumn adds LAUNCH_OWNER_PIDNS to a v5 store: SQLite column
// names ignore case, but the hop's probe compares them exactly, so the hop
// adds launch_owner_pid and launch_owner_starttime and then fails at
// launch_owner_pidns, its last column, as a duplicate column.
func breakV6PIDNSColumn(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "ALTER TABLE spawns ADD COLUMN LAUNCH_OWNER_PIDNS TEXT") })
}

// v5Fixture describes what makeV5Fixture seeded; rows holds quote() literals
// of every v5 spawns column read before the hop.
type v5Fixture struct {
	dir, path string

	pending, waiting, ended string
	ids                     []string
	spawnsCols              []string
	rows                    map[string]map[string]string
}

// makeV5Fixture builds a genuine v5 store under dir holding a pending row in
// the middle of a launch (launch start, token, socket, pane identity, no
// session), a waiting row with every identity, and an ended row, closed.
func makeV5Fixture(t *testing.T, dir string) v5Fixture {
	t.Helper()
	f := v5Fixture{dir: dir, path: makeVersionedDB(t, dir, 5), pending: "v5-pending", waiting: "v5-waiting", ended: "v5-ended"}
	f.ids = []string{f.pending, f.waiting, f.ended}
	// id, state, cwd, tmux name, relay, session, jsonl, started, ended, pid, proc
	// start, row version, launch start, life, no_pre_trust, token, socket, server
	// pid / started / starttime, pane id / pid / starttime, liveness since / note.
	rows := [][]any{
		{f.pending, "pending", "/work/pending", "ad-pending", "off", nil, nil, "2026-03-01 10:00:00", nil, nil, nil,
			1, int64(1772359200123), 0, 0, "0123456789abcdef", "/tmp/ad-sock", 4100, 1767225600, "123", "%1", 4201, "456",
			nil, nil},
		{f.waiting, "waiting", "/work/waiting", "ad-waiting", "on", "sess-w", "/t/w.jsonl", "2026-03-02 10:00:00", nil, 4202, "789",
			5, nil, 2, 1, "fedcba9876543210", "/tmp/ad-sock", 4100, 1767225600, "123", "%2", 4202, "789",
			"2026-03-02 11:00:00", "probe_eacces"},
		{f.ended, "ended", "/work/ended", "ad-ended", "off", "sess-e", "/t/e.jsonl", "2026-03-03 10:00:00", "2026-03-03 12:00:00",
			nil, nil, 9, nil, 1, 0, "00112233aabbccdd", "/tmp/ad-sock", nil, nil, nil, nil, nil, nil, nil, nil},
	}
	withRaw(t, f.path, func(db *sql.DB) {
		for _, r := range rows {
			mustExec(t, db, `INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode,
				claude_session_id, jsonl_path, started_at, ended_at, pid, proc_starttime, row_version, launch_started_at,
				life_number, no_pre_trust, launch_token, tmux_socket, tmux_server_pid, tmux_server_started,
				tmux_server_starttime, pane_id, pane_pid, pane_starttime, liveness_unverified_since, liveness_note)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r...)
		}
		f.spawnsCols = tableColumnNames(t, db, "spawns")
	})
	f.rows = map[string]map[string]string{}
	for _, id := range f.ids {
		f.rows[id] = readRawSpawn(t, f.path, id, f.spawnsCols)
	}
	return f
}

// v6ToV5RecipeStatements reverses migrateV5toV6: the three v6 columns dropped
// in the order the hop adds them, then the version stamped back to 5. It must
// match docs/migration-guide.md's "v6 → v5" recipe statement for statement.
var v6ToV5RecipeStatements = []string{
	"ALTER TABLE spawns DROP COLUMN launch_owner_pid",
	"ALTER TABLE spawns DROP COLUMN launch_owner_starttime",
	"ALTER TABLE spawns DROP COLUMN launch_owner_pidns",
	"PRAGMA user_version = 5",
}

// v7ColumnSpecs are the columns the v6→v7 step adds (b.146 steps 2, 2b and
// 2c), in schema order: nineteen on permission_requests, after created_at,
// and spawns.idle_since, after launch_owner_pidns.
var v7ColumnSpecs = []columnSpec{
	{"permission_requests", "hook_pid", "INTEGER", false, ""},
	{"permission_requests", "hook_starttime", "TEXT", false, ""},
	{"permission_requests", "hook_pidns", "TEXT", false, ""},
	{"permission_requests", "tool_use_id", "TEXT", false, ""},
	{"permission_requests", "agent_id", "TEXT", false, ""},
	{"permission_requests", "delivered_at", "INTEGER", false, ""},
	{"permission_requests", "settled_at", "INTEGER", false, ""},
	{"permission_requests", "hook_gone_at", "INTEGER", false, ""},
	{"permission_requests", "attempted_decision", "TEXT", false, ""},
	{"permission_requests", "attempted_at", "INTEGER", false, ""},
	{"permission_requests", "pane_answer", "TEXT", true, "'none'"},
	{"permission_requests", "pane_as", "TEXT", false, ""},
	{"permission_requests", "pane_sender_pid", "INTEGER", false, ""},
	{"permission_requests", "pane_sender_starttime", "TEXT", false, ""},
	{"permission_requests", "pane_sender_pidns", "TEXT", false, ""},
	{"permission_requests", "pane_intent_at", "INTEGER", false, ""},
	{"permission_requests", "closed_at", "INTEGER", false, ""},
	{"permission_requests", "proven_gone_at", "INTEGER", false, ""},
	{"permission_requests", "proven_gone_how", "TEXT", false, ""},
	{"spawns", "idle_since", "TEXT", false, ""},
}

// v7ColumnNames are table's v7 column names, in schema order.
func v7ColumnNames(table string) []string {
	var names []string
	for _, c := range v7ColumnSpecs {
		if c.table == table {
			names = append(names, c.name)
		}
	}
	return names
}

// assertNoV7Columns fails if any v7 column is present.
func assertNoV7Columns(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, want := range v7ColumnSpecs {
			for _, name := range tableColumnNames(t, db, want.table) {
				if strings.EqualFold(name, want.name) {
					t.Errorf("%s present; want absent", want.key())
				}
			}
		}
	})
}

// preAddV7Columns adds the named v7 columns ("table.column") with their
// definitions: a v6→v7 hop that stopped part-way.
func preAddV7Columns(t *testing.T, path string, keys ...string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, k := range keys {
			found := false
			for _, spec := range v7ColumnSpecs {
				if spec.key() == k {
					mustExec(t, db, spec.addColumnDDL())
					found = true
				}
			}
			if !found {
				t.Fatalf("unknown v7 column %q", k)
			}
		}
	})
}

// breakV7IdleSinceColumn adds IDLE_SINCE to a v6 store: the hop's probe
// compares names exactly, so it adds every permission_requests column and then
// fails at spawns.idle_since, its last column, as a duplicate column.
func breakV7IdleSinceColumn(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "ALTER TABLE spawns ADD COLUMN IDLE_SINCE TEXT") })
}

// breakV7Backfill makes every update of a permission request on a v6 store
// fail: the hop adds every v7 column, then fails at its backfill, which
// proves the finished rows' requests gone.
func breakV7Backfill(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `CREATE TRIGGER ad_test_fail_backfill BEFORE UPDATE ON permission_requests
			BEGIN SELECT RAISE(ABORT, 'test: the backfill fails'); END`)
	})
}

// v6Fixture describes what makeV6Fixture seeded: a relay-on row in
// check_permission with an open request and a decided one, recorded before
// schema v7 (no relay hook identity, no settle instant), and the finished rows
// (ended, missing), each with the same two requests. requests holds quote()
// literals of every v6 permission_requests column of id's requests, by token.
type v6Fixture struct {
	dir, path        string
	id               string
	finished         []string
	open, decided    string
	requestCols      []string
	requests         map[string]map[string]string
	spawnCols        []string
	spawn            map[string]string
	openCreatedAtSQL string
}

// makeV6Fixture builds a genuine v6 store under dir holding f.id in
// check_permission with request f.open undecided (created_at
// f.openCreatedAtSQL, long past any relay window) and request f.decided
// decided allow, closed; f.finished's rows, ended and missing, hold the same
// two requests.
func makeV6Fixture(t *testing.T, dir string) v6Fixture {
	t.Helper()
	f := v6Fixture{dir: dir, path: makeVersionedDB(t, dir, 6), id: "v6-relay", finished: []string{"v6-ended", "v6-missing"},
		open: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", decided: "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb",
		openCreatedAtSQL: "2026-04-01 10:00:00"}
	withRaw(t, f.path, func(db *sql.DB) {
		mustExec(t, db, `INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode, row_version)
			VALUES (?, 'check_permission', '/work/relay', 'ad-relay', 'on', 4)`, f.id)
		for _, id := range f.finished {
			mustExec(t, db, `INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode, row_version)
				VALUES (?, ?, '/work/relay', ?, 'on', 4)`, id, strings.TrimPrefix(id, "v6-"), "ad-"+id)
		}
		for _, id := range append([]string{f.id}, f.finished...) {
			mustExec(t, db, `INSERT INTO permission_requests (claude_instance_id, request_token, tool_name, tool_input, created_at)
				VALUES (?, ?, 'Bash', '{"command":"ls"}', ?)`, id, f.open, f.openCreatedAtSQL)
			mustExec(t, db, `INSERT INTO permission_requests (claude_instance_id, request_token, tool_name, tool_input,
				decision, decided_at, created_at) VALUES (?, ?, 'Read', '{}', 'allow', '2026-04-01 09:00:05', '2026-04-01 09:00:00')`,
				id, f.decided)
		}
		f.requestCols = tableColumnNames(t, db, "permission_requests")
		f.spawnCols = tableColumnNames(t, db, "spawns")
	})
	f.requests = map[string]map[string]string{}
	for _, r := range readQuotedRows(t, f.path, "permission_requests", f.id, "request_id", f.requestCols) {
		f.requests[unquote(r["request_token"])] = r
	}
	f.spawn = readRawSpawn(t, f.path, f.id, f.spawnCols)
	return f
}

// v7ToV6RecipeStatements reverses migrateV6toV7: the twenty v7 columns
// dropped in the order the hop adds them, then the version stamped back to 6.
// It must match docs/migration-guide.md's "v7 → v6" recipe statement for
// statement.
var v7ToV6RecipeStatements = []string{
	"ALTER TABLE permission_requests DROP COLUMN hook_pid",
	"ALTER TABLE permission_requests DROP COLUMN hook_starttime",
	"ALTER TABLE permission_requests DROP COLUMN hook_pidns",
	"ALTER TABLE permission_requests DROP COLUMN tool_use_id",
	"ALTER TABLE permission_requests DROP COLUMN agent_id",
	"ALTER TABLE permission_requests DROP COLUMN delivered_at",
	"ALTER TABLE permission_requests DROP COLUMN settled_at",
	"ALTER TABLE permission_requests DROP COLUMN hook_gone_at",
	"ALTER TABLE permission_requests DROP COLUMN attempted_decision",
	"ALTER TABLE permission_requests DROP COLUMN attempted_at",
	"ALTER TABLE permission_requests DROP COLUMN pane_answer",
	"ALTER TABLE permission_requests DROP COLUMN pane_as",
	"ALTER TABLE permission_requests DROP COLUMN pane_sender_pid",
	"ALTER TABLE permission_requests DROP COLUMN pane_sender_starttime",
	"ALTER TABLE permission_requests DROP COLUMN pane_sender_pidns",
	"ALTER TABLE permission_requests DROP COLUMN pane_intent_at",
	"ALTER TABLE permission_requests DROP COLUMN closed_at",
	"ALTER TABLE permission_requests DROP COLUMN proven_gone_at",
	"ALTER TABLE permission_requests DROP COLUMN proven_gone_how",
	"ALTER TABLE spawns DROP COLUMN idle_since",
	"PRAGMA user_version = 6",
}

// v8ColumnSpecs are the two spawns columns the v7→v8 step adds (b.3ie), in
// schema order, after idle_since.
var v8ColumnSpecs = []columnSpec{
	{"spawns", "settled_state", "TEXT", true, "''"},
	{"spawns", "state_changed_ms", "INTEGER", true, "0"},
}

// v8ColumnNames are v8ColumnSpecs' column names, in order.
func v8ColumnNames() []string {
	var names []string
	for _, c := range v8ColumnSpecs {
		names = append(names, c.name)
	}
	return names
}

// v8ToV7RecipeStatements reverses migrateV7toV8: the two v8 columns dropped
// in the order the hop adds them, then the version stamped back to 7. It must
// match docs/migration-guide.md's "v8 → v7" recipe statement for statement.
var v8ToV7RecipeStatements = []string{
	"ALTER TABLE spawns DROP COLUMN settled_state",
	"ALTER TABLE spawns DROP COLUMN state_changed_ms",
	"PRAGMA user_version = 7",
}

// assertV8Defaults checks id's v8 columns hold what the hop gives an existing
// row: settled_state its own state and state_changed_ms 0, so it reads settled
// at once for every settle time.
func assertV8Defaults(t *testing.T, path, id string) {
	t.Helper()
	got := readRawSpawn(t, path, id, []string{"state", "settled_state", "state_changed_ms"})
	if got["settled_state"] != got["state"] || got["state_changed_ms"] != "0" {
		t.Errorf("%s: state %s, settled_state %s, state_changed_ms %s; want settled_state the state and 0",
			id, got["state"], got["settled_state"], got["state_changed_ms"])
	}
}

// assertNoV8Columns fails if any v8 column is present, in any letter case.
func assertNoV8Columns(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, name := range tableColumnNames(t, db, "spawns") {
			for _, want := range v8ColumnSpecs {
				if strings.EqualFold(name, want.name) {
					t.Errorf("%s present; want absent", want.key())
				}
			}
		}
	})
}

// preAddV8Columns adds the named v8 spawns columns with their definitions: a
// v7→v8 hop that stopped part-way (before its backfill).
func preAddV8Columns(t *testing.T, path string, names ...string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, n := range names {
			for _, spec := range v8ColumnSpecs {
				if spec.name == n {
					mustExec(t, db, spec.addColumnDDL())
				}
			}
		}
	})
}

// breakV8StateChangedColumn adds STATE_CHANGED_MS to a v7 store: the hop's
// probe compares names exactly, so it adds settled_state and then fails at
// state_changed_ms, its last column, as a duplicate column.
func breakV8StateChangedColumn(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, "ALTER TABLE spawns ADD COLUMN STATE_CHANGED_MS INTEGER NOT NULL DEFAULT 0")
	})
}

// breakV8Backfill makes every update of a spawns row on a v7 store fail: the
// hop adds both v8 columns, then fails at its backfill of the settled state.
func breakV8Backfill(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `CREATE TRIGGER ad_test_fail_settle_backfill BEFORE UPDATE ON spawns
			BEGIN SELECT RAISE(ABORT, 'test: the settle backfill fails'); END`)
	})
}

// v7Fixture describes what makeV7Fixture seeded: one row per state, in
// v7States order (ids "v7-<state>"), and quote() literals of every v7 spawns
// column of each and of the check_permission row's open request, read before
// the hop.
type v7Fixture struct {
	dir, path   string
	spawnCols   []string
	rows        map[string]map[string]string
	requestCols []string
	requests    []map[string]string
}

// v7States are the states makeV7Fixture seeds a row in.
var v7States = []string{StatePending, StateWaiting, StateWorking, StateAskUser, StateCheckPermission, StateEnded, StateMissing}

// v7ID is makeV7Fixture's row in state.
func v7ID(state string) string { return "v7-" + state }

// makeV7Fixture builds a genuine v7 store under dir holding a row in every
// state, relay on: the pending row in the middle of a launch (launch start,
// token, socket), the check_permission row with an open relay request, every
// row at row_version 4 with a liveness note.
func makeV7Fixture(t *testing.T, dir string) v7Fixture {
	t.Helper()
	f := v7Fixture{dir: dir, path: makeVersionedDB(t, dir, 7), rows: map[string]map[string]string{}}
	withRaw(t, f.path, func(db *sql.DB) {
		for _, st := range v7States {
			var launch any
			if st == StatePending {
				launch = int64(1_790_000_000_123)
			}
			mustExec(t, db, `INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode, row_version,
				launch_started_at, launch_token, tmux_socket, liveness_note) VALUES (?, ?, '/work/v7', ?, 'on', 4, ?,
				'0123456789abcdef', '/tmp/ad-v7/sock', 'probe_eacces')`, v7ID(st), st, "ad-"+v7ID(st), launch)
		}
		mustExec(t, db, `INSERT INTO permission_requests (claude_instance_id, request_token, tool_name, tool_input,
			hook_pid, hook_starttime, tool_use_id, settled_at) VALUES (?, 'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa', 'Bash', '{}',
			4321, '777', 'toolu_v7', 1790000000000)`, v7ID(StateCheckPermission))
		f.spawnCols = tableColumnNames(t, db, "spawns")
		f.requestCols = tableColumnNames(t, db, "permission_requests")
	})
	for _, st := range v7States {
		f.rows[v7ID(st)] = readRawSpawn(t, f.path, v7ID(st), f.spawnCols)
	}
	f.requests = readQuotedRows(t, f.path, "permission_requests", v7ID(StateCheckPermission), "request_id", f.requestCols)
	return f
}

// applyRecipe runs stmts on the closed store at path in one transaction.
func applyRecipe(t *testing.T, path string, stmts []string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		for _, stmt := range stmts {
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

// idColumn is the instance id column of table in the store db holds:
// claude_instance_id before the v9 hop, instance_id after (found from the
// table's columns, so a part-migrated store answers too).
func idColumn(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var found []string
	for _, c := range tableColumnNames(t, db, table) {
		if c == "claude_instance_id" || c == "instance_id" {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s has instance id columns %v; want exactly one", table, found)
	}
	return found[0]
}

// readQuotedRows returns quote() literals of cols (from table_info, never
// input; named as the store's version names them) for table's rows of
// instance id, in orderBy order. The row is picked by the table's id column.
func readQuotedRows(t *testing.T, path, table, id, orderBy string, cols []string) []map[string]string {
	t.Helper()
	sel := make([]string, len(cols))
	for i, c := range cols {
		sel[i] = "quote(" + c + ")"
	}
	var out []map[string]string
	withRaw(t, path, func(db *sql.DB) {
		q := "SELECT " + strings.Join(sel, ", ") + " FROM " + table + " WHERE " + idColumn(t, db, table) + " = ?"
		if orderBy != "" {
			q += " ORDER BY " + orderBy
		}
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
			mustExec(t, db, spec.addColumnDDL())
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
	applyRecipe(t, path, v5ToV4RecipeStatements)
}

// v9RenameSpecs are the six column renames of the v8→v9 hop (SR-3.1), written
// out here and not read from the hop: its table and the old and new names.
var v9RenameSpecs = []struct{ table, oldName, newName string }{
	{"spawns", "claude_instance_id", "instance_id"},
	{"spawns", "claude_args", "agent_args"},
	{"spawns", "claude_session_id", "session_id"},
	{"permission_requests", "claude_instance_id", "instance_id"},
	{"session_history", "claude_instance_id", "instance_id"},
	{"session_history", "claude_session_id", "session_id"},
}

// v9Name is the name col has after the v8→v9 hop: its new name when it was
// renamed, col itself otherwise.
func v9Name(col string) string {
	for _, r := range v9RenameSpecs {
		if r.oldName == col {
			return r.newName
		}
	}
	return col
}

// v9Cols is cols under their v9 names, to read a migrated store with a column
// list taken before the hop.
func v9Cols(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = v9Name(c)
	}
	return out
}

// v9Row is row, read before the hop, with its keys under their v9 names, to
// compare with the same row read after it.
func v9Row(row map[string]string) map[string]string {
	if row == nil {
		return nil
	}
	out := make(map[string]string, len(row))
	for c, v := range row {
		out[v9Name(c)] = v
	}
	return out
}

// v9Rows is v9Row of each row (nil stays nil).
func v9Rows(rows []map[string]string) []map[string]string {
	if rows == nil {
		return nil
	}
	out := make([]map[string]string, len(rows))
	for i, r := range rows {
		out[i] = v9Row(r)
	}
	return out
}

// v9ColumnSpecs are the three spawns columns the v8→v9 step adds (SR-3.1; b.xjn
// SR-9 folded subagent_tracking into the same unreleased hop), in schema order,
// after state_changed_ms. agent's default is no agent: the hop backfills claude
// (assertV9Defaults). subagent_tracking is last: 0 is untracked, and no backfill
// sets 1.
var v9ColumnSpecs = []columnSpec{
	{"spawns", "agent", "TEXT", true, "''"},
	{"spawns", "end_reported_at", "INTEGER", false, ""},
	{"spawns", "subagent_tracking", "INTEGER", true, "0"},
}

// v9ColumnNames are v9ColumnSpecs' column names, in order.
func v9ColumnNames() []string {
	var names []string
	for _, c := range v9ColumnSpecs {
		names = append(names, c.name)
	}
	return names
}

// col is one expected PRAGMA table_info row (no default; pk 0 or the position
// in the primary key).
func col(name, typ string, notNull bool, pk int) tableColumn {
	c := tableColumn{name: name, typ: typ, pk: pk}
	if notNull {
		c.notNull = 1
	}
	return c
}

// v9SubagentTable is one table of the subagent storage the v8→v9 hop creates
// (b.xjn SR-10.1, SR-10.5), written out here and not read from the hop: its
// columns in order, the foreign key and unique key, and the CREATE text a
// part-migrated store is given (preAddV9SubagentObjects).
type v9SubagentTable struct {
	name    string
	columns []tableColumn
	fk      string   // the table's one foreign key, as schemaShape reports it
	unique  []string // the columns of the table's one UNIQUE constraint
	ddl     string
}

var v9SubagentTables = []v9SubagentTable{
	{
		name: "subagents",
		columns: []tableColumn{
			col("created_seq", "INTEGER", false, 1), col("instance_id", "TEXT", true, 0),
			col("subagent_id", "TEXT", true, 0), col("launch_token", "TEXT", true, 0), col("confirmed", "INTEGER", true, 0),
			col("session_id", "TEXT", false, 0), col("subagent_type", "TEXT", false, 0), col("description", "TEXT", false, 0),
			col("model", "TEXT", false, 0), col("parent_type", "TEXT", false, 0), col("parent_id", "TEXT", false, 0),
			col("tool_use_id", "TEXT", false, 0), col("run_mode", "TEXT", false, 0),
			col("first_started_at", "INTEGER", false, 0), col("started_at", "INTEGER", false, 0),
			col("stopped_at", "INTEGER", false, 0), col("lost_at", "INTEGER", false, 0),
			col("state", "TEXT", true, 0), col("close_reason", "TEXT", false, 0),
			col("opened_at", "INTEGER", true, 0), col("activity_at", "INTEGER", true, 0),
			col("subagent_type_source", "TEXT", false, 0), col("run_mode_source", "TEXT", false, 0),
			col("stopped_at_source", "TEXT", false, 0), col("life_number", "INTEGER", true, 0),
		},
		fk:     "instance_id -> spawns(instance_id) on delete CASCADE",
		unique: []string{"instance_id", "life_number", "subagent_id"},
		ddl: `CREATE TABLE subagents (
			created_seq INTEGER PRIMARY KEY AUTOINCREMENT,
			instance_id TEXT NOT NULL REFERENCES spawns(instance_id) ON DELETE CASCADE,
			subagent_id TEXT NOT NULL, launch_token TEXT NOT NULL, confirmed INTEGER NOT NULL,
			session_id TEXT, subagent_type TEXT, description TEXT, model TEXT,
			parent_type TEXT, parent_id TEXT, tool_use_id TEXT, run_mode TEXT,
			first_started_at INTEGER, started_at INTEGER, stopped_at INTEGER, lost_at INTEGER,
			state TEXT NOT NULL, close_reason TEXT,
			opened_at INTEGER NOT NULL, activity_at INTEGER NOT NULL,
			subagent_type_source TEXT, run_mode_source TEXT, stopped_at_source TEXT,
			life_number INTEGER NOT NULL,
			UNIQUE(instance_id, life_number, subagent_id))`,
	},
	{
		name: "subagent_launches",
		columns: []tableColumn{
			col("created_seq", "INTEGER", false, 1), col("instance_id", "TEXT", true, 0),
			col("launch_token", "TEXT", true, 0), col("tool_use_id", "TEXT", true, 0),
			col("subagent_type", "TEXT", true, 0), col("opened_at", "INTEGER", true, 0),
		},
		fk:     "instance_id -> spawns(instance_id) on delete CASCADE",
		unique: []string{"instance_id", "tool_use_id"},
		ddl: `CREATE TABLE subagent_launches (
			created_seq INTEGER PRIMARY KEY AUTOINCREMENT,
			instance_id TEXT NOT NULL REFERENCES spawns(instance_id) ON DELETE CASCADE,
			launch_token TEXT NOT NULL, tool_use_id TEXT NOT NULL, subagent_type TEXT NOT NULL,
			opened_at INTEGER NOT NULL,
			UNIQUE(instance_id, tool_use_id))`,
	},
}

// The one explicit index of the subagent tables: the claim's (a launch's open
// notes by launch, type and age), with its CREATE text and columns.
const (
	v9ClaimIndexName = "idx_subagent_launches_claim"
	v9ClaimIndexDDL  = `CREATE INDEX idx_subagent_launches_claim
		ON subagent_launches(instance_id, launch_token, subagent_type, opened_at)`
)

var v9ClaimIndexColumns = []string{"instance_id", "launch_token", "subagent_type", "opened_at"}

// assertV9SubagentSchema checks db's two subagent tables: each one's columns in
// order with type, NOT NULL and primary key; its foreign key to
// spawns(instance_id) ON DELETE CASCADE; its one unique key (the implicit
// index sqlite_autoindex_<table>_1, unique); and that the claim index is the
// only explicit index on them.
func assertV9SubagentSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	shape := schemaShape(t, db)
	for _, tbl := range v9SubagentTables {
		if got := shape["table "+tbl.name]; !reflect.DeepEqual(got, tbl.columns) {
			t.Errorf("columns of %s = %+v; want %+v", tbl.name, got, tbl.columns)
		}
		if got, want := shape["fk "+tbl.name], []string{tbl.fk}; !reflect.DeepEqual(got, want) {
			t.Errorf("foreign keys of %s = %v; want %v", tbl.name, got, want)
		}
		auto := "sqlite_autoindex_" + tbl.name + "_1"
		if got := shape["index "+auto]; !reflect.DeepEqual(got, tbl.unique) {
			t.Errorf("index %s = %v; want the unique key %v", auto, got, tbl.unique)
		}
		var unique int
		if err := db.QueryRow(`SELECT "unique" FROM pragma_index_list(?) WHERE name = ?`, tbl.name, auto).Scan(&unique); err != nil || unique != 1 {
			t.Errorf("%s unique = %d, %v; want 1", auto, unique, err)
		}
		var indexes []string
		for _, name := range queryStrings(t, db, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ?`, tbl.name) {
			if !strings.HasPrefix(name, "sqlite_autoindex_") {
				indexes = append(indexes, name)
			}
		}
		var wantIndexes []string
		if tbl.name == "subagent_launches" {
			wantIndexes = []string{v9ClaimIndexName}
		}
		if !slices.Equal(indexes, wantIndexes) {
			t.Errorf("explicit indexes on %s = %v; want %v", tbl.name, indexes, wantIndexes)
		}
	}
	if got := shape["index "+v9ClaimIndexName]; !reflect.DeepEqual(got, v9ClaimIndexColumns) {
		t.Errorf("index %s = %v; want %v", v9ClaimIndexName, got, v9ClaimIndexColumns)
	}
}

// subagentRowCounts is the number of entries and claimable launches in the
// subagent tables of the store at path, over all rows of spawns.
func subagentRowCounts(t *testing.T, path string) (entries, launches int) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for table, dst := range map[string]*int{"subagents": &entries, "subagent_launches": &launches} {
			if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(dst); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
		}
	})
	return entries, launches
}

// assertV9Defaults checks id's v9 columns hold what the hop gives an existing
// row: agent claude, no end report (NULL) and subagent_tracking 0; and that
// the store holds no subagent entry and no claimable launch (the tables the hop
// creates start empty).
func assertV9Defaults(t *testing.T, path, id string) {
	t.Helper()
	got := readRawSpawn(t, path, id, v9ColumnNames())
	if got["agent"] != "'claude'" || got["end_reported_at"] != "NULL" || got["subagent_tracking"] != "0" {
		t.Errorf("%s: agent %s, end_reported_at %s, subagent_tracking %s; want 'claude', NULL and 0",
			id, got["agent"], got["end_reported_at"], got["subagent_tracking"])
	}
	if entries, launches := subagentRowCounts(t, path); entries != 0 || launches != 0 {
		t.Errorf("%d subagent entries and %d claimable launches; want none", entries, launches)
	}
}

// preAddV9SubagentObjects creates the named subagent objects ("subagents",
// "subagent_launches", v9ClaimIndexName) from their own text: a v8→v9 hop that
// stopped part-way through its CREATEs. They follow the renames and column adds
// in the hop, so the store must already hold those.
func preAddV9SubagentObjects(t *testing.T, path string, names ...string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, n := range names {
			found := n == v9ClaimIndexName
			if found {
				mustExec(t, db, v9ClaimIndexDDL)
			}
			for _, tbl := range v9SubagentTables {
				if tbl.name == n {
					mustExec(t, db, tbl.ddl)
					found = true
				}
			}
			if !found {
				t.Fatalf("unknown v9 subagent object %q", n)
			}
		}
	})
}

// preRenameV9 renames the named columns ("table.oldname") to their v9 names: a
// v8→v9 hop that stopped part-way through its renames.
func preRenameV9(t *testing.T, path string, keys ...string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, k := range keys {
			found := false
			for _, r := range v9RenameSpecs {
				if r.table+"."+r.oldName == k {
					mustExec(t, db, fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", r.table, r.oldName, r.newName))
					found = true
				}
			}
			if !found {
				t.Fatalf("unknown v9 rename %q", k)
			}
		}
	})
}

// allV9RenameKeys is every rename as "table.oldname", in the hop's order.
func allV9RenameKeys() []string {
	var keys []string
	for _, r := range v9RenameSpecs {
		keys = append(keys, r.table+"."+r.oldName)
	}
	return keys
}

// preAddV9Columns adds the named v9 spawns columns with their definitions: a
// v8→v9 hop that stopped part-way (before its backfill).
func preAddV9Columns(t *testing.T, path string, names ...string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, n := range names {
			for _, spec := range v9ColumnSpecs {
				if spec.name == n {
					mustExec(t, db, spec.addColumnDDL())
				}
			}
		}
	})
}

// setRowAgent writes agent on id's spawns row, whichever id column the store
// has: a row that already holds an agent when the hop runs.
func setRowAgent(t *testing.T, path, id, agent string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, "UPDATE spawns SET agent = ? WHERE "+idColumn(t, db, "spawns")+" = ?", agent, id)
	})
}

// agentsHeld returns, by id, the agent of each of ids' rows that holds one
// (not ""); nil when spawns has no agent column yet.
func agentsHeld(t *testing.T, path string, ids []string) map[string]string {
	t.Helper()
	var has bool
	withRaw(t, path, func(db *sql.DB) { has = slices.Contains(tableColumnNames(t, db, "spawns"), "agent") })
	if !has {
		return nil
	}
	held := map[string]string{}
	for _, id := range ids {
		if a := unquote(readRawSpawn(t, path, id, []string{"agent"})["agent"]); a != "" {
			held[id] = a
		}
	}
	return held
}

// breakV9BothNames adds session_history.session_id beside claude_session_id: a
// table holding an old and its new name. The hop validates before it changes
// anything, so it renames nothing, not even spawns' columns that come first.
func breakV9BothNames(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "ALTER TABLE session_history ADD COLUMN session_id TEXT") })
}

// breakV9NeitherName renames spawns.claude_args to a name the hop does not
// know: a table holding neither the old nor the new name.
func breakV9NeitherName(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "ALTER TABLE spawns RENAME COLUMN claude_args TO args_backup") })
}

// breakV9Backfill makes every update of a spawns row fail: the hop renames
// every column and adds both new ones, then fails at its agent backfill.
func breakV9Backfill(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `CREATE TRIGGER ad_test_fail_agent_backfill BEFORE UPDATE ON spawns
			BEGIN SELECT RAISE(ABORT, 'test: the agent backfill fails'); END`)
	})
}

// breakV9FirstSubagentTable adds an index named subagents on spawns: the hop
// renames, adds every column, then fails creating the first subagent table (a
// table cannot take an existing index's name, IF NOT EXISTS or not).
func breakV9FirstSubagentTable(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "CREATE INDEX subagents ON spawns(state)") })
}

// breakV9SecondSubagentTable adds an index named subagent_launches on spawns:
// the hop renames, adds every column and creates subagents, then fails creating
// subagent_launches.
func breakV9SecondSubagentTable(t *testing.T, path string) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "CREATE INDEX subagent_launches ON spawns(state)") })
}

// seedPreMigrationRow inserts the row 'pre' (working, relay on, settled when
// the store has the settle columns) into the closed older-version store at
// path, and, at v1, a request of it (v1 has no request token).
func seedPreMigrationRow(t *testing.T, path string, from int) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		id := idColumn(t, db, "spawns")
		mustExec(t, db, "INSERT INTO spawns ("+id+`, state, cwd, tmux_session_name, relay_mode)
			VALUES ('pre', 'working', '/tmp', 'pre-sess', 'on')`)
		if slices.Contains(tableColumnNames(t, db, "spawns"), "settled_state") { // a store with the settle time has the row settled
			mustExec(t, db, `UPDATE spawns SET settled_state = state WHERE `+id+` = 'pre'`)
		}
		if from >= 9 {
			mustExec(t, db, `UPDATE spawns SET agent = 'claude' WHERE `+id+` = 'pre'`)
		}
		if from == 1 {
			mustExec(t, db, "INSERT INTO permission_requests ("+idColumn(t, db, "permission_requests")+
				", tool_name, tool_input) VALUES ('pre', 'Bash', '{}')")
		}
	})
}

// insertRow inserts row (column to value) into table; the columns are the
// test's own literals.
func insertRow(t *testing.T, db *sql.DB, table string, row map[string]any) {
	t.Helper()
	cols := make([]string, 0, len(row))
	for c := range row {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	vals := make([]any, len(cols))
	for i, c := range cols {
		vals[i] = row[c]
	}
	mustExec(t, db, "INSERT INTO "+table+" ("+strings.Join(cols, ", ")+") VALUES (?"+strings.Repeat(", ?", len(cols)-1)+")", vals...)
}

// The ids of makeV8Fixture's rows.
const (
	v8PendingID   = "v8-pending"
	v8LiveID      = "v8-live"
	v8RelayID     = "v8-relay"
	v8UnsettledID = "v8-unsettled"
	v8ReusedID    = "v8-reused"
	v8OptedOutID  = "v8-opted-out"
	v8MissingID   = "v8-missing"
)

// v8Fixture describes what makeV8Fixture seeded: the baseline of the v8→v9
// hop, also the start of every downgrade test. Every map holds quote()
// literals of every v8 column, under v8's names, read before the hop.
type v8Fixture struct {
	dir, path string
	ids       []string // spawns ids, parents first

	spawnCols, requestCols, historyCols []string
	rows                                map[string]map[string]string   // id → column → literal
	requests                            map[string][]map[string]string // id → requests, request_id order
	history                             map[string][]map[string]string // id → entries, newest first
}

// makeV8Fixture builds a genuine v8 store under dir, closed, holding the rows
// the v8→v9 hop and the downgrade tests need, seeded by raw SQL under v8's names:
//   - v8PendingID: pending, mid-launch, with its launch owner and identity;
//   - v8LiveID: waiting with args, session, labels, extra env, every identity,
//     a launch owner, an idle mark and one archived session;
//   - v8RelayID: check_permission with an open relay request holding its hook's
//     identity and a decided one;
//   - v8UnsettledID: working over a waiting settled state, child of v8LiveID;
//   - v8ReusedID: ended at life 2 with history in lives 0 to 2;
//   - v8OptedOutID: waiting, no_pre_trust, one archived session;
//   - v8MissingID: missing with a closed request proven gone.
func makeV8Fixture(t *testing.T, dir string) v8Fixture {
	t.Helper()
	f := v8Fixture{dir: dir, path: makeVersionedDB(t, dir, 8), rows: map[string]map[string]string{},
		requests: map[string][]map[string]string{}, history: map[string][]map[string]string{}}
	f.ids = []string{v8LiveID, v8PendingID, v8RelayID, v8UnsettledID, v8ReusedID, v8OptedOutID, v8MissingID}
	const (
		token  = "0123456789abcdef"
		socket = "/tmp/ad-v8/sock"
		pidns  = "pid:[4026531836]"
	)
	spawn := func(id string, over map[string]any) map[string]any {
		row := map[string]any{"claude_instance_id": id, "state": "waiting", "cwd": "/work/" + id, "tmux_session_name": "ad-" + id,
			"relay_mode": "off", "started_at": "2026-05-01 10:00:00", "last_seen_at": "2026-05-01 10:05:00",
			"row_version": 3, "settled_state": "waiting"}
		for c, v := range over {
			row[c] = v
		}
		return row
	}
	identity := func(pane string, pid int) map[string]any {
		return map[string]any{"launch_token": token, "tmux_socket": socket, "tmux_server_pid": 4100,
			"tmux_server_started": 1767225600, "tmux_server_starttime": "123", "pane_id": pane, "pane_pid": pid, "pane_starttime": "456"}
	}
	merge := func(base, more map[string]any) map[string]any {
		for c, v := range more {
			base[c] = v
		}
		return base
	}
	spawns := []map[string]any{
		spawn(v8LiveID, merge(map[string]any{"relay_mode": "on", "claude_args": `["--model","opus"]`, "jsonl_path": "/t/live.jsonl",
			"claude_session_id": "sess-live", "labels": `{"team":"red"}`, "extra_env": `{"K":"V"}`, "pid": 4242, "proc_starttime": "98765",
			"liveness_unverified_since": "2026-05-01 10:04:00", "liveness_note": "probe_eacces", "row_version": 5,
			"launch_owner_pid": 4300, "launch_owner_starttime": "12345", "launch_owner_pidns": pidns,
			"idle_since": "2026-05-01 10:03:00", "state_changed_ms": 1790000000000}, identity("%2", 4242))),
		spawn(v8PendingID, map[string]any{"state": "pending", "settled_state": "pending", "row_version": 1,
			"launch_started_at": int64(1790000000123), "launch_token": token, "tmux_socket": socket,
			"launch_owner_pid": 4300, "launch_owner_starttime": "12345", "launch_owner_pidns": pidns}),
		spawn(v8RelayID, map[string]any{"state": "check_permission", "settled_state": "check_permission", "relay_mode": "on", "row_version": 4}),
		spawn(v8UnsettledID, map[string]any{"state": "working", "settled_state": "waiting", "parent_id": v8LiveID,
			"state_changed_ms": 1790000000500, "row_version": 4}),
		spawn(v8ReusedID, merge(map[string]any{"state": "ended", "settled_state": "ended", "jsonl_path": "/t/r-2.jsonl",
			"claude_session_id": "sess-r-2", "started_at": "2026-02-01 10:00:00", "ended_at": "2026-02-03 10:00:00",
			"row_version": 7, "life_number": 2, "claude_args": `["--resume"]`}, identity("%3", 4200))),
		spawn(v8OptedOutID, map[string]any{"relay_mode": "on", "no_pre_trust": 1, "jsonl_path": "/t/o.jsonl",
			"claude_session_id": "sess-o", "launch_token": "fedcba9876543210", "tmux_socket": socket}),
		spawn(v8MissingID, map[string]any{"state": "missing", "settled_state": "missing", "ended_at": "2026-05-01 12:00:00"}),
	}
	const (
		openTok    = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
		decidedTok = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
		goneTok    = "cccccccc-cccc-4ccc-cccc-cccccccccccc"
	)
	requests := []map[string]any{
		{"claude_instance_id": v8RelayID, "request_token": openTok, "tool_name": "Bash", "tool_input": `{"command":"ls"}`,
			"created_at": "2026-05-03 10:00:00", "hook_pid": 4321, "hook_starttime": "777", "hook_pidns": pidns,
			"tool_use_id": "toolu_01", "agent_id": "agent-1", "settled_at": 1790000000000},
		{"claude_instance_id": v8RelayID, "request_token": decidedTok, "tool_name": "Read", "tool_input": "{}", "decision": "allow",
			"decided_at": "2026-05-03 09:00:05", "created_at": "2026-05-03 09:00:00", "delivered_at": 1790000000100},
		{"claude_instance_id": v8MissingID, "request_token": goneTok, "tool_name": "Bash", "tool_input": "{}", "decision": "deny",
			"decision_reason": "ended", "decided_at": "2026-05-01 11:00:00", "created_at": "2026-05-01 10:30:00",
			"closed_at": 1790000000200, "proven_gone_at": 1790000000300, "proven_gone_how": "agent_gone"},
	}
	histories := []map[string]any{
		{"claude_instance_id": v8LiveID, "claude_session_id": "sess-live-prior", "jsonl_path": "/t/live-prior.jsonl", "recorded_at": "2026-05-01 10:01:00"},
		{"claude_instance_id": v8ReusedID, "claude_session_id": "sess-r-0a", "jsonl_path": "/t/r-0a.jsonl", "recorded_at": "2026-02-01 11:00:00", "life_number": 0},
		{"claude_instance_id": v8ReusedID, "claude_session_id": "sess-r-0b", "recorded_at": "2026-02-01 12:00:00", "life_number": 0},
		{"claude_instance_id": v8ReusedID, "claude_session_id": "sess-r-1", "jsonl_path": "/t/r-1.jsonl", "recorded_at": "2026-02-02 11:00:00", "life_number": 1},
		{"claude_instance_id": v8ReusedID, "claude_session_id": "sess-r-2-prior", "jsonl_path": "/t/r-2p.jsonl", "recorded_at": "2026-02-03 09:00:00", "life_number": 2},
		{"claude_instance_id": v8OptedOutID, "claude_session_id": "sess-o-prior", "jsonl_path": "/t/o-prior.jsonl", "recorded_at": "2026-05-01 10:02:00"},
	}
	withRaw(t, f.path, func(db *sql.DB) {
		for _, tbl := range []struct {
			name string
			rows []map[string]any
		}{{"spawns", spawns}, {"permission_requests", requests}, {"session_history", histories}} {
			for _, r := range tbl.rows {
				insertRow(t, db, tbl.name, r)
			}
		}
		f.spawnCols, f.requestCols, f.historyCols = tableColumnNames(t, db, "spawns"),
			tableColumnNames(t, db, "permission_requests"), tableColumnNames(t, db, "session_history")
	})
	for _, id := range f.ids {
		f.rows[id] = readRawSpawn(t, f.path, id, f.spawnCols)
		f.requests[id] = readQuotedRows(t, f.path, "permission_requests", id, "request_id", f.requestCols)
		f.history[id] = readRawHistory(t, f.path, id, f.historyCols)
	}
	return f
}

// assertV8ValuesKept fails unless every value makeV8Fixture seeded in the
// store at path (migrated through the v8→v9 hop) reads back under its v9 name:
// every spawns row, request and history entry, each row's agent claude (or
// held's), no end report and subagent_tracking 0.
func assertV8ValuesKept(t *testing.T, path string, f v8Fixture, held map[string]string) {
	t.Helper()
	for _, id := range f.ids {
		if got, want := readRawSpawn(t, path, id, v9Cols(f.spawnCols)), v9Row(f.rows[id]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: v8 values changed:\n got  %v\n want %v", id, got, want)
		}
		if got, want := readQuotedRows(t, path, "permission_requests", id, "request_id", v9Cols(f.requestCols)), v9Rows(f.requests[id]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: requests changed:\n got  %v\n want %v", id, got, want)
		}
		if got, want := readRawHistory(t, path, id, v9Cols(f.historyCols)), v9Rows(f.history[id]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: history changed:\n got  %v\n want %v", id, got, want)
		}
		wantAgent := "'claude'"
		if a, ok := held[id]; ok {
			wantAgent = "'" + a + "'"
		}
		if got := readRawSpawn(t, path, id, v9ColumnNames()); got["agent"] != wantAgent || got["end_reported_at"] != "NULL" || got["subagent_tracking"] != "0" {
			t.Errorf("%s: agent %s, end_reported_at %s, subagent_tracking %s; want %s, NULL and 0",
				id, got["agent"], got["end_reported_at"], got["subagent_tracking"], wantAgent)
		}
	}
}

// Source-derived v10 fixture; it retains historical NULL/malformed agent values.
func seedV10EligibilityFixture(t *testing.T, path string, agent any, child any) {
	t.Helper()
	seedPreMigrationRow(t, path, 10)
	if agent == nil {
		withRaw(t, path, func(db *sql.DB) {
			db.SetMaxOpenConns(1)
			var ddl string
			var version int
			if err := db.QueryRow("SELECT sql FROM sqlite_schema WHERE name='spawns'").Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("PRAGMA schema_version").Scan(&version); err != nil {
				t.Fatal(err)
			}
			mustExec(t, db, "PRAGMA writable_schema=ON")
			mustExec(t, db, "UPDATE sqlite_schema SET sql=? WHERE name='spawns'", strings.Replace(ddl, "agent TEXT NOT NULL", "agent TEXT", 1))
			mustExec(t, db, fmt.Sprintf("PRAGMA schema_version=%d", version+1))
			mustExec(t, db, "PRAGMA writable_schema=RESET")
		})
	}
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE spawns SET agent=?, life_number=3, session_id='kept-session', end_reported_at=123456 WHERE instance_id='pre'`, agent)
		mustExec(t, db, `INSERT INTO permission_requests(instance_id, request_token, tool_name, tool_input, agent_id, decision, pane_answer, proven_gone_at, proven_gone_how) VALUES('pre', ?, 'Bash', '{}', ?, 'deny', 'outside', 1790812800000, 'tool_ran')`, tokenA, child)
		mustExec(t, db, `INSERT INTO session_history(instance_id, session_id, life_number) VALUES('pre', 'kept-session', 3)`)
		mustExec(t, db, `INSERT INTO cursor_runtimes(bound_instance_id,bound_launch_token,bound_life_number,bound_gateway_pid,bound_gateway_start,bound_runtime_pid,bound_runtime_start,bound_connection_id,bound_input_pipe,bound_output_pipe,bound_input_fd,bound_output_fd,bound_session_id,bound_terminated) VALUES('pre','1111111111111111',3,4101,'start',12345,'synthetic','kept','pipe:[1]','pipe:[2]',11,12,'kept-session',0)`)
	})
}

// startupJSON builds persisted origin variants for migration validation.
func startupJSON(t *testing.T, change func(map[string]any)) string {
	t.Helper()
	p := map[string]any{"launch_token": testLaunchToken, "session_id": "fresh-session", "jsonl_path": "/tmp/fresh-session.jsonl", "pid": testPane().PanePID, "starttime": testPaneStart}
	if change != nil {
		change(p)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// seedV11StartupFixture retains populated lifecycle, request, history and runtime rows.
func seedV11StartupFixture(t *testing.T, path, definition string, marker any) {
	t.Helper()
	seedV10EligibilityFixture(t, path, AgentCodex, nil)
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE permission_requests SET main_turn_end_eligible=1`)
		if definition != "" {
			mustExec(t, db, "ALTER TABLE spawns ADD COLUMN "+definition)
			mustExec(t, db, `UPDATE spawns SET codex_startup=?`, marker)
		}
	})
}

var v14ColumnSpecs = []columnSpec{
	{"spawns", "permission_epoch", "INTEGER", true, "0"},
	{"spawns", "observation_version", "INTEGER", true, "0"},
	{"spawns", "display_observation", "TEXT", false, ""},
	{"spawns", "display_exit", "TEXT", false, ""},
	{"spawns", "idle_screen_timeout_seconds", "INTEGER", false, ""},
	{"spawns", "pane_sha256", "TEXT", false, ""},
	{"spawns", "pane_since_ms", "INTEGER", false, ""},
	{"spawns", "pane_epoch", "INTEGER", false, ""},
	{"permission_requests", "owner_launch_token", "TEXT", false, ""},
	{"permission_requests", "owner_permission_epoch", "INTEGER", false, ""},
	{"permission_requests", "admitted_at", "INTEGER", false, ""},
	{"permission_requests", "fallback_at", "INTEGER", false, ""},
	{"permission_requests", "fallback_basis", "TEXT", false, ""},
}

func v14ColumnNames(table string) []string {
	var names []string
	for _, c := range v14ColumnSpecs {
		if c.table == table {
			names = append(names, c.name)
		}
	}
	return names
}

// preAddV14Columns adds specs (v14's, or a later hop's) to the closed store at
// path, as a partial run of their hop would have left them.
func preAddV14Columns(t *testing.T, path string, specs []columnSpec) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		for _, c := range specs {
			mustExec(t, db, c.addColumnDDL())
		}
	})
}

// v15ColumnSpecs are the two spawns columns the v14→v15 hop adds (b.x72).
// Must use: a migration test of the v15 columns seeds them through these
// specs (preAddV14Columns for columns already present), never new raw SQL.
var v15ColumnSpecs = []columnSpec{
	{"spawns", "codex_turn", "TEXT", false, ""},
	{"spawns", "codex_turn_ended", "TEXT", false, ""},
}

// v15ColumnNames are v15ColumnSpecs' column names, in order. Must use: a
// migration test that names or reads the v15 columns takes them from here,
// never a new list.
func v15ColumnNames() []string {
	var names []string
	for _, c := range v15ColumnSpecs {
		names = append(names, c.name)
	}
	return names
}

// assertV15Defaults fails unless id's row records no Codex turn: an upgraded
// row's turn columns are NULL, never backfilled. Must use: a migration test
// asserts an upgraded row's v15 columns through it, never new raw SQL.
func assertV15Defaults(t *testing.T, path, id string) {
	t.Helper()
	for c, v := range readRawSpawn(t, path, id, v15ColumnNames()) {
		if v != "NULL" {
			t.Errorf("%s: %s = %s; want NULL", id, c, v)
		}
	}
}

// seedV13PermissionFixture retains lifecycle/runtime history and independent
// unanswered, answered and proven requests, with distinct trustworthy clocks.
func seedV13PermissionFixture(t *testing.T, path string) {
	t.Helper()
	seedV11StartupFixture(t, path, "", nil)
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE spawns SET state='check_permission',settled_state='check_permission',
			launch_token=?,life_number=3,row_version=7,state_changed_ms=1792411200125,
			family_state='working',family_settled_state='waiting',family_changed_ms=123,
			pid=4242,proc_starttime='kept-process',pane_id='%5',pane_pid=4242,pane_starttime='kept-pane'`, testLaunchToken)
		mustExec(t, db, `UPDATE permission_requests SET created_at='2026-10-19 12:00:01',settled_at=1792411205000,
			hook_gone_at=1792411206000,decision_reason='pane_outside',decided_at='2026-10-19 12:00:07'`)
		for _, r := range []struct{ token, answer, proof string }{
			{tokenB, "none", ""}, {tokenC, "outside", ""}, {tokenD, "none", "turn_end"},
		} {
			var proofAt any
			if r.proof != "" {
				proofAt = int64(1792411207000)
			}
			insertRow(t, db, "permission_requests", map[string]any{"instance_id": "pre", "request_token": r.token,
				"tool_name": "Read", "tool_input": `{}`, "created_at": "2026-10-19 12:00:01",
				"settled_at": int64(1792411205000), "hook_gone_at": int64(1792411206000), "pane_answer": r.answer,
				"proven_gone_how": nullableStringArg(r.proof), "proven_gone_at": proofAt})
		}
	})
}
