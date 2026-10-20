package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
)

func TestV13MigrationStartupPreservation(t *testing.T) {
	for _, pre := range []string{"no column", "partial null", "partial marker", "partial bounded marker", "capacity null", "capacity marker", "both markers"} {
		t.Run(pre, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 12)
			seedV11StartupFixture(t, path, "", nil)
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `UPDATE spawns SET family_state='working',family_settled_state='waiting',family_changed_ms=99`)
			})
			var cols, requestCols []string
			var marker any
			var capacity any
			if pre == "partial marker" {
				marker = startupJSON(t, nil)
			}
			if pre == "partial bounded marker" {
				marker = startupJSON(t, func(p map[string]any) {
					p["session_id"] = strings.Repeat("s", 256)
					p["jsonl_path"] = "/" + strings.Repeat("x", 4095)
					p["starttime"] = strings.Repeat("x", 128)
				})
			}
			if pre == "capacity marker" || pre == "both markers" {
				capacity = capacityJSON(t, nil)
			}
			if pre == "both markers" {
				marker = startupJSON(t, nil)
			}
			withRaw(t, path, func(db *sql.DB) {
				cols = tableColumnNames(t, db, "spawns")
				requestCols = tableColumnNames(t, db, "permission_requests")
				if pre != "no column" && pre != "capacity null" && pre != "capacity marker" {
					mustExec(t, db, `ALTER TABLE spawns ADD COLUMN codex_startup TEXT`)
					mustExec(t, db, `UPDATE spawns SET codex_startup=?`, marker)
				}
				if pre == "capacity null" || pre == "capacity marker" || pre == "both markers" {
					mustExec(t, db, `ALTER TABLE spawns ADD COLUMN claude_capacity TEXT`)
					mustExec(t, db, `UPDATE spawns SET claude_capacity=?`, capacity)
				}
			})
			before := dbDump(t, path)
			prior := readQuotedRows(t, path, "spawns", "pre", "instance_id", cols)
			priorRequests := readQuotedRows(t, path, "permission_requests", "pre", "request_id", requestCols)
			id := assertOneStoreID(t, path)
			writeSentinel(t, dir, 12, schemaVersion)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _ := openTempStore(t)
			shape, wantShape := columnsByName(schemaShape(t, s.db)), columnsByName(schemaShape(t, fresh.db))
			if !reflect.DeepEqual(shape, wantShape) {
				t.Fatal("fresh and migrated schemas differ")
			}
			if s.StoreID() != id {
				t.Fatal("store identity changed")
			}
			sp, err := s.GetSpawn("pre")
			if err != nil || (sp.CodexStartup != nil) != (marker != nil) {
				t.Fatalf("startup=%+v %v", sp.CodexStartup, err)
			}
			if sp.ContextEvidence().FreshCodexWriter() {
				t.Fatal("migration fabricated a current writer")
			}
			if (sp.ClaudeCapacity != nil) != (capacity != nil) || sp.ContextEvidence().CurrentClaudeCapacity() != nil {
				t.Fatalf("capacity backfill/attribution: %+v", sp.ClaudeCapacity)
			}
			for run := 0; run < 2; run++ {
				if err := migrateV12toV13(s.db); err != nil {
					t.Fatal(err)
				}
			}
			if err := runMigrationChain(s.db, 13); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(prior, readQuotedRows(t, path, "spawns", "pre", "instance_id", cols)) {
				t.Fatal("migration changed existing spawn data")
			}
			if !reflect.DeepEqual(priorRequests, readQuotedRows(t, path, "permission_requests", "pre", "request_id", requestCols)) {
				t.Fatal("migration changed existing request data")
			}
			after := dbDump(t, path)
			for _, table := range []string{"session_history", "store_meta", "cursor_runtimes", "subagents", "subagent_launches"} {
				if !reflect.DeepEqual(before["rows "+table], after["rows "+table]) {
					t.Errorf("migration changed %s", table)
				}
			}
			assertSentinel(t, dir, false)
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			again, err := s.GetSpawn("pre")
			if err != nil || !reflect.DeepEqual(sp.CodexStartup, again.CodexStartup) || !reflect.DeepEqual(sp.ClaudeCapacity, again.ClaudeCapacity) {
				t.Fatalf("reentry/reopen changed marker: %+v %v", again.CodexStartup, err)
			}
		})
	}
}

