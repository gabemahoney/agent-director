package store

// The v5 → v4 emergency downgrade (SR-5.4, docs/migration-guide.md "#### v5 →
// v4"): the recipe the tests run is the guide's, it keeps every v4 value, and
// a re-migration gives the ordinary defaults. Fixtures: migration_fixtures_test.go.

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// guideV5ToV4Recipe parses the guide's "#### v5 → v4" SQL block into its
// statements, without `.bail on`, comments, BEGIN and COMMIT, which it returns apart.
func guideV5ToV4Recipe(t *testing.T) (stmts, frame []string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "migration-guide.md"))
	if err != nil {
		t.Fatalf("read migration guide: %v", err)
	}
	_, section, ok := strings.Cut(string(data), "\n#### v5 → v4")
	if !ok {
		t.Fatalf("migration guide has no \"#### v5 → v4\" section")
	}
	_, block, ok := strings.Cut(section, "```sql\n")
	if !ok {
		t.Fatalf("\"#### v5 → v4\" has no sql block")
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

// TestDowngradeRecipe_MatchesGuide: v5ToV4RecipeStatements is the guide's
// recipe statement for statement, run in bail mode inside one transaction.
func TestDowngradeRecipe_MatchesGuide(t *testing.T) {
	stmts, frame := guideV5ToV4Recipe(t)
	if !slices.Equal(stmts, v5ToV4RecipeStatements) {
		t.Errorf("guide recipe =\n  %s\nwant v5ToV4RecipeStatements =\n  %s",
			strings.Join(stmts, "\n  "), strings.Join(v5ToV4RecipeStatements, "\n  "))
	}
	if want := []string{".bail on", "BEGIN;", "COMMIT;"}; !slices.Equal(frame, want) {
		t.Errorf("guide recipe frame = %q; want %q", frame, want)
	}
}

// v4Columns returns table's columns that v4 also has, in table order.
func v4Columns(t *testing.T, path, table string) []string {
	t.Helper()
	db := openExistingRaw(t, "v4Columns", path)
	defer func() { _ = db.Close() }()
	var out []string
	for _, c := range tableColumnNames(t, db, table) {
		if _, v5 := v5ColumnSpecByKey(table + "." + c); !v5 {
			out = append(out, c)
		}
	}
	return out
}

// v4Values is one id's spawns row and history entries in their v4 columns.
type v4Values struct {
	row     map[string]string
	history []map[string]string
}

// TestDowngradeRecipe_KeepsRowsThenRemigratesToDefaults: the recipe keeps every
// v4 value of a reused row and an opted-out row; a re-migration gives the
// ordinary defaults, so earlier lives read as current until the next reuse and
// the opt-out is lost (SR-5.4).
func TestDowngradeRecipe_KeepsRowsThenRemigratesToDefaults(t *testing.T) {
	path, _ := newClosedStore(t)
	rows := seedV5DowngradeRows(t, path)
	spawnCols, historyCols := v4Columns(t, path, "spawns"), v4Columns(t, path, "session_history")
	read := func(id string) v4Values {
		return v4Values{readRawSpawn(t, path, id, spawnCols), readRawHistory(t, path, id, historyCols)}
	}
	ids := []string{rows.reused, rows.optedOut}
	before := map[string]v4Values{}
	for _, id := range ids {
		before[id] = read(id)
	}

	applyV5ToV4Recipe(t, path)
	for _, id := range ids {
		if got := read(id); !reflect.DeepEqual(got, before[id]) {
			t.Errorf("%s after the recipe = %+v; want the v4 values kept %+v", id, got, before[id])
		}
	}

	writeSentinel(t, filepath.Dir(path), 4, schemaVersion)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(authorised re-migration): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, id := range ids {
		if got := read(id); !reflect.DeepEqual(got, before[id]) {
			t.Errorf("%s after re-migration = %+v; want the v4 values kept %+v", id, got, before[id])
		}
	}
	assertV5Defaults(t, path, rows.reused, rows.reusedHistory)
	assertV5Defaults(t, path, rows.optedOut, 1) // no_pre_trust 0: the opt-out is lost

	visible := func(life int64) []string {
		t.Helper()
		entries, err := s.ListSessionHistory(rows.reused, life)
		if err != nil {
			t.Fatalf("ListSessionHistory(%d): %v", life, err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.ClaudeSessionID)
		}
		return out
	}
	earlier := []string{"sess-r-2-prior", "sess-r-1", "sess-r-0b", "sess-r-0a"}
	if got := visible(0); !slices.Equal(got, earlier) {
		t.Errorf("current life's history = %q; want every earlier life's entry %q", got, earlier)
	}

	rr, found, err := s.ReadForReuse(rows.reused)
	if err != nil || !found {
		t.Fatalf("ReadForReuse = found %v, %v", found, err)
	}
	fresh := Spawn{CWD: "/work/reused", TmuxSessionName: "ad-reused", RelayMode: "off",
		StartedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), LaunchStartedAtMillis: 1772323200000,
		Identity: LaunchIdentity{Token: "00112233aabbccdd", Socket: "/tmp/ad-sock"}}
	if res, _, _, err := s.ResetForReuse(rows.reused, rr.Snapshot, fresh); err != nil || res != CondApplied {
		t.Fatalf("ResetForReuse = %v, %v; want CondApplied", res, err)
	}
	sp, err := s.GetSpawn(rows.reused)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if got := visible(sp.LifeNumber); len(got) != 0 {
		t.Errorf("history after the next reuse (life %d) = %q; want none", sp.LifeNumber, got)
	}
}
