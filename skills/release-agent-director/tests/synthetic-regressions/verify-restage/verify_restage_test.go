// Package verifyrestage_test is a synthetic-regression test for the
// install.verify-pkg gate (b.uys / b.wvr verify-restage scenario).
//
// BACKGROUND
// ==========
// install-verify.sh runs two sub-gates against a packed tarball:
//
//   install.clean-env   — npm installs the tarball into a fresh directory
//   install.verify-pkg  — bun --eval checks that the installed package exports
//                         Client as a function
//
// NOTE: The original test-verify-restage.sh contract mutated
// verify-installed-pkg.ts and exercised it directly.  install-verify.sh does
// NOT call verify-installed-pkg.ts; instead it uses a minimal bun --eval
// import check (see gate header comment).  This test therefore targets the
// actual install.verify-pkg gate shape: a tarball whose dist/index.js does not
// export Client as a callable constructor triggers the gate.
//
// DESIGN
// ======
// 1. Pack a real tarball via pack-first.sh (bun pm pack).
// 2. Extract the tarball into a temp dir, replace package/dist/index.js with
//    a stub that exports Client as a non-function (numeric constant), repack
//    into a new mutated tarball.
// 3. Run install-verify.sh --tarball <mutated>.
// 4. npm install succeeds (the tarball is structurally valid).
// 5. bun --eval fails because `typeof Client !== 'function'`.
// 6. Assert exit 1 and stderr contains "gate":"install.verify-pkg".
//
// SLOW TEST
// =========
// This test invokes bun pm pack + npm install + bun --eval (≈5–15 s).
// It is skipped in -short mode to keep default `go test ./...` fast.
package verifyrestage_test

import (
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

// TestVerifyRestageFires verifies that install.verify-pkg fires (exit 1,
// stderr contains "gate":"install.verify-pkg") when the installed package's
// dist/index.js does not export Client as a callable constructor.
func TestVerifyRestageFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs bun pm pack, npm install, bun --eval")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not on PATH — skipping verify-restage test")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH — skipping verify-restage test")
	}

	root := repoRoot(t)

	// pack-first.sh packs the real pkg/ts-bun-client/dist/; serialize against
	// coverage-bun-test-fires which rebuilds it (b.aur).
	acquireDistPackLock(t)

	// ── 1. Pack a clean tarball via pack-first.sh into an isolated output
	//      dir. An absolute t.TempDir() path is used because a relative
	//      PACK_OUTPUT_DIR would land inside the shared worktree root and not
	//      isolate concurrent tests; t.TempDir() also auto-cleans, so no dist/
	//      cleanup is needed. ─────────────────────────────────────────────────
	outDir := t.TempDir()
	packScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "pack-first.sh")
	packCmd := exec.Command("bash", packScript)
	packCmd.Dir = root
	packCmd.Env = append(os.Environ(), "PACK_OUTPUT_DIR="+outDir)
	packOut, err := packCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pack-first.sh failed: %v\n%s", err, packOut)
	}

	tgzGlob := filepath.Join(outDir, "*.tgz")
	matches, err := filepath.Glob(tgzGlob)
	if err != nil || len(matches) == 0 {
		t.Fatalf("no .tgz found in %s after pack-first.sh; glob=%q err=%v", outDir, tgzGlob, err)
	}
	cleanTarball := matches[0]

	// ── 3. Extract clean tarball and mutate dist/index.js ─────────────────
	// All temp work lives in t.TempDir() — Go cleans it up automatically.
	extractDir := t.TempDir()

	tarExtract := exec.Command("tar", "-xzf", cleanTarball, "-C", extractDir)
	if out, err := tarExtract.CombinedOutput(); err != nil {
		t.Fatalf("tar extract: %v\n%s", err, out)
	}

	// Replace package/dist/index.js with a stub that exports Client as a
	// numeric constant (not a function).  The `typeof Client !== 'function'`
	// check in install-verify.sh's bun --eval will throw and exit 1.
	mutatedIndexJS := `// mutated by verify-restage regression test — Client is not a constructor
export const Client = 42;
`
	indexPath := filepath.Join(extractDir, "package", "dist", "index.js")
	if err := os.WriteFile(indexPath, []byte(mutatedIndexJS), 0o644); err != nil {
		t.Fatalf("write mutated dist/index.js: %v", err)
	}

	// ── 4. Repack the mutated tree ────────────────────────────────────────
	mutatedTarball := filepath.Join(t.TempDir(), "mutated.tgz")
	tarRepack := exec.Command("tar", "-czf", mutatedTarball, "-C", extractDir, "package")
	if out, err := tarRepack.CombinedOutput(); err != nil {
		t.Fatalf("tar repack: %v\n%s", err, out)
	}

	// ── 5. Run install-verify.sh with the mutated tarball ─────────────────
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "pack", "install-verify.sh")
	gateCmd := exec.Command("bash", gateScript, "--tarball", mutatedTarball)
	gateCmd.Dir = root
	var stderrBuf strings.Builder
	gateCmd.Stdout = os.Stdout
	gateCmd.Stderr = &stderrBuf
	_ = gateCmd.Run() // non-zero exit is expected

	stderr := stderrBuf.String()

	// ── 6. Assertions ─────────────────────────────────────────────────────
	if gateCmd.ProcessState.ExitCode() == 0 {
		t.Fatal("install-verify.sh should exit non-zero when dist/index.js does not export Client as a function; got exit 0")
	}

	const gateKey = `"gate":"install.verify-pkg"`
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	t.Logf("install.verify-pkg fired correctly (exit %d).\nGate stderr: %s",
		gateCmd.ProcessState.ExitCode(), stderr)
}