func TestV13MigrationRefusesIncompatibleStartup(t *testing.T) {
	for _, name := range []string{"case conflict", "wrong type", "not null", "default", "malformed", "trailing", "unknown key", "invalid token", "empty session", "session too long", "relative path", "path too long", "zero pid", "empty start", "start too long", "oversized", "blob"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 12)
			definition := "codex_startup TEXT"
			var value any = startupJSON(t, nil)
			switch name {
			case "case conflict":
				definition = "Codex_Startup TEXT"
			case "wrong type":
				definition = "codex_startup BLOB"
			case "not null":
				definition = "codex_startup TEXT NOT NULL DEFAULT '{}'"
			case "default":
				definition = "codex_startup TEXT DEFAULT NULL"
			case "malformed":
				value = "{"
			case "trailing":
				value = value.(string) + "{}"
			case "unknown key":
				value = startupJSON(t, func(p map[string]any) { p["usage"] = 1 })
			case "invalid token":
				value = startupJSON(t, func(p map[string]any) { p["launch_token"] = "unqualified" })
			case "empty session":
				value = startupJSON(t, func(p map[string]any) { p["session_id"] = "" })
			case "session too long":
				value = startupJSON(t, func(p map[string]any) { p["session_id"] = strings.Repeat("s", 257) })
			case "relative path":
				value = startupJSON(t, func(p map[string]any) { p["jsonl_path"] = "relative.jsonl" })
			case "path too long":
				value = startupJSON(t, func(p map[string]any) { p["jsonl_path"] = "/" + strings.Repeat("x", 4096) })
			case "zero pid":
				value = startupJSON(t, func(p map[string]any) { p["pid"] = 0 })
			case "empty start":
				value = startupJSON(t, func(p map[string]any) { p["starttime"] = "" })
			case "start too long":
				value = startupJSON(t, func(p map[string]any) { p["starttime"] = strings.Repeat("x", 129) })
			case "oversized":
				value = strings.Repeat(" ", maxCodexStartupBytes) + value.(string)
			case "blob":
				value = []byte(value.(string))
			}
			seedV11StartupFixture(t, path, definition, value)
			before := dbDump(t, path)
			writeSentinel(t, dir, 12, schemaVersion)
			s, err := Open(path)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, ErrSchemaMismatch) {
				t.Fatalf("Open=%v; want schema mismatch", err)
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) || readUserVersion(t, path) != 12 {
				t.Fatal("refused migration changed schema, data or version")
			}
			assertSentinel(t, dir, true)
		})
	}
}

func TestV13MigrationCommitRollbackAndRetry(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 12)
	seedPreMigrationRow(t, path, 12)
	before := dbDump(t, path)
	writeSentinel(t, dir, 12, schemaVersion)
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
	if err = tx.QueryRow("SELECT count(*) FROM spawns").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err = migrateV12toV13(db); err == nil {
		t.Fatal("commit succeeded with blocking reader")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// Open closes a failed migration connection before returning its error.
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openRaw(t, path)
	if userVersion(t, db) != 12 {
		t.Fatal("failed commit stamped version")
	}
	for _, col := range tableColumnNames(t, db, "spawns") {
		if col == "codex_startup" || col == "claude_capacity" {
			t.Fatal("failed commit retained DDL")
		}
	}
	if !reflect.DeepEqual(before, dbDump(t, path)) {
		t.Fatal("failed commit changed persisted data or schema")
	}
	assertSentinel(t, dir, true)
	mustExec(t, db, "PRAGMA journal_mode=WAL")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if userVersion(t, s.db) != schemaVersion {
		t.Fatal("retry did not finish the current migration chain")
	}
	assertSentinel(t, dir, false)
}

