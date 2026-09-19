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
// 5. Cleanup  : pack-first.sh writes into a per-test t.TempDir() (via
//               PACK_OUTPUT_DIR) rather than the shared repo-root dist/, so
//               the ephemeral tarball is auto-cleaned by the Go test runner
//               and concurrent tests never collide — no dist/ removal needed.
//
// SLOW TEST
// =========
// This test invokes `bun pm pack` (≈2–5 s).
// It is skipped in -short mode to keep default `go test ./...` fast.
package packfirstversionmismatch_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// acquireDistPackLock serializes tests that read or write the real
// pkg/ts-bun-client/dist/. This test `bun pm pack`s that dir via pack-first.sh;
// coverage-bun-test-fires rewrites it via `bun run build`. Without
// serialization a concurrent rebuild races the pack (b.aur). The lock lives
// under the OS temp dir — shared across these packages within a single
// `go test` run, and never touches the repo tree.
func acquireDistPackLock(t *testing.T) {
	t.Helper()
	lockPath := filepath.Join(os.TempDir(), "agent-director-ts-bun-dist-pack.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("acquireDistPackLock: open %s: %v", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatalf("acquireDistPackLock: flock: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	})
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

	// pack-first.sh packs the real pkg/ts-bun-client/dist/; serialize against
	// coverage-bun-test-fires which rebuilds it (b.aur).
	acquireDistPackLock(t)

	// ── 1. Run the gate with a mismatched target version, writing into an
	//      isolated output dir. An absolute t.TempDir() path is used because a
	//      relative PACK_OUTPUT_DIR would land inside the shared worktree root
	//      and not isolate concurrent tests; t.TempDir() also auto-cleans, so no
	//      dist/ cleanup is needed. ────────────────────────────────────────────
	outDir := t.TempDir()
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "pack-first.sh")
	gateCmd := exec.Command("bash", gateScript, "--target-version", "9.9.9")
	gateCmd.Dir = root
	gateCmd.Env = append(os.Environ(), "PACK_OUTPUT_DIR="+outDir)

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

	// The observed version is whatever pkg/ts-bun-client/package.json holds
	// at test time. On main it is the dev sentinel "0.0.0"; in a bumped
	// release worktree it is the post-bump version (e.g. "0.7.5"). Read it
	// dynamically so this test works in both contexts. Mirrors b.9ba's
	// runtime version read in cross-compile.sh.
	pkgJSON, err := os.ReadFile(filepath.Join(root, "pkg", "ts-bun-client", "package.json"))
	if err != nil {
		t.Fatalf("read pkg/ts-bun-client/package.json: %v", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(pkgJSON, &pkg); err != nil {
		t.Fatalf("unmarshal pkg/ts-bun-client/package.json: %v", err)
	}
	if pkg.Version == "" {
		t.Fatalf("pkg/ts-bun-client/package.json has empty .version")
	}

	if !strings.Contains(stderr, pkg.Version) {
		t.Errorf("gate stderr should mention observed version %q;\nstderr:\n%s", pkg.Version, stderr)
	}
	if !strings.Contains(stderr, "9.9.9") {
		t.Errorf("gate stderr should mention expected version 9.9.9;\nstderr:\n%s", stderr)
	}

	t.Logf("pack.first fired correctly (exit %d).\nGate stderr: %s", gateCmd.ProcessState.ExitCode(), stderr)
}

// TestPackFirstHonorsOutputDir is the b.ovv regression guard. It asserts that
// pack-first.sh writes the produced tarball into the directory named by
// PACK_OUTPUT_DIR (an absolute t.TempDir()) instead of the shared repo-root
// dist/. This is what lets the synthetic-regression tests use per-test output
// dirs and run under full `go test ./...` parallelism without racing on dist/.
//
// Pre-fix behaviour: the old pack-first.sh ignored PACK_OUTPUT_DIR and always
// wrote to repo-root dist/, so PACK_OUTPUT_DIR stayed empty — this test's
// "exactly one .tgz in the output dir" assertion failed. Post-fix it passes.
//
// The gate is run without --target-version, so pack-first.sh derives the
// target from the on-disk package.json, the embedded-version assert passes,
// and it exits 0 after landing the tarball in the output dir. (This mirrors
// the three sibling tests, which also invoke the gate with no --target-version.)
func TestPackFirstHonorsOutputDir(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs bun pm pack")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping pack.first output-dir test")
	}

	root := repoRoot(t)

	// pack-first.sh packs the real pkg/ts-bun-client/dist/; serialize against
	// coverage-bun-test-fires which rebuilds it (b.aur).
	acquireDistPackLock(t)

	// Isolated, absolute output dir. Auto-cleaned by the Go test runner.
	outDir := t.TempDir()

	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "pack-first.sh")
	gateCmd := exec.Command("bash", gateScript)
	gateCmd.Dir = root
	gateCmd.Env = append(os.Environ(), "PACK_OUTPUT_DIR="+outDir)
	out, err := gateCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pack-first.sh should exit 0 when target version matches; err=%v\n%s", err, out)
	}

	// The tarball must land in the honored output dir — this is the assertion
	// that fails against the pre-fix script (which wrote to repo-root dist/).
	matches, err := filepath.Glob(filepath.Join(outDir, "*.tgz"))
	if err != nil {
		t.Fatalf("glob %s: %v", outDir, err)
	}
	if len(matches) != 1 {
		t.Fatalf("pack-first.sh did not honor PACK_OUTPUT_DIR=%s: want exactly 1 .tgz there, found %d (%v).\nThe pre-fix script ignored the var and wrote to repo-root dist/.",
			outDir, len(matches), matches)
	}

	t.Logf("pack-first.sh honored PACK_OUTPUT_DIR: %s", matches[0])
}

