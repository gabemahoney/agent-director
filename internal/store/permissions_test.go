package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// statMode returns the permission bits at path, failing the test on stat
// errors. Centralized so individual cases stay one assertion deep.
func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat(%q): %v", path, err)
	}
	return info.Mode().Perm()
}

// openAt constructs path = tmp/subdir/state.db (so Open has to create the
// "subdir" parent itself) and returns the resolved DB and parent paths.
func openAt(t *testing.T) (dbPath, parentDir string) {
	t.Helper()
	parentDir = filepath.Join(t.TempDir(), "claude-director")
	dbPath = filepath.Join(parentDir, "state.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(%q): %v", dbPath, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return dbPath, parentDir
}

func TestOpenParentDirMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits don't apply on Windows")
	}
	_, parent := openAt(t)

	if got := statMode(t, parent); got != 0o700 {
		t.Errorf("parent dir mode = %o, want 0700", got)
	}
}

func TestOpenDBFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits don't apply on Windows")
	}
	path, _ := openAt(t)

	if got := statMode(t, path); got != 0o600 {
		t.Errorf("db file mode = %o, want 0600", got)
	}
}

// TestRepeatedOpenDoesNotWidenPermissions guards against a regression where
// a second Open() chmods upward — both dir and file must stay tight.
func TestRepeatedOpenDoesNotWidenPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits don't apply on Windows")
	}
	path, parent := openAt(t)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := statMode(t, parent); got != 0o700 {
		t.Errorf("parent dir mode widened to %o after second Open, want 0700", got)
	}
	if got := statMode(t, path); got != 0o600 {
		t.Errorf("db file mode widened to %o after second Open, want 0600", got)
	}
}
