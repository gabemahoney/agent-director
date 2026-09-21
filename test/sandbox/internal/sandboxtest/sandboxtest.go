// Package sandboxtest holds helpers shared by the Makefile-plumbing regression
// tests under test/sandbox/ (gitmount → b.kbe, cmdinject → b.ay3).
//
// Both suites exercise the real repo Makefile by running `make` against it (or
// a throwaway copy) with a fake tool on PATH, then asserting on what the recipe
// produced. They independently grew the same three helpers — locating the repo
// root, skipping when a required tool is missing, and scrubbing inherited make
// state out of the child `make`'s environment — so those live here once.
//
// This package holds no tests of its own: it is exercised transitively by every
// consumer, which is the signal the guide asks for (a break here fails a real
// test, not a meta-test).
package sandboxtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// RepoRoot walks up from the current working directory until it finds go.mod
// and returns that directory. It fails the test if no go.mod is found.
func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("RepoRoot: no go.mod above %s", dir)
		}
		dir = parent
	}
}

// RequireTools skips the test (with skipMsg) if any of bins is not on PATH.
// Each consumer passes its own binary set and a message naming the bee it
// anchors, e.g. RequireTools(t, "skipping b.ay3 …", "make", "bash").
func RequireTools(t *testing.T, skipMsg string, bins ...string) {
	t.Helper()
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH — %s", bin, skipMsg)
		}
	}
}

// ScrubbedMakeEnv returns os.Environ() with the make state variables emptied,
// plus any extra "K=V" entries appended. Emptying MAKEFLAGS/MFLAGS/MAKELEVEL
// stops an ancestor `make sandbox CMD=…` (the very target these tests drive)
// from leaking variable overrides into the child make and skewing the captured
// values.
func ScrubbedMakeEnv(extra ...string) []string {
	env := append(os.Environ(), "MAKEFLAGS=", "MFLAGS=", "MAKELEVEL=")
	return append(env, extra...)
}
