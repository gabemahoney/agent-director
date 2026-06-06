// Package tarballcoherencedrift_test is a synthetic-regression test for the
// coherence.tarball-version gate (b.wvr coherence-drift scenario).
//
// BACKGROUND
// ==========
// tarball-and-bump.sh sub-check 1 (coherence.tarball-version) extracts
// package/package.json from the packed tarball and asserts that its .version
// field matches the --target version.  A tarball whose embedded version was
// modified after packing (e.g. an accidental re-tar without re-bumping) must
// cause the gate to exit non-zero and emit an SR-14 diagnostic with
// "gate":"coherence.tarball-version".
//
// NOTE: pack-first-version-mismatch (added in round A, commit dcceb51)
// tests the pack.first gate — it fires when the embedded version in a
// freshly-produced tarball does not match the --target-version flag.  This
// test is complementary: it tests the coherence.tarball-version gate, which
// runs post-pack as part of the coherence phase.  The regression scenario
// here is drift introduced by post-pack tampering, not a stale package.json.
//
// DESIGN
// ======
// 1. Pack a clean tarball via pack-first.sh (embedded version "0.0.0").
// 2. Extract the tarball into a temp dir.
// 3. Edit package/package.json version to "9.9.9" (post-pack tampering).
// 4. Repack into a new mutated tarball.
// 5. Run tarball-and-bump.sh --tarball <mutated> --target 0.0.0.
//    Sub-check 1 (coherence.tarball-version): "9.9.9" != "0.0.0" → fail.
//    Sub-check 2 (coherence.bump-commit-integrity): pkg/ts-bun-client/
//    package.json is at "0.0.0" which matches --target → pass.
//    Overall: failed; exit 1.
// 6. Assert exit 1 and stderr contains "gate":"coherence.tarball-version".
//
// Cleanup: dist/ removed unconditionally via t.Cleanup; mutated tarballs
// live in t.TempDir() and are cleaned up by the Go test runner.
//
// SLOW TEST
// =========
// This test invokes `bun pm pack` once (≈2–5 s).
// It is skipped in -short mode to keep default `go test ./...` fast.
package tarballcoherencedrift_test

import (
	"encoding/json"
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

// TestTarballCoherenceDriftFires verifies that the coherence.tarball-version
// gate fires (exit 1, stderr contains "gate":"coherence.tarball-version")
// when the embedded package/package.json version in the tarball has been
// tampered with post-pack to a value that no longer matches --target.
func TestTarballCoherenceDriftFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs bun pm pack")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not on PATH — skipping tarball-coherence-drift test")
	}

	root := repoRoot(t)

	// ── 1. Remove dist/ when the test finishes ────────────────────────────
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(root, "dist")); err != nil {
			t.Errorf("t.Cleanup: remove dist/: %v", err)
		}
	})

	// ── 2. Pack a clean tarball (embedded version "0.0.0") ────────────────
	packScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "pack-first.sh")
	packCmd := exec.Command("bash", packScript)
	packCmd.Dir = root
	packOut, err := packCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pack-first.sh failed: %v\n%s", err, packOut)
	}

	tgzGlob := filepath.Join(root, "dist", "*.tgz")
	matches, err := filepath.Glob(tgzGlob)
	if err != nil || len(matches) == 0 {
		t.Fatalf("no .tgz found in dist/ after pack-first.sh; glob=%q err=%v", tgzGlob, err)
	}
	cleanTarball := matches[0]

	// ── 3. Extract clean tarball ──────────────────────────────────────────
	extractDir := t.TempDir()

	tarExtract := exec.Command("tar", "-xzf", cleanTarball, "-C", extractDir)
	if out, err := tarExtract.CombinedOutput(); err != nil {
		t.Fatalf("tar extract: %v\n%s", err, out)
	}

	// ── 4. Tamper: bump embedded package.json version to "9.9.9" ─────────
	embeddedPkgJSON := filepath.Join(extractDir, "package", "package.json")
	raw, err := os.ReadFile(embeddedPkgJSON)
	if err != nil {
		t.Fatalf("read embedded package.json: %v", err)
	}

	var pkgMap map[string]interface{}
	if err := json.Unmarshal(raw, &pkgMap); err != nil {
		t.Fatalf("unmarshal embedded package.json: %v", err)
	}
	pkgMap["version"] = "9.9.9"
	tampered, err := json.Marshal(pkgMap)
	if err != nil {
		t.Fatalf("marshal tampered package.json: %v", err)
	}
	if err := os.WriteFile(embeddedPkgJSON, append(tampered, '\n'), 0o644); err != nil {
		t.Fatalf("write tampered package.json: %v", err)
	}

	// ── 5. Repack the tampered tree ───────────────────────────────────────
	mutatedTarball := filepath.Join(t.TempDir(), "tampered.tgz")
	tarRepack := exec.Command("tar", "-czf", mutatedTarball, "-C", extractDir, "package")
	if out, err := tarRepack.CombinedOutput(); err != nil {
		t.Fatalf("tar repack: %v\n%s", err, out)
	}

	// ── 6. Run tarball-and-bump.sh with target "0.0.0" ────────────────────
	// tarball has "9.9.9" but target is "0.0.0" → coherence.tarball-version fires.
	// pkg/ts-bun-client/package.json is still "0.0.0" → bump-commit-integrity passes.
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "coherence", "tarball-and-bump.sh")
	gateCmd := exec.Command("bash", gateScript,
		"--tarball", mutatedTarball,
		"--target", "0.0.0",
		"--worktree-root", root,
	)
	gateCmd.Dir = root

	var stderrBuf strings.Builder
	gateCmd.Stdout = os.Stdout // consolidated JSON → stdout (visible in test log)
	gateCmd.Stderr = &stderrBuf
	_ = gateCmd.Run() // non-zero exit is expected

	stderr := stderrBuf.String()

	// ── 7. Assertions ─────────────────────────────────────────────────────
	if gateCmd.ProcessState.ExitCode() == 0 {
		t.Fatal("tarball-and-bump.sh should exit non-zero when tarball version was tampered; got exit 0")
	}

	const gateKey = `"gate":"coherence.tarball-version"`
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	if !strings.Contains(stderr, "9.9.9") {
		t.Errorf("gate stderr should mention observed version 9.9.9;\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "0.0.0") {
		t.Errorf("gate stderr should mention expected version 0.0.0;\nstderr:\n%s", stderr)
	}

	t.Logf("coherence.tarball-version fired correctly (exit %d).\nGate stderr: %s",
		gateCmd.ProcessState.ExitCode(), stderr)
}
