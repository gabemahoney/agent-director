// Package helpertagreplay_test is a synthetic-regression test for b.n4v.
//
// BACKGROUND (b.n4v incident class)
// ===================================
// Before Epic E1, pkg/api helpers lived in build-tagged files that were
// excluded from the standard build.  A wrong-arity call inside such a tagged
// file compiled silently under `go build ./...`, letting the bug slip through
// CI.  E1 retired build-tagged helper files and moved all seed helpers into
// pkg/api/apitest under the normal (untagged) build.  This test proves that
// introducing the same class of bug (wrong-arity call) in apitest is now
// caught by `go build ./...`.
//
// DESIGN
// ======
// 1. Target file : pkg/api/apitest/seeds.go — the canonical b.n4v-relevant
//    file created in this Epic.
// 2. Mutation    : insert `_ = SeedSpawn("only-one-arg")` at the top of the
//    InitStore function body.  SeedSpawn requires 7 string args plus a bool;
//    calling it with a single string is a compile error — exactly the b.n4v
//    bug class replayed.
// 3. Cleanup     : original bytes are captured before mutation; t.Cleanup
//    restores them unconditionally.  Go's testing framework guarantees
//    t.Cleanup runs even on test failure and even when t.Fatalf is called
//    (which calls runtime.Goexit, unwinding defers/cleanups in order).
// 4. Build       : exec.Command("go", "build", "./...") from repo root;
//    CombinedOutput captures stderr.
// 5. Assertions  : non-zero exit code AND stderr contains "seeds.go".
//
// CLEANUP-ON-FAILURE EXPERIMENT (subtask 9r)
// ==========================================
// After the working test was complete, `t.Fatalf("force-fail for 9r experiment")`
// was temporarily inserted immediately after the os.WriteFile mutation call.
// The test reported FAIL as expected.  Running `git status pkg/api/apitest/seeds.go`
// afterwards showed "nothing to commit, working tree clean" — t.Cleanup fired
// correctly and restored the original bytes despite the early Fatalf.  The
// forced failure was removed before this commit.
package helpertagreplay_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the package's working directory (set by `go test` to
// the package directory) until it finds a go.mod file.
func repoRoot(t *testing.T) string {
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
			t.Fatalf("repoRoot: could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
	panic("unreachable")
}

// TestHelperTagReplay demonstrates AC-1: a wrong-arity call to an apitest
// symbol is caught by `go build ./...` now that build-tagged helper files have
// been retired (b.n4v incident, Epic E1).
func TestHelperTagReplay(t *testing.T) {
	root := repoRoot(t)
	targetFile := filepath.Join(root, "pkg", "api", "apitest", "seeds.go")

	// ── 1. Read original bytes ──────────────────────────────────────────────
	orig, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read seeds.go: %v", err)
	}
	origStat, err := os.Stat(targetFile)
	if err != nil {
		t.Fatalf("stat seeds.go: %v", err)
	}

	// ── 2. Register cleanup BEFORE mutating ────────────────────────────────
	// t.Cleanup is guaranteed to run even if the test calls t.Fatalf / panics.
	t.Cleanup(func() {
		if err := os.WriteFile(targetFile, orig, origStat.Mode()); err != nil {
			t.Errorf("t.Cleanup: restore seeds.go: %v", err)
		}
	})

	// ── 3. Inject mutation ─────────────────────────────────────────────────
	// We replace the opening of InitStore's body with a version that contains
	// a wrong-arity call to SeedSpawn.  The real signature is:
	//   SeedSpawn(dbPath, id, state, cwd, relayMode, sessionID string, createStore bool)
	// Passing a single string arg is a compile error — the b.n4v bug class.
	const marker = "func InitStore(dbPath string) (string, error) {\n\ts, err := store.OpenOrInit(dbPath)"
	const mutated = "func InitStore(dbPath string) (string, error) {\n" +
		"\t_ = SeedSpawn(\"only-one-arg\") // wrong-arity: SeedSpawn needs 7 args + bool (b.n4v replay)\n" +
		"\ts, err := store.OpenOrInit(dbPath)"

	if !bytes.Contains(orig, []byte(marker)) {
		t.Fatalf("mutation marker not found in seeds.go — update the marker if InitStore was refactored")
	}

	mutatedContent := bytes.Replace(orig, []byte(marker), []byte(mutated), 1)
	if err := os.WriteFile(targetFile, mutatedContent, origStat.Mode()); err != nil {
		t.Fatalf("write mutated seeds.go: %v", err)
	}
	// ── 4. Build: must fail ────────────────────────────────────────────────
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = root
	output, _ := cmd.CombinedOutput() // non-zero exit is expected — ignore the error

	// ── 5. Assertions ──────────────────────────────────────────────────────
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("expected `go build ./...` to fail after wrong-arity mutation, but it succeeded")
	}

	if !strings.Contains(string(output), "seeds.go") {
		t.Fatalf("expected compiler stderr to reference seeds.go; got:\n%s", output)
	}

	// Green means the bug was detected — log the evidence.
	t.Logf("AC-1 verified: `go build ./...` caught the wrong-arity mutation.\nCompiler output:\n%s", output)
}
