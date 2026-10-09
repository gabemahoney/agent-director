package store

// Schema v5 (b.fmk, SR-5.1, SR-5.4, SR-20.6): the v4 history fixture's rows
// after the hop (AC-REUSE-25, AC-RES-20, AC-FM-14), the store id, the
// documented v5 → v4 downgrade, and the guide match of every downgrade
// recipe. Fixtures: migration_fixtures_test.go.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// wellFormedID is the SR-5.1 store id form: 16 lowercase hex characters.
var wellFormedID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// assertOneStoreID fails unless store_meta holds exactly one row, a
// well-formed TEXT store_id, and returns it.
func assertOneStoreID(t *testing.T, path string) string {
	t.Helper()
	rows, present := readStoreMeta(t, path)
	if !present || len(rows) != 1 || rows[0].key != "store_id" || rows[0].typ != "text" || !wellFormedID.MatchString(rows[0].value) {
		t.Fatalf("store_meta = %+v (present %v); want one well-formed TEXT store_id", rows, present)
	}
	return rows[0].value
}

// openStoreID opens path with open, returns StoreID() and closes the store.
func openStoreID(t *testing.T, open func(string) (*Store, error), path string) string {
	t.Helper()
	s, err := open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	id := s.StoreID()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return id
}

// newClosedStore creates a store in a temp dir, closes it, and returns its
// path and StoreID().
func newClosedStore(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	return path, openStoreID(t, OpenOrInit, path)
}

