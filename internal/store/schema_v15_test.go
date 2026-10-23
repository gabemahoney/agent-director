package store

// The v14→v15 hop (b.x72): spawns.codex_turn and spawns.codex_turn_ended,
// nullable TEXT, no default and no backfill.

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestV15MigrationPreservationAndReentry(t *testing.T) {
	bound := strings.Repeat("t", MaxCodexTurnBytes)
	for _, tc := range []struct {
		name        string
		specs       []columnSpec // the v15 columns the store already has
		turn, ended any
	}{
		{"no columns", nil, nil, nil},
		{"codex_turn alone, NULL", v15ColumnSpecs[:1], nil, nil},
		{"codex_turn alone, a turn", v15ColumnSpecs[:1], "01a16668-4862-7930-b160-2863caf991d1", nil},
		{"both, an open turn", v15ColumnSpecs, "01a16668-4862-7930-b160-2863caf991d1", nil},
		{"both, an ended turn at the size bound", v15ColumnSpecs, bound, bound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 14)
			seedV13PermissionFixture(t, path)
			preAddV14Columns(t, path, tc.specs)
			var spawnCols []string
			withRaw(t, path, func(db *sql.DB) {
				if len(tc.specs) > 0 {
					mustExec(t, db, `UPDATE spawns SET codex_turn=?`, tc.turn)
				}
				if len(tc.specs) > 1 {
					mustExec(t, db, `UPDATE spawns SET codex_turn_ended=?`, tc.ended)
				}
				spawnCols = tableColumnNames(t, db, "spawns")
			})
			before := dbDump(t, path)
			prior := readRawSpawn(t, path, "pre", spawnCols)
			id := assertOneStoreID(t, path)
			writeSentinel(t, dir, 14, schemaVersion)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _ := openTempStore(t)
			if !reflect.DeepEqual(columnsByName(schemaShape(t, s.db)), columnsByName(schemaShape(t, fresh.db))) {
				t.Fatal("fresh and upgraded schemas differ")
			}
			if s.StoreID() != id {
				t.Fatal("store identity changed")
			}
			if !reflect.DeepEqual(prior, readRawSpawn(t, path, "pre", spawnCols)) {
				t.Fatal("migration changed existing spawn data or a retained turn")
			}
			var added []string
			for _, c := range v15ColumnNames() {
				if !slices.Contains(spawnCols, c) {
					added = append(added, c)
				}
			}
			if len(added) > 0 {
				for c, v := range readRawSpawn(t, path, "pre", added) {
					if v != "NULL" {
						t.Errorf("added %s = %s; want NULL (no backfill)", c, v)
					}
				}
			}
			after := dbDump(t, path)
			if len(added) == 0 && !reflect.DeepEqual(before, after) {
				t.Fatal("migration of a store holding both columns changed it")
			}
			for _, table := range []string{"permission_requests", "session_history", "store_meta", "cursor_runtimes", "subagents", "subagent_launches"} {
				if !reflect.DeepEqual(before["rows "+table], after["rows "+table]) {
					t.Errorf("changed %s", table)
				}
			}
			for run := 0; run < 2; run++ {
				if err := migrateV14toV15(s.db); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(after, dbDump(t, path)) {
					t.Fatal("reentry changed retained data")
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			assertSentinel(t, dir, false)
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, dbDump(t, path)) {
				t.Fatal("reopen changed retained data")
			}
		})
	}
}

