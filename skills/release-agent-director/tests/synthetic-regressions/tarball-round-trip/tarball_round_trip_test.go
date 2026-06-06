// Package tarballroundtrip_test is a synthetic-regression test for
// the pack.byte-identical-normalized gate (b.wvr round-trip scenario).
//
// BACKGROUND
// ==========
// The repack-and-verify.sh gate packs the ts-bun-client package a second time
// and byte-diffs the extracted contents of both packs.  Nondeterminism in bun
// pm pack (timestamps, platform artefacts, etc.) must be caught before a
// release tarball is published.  This file re-anchors the shell script
// test-tarball-round-trip.sh with two Go table entries:
//
//  1. TestTarballRoundTripByteIdentical — two packs of identical source
//     produce identical content; gate exits 0.
//  2. TestTarballRoundTripMismatchDetected — a synthetic "first" tarball
//     with a sentinel-injected dist/index.js differs from a fresh pack;
//     gate exits 1 with "gate":"pack.byte-identical-normalized" on stderr.
//
// DESIGN
// ======
// Mismatch detection (test 2) avoids needing a second full bun pack by
// constructing a minimal fake tarball whose package/dist/index.js contains
// a sentinel comment guaranteed to differ from the real build output.
// repack-and-verify.sh packs internally when called, so the gate sees the
// real second-pack contents versus our mutated first-pack contents.
//
// Cleanup: dist/ is removed unconditionally via t.Cleanup (both tests).
// Temporary tarballs/dirs created outside the repo live in t.TempDir() and
// are cleaned up by the Go test runner automatically.
//
// SLOW TEST
// =========
// Both tests invoke `bun pm pack` (≈2–5 s each).
// They are skipped in -short mode to keep default `go test ./...` fast.
package tarballroundtrip_test

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

// TestTarballRoundTripByteIdentical verifies that packing the same source
// twice produces byte-identical tarballs: pack-first.sh writes the first
// tarball, then repack-and-verify.sh packs a second time internally and diffs
// both; the gate must exit 0.
func TestTarballRoundTripByteIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs bun pm pack twice")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not on PATH — skipping tarball-round-trip test")
	}

	root := repoRoot(t)

	// ── 1. Remove dist/ when the test finishes ────────────────────────────
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(root, "dist")); err != nil {
			t.Errorf("t.Cleanup: remove dist/: %v", err)
		}
	})

	// ── 2. Pack the first tarball ─────────────────────────────────────────
	packScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "pack-first.sh")
	packCmd := exec.Command("bash", packScript)
	packCmd.Dir = root
	packOut, err := packCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pack-first.sh failed: %v\n%s", err, packOut)
	}

	// Locate the produced tarball.
	tgzGlob := filepath.Join(root, "dist", "*.tgz")
	matches, err := filepath.Glob(tgzGlob)
	if err != nil || len(matches) == 0 {
		t.Fatalf("no .tgz found in dist/ after pack-first.sh; glob=%q err=%v", tgzGlob, err)
	}
	firstTarball := matches[0]

	// ── 3. Run repack-and-verify.sh — expects exit 0 ──────────────────────
	repackScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "repack-and-verify.sh")
	repackCmd := exec.Command("bash", repackScript, "--first", firstTarball)
	repackCmd.Dir = root
	var stderrBuf strings.Builder
	repackCmd.Stdout = os.Stdout
	repackCmd.Stderr = &stderrBuf
	_ = repackCmd.Run()

	// ── 4. Assertions ─────────────────────────────────────────────────────
	exitCode := repackCmd.ProcessState.ExitCode()
	if exitCode != 0 {
		t.Fatalf("repack-and-verify.sh should exit 0 for identical source packs; got exit %d\nstderr:\n%s",
			exitCode, stderrBuf.String())
	}

	t.Logf("pack.byte-identical-normalized passed (exit 0). Two packs of identical source are byte-identical.")
}

// TestTarballRoundTripMismatchDetected verifies that the
// pack.byte-identical-normalized gate fires when the "first" tarball differs
// from a fresh pack: a synthetic tarball with a sentinel-injected
// package/dist/index.js is passed as --first; the gate must exit 1 and emit
// an SR-14 diagnostic containing "gate":"pack.byte-identical-normalized".
func TestTarballRoundTripMismatchDetected(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs bun pm pack (repack-and-verify.sh internal second pack)")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not on PATH — skipping tarball-round-trip mismatch test")
	}

	root := repoRoot(t)

	// ── 1. Remove dist/ when the test finishes ────────────────────────────
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(root, "dist")); err != nil {
			t.Errorf("t.Cleanup: remove dist/: %v", err)
		}
	})

	// ── 2. Build a synthetic "first" tarball with a mutated dist/index.js ─
	// The tarball is placed outside the repo (in t.TempDir()) so it cannot
	// interfere with the repo tree.  The file structure mirrors what bun pm
	// pack produces (package/ prefix), but the dist/index.js content
	// contains a sentinel comment that will never appear in a real pack.
	synthDir := t.TempDir()
	pkgDir := filepath.Join(synthDir, "package")

	// Create the minimal package tree.
	for _, dir := range []string{
		filepath.Join(pkgDir, "dist"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// Write a minimal package.json that matches the real package name/version.
	pkgJSON := `{"name":"agent-director","version":"0.0.0","main":"dist/index.js"}` + "\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatalf("write synthetic package.json: %v", err)
	}

	// Write dist/index.js with a sentinel that guarantees a diff with the
	// real pack output.
	sentinel := "/* synthetic-mismatch-sentinel — tarball-round-trip regression test */\nexport const Client = null;\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "dist", "index.js"), []byte(sentinel), 0o644); err != nil {
		t.Fatalf("write synthetic dist/index.js: %v", err)
	}

	// Pack the synthetic directory into a .tgz.
	syntheticTarball := filepath.Join(synthDir, "synthetic-first.tgz")
	tarCmd := exec.Command("tar", "-czf", syntheticTarball, "-C", synthDir, "package")
	tarCmd.Dir = synthDir
	if out, err := tarCmd.CombinedOutput(); err != nil {
		t.Fatalf("tar create synthetic tarball: %v\n%s", err, out)
	}

	// ── 3. Run repack-and-verify.sh with the synthetic "first" tarball ────
	repackScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "repack-and-verify.sh")
	repackCmd := exec.Command("bash", repackScript, "--first", syntheticTarball)
	repackCmd.Dir = root
	var stderrBuf strings.Builder
	repackCmd.Stdout = os.Stdout
	repackCmd.Stderr = &stderrBuf
	_ = repackCmd.Run()

	stderr := stderrBuf.String()

	// ── 4. Assertions ─────────────────────────────────────────────────────
	if repackCmd.ProcessState.ExitCode() == 0 {
		t.Fatal("repack-and-verify.sh should exit non-zero when first tarball differs from fresh pack; got exit 0")
	}

	const gateKey = `"gate":"pack.byte-identical-normalized"`
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	t.Logf("pack.byte-identical-normalized fired correctly (exit %d).\nGate stderr: %s",
		repackCmd.ProcessState.ExitCode(), stderr)
}
