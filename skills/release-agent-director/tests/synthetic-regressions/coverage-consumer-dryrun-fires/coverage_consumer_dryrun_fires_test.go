// Package coverageconsumerdryrunfires_test is a synthetic-regression test for
// the coverage.go-consumer-dryrun gate (b.wvr coverage phase).
//
// BACKGROUND
// ==========
// The coverage.go-consumer-dryrun gate runs `go test ./... -race -count=1`
// inside tools/consumer-dryrun/.  A failing test in that module must cause
// the gate to exit non-zero and emit a structured SR-14 JSON diagnostic to
// stderr with "gate":"coverage.go-consumer-dryrun".  This test temporarily
// injects a _test.go file that forces a failure, verifies the gate fires,
// then removes the injected file.
//
// DESIGN
// ======
// 1. Target dir  : tools/consumer-dryrun/ — the module checked by the gate.
//    This module has no existing _test.go files, so we inject one temporarily.
// 2. Mutation    : write tools/consumer-dryrun/forced_failure_test.go containing
//    a TestForcedFailure that calls t.Fatal.  The file uses `package main`
//    to match the module's package declaration.
//    NOTE: files beginning with `_` are ignored by the Go toolchain; the
//    injected file must NOT use a leading-underscore name.
// 3. Gate        : bash skills/release-agent-director/gates/coverage/go-consumer-dryrun.sh
//    is run from repo root.  The gate's stderr must contain the JSON
//    "gate":"coverage.go-consumer-dryrun" diagnostic on failure.
// 4. Cleanup     : t.Cleanup removes the injected file unconditionally, even
//    when t.Fatalf fires mid-test.  After cleanup, git status must be clean.
//
// SLOW TEST
// =========
// This test runs `go test ./... -race -count=1` in tools/consumer-dryrun/
// (≈45s including compilation of the module's dependencies).
// It is skipped in -short mode to keep default `go test ./...` fast.
package coverageconsumerdryrunfires_test

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

// regressionTestContent is the injected test file that forces a gate failure.
const regressionTestContent = `package main

import "testing"

// TestForcedFailure is injected by coverage-consumer-dryrun-fires regression
// test to verify that coverage.go-consumer-dryrun catches test failures.
// This file is created and removed by the Go test; it must not be committed.
func TestForcedFailure(t *testing.T) {
	t.Fatal("forced failure for coverage.go-consumer-dryrun regression test")
}
`

// TestCoverageConsumerDryrunFires verifies that coverage.go-consumer-dryrun
// fires (exit != 0, stderr contains "gate":"coverage.go-consumer-dryrun")
// when a test inside tools/consumer-dryrun fails.
func TestCoverageConsumerDryrunFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs full coverage suite (go test ./... -race -count=1 in tools/consumer-dryrun)")
	}

	root := repoRoot(t)
	// NOTE: Go ignores files beginning with '_' — use a plain name here.
	injectedFile := filepath.Join(root, "tools", "consumer-dryrun", "forced_failure_test.go")

	// ── 1. Inject temporary test file ──────────────────────────────────────
	if err := os.WriteFile(injectedFile, []byte(regressionTestContent), 0o644); err != nil {
		t.Fatalf("write _regression_test.go: %v", err)
	}

	// ── 2. Register cleanup BEFORE any assertion ───────────────────────────
	// t.Cleanup fires even when t.Fatalf is called (runtime.Goexit path).
	t.Cleanup(func() {
		if err := os.Remove(injectedFile); err != nil && !os.IsNotExist(err) {
			t.Errorf("t.Cleanup: remove _regression_test.go: %v", err)
		}
	})

	// ── 3. Run coverage.go-consumer-dryrun gate ────────────────────────────
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "coverage", "go-consumer-dryrun.sh")
	cmd := exec.Command("bash", gateScript)
	cmd.Dir = root
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	// stdout flows to the test log for progress visibility
	cmd.Stdout = os.Stdout
	_ = cmd.Run() // non-zero exit is expected — ignore the returned error

	stderr := stderrBuf.String()

	// ── 4. Assertions ──────────────────────────────────────────────────────
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("expected coverage.go-consumer-dryrun gate to exit non-zero after forced test failure, but it exited 0")
	}

	const gateKey = `"gate":"coverage.go-consumer-dryrun"`
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	t.Logf("coverage.go-consumer-dryrun fired correctly (exit %d).\nGate stderr: %s", cmd.ProcessState.ExitCode(), stderr)
}