func TestV15MigrationRefusesIncompatibleColumns(t *testing.T) {
	for _, tc := range []struct {
		name, ddl, update, msg string
		bare                   bool // no seeded rows, so a NOT NULL column with no default can be added
	}{
		{"case conflict", `Codex_Turn TEXT`, "", "v14→v15 incompatible spawns.codex_turn", false},
		{"wrong type", `codex_turn_ended BLOB`, "", "v14→v15 incompatible spawns.codex_turn_ended", false},
		// No default, so only the NOT NULL check can refuse it.
		{"not null", `codex_turn TEXT NOT NULL`, "", "v14→v15 incompatible spawns.codex_turn", true},
		{"default", `codex_turn_ended TEXT DEFAULT NULL`, "", "v14→v15 incompatible spawns.codex_turn_ended", false},
		{"empty turn", `codex_turn TEXT`, `UPDATE spawns SET codex_turn=''`, "v14→v15 invalid spawns.codex_turn", false},
		{"oversized turn", `codex_turn TEXT`, `UPDATE spawns SET codex_turn=printf('%257s','x')`, "v14→v15 invalid spawns.codex_turn", false},
		// Only codex_turn_ended is present: the refusal must not add codex_turn.
		{"blob ended turn", `codex_turn_ended TEXT`, `UPDATE spawns SET codex_turn_ended=x'6162'`, "v14→v15 invalid spawns.codex_turn_ended", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 14)
			if !tc.bare {
				seedV13PermissionFixture(t, path)
			}
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `ALTER TABLE spawns ADD COLUMN `+tc.ddl)
				if tc.update != "" {
					mustExec(t, db, tc.update)
				}
			})
			before := dbDump(t, path)
			writeSentinel(t, dir, 14, schemaVersion)
			s, err := Open(path)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, ErrSchemaMismatch) || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("Open=%v; want a schema mismatch naming %q", err, tc.msg)
			}
			assertV15AdviceQuotes(t, tc.msg)
			if readUserVersion(t, path) != 14 || !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("refusal changed schema, data or version")
			}
			assertSentinel(t, dir, true)
		})
	}
}

// v15RefusalShape turns a refusal naming one spawns column into the generic
// "spawns.<column>" form the install skill's advice quotes.
var v15RefusalShape = regexp.MustCompile(`^(v14→v15 (?:incompatible|invalid) spawns)\.([a-z_]+)$`)

// v15InstallSkillRefusalAdvice is the whitespace-normalized "Authorized
// migration refuses Codex turn columns" paragraph of
// skills/install-agent-director/SKILL.md, which quotes the refusal names.
func v15InstallSkillRefusalAdvice(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../skills/install-agent-director/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "**Authorized migration refuses Codex turn columns**")
	if !ok {
		t.Fatal("install skill lacks the v14→v15 refusal advice paragraph")
	}
	para, _, _ := strings.Cut(rest, "\n\n")
	return strings.Join(strings.Fields(para), " ")
}

// assertV15AdviceQuotes fails unless the install skill's v14→v15 refusal
// advice quotes msg's generic form and names the column msg names, so an
// operator who meets msg finds the advice that covers it.
func assertV15AdviceQuotes(t *testing.T, msg string) {
	t.Helper()
	m := v15RefusalShape.FindStringSubmatch(msg)
	if m == nil {
		t.Fatalf("refusal %q is not of the form the install skill's advice quotes", msg)
	}
	advice := v15InstallSkillRefusalAdvice(t)
	if quoted := m[1] + ".<column>"; !strings.Contains(advice, `"`+quoted+`"`) {
		t.Errorf("install skill's v14→v15 refusal advice does not quote %q", quoted)
	}
	if !strings.Contains(advice, "`spawns."+m[2]+"`") {
		t.Errorf("install skill's v14→v15 refusal advice does not name spawns.%s", m[2])
	}
}

