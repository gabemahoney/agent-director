// Package coveragegorootfires_test is a synthetic-regression test for the
// coverage.go-root gate (b.wvr coverage phase; scoped down under b.mgw).
//
// BACKGROUND
// ==========
// The coverage.go-root gate runs `go test ./... -race -count=1` at a worktree
// root.  A compile-time bug (e.g. a wrong-arity function call) must cause the
// gate to exit non-zero and emit a structured SR-14 JSON diagnostic to stderr
// with "gate":"coverage.go-root", naming the failing package by import path.
// This test injects exactly that class of defect and verifies the gate fires,
// then removes it and verifies the gate passes.
//
// DESIGN (b.mgw t1.2mt.s5, SR-4.1/4.3/4.4)
// ========================================
//  1. Scoped fixture tree.  Instead of running the gate bare at the REAL repo
//     root (which spawned a nested full-tree `go test ./... -race -count=1` of
//     the whole repository, ≈45s), the test materializes a tiny fixture Go
//     module into t.TempDir() and points the UNMODIFIED gate at it via the
//     gate's existing optional worktree-root argument (go-root.sh:14-16).  The
//     gate script itself is unchanged — only what it is pointed at differs.
//  2. Single-tree toggle.  ONE fixture tree is materialized.  The test injects
//     a wrong-arity call (compile error) into a fixture source file, runs the
//     real gate (FIRING proof), then removes the failure from the SAME tree and
//     runs the gate again (PASSING proof).  Two separately committed trees would
//     not prove the gate's failure-parsing fired on the injected defect.
//  3. Module path contains a "/" (example.test/gorootfixture).  The gate's
//     b.93m anchored FIRST_FAIL regex (`grep -E '^FAIL[[:space:]]+[^[:space:]]+/'`,
//     go-root.sh:37) resolves the import path only when it contains a "/";
//     a single-segment module would degrade the diagnostic to "(unknown
//     package)" and defeat the import-path identity assertion.
//  4. cd-fallback hazard closed.  go-root.sh runs `set -uo pipefail` WITHOUT
//     `-e` (go-root.sh:8): if the `cd "$1"` into the fixture were to fail, the
//     gate would silently fall through to `go test ./...` in the ORIGINAL cwd —
//     re-introducing the very full-tree run this bee removes — and a bare
//     exit-0 check would pass vacuously.  The passing path therefore asserts on
//     CAPTURED gate stdout that an `ok example.test/gorootfixture` line is
//     present, proving the FIXTURE module was the thing actually tested.
//
// b.93m TRAIL-LEAK ISOLATION DISPOSITIONS (SR-5)
// ==============================================
//   - HOME redirect (SR-5.1): KEPT, unconditionally.  The gate subprocess still
//     runs `go test`, whose child binaries resolve ~/.agent-director from HOME;
//     the trail singleton pins to it on first Emit.  Redirecting HOME to a
//     per-test temp dir keeps any nested emitter from writing ad-trail.jsonl
//     into the real home and racing the trail-leak canary.  Retained verbatim.
//   - COVERAGE_GO_ROOT_NESTED guard-SET (SR-5.2/5.3): REMOVED as demonstrably
//     obsolete.  The guard existed so that the nested full-tree run (which
//     re-entered THIS package and helper-tag-replay) would skip their seeds.go
//     mutation.  With the gate scoped to a self-contained fixture module, the
//     nested `go test ./...` walks only the fixture tree in t.TempDir() and can
//     never re-enter this package or the real repo — there is no recursion to
//     guard.  helper-tag-replay's own skip check is untouched (SR-5.2).
//   - Seeds flock (acquireSeedsLock / .seeds-mutation.lock, SR-5.3): REMOVED as
//     demonstrably obsolete.  It serialized concurrent mutations of the shared
//     real file pkg/api/apitest/seeds.go across this package and
//     helper-tag-replay.  This test no longer mutates seeds.go (or any tracked
//     file) — it mutates only fixture files under t.TempDir() — so there is no
//     shared state to serialize.  helper-tag-replay retains its own copy.
//   - Skip CHECK at the top of the test (SR-5.2): KEPT, disposition = KEEP.  It
//     is NOT obsolete: coverage-consumer-dryrun-fires (Epic t1.2mt.vg, not yet
//     landed) still spawns a nested full-tree `go test` of the real repo
//     without -short, which would re-enter this package.  Any future removal
//     belongs to t1.2mt.vg / t1.2mt.z4, not this Epic.  PM-PINNED — do not
//     re-open.
//
// CLEANUP (SR-5.4)
// ================
// The executed fixture lives entirely in t.TempDir(), which the testing
// framework removes automatically; no tracked file is mutated, so `git status`
// stays clean.  Every fixture-file mutation still registers its restore via
// t.Cleanup before the mutation, per the synthetic-regression convention.
//
// SLOW TEST
// =========
// Scoped down to seconds-scale: the nested run now compiles and tests a single
// trivial fixture module rather than the whole repo with -race.  Still skipped
// in -short mode and exercised via `make release-smoke`.
package coveragegorootfires_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Gate identifiers and assertion literals, declared once per package (SR-7.3).
const (
	// gateKey is the SR-14 diagnostic field proving the coverage.go-root gate
	// emitted the failure.
	gateKey = `"gate":"coverage.go-root"`
	// fixtureModulePath is the fixture module's import root.  It contains a "/"
	// so the gate's anchored FIRST_FAIL regex resolves the import path.
	fixtureModulePath = "example.test/gorootfixture"
	// fixturePkgImportPath is the import path of the failing fixture package —
	// what the firing diagnostic must name.
	fixturePkgImportPath = fixtureModulePath + "/broken"
	// okLine is the substring of `go test` stdout proving the fixture module was
	// actually tested (closes the cd-fallback hazard).
	okLine = "ok  \t" + fixturePkgImportPath
)

