package api_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// writeConfig writes TOML content to path, creating parent dirs as needed.
func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("writeConfig MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeConfig WriteFile: %v", err)
	}
}

// stampUserVersion opens the SQLite file at path and writes
// PRAGMA user_version = v, used to simulate a schema mismatch.
func stampUserVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("stampUserVersion open: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("stampUserVersion close: %v", cerr)
		}
	}()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatalf("stampUserVersion exec(user_version=%d): %v", v, err)
	}
}

// --- Case 10: sentinel check ---------------------------------------------------

// TestErrClientClosedSentinel verifies the exported sentinel is non-nil.
func TestErrClientClosedSentinel(t *testing.T) {
	if api.ErrClientClosed == nil {
		t.Fatal("api.ErrClientClosed must not be nil")
	}
}

// --- Cases 1–3: default path behaviour ----------------------------------------

// TestNewDefaultsMissingStoreNoCreate: default paths, missing store,
// CreateIfMissing=false → ErrStoreNotInitialized; no file side effects (H1).
func TestNewDefaultsMissingStoreNoCreate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	client, err := api.New(api.Options{})
	if client != nil {
		_ = client.Close()
		t.Error("expected nil *Client on error path, got non-nil")
	}
	if !errors.Is(err, store.ErrStoreNotInitialized) {
		t.Fatalf("errors.Is(err, ErrStoreNotInitialized) = false; err = %v", err)
	}

	// H1 invariant: constructor must not create the DB file.
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	if _, statErr := os.Stat(dbPath); !os.IsNotExist(statErr) {
		t.Errorf("store file %q must not exist after failed New; stat: %v", dbPath, statErr)
	}
}

// TestNewDefaultsMissingStoreWithCreate: CreateIfMissing=true creates the
// store file and parent dir (CLI parity path).
func TestNewDefaultsMissingStoreWithCreate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	client, err := api.New(api.Options{CreateIfMissing: true})
	if err != nil {
		t.Fatalf("New with CreateIfMissing=true: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil *Client")
	}
	if cerr := client.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	dbPath := filepath.Join(home, ".agent-director", "state.db")
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Errorf("store file %q must exist after CreateIfMissing=true; stat: %v", dbPath, statErr)
	}
}

// TestNewDefaultsPreexistingStore: default paths, pre-seeded valid store →
// success.
func TestNewDefaultsPreexistingStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	apitest.SeedStore(t, filepath.Join(home, ".agent-director", "state.db"))

	client, err := api.New(api.Options{})
	if err != nil {
		t.Fatalf("New with preexisting store: %v", err)
	}
	if cerr := client.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
}

// --- Cases 4–5: StorePath precedence ------------------------------------------

// TestStorePathFromConfig: H2 invariant — cfg.Store.DbPath is used when
// Options.StorePath is empty.
func TestStorePathFromConfig(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "cfg.toml")
	elsewhere := filepath.Join(home, "elsewhere.db")

	writeConfig(t, cfgPath, fmt.Sprintf("[store]\ndb_path = %q\n", elsewhere))
	apitest.SeedStore(t, elsewhere)

	client, err := api.New(api.Options{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cerr := client.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
}

// TestStorePathOptionsWins: H2 invariant — Options.StorePath wins over
// cfg.Store.DbPath. The cfg db_path is never opened.
func TestStorePathOptionsWins(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "cfg.toml")
	elsewhere := filepath.Join(home, "elsewhere.db")
	override := filepath.Join(home, "override.db")

	writeConfig(t, cfgPath, fmt.Sprintf("[store]\ndb_path = %q\n", elsewhere))
	// Seed only the override path; elsewhere.db intentionally absent.
	apitest.SeedStore(t, override)

	client, err := api.New(api.Options{StorePath: override, ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("New with StorePath override: %v", err)
	}
	if cerr := client.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// cfg db_path must not have been touched.
	if _, statErr := os.Stat(elsewhere); !os.IsNotExist(statErr) {
		t.Errorf("cfg db_path %q must not be created when StorePath overrides it", elsewhere)
	}
}

// --- Case 6: tilde expansion --------------------------------------------------

// TestTildeExpansion verifies that "~/..." in StorePath and ConfigPath are
// expanded correctly. api.expandTilde uses user.Current() (CGO path) which
// ignores $HOME, so we use the real home dir with PID-scoped filenames.
func TestTildeExpansion(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user home: %v", err)
	}
	home := u.HomeDir

	storeBase := fmt.Sprintf(".ad-test-store-%d.db", os.Getpid())
	cfgBase := fmt.Sprintf(".ad-test-cfg-%d.toml", os.Getpid())
	storePath := filepath.Join(home, storeBase)
	cfgPath := filepath.Join(home, cfgBase)

	apitest.SeedStore(t, storePath)
	writeConfig(t, cfgPath, "")

	t.Cleanup(func() {
		os.Remove(storePath)
		os.Remove(storePath + "-wal")
		os.Remove(storePath + "-shm")
		os.Remove(cfgPath)
	})

	client, err := api.New(api.Options{
		StorePath:  "~/" + storeBase,
		ConfigPath: "~/" + cfgBase,
	})
	if err != nil {
		t.Fatalf("New with tilde paths: %v", err)
	}
	if cerr := client.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
}

// --- Case 7: double Close -----------------------------------------------------

// TestDoubleClose verifies that calling Close() twice returns nil both times
// and does not panic.
func TestDoubleClose(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	apitest.SeedStore(t, filepath.Join(home, ".agent-director", "state.db"))

	client, err := api.New(api.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cerr := client.Close(); cerr != nil {
		t.Fatalf("first Close: %v", cerr)
	}
	if cerr := client.Close(); cerr != nil {
		t.Fatalf("second Close: %v", cerr)
	}
}

// --- Case 8: concurrent Close (SR-1.2) ----------------------------------------

// TestConcurrentClose stress-tests the closed-flag mutex: N goroutines all
// call Close() concurrently. Run with -race; no panic, no torn reads.
func TestConcurrentClose(t *testing.T) {
	const n = 8
	home := t.TempDir()
	t.Setenv("HOME", home)
	apitest.SeedStore(t, filepath.Join(home, ".agent-director", "state.db"))

	client, err := api.New(api.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if cerr := client.Close(); cerr != nil {
				t.Errorf("concurrent Close returned non-nil: %v", cerr)
			}
		}()
	}
	wg.Wait()
}

// --- Case 9: schema mismatch --------------------------------------------------

// TestSchemaMismatch verifies that a store with a tampered user_version
// surfaces store.ErrSchemaMismatch through errors.Is.
func TestSchemaMismatch(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "state.db")
	cfgPath := filepath.Join(home, "cfg.toml")

	// Bootstrap a valid v1 store, then corrupt its schema version.
	apitest.SeedStore(t, dbPath)
	stampUserVersion(t, dbPath, 99)

	writeConfig(t, cfgPath, fmt.Sprintf("[store]\ndb_path = %q\n", dbPath))

	_, err := api.New(api.Options{ConfigPath: cfgPath})
	if !errors.Is(err, store.ErrSchemaMismatch) {
		t.Fatalf("errors.Is(err, ErrSchemaMismatch) = false; err = %v", err)
	}
}
