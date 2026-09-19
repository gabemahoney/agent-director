// Package coherencebinaryversionfires_test is a synthetic-regression test for
// the coherence.binary-version gate (b.b3h replay scenario).
//
// BACKGROUND
// ==========
// The coherence.binary-version gate invokes each host-executable binary with
// the `version` verb and compares the reported .version field against the
// target.  A binary stamped with the wrong version string (e.g. an accidental
// AGENT_DIRECTOR_BUILD_VERSION override) must cause the gate to exit non-zero
// and emit a structured SR-14 JSON diagnostic to stderr with
// "gate":"coherence.binary-version.linux-amd64".
//
// DESIGN
// ======
// 1. Pre-req     : requires linux/amd64 host (gate skips non-host binaries).
// 2. Mutation    : build agent-director-linux-amd64 into a per-test
//                  t.TempDir() (RELEASE_DIST_DIR) with
//                  AGENT_DIRECTOR_BUILD_VERSION=9.9.9; the binary now reports
//                  version "9.9.9" while the target "0.0.0" is passed to the gate.
// 3. Gate        : bash skills/release-agent-director/gates/coherence/binary-version.sh
//                  is run from repo root with target arg "0.0.0", pointed at the
//                  isolated dir via COHERENCE_DIST_DIR.
// 4. Cleanup     : none needed — the binary lives in the isolated t.TempDir(),
//                  which the Go runner cleans up (b.aur). The prior
//                  save/restore of the shared repo-root binary raced concurrent
//                  tests.
//
// SLOW TEST
// =========
// This test runs `make release-binaries` (≈10s).
// It is skipped in -short mode to keep default `go test ./...` fast.
package coherencebinaryversionfires_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestCoherenceBinaryVersionFires verifies that the coherence.binary-version
// gate fires (exit != 0, stderr contains SR-14 diagnostic with observed vs
// expected version) when the host binary is stamped with a mismatched version.
func TestCoherenceBinaryVersionFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make release-binaries")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skipf("coherence.binary-version.linux-amd64 only runs on linux/amd64; host is %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	root := repoRoot(t)

	// Isolate the release output dir per-test (b.aur): we build a
	// wrong-version binary into it, so it must not be the shared repo-root
	// dist/. An absolute t.TempDir() also lets the Go runner auto-clean it,
	// which replaces the old save/restore-original-binary-bytes cleanup.
	distDir := t.TempDir()

	// ── 1. Build binary stamped with wrong version into the isolated dir ────
	makeCmd := exec.Command("make", "release-binaries")
	makeCmd.Dir = root
	makeCmd.Env = append(os.Environ(),
		"AGENT_DIRECTOR_BUILD_VERSION=9.9.9",
		"RELEASE_DIST_DIR="+distDir,
	)
	makeOut, err := makeCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make release-binaries (AGENT_DIRECTOR_BUILD_VERSION=9.9.9) failed: %v\n%s", err, makeOut)
	}

	// ── 2. Run the coherence gate with target 0.0.0 ─────────────────────────
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "coherence", "binary-version.sh")
	gateCmd := exec.Command("bash", gateScript, "0.0.0")
	gateCmd.Dir = root
	// Point the coherence gate at the isolated dist dir the binary was built into.
	gateCmd.Env = append(os.Environ(), "COHERENCE_DIST_DIR="+distDir)
	var stderrBuf strings.Builder
	gateCmd.Stderr = &stderrBuf
	gateCmd.Stdout = os.Stdout
	_ = gateCmd.Run() // non-zero exit is expected

	stderr := stderrBuf.String()

	// ── 5. Assertions ───────────────────────────────────────────────────────
	if gateCmd.ProcessState.ExitCode() == 0 {
		t.Fatal("expected coherence.binary-version gate to exit non-zero when version mismatches, but it exited 0")
	}

	const gateKey = `"gate":"coherence.binary-version.linux-amd64"`
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	if !strings.Contains(stderr, "9.9.9") {
		t.Errorf("gate stderr should mention observed version 9.9.9;\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "0.0.0") {
		t.Errorf("gate stderr should mention expected version 0.0.0;\nstderr:\n%s", stderr)
	}

	t.Logf("coherence.binary-version fired correctly (exit %d).\nGate stderr: %s", gateCmd.ProcessState.ExitCode(), stderr)
}
