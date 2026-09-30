package store

// schema_v5_store_id_test.go — store_meta.store_id coverage (SR-5.1, SR-5.4,
// SR-20.6 "The store id"; WD 2026-09-29 STORE). Template:
// schema_v5_migration_test.go. Fixtures live in migration_fixtures_test.go
// (SR-20.3); this file adds no inline SQL.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// wellFormedID is the SR-5.1 store id form: 16 lowercase hex characters.
var wellFormedID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// assertOneStoreID fails unless store_meta holds exactly one row, a well-formed
// TEXT store_id, and returns its value.
func assertOneStoreID(t *testing.T, path string) string {
	t.Helper()
	m := readStoreMetaRaw(t, path)
	if !m.present {
		t.Fatalf("store_meta absent; want one store_id row")
	}
	id, ok := m.storeID()
	if !ok || len(m.rows) != 1 {
		t.Fatalf("store_meta rows = %+v; want exactly one store_id row", m.rows)
	}
	if m.rows[0].typ != "text" || !wellFormedID.MatchString(id) {
		t.Fatalf("store_id = %q (%s); want 16 lowercase hex TEXT", id, m.rows[0].typ)
	}
	return id
}

// openStoreID opens path with open, returns StoreID(), and closes the store.
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

// newClosedStore creates a fresh store under a temp dir, closes it, and returns
// its path and StoreID().
func newClosedStore(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	return path, openStoreID(t, OpenOrInit, path)
}

// TestStoreID_FreshStore: a fresh store holds one well-formed id equal to
// StoreID(); a second fresh store gets a different id.
func TestStoreID_FreshStore(t *testing.T) {
	pathA, idA := newClosedStore(t)
	if raw := assertOneStoreID(t, pathA); raw != idA {
		t.Errorf("StoreID() = %q; raw store_id = %q", idA, raw)
	}
	_, idB := newClosedStore(t)
	if idA == idB {
		t.Errorf("two fresh stores share store_id %q", idA)
	}
}

// TestStoreID_ReopenKeepsID: close and reopen (Open and OpenOrInit) gives the
// same StoreID() and leaves one row.
func TestStoreID_ReopenKeepsID(t *testing.T) {
	path, id := newClosedStore(t)
	for name, open := range map[string]func(string) (*Store, error){"Open": Open, "OpenOrInit": OpenOrInit} {
		if got := openStoreID(t, open, path); got != id {
			t.Errorf("%s after reopen: StoreID() = %q; want %q", name, got, id)
		}
	}
	if raw := assertOneStoreID(t, path); raw != id {
		t.Errorf("raw store_id = %q after reopen; want %q", raw, id)
	}
}

// TestStoreID_AuthorizedFromV4: the authorised hop gives the v4 history fixture
// one well-formed id, which the next open reads.
func TestStoreID_AuthorizedFromV4(t *testing.T) {
	f := makeV4HistoryFixture(t, t.TempDir())
	if readStoreMetaRaw(t, f.path).present {
		t.Fatalf("v4 fixture already has store_meta")
	}
	migrateV4Fixture(t, f)

	raw := assertOneStoreID(t, f.path)
	if got := openStoreID(t, Open, f.path); got != raw {
		t.Errorf("StoreID() = %q; raw store_id = %q", got, raw)
	}
}

// TestStoreID_IdempotentReentry: the hop never adds a second row or replaces an
// existing id, whether run twice or re-entered with store_meta already present.
func TestStoreID_IdempotentReentry(t *testing.T) {
	const preID = "0123456789abcdef"
	cases := []struct {
		name    string
		preMeta bool   // create store_meta before the first run
		preID   string // its store_id row; "" for an empty table
		runs    int
	}{
		{"hop run twice keeps the first id", false, "", 2},
		{"store_meta present holding an id", true, preID, 1},
		{"store_meta present but empty", true, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := makeVersionedDB(t, t.TempDir(), 4)
			if tc.preMeta {
				preAddStoreMeta(t, path, tc.preID)
			}
			db := openRaw(t, path)
			first := tc.preID
			for run := 1; run <= tc.runs; run++ {
				if err := migrateV4toV5(db); err != nil {
					t.Fatalf("migrateV4toV5 run %d: %v", run, err)
				}
				got := assertOneStoreID(t, path)
				if first == "" {
					first = got
				}
				if got != first {
					t.Errorf("run %d: store_id = %q; want kept %q", run, got, first)
				}
			}
			if v := readUserVersionDB(t, db); v != 5 {
				t.Errorf("user_version = %d; want 5", v)
			}
		})
	}
}

// TestStoreID_RollbackOnFailedStoreMetaStep: a failing store_meta step rolls
// back the whole hop — v4, no new columns, no store_id, rows kept, sentinel kept.
func TestStoreID_RollbackOnFailedStoreMetaStep(t *testing.T) {
	f := makeV4HistoryFixture(t, t.TempDir())
	breakV5StoreMetaStep(t, f.path)
	writeSentinel(t, f.dir, 4, schemaVersion)

	s, err := Open(f.path)
	if err == nil {
		_ = s.Close()
		t.Fatalf("Open(v4 with failing store_meta step) succeeded; want error")
	}
	if errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("Open err = %v; want a migration failure, not a refusal", err)
	}

	if v := readUserVersion(t, f.path); v != 4 {
		t.Errorf("user_version = %d after failed hop; want 4", v)
	}
	assertNoV5Columns(t, f.path)
	if m := readStoreMetaRaw(t, f.path); len(m.rows) != 0 {
		t.Errorf("store_meta rows = %+v after failed hop; want none", m.rows)
	}
	for _, id := range f.ids {
		if got := readRawSpawn(t, f.path, id, f.spawnsCols); !reflect.DeepEqual(got, f.rows[id]) {
			t.Errorf("%s row changed after rollback:\n got  %v\n want %v", id, got, f.rows[id])
		}
		if got := readRawHistory(t, f.path, id, f.historyCols); !reflect.DeepEqual(got, f.history[id]) {
			t.Errorf("%s history changed after rollback:\n got  %v\n want %v", id, got, f.history[id])
		}
	}
	assertSentinelPresent(t, f.dir)
}