// fixtureGoMod is the fixture module manifest.  Module path contains a "/" so
// the gate's FIRST_FAIL parse resolves an import path (go-root.sh:37).
const fixtureGoMod = "module " + fixtureModulePath + "\n\ngo 1.21\n"

// fixtureSourceGood is the compiling fixture source: a package with a function
// and a passing test, so the gate's `go test ./...` produces an `ok` line.
const fixtureSourceGood = `package broken

// Add is a trivial function the fixture test exercises.
func Add(a, b int) int { return a + b }
`

// fixtureSourceBroken injects a wrong-arity call to Add — a compile error that
// `go test ./...` must catch, causing the gate to fire.
const fixtureSourceBroken = `package broken

// Add is a trivial function the fixture test exercises.
func Add(a, b int) int { return a + b }

// wrongArity is a deliberate compile error: Add takes 2 args, called with 1.
var _ = Add(1) // coverage.go-root synthetic regression (b.mgw)
`

const fixtureTestSource = `package broken

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add(1, 2) != 3")
	}
}
`

// materializeFixture writes the compiling fixture module into dir/broken and
// returns the path of the source file whose contents the failure toggle swaps.
func materializeFixture(t *testing.T, dir string) (brokenSrc string) {
	t.Helper()
	pkgDir := filepath.Join(dir, "broken")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture pkg: %v", err)
	}
	writes := map[string]string{
		filepath.Join(dir, "go.mod"):         fixtureGoMod,
		filepath.Join(pkgDir, "add.go"):      fixtureSourceGood,
		filepath.Join(pkgDir, "add_test.go"): fixtureTestSource,
	}
	for path, content := range writes {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", path, err)
		}
	}
	return filepath.Join(pkgDir, "add.go")
}

