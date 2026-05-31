package store

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openTempStore opens a Store under t.TempDir and registers cleanup.
// Returns the resolved DB path so tests can re-open it raw if they need to.
// The canonical public form lives in internal/testsupport/storefix; this
// copy stays here because schema_test.go is package store (white-box) and
// cannot import a package that imports store (circular import).
func openTempStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// openRaw is a test helper that opens the same DB through database/sql
// directly so tests can poke PRAGMAs and sqlite_master without going through
// the Store API (which deliberately exposes no SQL surface).
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw sql.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenCreatesSchemaV2(t *testing.T) {
	s, path := openTempStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db := openRaw(t, path)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 2 {
		t.Fatalf("user_version = %d, want 2", version)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	for i := 0; i < 2; i++ {
		s, err := OpenOrInit(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}

	db := openRaw(t, path)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 2 {
		t.Fatalf("user_version = %d after two opens, want 2", version)
	}
}

func TestSchemaObjectsExist(t *testing.T) {
	s, path := openTempStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db := openRaw(t, path)

	wantTables := []string{"spawns", "permission_requests"}
	for _, name := range wantTables {
		if !objectExists(t, db, "table", name) {
			t.Errorf("table %q missing from sqlite_master", name)
		}
	}

	wantIndexes := []string{
		"idx_spawns_state",
		"idx_spawns_last_seen",
		"idx_spawns_parent",
		"idx_permission_requests_instance_decision",
		"idx_permission_requests_decision_decided_at",
	}
	for _, name := range wantIndexes {
		if !objectExists(t, db, "index", name) {
			t.Errorf("index %q missing from sqlite_master", name)
		}
	}
}

func TestPragmasApplied(t *testing.T) {
	s, path := openTempStore(t)

	// journal_mode persists in the DB header, so a fresh raw connection
	// observes the same value the Store set.
	rawDB := openRaw(t, path)
	var journal string
	if err := rawDB.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want %q", journal, "wal")
	}

	// foreign_keys is per-connection, so verify on the Store's own conn.
	var fk int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

// TestSingleWriterSerializesExec exercises SetMaxOpenConns(1): two writes
// back-to-back through the Store's *sql.DB must both succeed without
// SQLite's "database is locked" error.
func TestSingleWriterSerializesExec(t *testing.T) {
	s, _ := openTempStore(t)

	insert := `INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode)
	           VALUES (?, 'idle', '/tmp', 'sess', 'mirror')`
	if _, err := s.db.Exec(insert, "a"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.db.Exec(insert, "b"); err != nil {
		t.Fatalf("second insert: %v", err)
	}
}

// openV1DB creates a SQLite file at path seeded with the v1 DDL fixture and
// WAL journal mode, then closes it. The caller opens it via the Store API.
func openV1DB(t *testing.T, path string) {
	t.Helper()
	v1SQL, err := os.ReadFile("testdata/schema_v1.sql")
	if err != nil {
		t.Fatalf("read v1 fixture: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open for v1 load: %v", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		t.Fatalf("set WAL on v1 DB: %v", err)
	}
	if _, err := db.Exec(string(v1SQL)); err != nil {
		_ = db.Close()
		t.Fatalf("apply v1 DDL: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v1 raw DB: %v", err)
	}
}

// columnNames returns the column names from PRAGMA table_info for the given table.
// It fully drains and closes the result set before returning.
func columnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	var cols []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			_ = rows.Close()
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		cols = append(cols, name)
	}
	_ = rows.Close()
	return cols
}

// uniqueIndexCols returns a map from unique index name to its ordered column list
// for the given table. Drains each sub-query before opening the next.
func uniqueIndexCols(t *testing.T, db *sql.DB, table string) map[string][]string {
	t.Helper()
	rows, err := db.Query("PRAGMA index_list(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA index_list(%s): %v", table, err)
	}
	type idxEntry struct {
		name   string
		unique int
	}
	var indexes []idxEntry
	for rows.Next() {
		var seq int
		var idxName string
		var unique int
		var origin, partial string
		if err := rows.Scan(&seq, &idxName, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			t.Fatalf("scan index_list(%s): %v", table, err)
		}
		indexes = append(indexes, idxEntry{idxName, unique})
	}
	_ = rows.Close()

	result := make(map[string][]string)
	for _, idx := range indexes {
		if idx.unique != 1 {
			continue
		}
		infoRows, err := db.Query("PRAGMA index_info(" + idx.name + ")")
		if err != nil {
			t.Fatalf("PRAGMA index_info(%s): %v", idx.name, err)
		}
		var cols []string
		for infoRows.Next() {
			var seqno, cid int
			var colName string
			if err := infoRows.Scan(&seqno, &cid, &colName); err != nil {
				_ = infoRows.Close()
				t.Fatalf("scan index_info(%s): %v", idx.name, err)
			}
			cols = append(cols, colName)
		}
		_ = infoRows.Close()
		result[idx.name] = cols
	}
	return result
}

func TestSchemaV2Migration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	openV1DB(t, path)

	// Seed a spawn row + one permission_requests row into the v1 schema so the
	// "0 rows after migration" assertion is non-vacuous: it verifies that DROP
	// TABLE actually fired and the v1 row didn't survive into v2.
	{
		rawV1, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatalf("open raw v1 for seeding: %v", err)
		}
		if _, err := rawV1.Exec(
			`INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode)
			 VALUES ('v1-seed-id', 'working', '/tmp', 'v1-sess', 'on')`,
		); err != nil {
			_ = rawV1.Close()
			t.Fatalf("insert v1 spawn row: %v", err)
		}
		// v1 permission_requests has no request_token column.
		if _, err := rawV1.Exec(
			`INSERT INTO permission_requests (claude_instance_id, tool_name, tool_input)
			 VALUES ('v1-seed-id', 'Bash', '{"cmd":"ls"}')`,
		); err != nil {
			_ = rawV1.Close()
			t.Fatalf("insert v1 permission_requests row: %v", err)
		}
		if err := rawV1.Close(); err != nil {
			t.Fatalf("close raw v1: %v", err)
		}
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v1 DB: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// user_version must be 2 post-migration.
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 2 {
		t.Fatalf("user_version = %d, want 2", version)
	}

	// request_token column must exist.
	cols := columnNames(t, s.db, "permission_requests")
	foundToken := false
	for _, c := range cols {
		if c == "request_token" {
			foundToken = true
		}
	}
	if !foundToken {
		t.Errorf("request_token column missing after migration; cols: %v", cols)
	}

	// Composite UNIQUE(claude_instance_id, request_token) must exist.
	foundComposite := false
	for _, idxCols := range uniqueIndexCols(t, s.db, "permission_requests") {
		if len(idxCols) == 2 && idxCols[0] == "claude_instance_id" && idxCols[1] == "request_token" {
			foundComposite = true
		}
	}
	if !foundComposite {
		t.Error("composite UNIQUE(claude_instance_id, request_token) not found after migration")
	}

	// Both supporting indexes must be present.
	for _, idx := range []string{
		"idx_permission_requests_instance_decision",
		"idx_permission_requests_decision_decided_at",
	} {
		if !objectExists(t, s.db, "index", idx) {
			t.Errorf("index %q missing after migration", idx)
		}
	}

	// permission_requests must be empty — no v1 row backfill.
	var rowCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM permission_requests").Scan(&rowCount); err != nil {
		t.Fatalf("count permission_requests: %v", err)
	}
	if rowCount != 0 {
		t.Errorf("permission_requests has %d rows after migration, want 0", rowCount)
	}
}