// TestV5MigrationKeepsV4Rows: after the authorised hop every v4 value and
// history entry of the fixture reads back exactly as seeded, each row and
// entry at life 0 with the ordinary v5 defaults (the pending row's launch start
// NULL), GetSpawn and ListSpawns agree, and history reads at the row's life.
func TestV5MigrationKeepsV4Rows(t *testing.T) {
	f := makeV4HistoryFixture(t, t.TempDir())
	migrateV4Fixture(t, f)
	assertOneStoreID(t, f.path)
	for _, id := range f.ids {
		assertV5Defaults(t, f.path, id, len(f.history[id]))
		if got := readRawSpawn(t, f.path, id, f.spawnsCols); !reflect.DeepEqual(got, f.rows[id]) {
			t.Errorf("%s v4 values changed:\n got  %v\n want %v", id, got, f.rows[id])
		}
		if got := readRawHistory(t, f.path, id, f.historyCols); !reflect.DeepEqual(got, f.history[id]) {
			t.Errorf("%s history changed:\n got  %v\n want %v", id, got, f.history[id])
		}
	}

	s, err := Open(f.path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer s.Close()
	listed, err := s.ListSpawns(ListFilters{})
	if err != nil || len(listed) != len(f.ids) {
		t.Fatalf("ListSpawns = %d rows, %v; want %d", len(listed), err, len(f.ids))
	}
	for _, fromList := range listed {
		id := fromList.ClaudeInstanceID
		sp, err := s.GetSpawn(id)
		if err != nil {
			t.Fatalf("GetSpawn(%s): %v", id, err)
		}
		if !reflect.DeepEqual(sp, fromList) {
			t.Errorf("%s: GetSpawn %+v differs from ListSpawns %+v", id, sp, fromList)
		}
		assertSpawnMatchesRaw(t, sp, f.rows[id])
		if sp.LifeNumber != 0 {
			t.Errorf("%s: LifeNumber = %d; want 0", id, sp.LifeNumber)
		}
		hist, err := s.ListSessionHistory(id, sp.LifeNumber)
		if err != nil || len(hist) != len(f.history[id]) {
			t.Fatalf("%s: ListSessionHistory = %d entries, %v; want %d", id, len(hist), err, len(f.history[id]))
		}
		for i, e := range hist {
			want := f.history[id][i]
			if e.ClaudeSessionID != unquote(want["claude_session_id"]) || e.JSONLPath != unquote(want["jsonl_path"]) {
				t.Errorf("%s history[%d] = {%q %q}; want {%s %s}", id, i, e.ClaudeSessionID, e.JSONLPath,
					want["claude_session_id"], want["jsonl_path"])
			}
		}
	}
}

// assertSpawnMatchesRaw checks a GetSpawn row against its raw v4 values.
func assertSpawnMatchesRaw(t *testing.T, sp Spawn, raw map[string]string) {
	t.Helper()
	pid := ""
	if sp.PID != 0 {
		pid = strconv.Itoa(sp.PID)
	}
	args, _ := json.Marshal(sp.ClaudeArgs)
	got := map[string]string{
		"parent_id": sp.ParentID, "state": sp.State, "cwd": sp.CWD, "tmux_session_name": sp.TmuxSessionName,
		"relay_mode": sp.RelayMode, "jsonl_path": sp.JSONLPath, "claude_session_id": sp.ClaudeSessionID,
		"pid": pid, "proc_starttime": sp.ProcStarttime, "liveness_unverified_since": sp.LivenessUnverifiedSince,
		"liveness_note": sp.LivenessNote, "claude_args": string(args),
	}
	for col, g := range got {
		if want := unquote(raw[col]); g != want {
			t.Errorf("%s: GetSpawn %s = %q; want %q", sp.ClaudeInstanceID, col, g, want)
		}
	}
	for col, m := range map[string]map[string]string{"labels": sp.Labels, "extra_env": sp.ExtraEnv} {
		var want map[string]string
		if err := json.Unmarshal([]byte(unquote(raw[col])), &want); err != nil || !reflect.DeepEqual(m, want) {
			t.Errorf("%s: GetSpawn %s = %v; want %v (%v)", sp.ClaudeInstanceID, col, m, want, err)
		}
	}
	if (sp.EndedAt == nil) != (raw["ended_at"] == "NULL") {
		t.Errorf("%s: GetSpawn EndedAt = %v; raw ended_at %s", sp.ClaudeInstanceID, sp.EndedAt, raw["ended_at"])
	}
}

// TestStoreIDKept: a fresh store holds one well-formed id, StoreID(), unlike
// another fresh store's; a reopen (Open or OpenOrInit), a fresh-create re-run
// (user_version stamped 0) and a copy of the files all keep it (SR-5.1, SR-5.4).
func TestStoreIDKept(t *testing.T) {
	path, id := newClosedStore(t)
	if raw := assertOneStoreID(t, path); raw != id {
		t.Errorf("StoreID() = %q; raw store_id = %q", id, raw)
	}
	if _, other := newClosedStore(t); other == id {
		t.Errorf("two fresh stores share store_id %q", id)
	}
	for name, open := range map[string]func(string) (*Store, error){"Open": Open, "OpenOrInit": OpenOrInit} {
		if got := openStoreID(t, open, path); got != id {
			t.Errorf("%s after reopen: StoreID() = %q; want %q", name, got, id)
		}
	}
	stampUserVersion(t, path, 0)
	if got := openStoreID(t, OpenOrInit, path); got != id || readUserVersion(t, path) != schemaVersion {
		t.Errorf("fresh-create re-run: StoreID() = %q, user_version %d; want %q, %d", got, readUserVersion(t, path), id, schemaVersion)
	}
	assertOneStoreID(t, path)

	restored := filepath.Join(t.TempDir(), "state.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || os.WriteFile(restored+suffix, data, 0o600) != nil {
			t.Fatalf("copy %s%s: %v", path, suffix, err)
		}
	}
	if got := openStoreID(t, Open, restored); got != id {
		t.Errorf("restored copy StoreID() = %q; want %q", got, id)
	}
}