// runGate invokes the real, unmodified coverage.go-root gate against the
// fixture worktree root (worktreeRoot passed as the gate's optional argument)
// and returns its exit code and captured stdout/stderr.
func runGate(t *testing.T, root, worktreeRoot string) (exitCode int, stdout, stderr string) {
	t.Helper()
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "coverage", "go-root.sh")
	cmd := exec.Command("bash", gateScript, worktreeRoot)
	// HOME redirect (SR-5.1) — kept unconditionally: the gate's inner `go test`
	// binaries resolve ~/.agent-director from HOME; an isolated HOME prevents a
	// nested emitter from racing the trail-leak canary (b.93m).
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit is expected on the firing path — ignore err
	return cmd.ProcessState.ExitCode(), stdoutBuf.String(), stderrBuf.String()
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

// TestCoverageGoRootFires proves the unmodified coverage.go-root gate fires on
// an injected compile error (naming the failing package by import path) and
// passes once the error is removed (with the fixture module actually tested).
func TestCoverageGoRootFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs the coverage.go-root gate over a fixture module")
	}
	// PM-PINNED KEEP (SR-5.2): retained because coverage-consumer-dryrun-fires
	// (Epic t1.2mt.vg, not yet landed) still spawns a nested full-tree `go test`
	// of the real repo without -short, which would re-enter this package. Any
	// removal belongs to t1.2mt.vg / t1.2mt.z4, not this Epic. Do not re-open.
	if os.Getenv("COVERAGE_GO_ROOT_NESTED") == "1" {
		t.Skip("skipping recursive invocation from a nested coverage.go-root gate suite")
	}

	root := repoRoot(t)
	fixtureRoot := t.TempDir()
	brokenSrc := materializeFixture(t, fixtureRoot)

	// ── FIRING PROOF ────────────────────────────────────────────────────────
	// Inject the wrong-arity call into the SAME tree; restore is registered
	// before the mutation (SR-5.4) even though t.TempDir() is auto-removed.
	good, err := os.ReadFile(brokenSrc)
	if err != nil {
		t.Fatalf("read fixture source: %v", err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(brokenSrc, good, 0o644); err != nil {
			t.Errorf("t.Cleanup: restore fixture source: %v", err)
		}
	})
	if err := os.WriteFile(brokenSrc, []byte(fixtureSourceBroken), 0o644); err != nil {
		t.Fatalf("inject failure into fixture: %v", err)
	}

	exit, _, stderr := runGate(t, root, fixtureRoot)
	if exit == 0 {
		t.Fatalf("firing: expected coverage.go-root to exit non-zero on wrong-arity mutation, got 0\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("firing: gate stderr missing %q\nstderr:\n%s", gateKey, stderr)
	}
	// Import-path identity: the diagnostic must name the INJECTED failing
	// package, proving the b.93m parse fired on our defect (not a generic one).
	if !strings.Contains(stderr, fixturePkgImportPath) {
		t.Fatalf("firing: diagnostic does not name injected import path %q\nstderr:\n%s", fixturePkgImportPath, stderr)
	}

	// ── PASSING PROOF ───────────────────────────────────────────────────────
	// Remove the failure from the SAME tree and re-run the gate.
	if err := os.WriteFile(brokenSrc, good, 0o644); err != nil {
		t.Fatalf("remove failure from fixture: %v", err)
	}

	exit, stdout, stderr := runGate(t, root, fixtureRoot)
	if exit != 0 {
		t.Fatalf("passing: expected coverage.go-root to exit 0 after removing failure, got %d\nstderr:\n%s", exit, stderr)
	}
	// cd-fallback hazard closure: assert the FIXTURE module was actually tested.
	// A failed `cd` would have run `go test ./...` in the original cwd and this
	// ok-line would be absent (go-root.sh:8 uses `set -uo pipefail` without -e).
	if !strings.Contains(stdout, okLine) {
		t.Fatalf("passing: gate stdout missing %q — fixture module was not tested (cd-fallback?)\nstdout:\n%s", okLine, stdout)
	}
}
