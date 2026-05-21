package main_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // raw driver access for the tamper helper
)

// directorDir returns the path to the .claude-director directory under home.
func directorDir(home string) string {
	return filepath.Join(home, ".claude-director")
}

// stateDB returns the path to state.db under home.
func stateDB(home string) string {
	return filepath.Join(directorDir(home), "state.db")
}

// statMode returns the permission bits of path or fails the test.
func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func TestHelpCreatesDirAndDBOnFirstRun(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runCLIWithHome(t, home, "help")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if stdout == "" {
		t.Errorf("stdout empty; want help JSON")
	}

	dir := directorDir(home)
	if mode := statMode(t, dir); mode != 0o700 {
		t.Errorf(".claude-director mode = %o, want 0700", mode)
	}
	db := stateDB(home)
	if mode := statMode(t, db); mode != 0o600 {
		t.Errorf("state.db mode = %o, want 0600", mode)
	}
}

func TestHelpIdempotentAcrossInvocations(t *testing.T) {
	home := t.TempDir()

	// First run creates dir + db.
	if _, _, code := runCLIWithHome(t, home, "help"); code != 0 {
		t.Fatalf("first invocation exit=%d want 0", code)
	}
	firstDirMode := statMode(t, directorDir(home))
	firstDBMode := statMode(t, stateDB(home))

	// Second run: must succeed and not change modes.
	stdout2, stderr2, code2 := runCLIWithHome(t, home, "help")
	if code2 != 0 {
		t.Fatalf("second invocation exit=%d stderr=%q", code2, stderr2)
	}
	if stdout2 == "" {
		t.Errorf("second stdout empty")
	}
	if got := statMode(t, directorDir(home)); got != firstDirMode {
		t.Errorf(".claude-director mode changed: %o -> %o", firstDirMode, got)
	}
	if got := statMode(t, stateDB(home)); got != firstDBMode {
		t.Errorf("state.db mode changed: %o -> %o", firstDBMode, got)
	}
}

// setupTamperedDB creates a real schema-v1 state.db under home (by running
// the binary once) then stamps PRAGMA user_version=99 directly so the next
// invocation must surface ErrSchemaMismatch.
func setupTamperedDB(t *testing.T, home string) {
	t.Helper()
	if _, _, code := runCLIWithHome(t, home, "help"); code != 0 {
		t.Fatalf("bootstrap run exit=%d", code)
	}
	db, err := sql.Open("sqlite", stateDB(home))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close raw db: %v", cerr)
		}
	}()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", 99)); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
}

func TestHelpEmitsSchemaMismatchEnvelopeWhenDBTampered(t *testing.T) {
	home := t.TempDir()
	setupTamperedDB(t, home)

	stdout, stderr, code := runCLIWithHome(t, home, "help")
	if code == 0 {
		t.Fatalf("exit=0 want non-zero; stdout=%q stderr=%q", stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout=%q want empty on error", stdout)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSchemaMismatch" {
		t.Errorf("err_name=%q want %q", env.ErrName, "ErrSchemaMismatch")
	}
	if env.ErrDescription == "" {
		t.Errorf("err_description empty in %q", stderr)
	}
}