func capacityJSON(t *testing.T, change func(map[string]any)) string {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(startupJSON(t, nil)), &p); err != nil {
		t.Fatal(err)
	}
	s := contextfix.Must(t, "claude-parent")
	var callback struct {
		Model  struct{ ID string }
		Window struct {
			Size int64 `json:"context_window_size"`
		} `json:"context_window"`
	}
	if err := json.Unmarshal(s.Callback, &callback); err != nil {
		t.Fatal(err)
	}
	p["version"], p["provider"] = "2.1.280", "anthropic"
	p["main"] = map[string]any{"model": callback.Model.ID, "window": callback.Window.Size, "response_uuid": s.ResponseUUID, "observed_at": s.ObservedAt, "invalidated": false}
	p["children"] = map[string]any{}
	if change != nil {
		change(p)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestV13MigrationRefusesIncompatibleCapacity(t *testing.T) {
	for _, name := range []string{"case conflict", "wrong type", "not null", "default", "malformed", "trailing", "unknown usage cache", "invalid token", "relative path", "zero pid", "empty start", "unknown provider", "zero window", "missing response", "invalid timestamp", "invalid child id", "oversized", "blob"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 12)
			seedV11StartupFixture(t, path, "", nil)
			definition := "claude_capacity TEXT"
			var value any = capacityJSON(t, nil)
			mutate := func(change func(map[string]any)) { value = capacityJSON(t, change) }
			switch name {
			case "case conflict":
				definition = "Claude_Capacity TEXT"
			case "wrong type":
				definition = "claude_capacity BLOB"
			case "not null":
				definition = "claude_capacity TEXT NOT NULL DEFAULT '{}'"
			case "default":
				definition = "claude_capacity TEXT DEFAULT NULL"
			case "malformed":
				value = "{"
			case "trailing":
				value = value.(string) + "{}"
			case "unknown usage cache":
				mutate(func(p map[string]any) { p["used_tokens"] = 1 })
			case "invalid token":
				mutate(func(p map[string]any) { p["launch_token"] = "invalid" })
			case "relative path":
				mutate(func(p map[string]any) { p["jsonl_path"] = "relative.jsonl" })
			case "zero pid":
				mutate(func(p map[string]any) { p["pid"] = 0 })
			case "empty start":
				mutate(func(p map[string]any) { p["starttime"] = "" })
			case "unknown provider":
				mutate(func(p map[string]any) { p["provider"] = "unverified" })
			case "zero window":
				mutate(func(p map[string]any) { p["main"].(map[string]any)["window"] = 0 })
			case "missing response":
				mutate(func(p map[string]any) { delete(p["main"].(map[string]any), "response_uuid") })
			case "invalid timestamp":
				mutate(func(p map[string]any) { p["main"].(map[string]any)["observed_at"] = "invalid" })
			case "invalid child id":
				mutate(func(p map[string]any) { p["children"] = map[string]any{"../other": p["main"]} })
			case "oversized":
				value = strings.Repeat(" ", maxClaudeCapacityBytes) + value.(string)
			case "blob":
				value = []byte(value.(string))
			}
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `ALTER TABLE spawns ADD COLUMN `+definition)
				mustExec(t, db, `UPDATE spawns SET claude_capacity=?`, value)
			})
			before := dbDump(t, path)
			writeSentinel(t, dir, 12, schemaVersion)
			s, err := Open(path)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, ErrSchemaMismatch) {
				t.Fatalf("Open=%v; want schema mismatch", err)
			}
			if !reflect.DeepEqual(before, dbDump(t, path)) || readUserVersion(t, path) != 12 {
				t.Fatal("refusal changed schema/data/version, including partially added Codex column")
			}
			assertSentinel(t, dir, true)
		})
	}
}