// TestPackFirstHonorsPkgDir is the b.aur regression guard for RELEASE_PKG_DIR.
// It proves pack-first.sh packs the package named by RELEASE_PKG_DIR (deriving
// the target version from *that* copy's package.json), not the hardcoded
// repo-root pkg/ts-bun-client.
//
// Recipe (matches the TestSmokeHonorsDistDir isolation pattern):
//   - Copy the real package to an isolated dir INSIDE the repo root
//     (os.MkdirTemp(root, "pkgcopy") — not t.TempDir(), because `bun pm pack`'s
//     file resolution assumes the in-repo layout). The copy includes the
//     prebuilt dist/ so the pack has real content. Cleaned up via t.Cleanup.
//   - Rewrite the copy's package.json version to a distinctive 9.9.9.
//   - Run pack-first.sh with RELEASE_PKG_DIR=<copy> and an isolated
//     PACK_OUTPUT_DIR=<t.TempDir()>, no --target-version so the gate derives it
//     from the copy's package.json.
//   - Assert the produced tarball embeds 9.9.9.
//
// Pre-fix behaviour: a gate that re-hardcodes pkg/ts-bun-client would pack the
// real package (version 0.0.0 on main), and the embedded 9.9.9 assertion fails.
// Post-fix it packs the copy and 9.9.9 is embedded, so this passes.
//
// LOCK DECISION: no acquireDistPackLock needed for the pack itself — the gate
// packs the isolated in-repo copy's own dist/, and `bun pm pack` on the copy
// never touches the shared pkg/ts-bun-client/dist/. We DO hold the lock, but
// only to protect the `cp -r pkg/ts-bun-client/. <copy>/` read of the shared
// dist/ against coverage-bun-test-fires, which rebuilds it concurrently
// (b.aur); reading a half-rebuilt dist/ would produce a torn copy. Holding the
// shared lock for the (fast) copy is the least-surprising way to serialize
// that read.
func TestPackFirstHonorsPkgDir(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs bun pm pack")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping pack.first pkg-dir test")
	}

	root := repoRoot(t)

	// Serialize the copy's read of the shared dist/ against concurrent rebuilds
	// (b.aur). See LOCK DECISION in the doc comment.
	acquireDistPackLock(t)

	// Copy the real package INTO the repo root so `bun pm pack`'s in-repo path
	// resolution holds. Include the prebuilt dist/ (cp -r <src>/. <dst>/).
	pkgCopy, err := os.MkdirTemp(root, "pkgcopy")
	if err != nil {
		t.Fatalf("MkdirTemp under repo root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(pkgCopy) })

	realPkg := filepath.Join(root, "pkg", "ts-bun-client")
	cpCmd := exec.Command("cp", "-r", realPkg+"/.", pkgCopy+"/")
	if out, err := cpCmd.CombinedOutput(); err != nil {
		t.Fatalf("copy %s -> %s: %v\n%s", realPkg, pkgCopy, err, out)
	}

	// Rewrite the copy's package.json version to a distinctive marker.
	const wantVersion = "9.9.9"
	copyPkgJSON := filepath.Join(pkgCopy, "package.json")
	raw, err := os.ReadFile(copyPkgJSON)
	if err != nil {
		t.Fatalf("read copy package.json: %v", err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("unmarshal copy package.json: %v", err)
	}
	pkg["version"] = wantVersion
	rewritten, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		t.Fatalf("marshal copy package.json: %v", err)
	}
	if err := os.WriteFile(copyPkgJSON, rewritten, 0o644); err != nil {
		t.Fatalf("write copy package.json: %v", err)
	}

	// RELEASE_PKG_DIR is resolved relative to the worktree root (gateCmd.Dir),
	// so pass the copy's path relative to root.
	relPkgDir, err := filepath.Rel(root, pkgCopy)
	if err != nil {
		t.Fatalf("rel path of pkg copy: %v", err)
	}

	outDir := t.TempDir()
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "pack-first.sh")
	gateCmd := exec.Command("bash", gateScript)
	gateCmd.Dir = root
	gateCmd.Env = append(os.Environ(),
		"RELEASE_PKG_DIR="+relPkgDir,
		"PACK_OUTPUT_DIR="+outDir,
	)
	if out, err := gateCmd.CombinedOutput(); err != nil {
		t.Fatalf("pack-first.sh should exit 0 packing the copy (version %s); err=%v\n%s",
			wantVersion, err, out)
	}

	// Find the produced tarball and read its embedded package/package.json.
	matches, err := filepath.Glob(filepath.Join(outDir, "*.tgz"))
	if err != nil {
		t.Fatalf("glob %s: %v", outDir, err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly 1 .tgz in %s, found %d (%v)", outDir, len(matches), matches)
	}

	tarCmd := exec.Command("tar", "-xzf", matches[0], "--to-stdout", "package/package.json")
	embedded, err := tarCmd.Output()
	if err != nil {
		t.Fatalf("extract package/package.json from tarball: %v", err)
	}
	var embeddedPkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(embedded, &embeddedPkg); err != nil {
		t.Fatalf("unmarshal embedded package.json: %v\nraw:\n%s", err, embedded)
	}

	// The heart of the guard: the tarball embeds the COPY's version, proving the
	// gate honored RELEASE_PKG_DIR. A gate re-hardcoding pkg/ts-bun-client packs
	// the real package (0.0.0 on main) and this fails.
	if embeddedPkg.Version != wantVersion {
		t.Errorf("embedded tarball version: got %q, want %q — pack-first.sh did not "+
			"honor RELEASE_PKG_DIR=%s (b.aur)", embeddedPkg.Version, wantVersion, relPkgDir)
	}

	t.Logf("pack-first.sh honored RELEASE_PKG_DIR: tarball %s embeds version %s", matches[0], embeddedPkg.Version)
}