// TestV15RefusalFollowsInstallAdvice (b.x72) follows the install skill's
// "Authorized migration refuses Codex turn columns" advice step by step on a
// v13 store holding a codex_turn of the wrong type: the refusal is one the
// advice quotes and describes (its size bound is MaxCodexTurnBytes); the
// failed hop rolls back while the earlier v13→v14 hop stays committed; going
// back restores the matching pre-upgrade file set as it was; and once the
// incompatible state is resolved, the old authorization no longer applies to
// the resulting version, and rerunning the installer (a fresh authorization
// from it) completes the upgrade.
func TestV15RefusalFollowsInstallAdvice(t *testing.T) {
	const msg = "v14→v15 incompatible spawns.codex_turn"
	if advice := v15InstallSkillRefusalAdvice(t); !strings.Contains(advice, fmt.Sprintf("1 to %d bytes", MaxCodexTurnBytes)) {
		t.Errorf("install skill's v14→v15 refusal advice does not state the %d-byte bound", MaxCodexTurnBytes)
	}
	refused := func(t *testing.T) (dir, path string, preUpgrade map[string][]byte, before map[string]any) {
		t.Helper()
		dir = t.TempDir()
		path = makeVersionedDB(t, dir, 13)
		seedV13PermissionFixture(t, path)
		withRaw(t, path, func(db *sql.DB) { mustExec(t, db, `ALTER TABLE spawns ADD COLUMN codex_turn BLOB`) })
		before, preUpgrade = dbDump(t, path), readDBFileSet(t, path)
		writeSentinel(t, dir, 13, schemaVersion)
		s, err := Open(path)
		if s != nil {
			s.Close()
		}
		if !errors.Is(err, ErrSchemaMismatch) || !strings.Contains(err.Error(), msg) {
			t.Fatalf("Open=%v; want a schema mismatch naming %q", err, msg)
		}
		assertV15AdviceQuotes(t, msg)
		if got := readUserVersion(t, path); got != 14 {
			t.Fatalf("user_version after the refusal = %d; want 14, the earlier hop committed", got)
		}
		withRaw(t, path, func(db *sql.DB) {
			cols := tableColumnNames(t, db, "spawns")
			if !slices.Contains(cols, v14ColumnNames("spawns")[0]) {
				t.Error("the committed v13→v14 hop's columns are missing")
			}
			if slices.Contains(cols, "codex_turn_ended") {
				t.Error("the refused v14→v15 hop left codex_turn_ended added")
			}
		})
		assertSentinel(t, dir, true)
		return dir, path, preUpgrade, before
	}

	t.Run("going back", func(t *testing.T) {
		_, path, preUpgrade, before := refused(t)
		writeDBFileSet(t, path, preUpgrade)
		if readUserVersion(t, path) != 13 || !reflect.DeepEqual(before, dbDump(t, path)) {
			t.Fatal("restoring the pre-upgrade DB, WAL and SHM did not give back the pre-upgrade store")
		}
	})

	t.Run("resolved, installer rerun", func(t *testing.T) {
		dir, path, _, _ := refused(t)
		// The human help the advice asks for, standing in for whatever
		// resolves the incompatible column.
		withRaw(t, path, func(db *sql.DB) { mustExec(t, db, `ALTER TABLE spawns DROP COLUMN codex_turn`) })
		resolved := dbDump(t, path)
		if s, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
			if s != nil {
				s.Close()
			}
			t.Fatalf("Open with the v13 authorization = %v; want ErrSchemaMigrationRequired for the resulting v14", err)
		}
		if readUserVersion(t, path) != 14 || !reflect.DeepEqual(resolved, dbDump(t, path)) {
			t.Fatal("the stale authorization changed the store")
		}
		writeSentinel(t, dir, 14, schemaVersion)
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if userVersion(t, s.db) != schemaVersion {
			t.Fatal("the rerun's authorization did not finish the upgrade")
		}
		assertSentinel(t, dir, false)
		assertV15Defaults(t, path, "pre")
	})
}

// readDBFileSet reads the closed store at path with its -wal and -shm
// sidecars, each only when present: the pre-upgrade set the install skill's
// "Going back" restores.
func readDBFileSet(t *testing.T, path string) map[string][]byte {
	t.Helper()
	set := map[string][]byte{}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		set[p] = b
	}
	return set
}

// writeDBFileSet puts set back as the closed store at path's whole file set,
// removing any sidecar set lacks.
func writeDBFileSet(t *testing.T, path string, set map[string][]byte) {
	t.Helper()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		b, ok := set[p]
		if !ok {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestV15MigrationCommitRollbackAndRetry(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 14)
	seedV13PermissionFixture(t, path)
	before := dbDump(t, path)
	writeSentinel(t, dir, 14, schemaVersion)
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
	if err := tx.QueryRow("SELECT count(*) FROM spawns").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := migrateV14toV15(db); err == nil {
		t.Fatal("commit succeeded with blocking reader")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if readUserVersion(t, path) != 14 || !reflect.DeepEqual(before, dbDump(t, path)) {
		t.Fatal("failed commit retained DDL, data or version")
	}
	assertSentinel(t, dir, true)
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "PRAGMA journal_mode=WAL") })
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if userVersion(t, s.db) != schemaVersion {
		t.Fatal("retry did not finish the migration")
	}
	assertSentinel(t, dir, false)
}
