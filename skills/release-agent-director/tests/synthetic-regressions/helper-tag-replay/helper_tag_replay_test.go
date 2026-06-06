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
	"syscall"
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

// seedsMutationLockPath returns the path to the cross-process advisory lock
// file used to serialize seeds.go mutations across parallel test packages.
func seedsMutationLockPath(root string) string {
	return filepath.Join(root, "pkg", "api", "apitest", ".seeds-mutation.lock")
}

// acquireSeedsLock grabs an exclusive flock on a shared lock file before any
// test mutates seeds.go.  Two test packages (helper-tag-replay and
// coverage-go-root-fires) both mutate that file; running them in parallel
// without serialization causes a marker-not-found race.  The lock is released
// after t.Cleanup restores the file (t.Cleanup is LIFO: register lock-release
// first, then file-restore, so restore runs before unlock).
//
// NOTE: never call this inside a subprocess invoked by coverage-go-root-fires;
// that test holds the same lock while running its gate, causing a deadlock.
// coverage-go-root-fires sets COVERAGE_GO_ROOT_NESTED=1 in its subprocess env;
// callers should call t.Skip when that variable is set (see TestHelperTagReplay).
func acquireSeedsLock(t *testing.T, root string) {
	t.Helper()
	lockPath := seedsMutationLockPath(root)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("acquireSeedsLock: open %s: %v", lockPath, err)
	}
	// LOCK_EX blocks until no other process holds the lock.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatalf("acquireSeedsLock: flock: %v", err)
	}
	// Register lock-release FIRST so it runs AFTER the file-restore cleanup
	// that the caller registers next (LIFO order).
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	})
}

// TestHelperTagReplay demonstrates AC-1: a wrong-arity call to an apitest
// symbol is caught by `go build ./...` now that build-tagged helper files have
// been retired (b.n4v incident, Epic E1).
func TestHelperTagReplay(t *testing.T) {
	// ── 0a. Skip when running inside coverage-go-root-fires' inner test run ─
	// coverage-go-root-fires sets COVERAGE_GO_ROOT_NESTED=1 in the subprocess
	// env before invoking the gate (which runs go test ./... internally).
	// Without this skip, the inner TestHelperTagReplay would block on the flock
	// held by the outer coverage-go-root-fires test, causing a deadlock.
	if os.Getenv("COVERAGE_GO_ROOT_NESTED") == "1" {
		t.Skip("skipping seeds.go mutation: running inside coverage.go-root gate inner test suite")
	}

	root := repoRoot(t)
	targetFile := filepath.Join(root, "pkg", "api", "apitest", "seeds.go")

	// ── 0b. Serialize access to seeds.go across parallel test packages ──────
	acquireSeedsLock(t, root)

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
