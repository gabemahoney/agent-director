// Package cwdfix moves a test's process into a fresh temp directory for the
// rest of the test (go 1.22 has no t.Chdir).
//
// This is a LEAF test-support package: it imports nothing from agent-director,
// so any test package can use it. The working directory is process-wide, so a
// test using it must not be parallel; Temp enforces that the way go 1.24's
// t.Chdir does, by setting $PWD with t.Setenv, which panics in a parallel test
// and makes a later t.Parallel call panic.
package cwdfix

import (
	"os"
	"testing"
)

// Temp makes a fresh t.TempDir() the working directory until t ends, then
// restores the previous one, and returns the temp dir.
func Temp(t *testing.T) string {
	t.Helper()
	wd := t.TempDir()
	saved, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Setenv("PWD", wd)
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("chdir %s: %v", wd, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(saved); err != nil {
			t.Errorf("chdir back to %s: %v", saved, err)
		}
	})
	return wd
}