// TestStoreID_MissingOrMalformedFailsOpen: a closed store whose store_id row
// or store_meta table is gone, or whose id is malformed, fails Open and
// OpenOrInit with ErrSchemaMismatch, never shows the value, and is left
// byte-identical (SR-20.6).
func TestStoreID_MissingOrMalformedFailsOpen(t *testing.T) {
	cases := []struct {
		name  string
		edit  string
		value any
	}{
		{"row deleted", `DELETE FROM store_meta WHERE key = 'store_id'`, nil},
		{"table dropped", `DROP TABLE store_meta`, nil},
		{"uppercase hex", "", "0123456789ABCDEF"},
		{"fifteen characters", "", "0123456789abcde"},
		{"non-hex character", "", "0123456789abcdeg"},
		{"blob of well-formed hex", "", []byte("fedcba9876543210")},
	}
	for _, tc := range cases {
		for name, open := range map[string]func(string) (*Store, error){"Open": Open, "OpenOrInit": OpenOrInit} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				path, origID := newClosedStore(t)
				withRaw(t, path, func(db *sql.DB) {
					if tc.edit != "" {
						mustExec(t, db, tc.edit)
					} else {
						mustExec(t, db, `UPDATE store_meta SET value = ? WHERE key = 'store_id'`, tc.value)
					}
				})
				before := snapshotDBBytes(t, path)
				s, err := open(path)
				if err == nil {
					_ = s.Close()
					t.Fatal("open succeeded; want ErrSchemaMismatch")
				}
				if !errors.Is(err, ErrSchemaMismatch) {
					t.Errorf("err = %v; want ErrSchemaMismatch", err)
				}
				if v := fmt.Sprintf("%s", tc.value); (tc.value != nil && strings.Contains(err.Error(), v)) || strings.Contains(err.Error(), origID) {
					t.Errorf("err %q shows the stored or original id", err)
				}
				assertDBBytesUnchanged(t, path, before)
			})
		}
	}
}

// guideRecipe parses the first SQL block after the guide's heading (e.g.
// "#### v5 → v4") into its statements and, apart, its frame (.bail on, BEGIN,
// COMMIT).
func guideRecipe(t *testing.T, heading string) (stmts, frame []string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "migration-guide.md"))
	if err != nil {
		t.Fatalf("read migration guide: %v", err)
	}
	_, section, ok := strings.Cut(string(data), "\n"+heading)
	_, block, ok2 := strings.Cut(section, "```sql\n")
	if !ok || !ok2 {
		t.Fatalf("migration guide has no %q sql block", heading)
	}
	block, _, _ = strings.Cut(block, "```")
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "--"):
		case line == ".bail on" || line == "BEGIN;" || line == "COMMIT;":
			frame = append(frame, line)
		case strings.HasSuffix(line, ";"):
			stmts = append(stmts, strings.TrimSuffix(line, ";"))
		default:
			t.Fatalf("guide recipe line %q is not one whole statement", line)
		}
	}
	return stmts, frame
}

// TestDowngradeRecipe_MatchesGuide: each downgrade recipe's test copy
// (v7ToV6RecipeStatements, v6ToV5RecipeStatements, v5ToV4RecipeStatements) is
// the guide's recipe statement for statement, run in bail mode in one
// transaction.
func TestDowngradeRecipe_MatchesGuide(t *testing.T) {
	for _, r := range []struct {
		heading string
		want    []string
	}{
		{"#### v7 → v6", v7ToV6RecipeStatements},
		{"#### v6 → v5", v6ToV5RecipeStatements},
		{"#### v5 → v4", v5ToV4RecipeStatements},
	} {
		t.Run(r.heading, func(t *testing.T) {
			stmts, frame := guideRecipe(t, r.heading)
			if !slices.Equal(stmts, r.want) {
				t.Errorf("guide recipe =\n  %s\nwant the test copy =\n  %s",
					strings.Join(stmts, "\n  "), strings.Join(r.want, "\n  "))
			}
			if want := []string{".bail on", "BEGIN;", "COMMIT;"}; !slices.Equal(frame, want) {
				t.Errorf("guide recipe frame = %q; want %q", frame, want)
			}
		})
	}
}

