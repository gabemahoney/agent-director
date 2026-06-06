// Package coveragebuntestfires_test is a synthetic-regression test for the
// coverage.bun-test gate (b.wvr coverage phase).
//
// BACKGROUND
// ==========
// The coverage.bun-test gate runs `bun install`, `bun run build`, and
// `bun test` for pkg/ts-bun-client.  A failing bun test must cause the gate
// to exit non-zero and emit a structured SR-14 JSON diagnostic to stderr
// with "gate":"coverage.bun-test".  This test injects a forced assertion
// failure into a small, self-contained test file and verifies the gate fires.
//
// DESIGN
// ======
// 1. Target file : pkg/ts-bun-client/test/setup.test.ts — smallest test in
//    the suite (24 lines); touches only the fake-tmux stub check and has no
//    interactions with other test files.
// 2. Mutation    : append `expect(1).toBe(2); // forced failure` to the end
//    of the existing test body (before the closing `}`), making the test
//    unconditionally fail on any bun test run.
// 3. Gate        : bash skills/release-agent-director/gates/coverage/bun-test.sh
//    is run from repo root.  The gate's stderr must contain the JSON
//    "gate":"coverage.bun-test" diagnostic on failure.
// 4. Cleanup     : original bytes are captured before mutation; t.Cleanup
//    restores them unconditionally even when t.Fatalf fires mid-test.
//
// SLOW TEST
// =========
// This test runs bun install + bun run build + bun test (≈12s).
// It is skipped in -short mode to keep default `go test ./...` fast.
package coveragebuntestfires_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	panic("unreachable")
}

// TestCoverageBunTestFires verifies that coverage.bun-test fires (exit != 0,
// stderr contains "gate":"coverage.bun-test") when a bun test is broken.
func TestCoverageBunTestFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs full coverage suite (bun install + build + test)")
	}

	// Dependency guard
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not in PATH — skipping coverage.bun-test gate test")
	}

	root := repoRoot(t)
	targetFile := filepath.Join(root, "pkg", "ts-bun-client", "test", "setup.test.ts")

	// ── 1. Read original bytes ──────────────────────────────────────────────
	orig, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read setup.test.ts: %v", err)
	}
	origStat, err := os.Stat(targetFile)
	if err != nil {
		t.Fatalf("stat setup.test.ts: %v", err)
	}

	// ── 2. Register cleanup BEFORE mutating ────────────────────────────────
	t.Cleanup(func() {
		if err := os.WriteFile(targetFile, orig, origStat.Mode()); err != nil {
			t.Errorf("t.Cleanup: restore setup.test.ts: %v", err)
		}
	})

	// ── 3. Inject mutation ─────────────────────────────────────────────────
	// Append a forced failure assertion after the last line of the test body.
	// We insert before the closing `});` so Bun parses the file correctly.
	const marker = "  expect(mode & 0o111).toBeGreaterThan(0);\n});"
	const mutated = "  expect(mode & 0o111).toBeGreaterThan(0);\n" +
		"  expect(1).toBe(2); // forced failure for coverage.bun-test regression\n});"

	origStr := string(orig)
	if !strings.Contains(origStr, marker) {
		t.Fatalf("mutation marker not found in setup.test.ts — update the marker if the file was refactored")
	}

	mutatedContent := strings.Replace(origStr, marker, mutated, 1)
	if err := os.WriteFile(targetFile, []byte(mutatedContent), origStat.Mode()); err != nil {
		t.Fatalf("write mutated setup.test.ts: %v", err)
	}

	// ── 4. Run coverage.bun-test gate ──────────────────────────────────────
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "coverage", "bun-test.sh")
	cmd := exec.Command("bash", gateScript)
	cmd.Dir = root
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	// stdout flows to the test log for progress visibility
	cmd.Stdout = os.Stdout
	_ = cmd.Run() // non-zero exit is expected — ignore the returned error

	stderr := stderrBuf.String()

	// ── 5. Assertions ──────────────────────────────────────────────────────
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("expected coverage.bun-test gate to exit non-zero after forced bun test failure, but it exited 0")
	}

	const gateKey = `"gate":"coverage.bun-test"`
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	t.Logf("coverage.bun-test fired correctly (exit %d).\nGate stderr: %s", cmd.ProcessState.ExitCode(), stderr)
}
