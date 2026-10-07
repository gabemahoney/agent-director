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
// TestCoverageBunTestTreeWriteLock (b.k42) runs the same gate over a passing
// fixture whose install, build and test steps each record whether the
// fixture root's .tree-write.lock is held exclusively.
//
// SLOW TEST
// =========
// Each test runs bun three times over its fixture (about a second, plus any
// wait for the dist-pack lock).  Skipped in -short mode.
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

// lockProbe writes "exclusive" or "free" to <fixture root>/<step>.lock-state,
// from the fixture package directory: a shared non-blocking flock fails only
// while another process holds the root's .tree-write.lock exclusively.
const lockProbe = `if flock -n -s ../../.tree-write.lock true; then echo free; else echo exclusive; fi > ../../`

// lockFixturePackageJSON records the lock state during the gate's install
// (root postinstall script) and build steps.
const lockFixturePackageJSON = `{
  "name": "coverage-bun-test-lock-fixture",
  "private": true,
  "scripts": {
    "postinstall": "` + lockProbe + `install.lock-state",
    "build": "` + lockProbe + `build.lock-state"
  }
}
`

// lockFixtureTest records the lock state during the gate's bun test step.
const lockFixtureTest = `import { test } from "bun:test";

test("record the tree-write lock state", () => {
  Bun.spawnSync(["sh", "-c", ` + "`" + lockProbe + "test.lock-state`" + `]);
});
`

// materializeFixture writes a fixture pkg/ts-bun-client, with the given
// package.json and one test file, under worktreeRoot.
func materializeFixture(t *testing.T, worktreeRoot, packageJSON, testSource string) {
	t.Helper()
	pkgDir := filepath.Join(worktreeRoot, "pkg", "ts-bun-client")
	if err := os.MkdirAll(filepath.Join(pkgDir, "test"), 0o755); err != nil {
		t.Fatalf("mkdir fixture package: %v", err)
	}
	writes := map[string]string{
		filepath.Join(pkgDir, "package.json"):            packageJSON,
		filepath.Join(pkgDir, "test", "fixture.test.ts"): testSource,
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
	materializeFixture(t, fixtureRoot, fixturePackageJSON, fixtureFailingTest)

	cmd := exec.Command("bash", gateScript(root), fixtureRoot)
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

// TestCoverageBunTestTreeWriteLock verifies that coverage.bun-test runs bun
// install and build under an exclusive hold of the root's .tree-write.lock,
// even from a relative root, and bun test outside it (b.k42).
func TestCoverageBunTestTreeWriteLock(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs the coverage.bun-test gate over a fixture package")
	}
	for _, tool := range []string{"bun", "flock"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH — skipping coverage.bun-test lock test", tool)
		}
	}

	fixtureRoot := t.TempDir()
	materializeFixture(t, fixtureRoot, lockFixturePackageJSON, lockFixtureTest)

	// A relative root: the gate must resolve the lock before it cds into the package.
	cmd := exec.Command("bash", gateScript(repoRoot(t)), filepath.Base(fixtureRoot))
	cmd.Dir = filepath.Dir(fixtureRoot)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("coverage.bun-test gate on the passing fixture: %v\n%s", err, out)
	}

	for _, step := range []struct{ name, want string }{
		{"install", "exclusive"},
		{"build", "exclusive"},
		{"test", "free"},
	} {
		got, err := os.ReadFile(filepath.Join(fixtureRoot, step.name+".lock-state"))
		if err != nil {
			t.Errorf("%s step recorded no lock state: %v", step.name, err)
			continue
		}
		if s := strings.TrimSpace(string(got)); s != step.want {
			t.Errorf("tree-write lock during the %s step = %q, want %q", step.name, s, step.want)
		}
	}
}

// gateScript is the coverage.bun-test gate under root.
func gateScript(root string) string {
	return filepath.Join(root, "skills", "release-agent-director", "gates", "coverage", "bun-test.sh")
}