// TestStoreID_FreshCreateRerunKeepsID: re-running the fresh-create path on a
// store whose tables exist (user_version stamped 0) keeps the existing id.
func TestStoreID_FreshCreateRerunKeepsID(t *testing.T) {
	path, id := newClosedStore(t)
	stampUserVersion(t, path, 0)

	if got := openStoreID(t, OpenOrInit, path); got != id {
		t.Errorf("StoreID() after fresh-create re-run = %q; want %q", got, id)
	}
	if raw := assertOneStoreID(t, path); raw != id {
		t.Errorf("raw store_id = %q; want %q", raw, id)
	}
	if v := readUserVersion(t, path); v != schemaVersion {
		t.Errorf("user_version = %d; want %d", v, schemaVersion)
	}
}

// TestStoreID_RestoredCopyKeepsID: a closed store's files copied to a new path,
// as a restore would, open with the same StoreID().
func TestStoreID_RestoredCopyKeepsID(t *testing.T) {
	path, id := newClosedStore(t)
	restored := filepath.Join(t.TempDir(), "state.db")
	src, dst := dbSidecars(path), dbSidecars(restored)
	for i := range src {
		data, err := os.ReadFile(src[i])
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", src[i], err)
		}
		if err := os.WriteFile(dst[i], data, 0o600); err != nil {
			t.Fatalf("write %s: %v", dst[i], err)
		}
	}

	if got := openStoreID(t, Open, restored); got != id {
		t.Errorf("restored StoreID() = %q; want %q", got, id)
	}
}

// TestStoreID_MissingOrMalformedFailsOpen: a closed v5 store whose store_id row
// (or whole store_meta table) is gone, or whose value is malformed, fails Open
// and OpenOrInit with ErrSchemaMismatch, never shows the value, and is left
// byte-identical.
func TestStoreID_MissingOrMalformedFailsOpen(t *testing.T) {
	cases := []struct {
		name      string
		value     any  // nil deletes the row (or the table, with dropTable)
		dropTable bool // drop store_meta instead of editing its row
	}{
		{name: "row deleted"},
		{name: "table dropped", dropTable: true},
		{name: "uppercase hex", value: "0123456789ABCDEF"},
		{name: "planted secret", value: "SECRETVALUEXXXXX"},
		{name: "fifteen characters", value: "0123456789abcde"},
		{name: "seventeen characters", value: "0123456789abcdef0"},
		{name: "non-hex character", value: "0123456789abcdeg"},
		{name: "empty", value: ""},
		{name: "blob of well-formed hex", value: []byte("fedcba9876543210")},
	}
	openers := []struct {
		name string
		open func(string) (*Store, error)
	}{{"Open", Open}, {"OpenOrInit", OpenOrInit}}
	for _, tc := range cases {
		for _, o := range openers {
			t.Run(tc.name+"/"+o.name, func(t *testing.T) {
				path, origID := newClosedStore(t)
				switch {
				case tc.dropTable:
					dropStoreMetaTable(t, path)
				case tc.value == nil:
					deleteStoreIDRow(t, path)
				default:
					setStoreIDRaw(t, path, tc.value)
				}
				before := snapshotDBBytes(t, path)

				s, err := o.open(path)
				if err == nil {
					_ = s.Close()
					t.Fatalf("%s succeeded; want ErrSchemaMismatch", o.name)
				}
				if !errors.Is(err, ErrSchemaMismatch) {
					t.Errorf("%s err = %v; want errors.Is ErrSchemaMismatch", o.name, err)
				}
				if v := fmt.Sprintf("%s", tc.value); tc.value != nil && v != "" && strings.Contains(err.Error(), v) {
					t.Errorf("%s err %q contains the stored value", o.name, err)
				}
				if strings.Contains(err.Error(), origID) {
					t.Errorf("%s err %q contains the original store id", o.name, err)
				}
				assertDBBytesUnchanged(t, path, before)
			})
		}
	}
}

// TestStoreID_DowngradeRecipe: the documented v5→v4 recipe leaves v4 with no
// store_meta and none of the thirteen columns; an authorised re-migration then
// creates exactly one new, different id (SR-5.4).
func TestStoreID_DowngradeRecipe(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T) (dir, path string)
	}{
		{"fresh store", func(t *testing.T) (string, string) {
			path, _ := newClosedStore(t)
			return filepath.Dir(path), path
		}},
		{"store migrated from v4", func(t *testing.T) (string, string) {
			f := makeV4HistoryFixture(t, t.TempDir())
			migrateV4Fixture(t, f)
			return f.dir, f.path
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, path := tc.build(t)
			before := assertOneStoreID(t, path)

			applyV5ToV4Recipe(t, path)

			if v := readUserVersion(t, path); v != 4 {
				t.Errorf("user_version = %d after recipe; want 4", v)
			}
			if readStoreMetaRaw(t, path).present {
				t.Errorf("store_meta still present after recipe")
			}
			assertNoV5Columns(t, path)

			writeSentinel(t, dir, 4, schemaVersion)
			after := openStoreID(t, Open, path)
			if raw := assertOneStoreID(t, path); raw != after {
				t.Errorf("StoreID() = %q; raw store_id = %q", after, raw)
			}
			if after == before {
				t.Errorf("re-migration kept pre-rollback id %q; want a new id", before)
			}
		})
	}
}
