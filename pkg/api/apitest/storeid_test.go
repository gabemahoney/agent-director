package apitest

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// storeIDForm is the store id form of SR-5.1: 16 lowercase hex characters.
var storeIDForm = regexp.MustCompile(`^[0-9a-f]{16}$`)

// initStoreID creates a fresh store under t.TempDir() and returns its path.
func initStoreID(t *testing.T) string {
	t.Helper()
	dbPath, err := InitStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	return dbPath
}

// openedStoreID returns the StoreID() of a newly opened store at dbPath.
func openedStoreID(t *testing.T, dbPath string) string {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck
	return s.StoreID()
}

// mustReadStoreID returns ReadStoreID(dbPath) or fails the test.
func mustReadStoreID(t *testing.T, dbPath string) string {
	t.Helper()
	id, err := ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return id
}

// TestReadStoreID_MatchesOpenedStore checks the raw read agrees with the id the store reads at open.
func TestReadStoreID_MatchesOpenedStore(t *testing.T) {
	t.Parallel()
	dbPath := initStoreID(t)

	got := mustReadStoreID(t, dbPath)
	if !storeIDForm.MatchString(got) {
		t.Errorf("ReadStoreID = %q; want 16 lowercase hex", got)
	}
	if want := openedStoreID(t, dbPath); got != want {
		t.Errorf("ReadStoreID = %q; StoreID() = %q", got, want)
	}
}

// TestReadStoreID_MissingFile errors and never creates the store file.
func TestReadStoreID_MissingFile(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "absent.db")

	if _, err := ReadStoreID(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadStoreID err = %v; want one wrapping os.ErrNotExist", err)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s after ReadStoreID: err = %v; want not-exist", dbPath, err)
	}
}

// TestStoreID_NoStoreMetaTable: on a store file with no store_meta table,
// ReadStoreID and SeedStoreID return an error wrapping ErrNoStoreID. The file
// is zero bytes, which SQLite opens as an empty database, so no SQL is needed.
func TestStoreID_NoStoreMetaTable(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatalf("create empty store file: %v", err)
	}

	if _, err := ReadStoreID(dbPath); !errors.Is(err, ErrNoStoreID) {
		t.Errorf("ReadStoreID err = %v; want one wrapping ErrNoStoreID", err)
	}
	if err := SeedStoreID(dbPath, "0123456789abcdef"); !errors.Is(err, ErrNoStoreID) {
		t.Errorf("SeedStoreID err = %v; want one wrapping ErrNoStoreID", err)
	}
}

// TestSeedStoreID_RoundTrip checks a seeded id is what both the raw read and a new open see.
func TestSeedStoreID_RoundTrip(t *testing.T) {
	t.Parallel()
	dbPath := initStoreID(t)
	seeded := OtherStoreID(mustReadStoreID(t, dbPath))

	if err := SeedStoreID(dbPath, seeded); err != nil {
		t.Fatalf("SeedStoreID(%q): %v", seeded, err)
	}
	if got := mustReadStoreID(t, dbPath); got != seeded {
		t.Errorf("ReadStoreID = %q; want %q", got, seeded)
	}
	if got := openedStoreID(t, dbPath); got != seeded {
		t.Errorf("StoreID() = %q; want %q", got, seeded)
	}
}

// TestSeedStoreID_RejectsMalformed rejects each malformed id and leaves the stored id unchanged.
func TestSeedStoreID_RejectsMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   string
	}{
		{name: "uppercase", id: "0123456789ABCDEF"},
		{name: "15 chars", id: "0123456789abcde"},
		{name: "17 chars", id: "0123456789abcdef0"},
		{name: "empty", id: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath := initStoreID(t)
			before := mustReadStoreID(t, dbPath)

			if err := SeedStoreID(dbPath, tc.id); err == nil {
				t.Fatalf("SeedStoreID(%q) = nil; want an error", tc.id)
			}
			if got := mustReadStoreID(t, dbPath); got != before {
				t.Errorf("store id after rejected seed = %q; want unchanged %q", got, before)
			}
		})
	}
}

// TestOtherStoreID returns a well-formed id that differs from its input.
func TestOtherStoreID(t *testing.T) {
	t.Parallel()
	for _, id := range []string{
		"ffffffffffffffff",
		"0000000000000000",
		"0123456789abcdef",
		"a1b2c3d4e5f60789",
		"0123456789ABCDEF", // malformed input still yields a well-formed, different id
		"",
	} {
		got := OtherStoreID(id)
		if !storeIDForm.MatchString(got) {
			t.Errorf("OtherStoreID(%q) = %q; want 16 lowercase hex", id, got)
		}
		if got == id {
			t.Errorf("OtherStoreID(%q) = %q; want a different id", id, got)
		}
	}
}