// TestDowngradeRecipe_KeepsRowsThenRemigratesToDefaults: on a store taken
// back to v5 first (the v7 → v6 and v6 → v5 recipes), the recipe leaves v4 with no
// store_meta or v5 column and every v4 value of a reused row and an opted-out
// row; an authorised re-migration gives the ordinary defaults (earlier lives
// read as current until the next reuse; the opt-out is lost; no launch owner)
// and one new store id (SR-5.4).
func TestDowngradeRecipe_KeepsRowsThenRemigratesToDefaults(t *testing.T) {
	path, oldID := newClosedStore(t)
	reused, optedOut := seedV5DowngradeRows(t, path)
	applyRecipe(t, path, v7ToV6RecipeStatements)
	applyRecipe(t, path, v6ToV5RecipeStatements)
	cols := map[string][]string{}
	withRaw(t, path, func(db *sql.DB) {
		for _, table := range []string{"spawns", "session_history"} {
			for _, c := range tableColumnNames(t, db, table) {
				if _, v5 := v5ColumnSpecByKey(table + "." + c); !v5 {
					cols[table] = append(cols[table], c)
				}
			}
		}
	})
	read := func(id string) any {
		return []any{readRawSpawn(t, path, id, cols["spawns"]), readRawHistory(t, path, id, cols["session_history"])}
	}
	before := map[string]any{reused: read(reused), optedOut: read(optedOut)}

	applyV5ToV4Recipe(t, path)
	if _, present := readStoreMeta(t, path); present || readUserVersion(t, path) != 4 {
		t.Errorf("after the recipe: store_meta present %v, user_version %d; want absent, 4", present, readUserVersion(t, path))
	}
	assertNoV5Columns(t, path)
	for id, want := range before {
		if got := read(id); !reflect.DeepEqual(got, want) {
			t.Errorf("%s after the recipe = %+v; want the v4 values kept %+v", id, got, want)
		}
	}

	writeSentinel(t, filepath.Dir(path), 4, schemaVersion)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(authorised re-migration): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if newID := assertOneStoreID(t, path); newID == oldID || newID != s.StoreID() {
		t.Errorf("re-migrated store_id = %q, StoreID() %q; want one new id, not %q", newID, s.StoreID(), oldID)
	}
	for id, want := range before {
		if got := read(id); !reflect.DeepEqual(got, want) {
			t.Errorf("%s after re-migration = %+v; want the v4 values kept %+v", id, got, want)
		}
	}
	assertV5Defaults(t, path, reused, 4)
	assertV5Defaults(t, path, optedOut, 1) // no_pre_trust 0: the opt-out is lost
	assertV6Defaults(t, path, reused)
	assertV6Defaults(t, path, optedOut)

	visible := func(life int64) (out []string) {
		t.Helper()
		entries, err := s.ListSessionHistory(reused, life)
		if err != nil {
			t.Fatalf("ListSessionHistory(%d): %v", life, err)
		}
		for _, e := range entries {
			out = append(out, e.ClaudeSessionID)
		}
		return out
	}
	if got, want := visible(0), []string{"sess-r-2-prior", "sess-r-1", "sess-r-0b", "sess-r-0a"}; !slices.Equal(got, want) {
		t.Errorf("current life's history = %q; want every earlier life's entry %q", got, want)
	}
	rr, found, err := s.ReadForReuse(reused)
	if err != nil || !found {
		t.Fatalf("ReadForReuse = found %v, %v", found, err)
	}
	fresh := Spawn{CWD: "/work/reused", TmuxSessionName: "ad-reused", RelayMode: "off",
		StartedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), LaunchStartedAtMillis: 1772323200000,
		Identity: LaunchIdentity{Token: "00112233aabbccdd", Socket: "/tmp/ad-sock"}}
	if res, _, _, err := s.ResetForReuse(reused, rr.Snapshot, fresh); err != nil || res != CondApplied {
		t.Fatalf("ResetForReuse = %v, %v; want CondApplied", res, err)
	}
	if sp, err := s.GetSpawn(reused); err != nil || len(visible(sp.LifeNumber)) != 0 {
		t.Errorf("history after the next reuse (life %d, %v) = %q; want none", sp.LifeNumber, err, visible(sp.LifeNumber))
	}
}