// objectExists returns true if sqlite_master contains a row of the given
// type and name. Centralizing the query keeps individual tests readable.
func objectExists(t *testing.T, db *sql.DB, kind, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type = ? AND name = ?",
		kind, name,
	).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("sqlite_master lookup %s %q: %v", kind, name, err)
	}
	return got == name
}

// TestNoAgentSuppliedIdentifierInSchema asserts that no agent-supplied
// per-call identifier (tool_use_id / toolUseID / ToolUseID) appears in the
// v2 schema or in AD production Go source.
func TestNoAgentSuppliedIdentifierInSchema(t *testing.T) {
	forbidden := []string{"tool_use_id", "toolUseID", "ToolUseID"}

	// Schema-walk: open a fresh v2 store and inspect column names + DDL.
	s, _ := openTempStore(t)

	cols := columnNames(t, s.db, "permission_requests")
	for _, col := range cols {
		for _, bad := range forbidden {
			if strings.EqualFold(col, bad) {
				t.Errorf("schema column %q contains forbidden identifier %q", col, bad)
			}
		}
	}

	// Also check all DDL in sqlite_master.
	ddlRows, err := s.db.Query("SELECT sql FROM sqlite_master WHERE sql IS NOT NULL")
	if err != nil {
		t.Fatalf("query sqlite_master DDL: %v", err)
	}
	var ddls []string
	for ddlRows.Next() {
		var ddl string
		if err := ddlRows.Scan(&ddl); err != nil {
			_ = ddlRows.Close()
			t.Fatalf("scan DDL: %v", err)
		}
		ddls = append(ddls, ddl)
	}
	_ = ddlRows.Close()
	for _, ddl := range ddls {
		for _, bad := range forbidden {
			if strings.Contains(strings.ToLower(ddl), strings.ToLower(bad)) {
				t.Errorf("sqlite_master DDL contains forbidden identifier %q:\n%s", bad, ddl)
			}
		}
	}

	// Source-walk: scan production Go files in key packages.
	// Go tests run from the package dir (internal/store/), so the project root is ../../
	projectRoot := filepath.Join("..", "..")
	scanDirs := []string{
		filepath.Join(projectRoot, "internal", "hook"),
		filepath.Join(projectRoot, "internal", "store"),
		filepath.Join(projectRoot, "pkg", "api"),
		filepath.Join(projectRoot, "cmd", "agent-director"),
	}

	for _, dir := range scanDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue // directory may not exist yet
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(content)
			for _, bad := range forbidden {
				if strings.Contains(src, bad) {
					t.Errorf("production file %q contains forbidden identifier %q", path, bad)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
}
