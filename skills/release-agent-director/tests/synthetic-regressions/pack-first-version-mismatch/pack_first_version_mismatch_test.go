// Package packfirstversionmismatch_test is a synthetic-regression test for
// the pack.first gate (SR-14 replay scenario).
//
// BACKGROUND
// ==========
// The pack.first gate runs `bun pm pack` and asserts the embedded
// package/package.json version inside the produced tarball matches the
// --target-version argument.  When the target version is artificially set
// to a value that does not match the package.json on disk (e.g. 9.9.9 vs
// 0.0.0), the gate must emit an SR-14 diagnostic to stderr and exit non-zero.
//
// DESIGN
// ======
// 1. Pre-req  : requires `bun` on PATH; test is skipped if `bun` is absent.
// 2. Mutation : none — the mismatch is induced by passing --target-version 9.9.9
//               while pkg/ts-bun-client/package.json remains at "0.0.0".
// 3. Gate     : bash skills/release-agent-director/gates/pack/pack-first.sh
//               --target-version 9.9.9
//               is run from repo root.
// 4. Assertions: (a) gate exits non-zero; (b) stderr contains an SR-14 JSON
//                object with gate=="pack.first"; (c) stderr mentions both
//                observed version "0.0.0" and expected version "9.9.9".
// 5. Cleanup  : rm -rf dist/ — the tarball is an ephemeral artifact.
//
// SLOW TEST
// =========
// This test invokes `bun pm pack` (≈2–5 s).
// It is skipped in -short mode to keep default `go test ./...` fast.
package packfirstversionmismatch_test

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

// TestPackFirstVersionMismatch verifies that the pack.first gate fires
// (exit != 0, SR-14 diagnostic on stderr) when --target-version does not
// match the version embedded in the packed tarball.
func TestPackFirstVersionMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs bun pm pack")
	}

	// Require bun on PATH.
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping pack.first test")
	}

	root := repoRoot(t)

	// ── 1. Register cleanup: remove dist/ unconditionally ─────────────────
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(root, "dist")); err != nil {
			t.Errorf("t.Cleanup: remove dist/: %v", err)
		}
	})

	// ── 2. Run the gate with a mismatched target version ──────────────────
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "pack-first.sh")
	gateCmd := exec.Command("bash", gateScript, "--target-version", "9.9.9")
	gateCmd.Dir = root

	var stderrBuf strings.Builder
	gateCmd.Stdout = os.Stdout
	gateCmd.Stderr = &stderrBuf

	_ = gateCmd.Run() // non-zero exit is expected; checked below

	stderr := stderrBuf.String()

	// ── 3. Assertions ─────────────────────────────────────────────────────
	if gateCmd.ProcessState.ExitCode() == 0 {
		t.Fatal("pack.first gate should exit non-zero on version mismatch; got exit 0")
	}

	const gateKey = `"gate":"pack.first"`
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	if !strings.Contains(stderr, "0.0.0") {
		t.Errorf("gate stderr should mention observed version 0.0.0;\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "9.9.9") {
		t.Errorf("gate stderr should mention expected version 9.9.9;\nstderr:\n%s", stderr)
	}

	t.Logf("pack.first fired correctly (exit %d).\nGate stderr: %s", gateCmd.ProcessState.ExitCode(), stderr)
}
