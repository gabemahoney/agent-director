// Package coveragebuntestfires_test is a synthetic-regression test for the
// coverage.bun-test gate (b.wvr coverage phase).
//
// BACKGROUND
// ==========
// The coverage.bun-test gate runs `bun install --frozen-lockfile`,
// `bun run build` and `bun test` in a worktree's pkg/ts-bun-client.  A failing
// bun test must make the gate exit non-zero and emit a structured SR-14 JSON
// diagnostic to stderr with "gate":"coverage.bun-test".  This test plants a
// failing bun test and verifies the gate fires.
//
// DESIGN
// ======
//  1. Fixture worktree.  The test writes a tiny pkg/ts-bun-client into
//     t.TempDir(): a package.json with no dependencies and a no-op `build`
//     script, and one test that always fails.  It points the UNMODIFIED gate at
//     it through the gate's optional worktree-root argument, so nothing outside
//     t.TempDir() is written (b.jct).  With no dependencies,
//     `bun install --frozen-lockfile` needs neither a lockfile nor the network.
//  2. Assertions.  The gate exits non-zero with the diagnostic of its `bun test`
//     step, which it reaches only after install and build pass, and the bun
//     output names the fixture's test as the failure.  So the gate fired on the
//     planted failure, not on a broken fixture.  The test also fails if the real
//     setup.test.ts is written (requireUnwritten).
//  3. Locking.  The test holds no lock.  The gate takes the dist-pack lock
//     itself, so this test may wait for a pack-first test or another
//     coverage.bun-test gate.
//
// SLOW TEST
// =========
// Runs bun three times over the fixture (about a second, plus any wait for the
// dist-pack lock).  Skipped in -short mode.
package coveragebuntestfires_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const (
	// gateKey is the SR-14 diagnostic field proving coverage.bun-test emitted
	// the failure.
	gateKey = `"gate":"coverage.bun-test"`
	// testStepDiagnostic is the description the gate emits only when `bun test`
	// fails; install and build failures carry their own descriptions.
	testStepDiagnostic = `"description":"bun test failed"`
	// fixtureTestName names the planted failing test, so the gate's output can
	// be matched to it.
	fixtureTestName = "planted failure (coverage-bun-test-fires)"
)

// fixturePackageJSON declares no dependencies, so the gate's
// `bun install --frozen-lockfile` succeeds offline without a lockfile.
const fixturePackageJSON = `{
  "name": "coverage-bun-test-fixture",
  "private": true,
  "scripts": { "build": "true" }
}
`

// fixtureFailingTest always fails: the planted failure the gate must report.
const fixtureFailingTest = `import { expect, test } from "bun:test";

test("` + fixtureTestName + `", () => {
  expect(1).toBe(2);
});
`

// materializeFixture writes the fixture pkg/ts-bun-client under worktreeRoot.
func materializeFixture(t *testing.T, worktreeRoot string) {
	t.Helper()
	pkgDir := filepath.Join(worktreeRoot, "pkg", "ts-bun-client")
	if err := os.MkdirAll(filepath.Join(pkgDir, "test"), 0o755); err != nil {
		t.Fatalf("mkdir fixture package: %v", err)
	}
	writes := map[string]string{
		filepath.Join(pkgDir, "package.json"):          fixturePackageJSON,
		filepath.Join(pkgDir, "test", "fires.test.ts"): fixtureFailingTest,
	}
	for path, content := range writes {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", path, err)
		}
	}
}

// repoRoot walks up from the package working directory until it finds go.mod.
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
}

// fileSnapshot is what any write to a file changes: its content, mtime or inode.
type fileSnapshot struct {
	sha256  string
	mtimeNs int64
	inode   uint64
}

func snapshotFile(t *testing.T, path string) fileSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: no inode in %T", path, info.Sys())
	}
	return fileSnapshot{fmt.Sprintf("%x", sha256.Sum256(data)), info.ModTime().UnixNano(), st.Ino}
}

// requireUnwritten fails the test if path is written after this call, including
// by cleanups registered after it (b.jct: no test may rewrite a tracked file,
// even to restore it).
func requireUnwritten(t *testing.T, path string) {
	t.Helper()
	before := snapshotFile(t, path)
	t.Cleanup(func() {
		if after := snapshotFile(t, path); after != before {
			t.Errorf("%s was written during the test:\nbefore %+v\nafter  %+v", path, before, after)
		}
	})
}

// TestCoverageBunTestFires verifies that coverage.bun-test fires (exit != 0,
// stderr contains "gate":"coverage.bun-test") when a bun test fails.
func TestCoverageBunTestFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs the coverage.bun-test gate over a fixture package")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not in PATH — skipping coverage.bun-test gate test")
	}

	root := repoRoot(t)
	// The tracked file the test used to plant its failure in. Registered first,
	// so it checks after every other cleanup has run.
	requireUnwritten(t, filepath.Join(root, "pkg", "ts-bun-client", "test", "setup.test.ts"))

	fixtureRoot := t.TempDir()
	materializeFixture(t, fixtureRoot)

	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "coverage", "bun-test.sh")
	cmd := exec.Command("bash", gateScript, fixtureRoot)
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	stdout, stderr := stdoutBuf.String(), stderrBuf.String()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		if err == nil {
			t.Fatalf("expected coverage.bun-test gate to exit non-zero on the planted bun test failure, but it exited 0\nstdout:\n%s", stdout)
		}
		t.Fatalf("run coverage.bun-test gate: %v", err)
	}
	if !strings.Contains(stderr, gateKey) || !strings.Contains(stderr, testStepDiagnostic) {
		t.Fatalf("gate stderr is not the bun test step's diagnostic (want %s and %s)\nstderr:\n%s\nstdout:\n%s",
			gateKey, testStepDiagnostic, stderr, stdout)
	}
	if want := "(fail) " + fixtureTestName; !strings.Contains(stdout, want) {
		t.Fatalf("gate output does not report the planted failure %q\nstdout:\n%s", want, stdout)
	}

	t.Logf("coverage.bun-test fired correctly (exit %d).\nGate stderr: %s", exitErr.ExitCode(), stderr)
}
