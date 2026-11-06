// Package journalfix is the reusable test fixture for genuine pre-cutover
// stores (schema v14 or v15) and their migration authorization, loaded from
// test/install-sh/testdata/pre-cutover-v<N>.sql. A test outside internal/store
// and test/install-sh that needs a pre-cutover store must use it; internal/store
// keeps makeVersionedDB, and test/install-sh its genuine-release builder.
package journalfix

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// PreCutoverStore loads pre-cutover-v<version>.sql (14 or 15) into
// <dir>/state.db as the installer's tests do: WAL mode, closed, mode 0600, dir
// 0700. It returns the store's path.
func PreCutoverStore(t testing.TB, dir string, version int) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(file) {
		t.Fatalf("journalfix: this source file's location is unknown")
	}
	fixture := filepath.Join(filepath.Dir(file), "..", "..", "..", "test", "install-sh", "testdata", fmt.Sprintf("pre-cutover-v%d.sql", version))
	ddl, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("journalfix: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("journalfix: %v", err)
	}
	db := filepath.Join(dir, "state.db")
	withDB(t, db, func(raw *sql.DB) error {
		if _, err := raw.Exec(string(ddl)); err != nil {
			return err
		}
		_, err := raw.Exec("PRAGMA journal_mode=WAL")
		return err
	})
	if err := os.Chmod(db, 0o600); err != nil {
		t.Fatalf("journalfix: %v", err)
	}
	return db
}

// Authorize writes the migrate-authorized sentinel beside db, authorizing
// its version to the current one, read from a fresh store.
func Authorize(t testing.TB, db string) {
	t.Helper()
	reference, err := apitest.InitStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("journalfix: %v", err)
	}
	payload, err := json.Marshal(struct {
		From int `json:"from"`
		To   int `json:"to"`
	}{userVersion(t, db), userVersion(t, reference)})
	if err != nil {
		t.Fatalf("journalfix: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(db), "migrate-authorized"), payload, 0o600); err != nil {
		t.Fatalf("journalfix: %v", err)
	}
}

func userVersion(t testing.TB, db string) (version int) {
	t.Helper()
	withDB(t, db, func(raw *sql.DB) error {
		return raw.QueryRow("PRAGMA user_version").Scan(&version)
	})
	return version
}

func withDB(t testing.TB, path string, use func(*sql.DB) error) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("journalfix: %v", err)
	}
	err = use(raw)
	if cerr := raw.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("journalfix: %s: %v", path, err)
	}
}
