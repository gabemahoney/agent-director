// Package coverageconsumerdryrunfires_test is a synthetic-regression test for
// the coverage.go-consumer-dryrun gate (b.wvr coverage phase; scoped down under
// b.mgw).
//
// BACKGROUND
// ==========
// The coverage.go-consumer-dryrun gate runs `go test ./... -race -count=1`
// inside a worktree's tools/consumer-dryrun/ module (go-consumer-dryrun.sh:14-18
// cds into `$1/tools/consumer-dryrun` when a worktree-root argument is given).
// A failing test in that module must cause the gate to exit non-zero and emit a
// structured SR-14 JSON diagnostic to stderr with
// "gate":"coverage.go-consumer-dryrun", naming the failing package by import
// path. This test injects exactly that class of defect and verifies the gate
// fires, then removes it and verifies the gate passes.
//
// DESIGN (b.mgw t1.2mt.vg, SR-4.2 [all SR-4.1 requirements apply symmetrically],
// SR-4.3/4.4, SR-7.3)
// ===================================================================
//  1. Scoped fixture tree.  Instead of injecting a failing _test.go into the
//     REAL tools/consumer-dryrun/ module and running the gate at the real repo
//     root (which nested a ~45s `go test ./... -race -count=1` dominated by
//     compiling the parent agent-director module dependency), the test
//     materializes a tiny SELF-CONTAINED fixture worktree into t.TempDir() and
//     points the UNMODIFIED gate at it via the gate's existing optional
//     worktree-root argument (go-consumer-dryrun.sh:14-16).  The gate script
//     itself is unchanged — only what it is pointed at differs.  The fixture
//     module has NO dependency on the parent module (that dependency is what
//     made the old run ~45s), so the nested run is now seconds-scale.
//  2. Fixture layout.  The gate cds into `$1/tools/consumer-dryrun`, so the
//     fixture worktree MUST contain that exact subpath: the fixture Go module is
//     rooted at <fixtureRoot>/tools/consumer-dryrun/go.mod.  This layout is
//     load-bearing and is the cd-fallback hazard's trigger surface (see 4).
//  3. Single-tree toggle.  ONE fixture tree is materialized.  The test injects a
//     failing _test.go into the fixture module, runs the real gate (FIRING
//     proof), then removes the failure from the SAME tree and runs the gate
//     again (PASSING proof).  Two separately committed trees would not prove the
//     gate's failure-parsing fired on the injected defect.
//  4. cd-fallback hazard closed — WORSE HERE than for go-root.  go-consumer-
//     dryrun.sh runs `set -uo pipefail` WITHOUT `-e` (go-consumer-dryrun.sh:8).
//     If a fixture layout typo left `$1/tools/consumer-dryrun` missing, the
//     `cd "$1/tools/consumer-dryrun"` would fail silently and the gate would
//     fall through to `go test ./...` in the ORIGINAL cwd (the real repo) —
//     re-introducing exactly the full-tree run this bee removes — and a bare
//     exit-0 check would pass vacuously against the wrong tree.  The passing
//     path therefore asserts on CAPTURED gate stdout that an
//     `ok <fixture-package-import-path>` line is present, proving the FIXTURE
//     module was the thing actually tested.  This assertion is NON-NEGOTIABLE:
//     it is the only closure of the cd-fallback hole.  It also requires
//     capturing gate stdout to a buffer (the pre-rework test piped stdout
//     straight to os.Stdout, which could not be asserted on).
//  5. Module path contains a "/" (example.test/consumerdryrunfixture).  The
//     gate's b.93m anchored FIRST_FAIL regex
//     (`grep -E '^FAIL[[:space:]]+[^[:space:]]+/'`, go-consumer-dryrun.sh:39)
//     resolves the import path only when it contains a "/"; a single-segment
//     module would degrade the diagnostic to "(unknown package)" and defeat the
//     import-path identity assertion.
//
// b.93m TRAIL-LEAK ISOLATION DISPOSITIONS (SR-5, recorded per-mechanism —
// silent removal prohibited)
// ==============================================================
//   - HOME redirect (SR-5.1): KEPT, unconditionally.  The gate subprocess still
//     runs `go test`, whose child binaries resolve ~/.agent-director from HOME;
//     the trail singleton pins to it on first Emit.  Redirecting HOME to a
//     per-test temp dir keeps any nested emitter from writing ad-trail.jsonl
//     into the real home and racing the trail-leak canary's snapshot window
//     under `go test ./...` parallelism.  Retained verbatim (see runGate).
//   - COVERAGE_GO_ROOT_NESTED guard-SET / seeds flock (acquireSeedsLock /
//     .seeds-mutation.lock, SR-5.2/5.3): NEVER EXISTED in this package.  A
//     PM-confirmed independent grep of this package's directory finds no
//     COVERAGE_GO_ROOT_NESTED set/read, no acquireSeedsLock, no
//     .seeds-mutation.lock, and no seeds.go mutation — this test never spawned a
//     nested run of THIS package nor mutated the shared seeds.go, so neither the
//     nested-run skip guard nor the seeds flock was ever wired here.  There is
//     accordingly no SR-5.3 removal question to record: nothing to remove.
//     (helper-tag-replay retains its own copies of both, unaffected by this
//     work.)  After this scope-down, this package spawns NO nested run of any
//     real tree — the gate's nested `go test ./...` walks only the fixture
//     module in t.TempDir().
//   - -short skip guard (SR-4.3): KEPT.  The fixture stays -short-skipped in the
//     outer `go test ./...` tree and runs via `make release-smoke`.  The stale
//     "~45s" reasoning is rewritten below to seconds-scale, reflecting that the
//     nested run now compiles and tests only the trivial self-contained fixture.
//   - t.Cleanup (SR-5.4): KEPT, adapted to the TempDir fixture.  The executed
//     fixture lives entirely in t.TempDir() (auto-removed by the framework) and
//     this test mutates NO tracked file, so `git status` stays clean; every
//     fixture-file mutation still registers its restore via t.Cleanup before the
//     mutation, per the synthetic-regression convention.
//
// SLOW TEST
// =========
// Scoped down to seconds-scale: the nested run now compiles and tests a single
// trivial, self-contained fixture module rather than the real tools/consumer-
// dryrun/ module (whose parent-module dependency dominated the former ~45s).
// Still skipped in -short mode and exercised via `make release-smoke`.
package coverageconsumerdryrunfires_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Gate identifiers and assertion literals, declared once per package (SR-7.3).
const (
	// gateKey is the SR-14 diagnostic field proving the coverage.go-consumer-
	// dryrun gate emitted the failure.
	gateKey = `"gate":"coverage.go-consumer-dryrun"`
	// fixtureModulePath is the fixture module's import root.  It contains a "/"
	// so the gate's anchored FIRST_FAIL regex resolves the import path
	// (go-consumer-dryrun.sh:39).
	fixtureModulePath = "example.test/consumerdryrunfixture"
	// fixturePkgImportPath is the import path of the fixture package — what the
	// firing diagnostic must name and the passing `ok` line must carry.
	fixturePkgImportPath = fixtureModulePath
	// offendingArtifactField is the exact SR-14 offending_file_or_artifact JSON
	// field the firing diagnostic must carry. Its value is derived from the gate's
	// anchored FIRST_FAIL parse (go-consumer-dryrun.sh:39): the injected t.Fatal is
	// a runtime test failure, so `go test` emits "FAIL\texample.test/
	// consumerdryrunfixture\t<time>" and the awk field-2 extraction yields exactly
	// the package import path (no build-failed suffix). Anchoring the assertion on
	// this field (not on a bare Contains of the import path, which the SR-14
	// last-50-lines excerpt would also satisfy) makes a degraded parse
	// ("(unknown package — tools/consumer-dryrun)") fail the test.
	offendingArtifactField = `"offending_file_or_artifact":"` + fixturePkgImportPath + `"`
	// okLine is the substring of `go test` stdout proving the fixture module was
	// actually tested (closes the cd-fallback hazard).  `go test` renders passing
	// package lines as "ok  \t<import-path>".
	okLine = "ok  \t" + fixturePkgImportPath
	// consumerDryrunSubpath is the exact subpath the gate cds into. The fixture
	// module MUST be rooted here or the cd fails silently (set -uo pipefail
	// without -e) and the gate tests the original cwd instead.
	consumerDryrunSubpath = "tools/consumer-dryrun"
)

// fixtureGoMod is the fixture module manifest.  Module path contains a "/" so
// the gate's FIRST_FAIL parse resolves an import path (go-consumer-dryrun.sh:39).
const fixtureGoMod = "module " + fixtureModulePath + "\n\ngo 1.21\n"

// fixtureSource is the trivial fixture package: a function and a passing test,
// so the gate's `go test ./...` produces an `ok` line on the passing path.
const fixtureSource = `package consumerdryrunfixture

// Add is a trivial function the fixture test exercises.
func Add(a, b int) int { return a + b }
`

// fixtureTestSourceGood is the passing test file, present on both paths.
const fixtureTestSourceGood = `package consumerdryrunfixture

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add(1, 2) != 3")
	}
}
`

// fixtureTestSourceFailing is the injected failing test — a runtime t.Fatal in
// the fixture package that `go test ./...` must report as a FAIL line, causing
// the gate to fire and name this package by import path.
const fixtureTestSourceFailing = `package consumerdryrunfixture

import "testing"

// TestForcedFailure is toggled in by the coverage-consumer-dryrun-fires
// regression test to verify coverage.go-consumer-dryrun catches test failures.
func TestForcedFailure(t *testing.T) {
	t.Fatal("forced failure for coverage.go-consumer-dryrun regression test")
}
`

// materializeFixture writes the passing fixture module into
// dir/tools/consumer-dryrun (the subpath the gate cds into) and returns the path
// of the failure-toggle test file that the test swaps in and out.
func materializeFixture(t *testing.T, dir string) (togglePath string) {
	t.Helper()
	pkgDir := filepath.Join(dir, filepath.FromSlash(consumerDryrunSubpath))
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture pkg: %v", err)
	}
	writes := map[string]string{
		filepath.Join(pkgDir, "go.mod"):      fixtureGoMod,
		filepath.Join(pkgDir, "add.go"):      fixtureSource,
		filepath.Join(pkgDir, "add_test.go"): fixtureTestSourceGood,
	}
	for path, content := range writes {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", path, err)
		}
	}
	// The failure toggle is a separate file so the passing tree is exactly the
	// materialized tree with this file absent.
	return filepath.Join(pkgDir, "forced_failure_test.go")
}

// runGate invokes the real, unmodified coverage.go-consumer-dryrun gate against
// the fixture worktree root (worktreeRoot passed as the gate's optional
// argument) and returns its exit code and captured stdout/stderr.
func runGate(t *testing.T, root, worktreeRoot string) (exitCode int, stdout, stderr string) {
	t.Helper()
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "coverage", "go-consumer-dryrun.sh")
	cmd := exec.Command("bash", gateScript, worktreeRoot)
	// HOME redirect (SR-5.1) — kept unconditionally: the gate's inner `go test`
	// binaries resolve ~/.agent-director from HOME; an isolated HOME prevents a
	// nested emitter from racing the trail-leak canary (b.93m).
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf // captured, not piped to os.Stdout — see DESIGN 4.
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

// TestCoverageConsumerDryrunFires proves the unmodified coverage.go-consumer-
// dryrun gate fires on an injected test failure (naming the failing package by
// import path) and passes once the failure is removed (with the fixture module
// actually tested, per the cd-fallback closure).
func TestCoverageConsumerDryrunFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs the coverage.go-consumer-dryrun gate over a fixture module")
	}

	root := repoRoot(t)
	fixtureRoot := t.TempDir()
	togglePath := materializeFixture(t, fixtureRoot)

	// ── FIRING PROOF ────────────────────────────────────────────────────────
	// Inject the failing test into the SAME tree; restore (removal) is registered
	// before the mutation (SR-5.4) even though t.TempDir() is auto-removed.
	t.Cleanup(func() {
		if err := os.Remove(togglePath); err != nil && !os.IsNotExist(err) {
			t.Errorf("t.Cleanup: remove injected failure: %v", err)
		}
	})
	if err := os.WriteFile(togglePath, []byte(fixtureTestSourceFailing), 0o644); err != nil {
		t.Fatalf("inject failure into fixture: %v", err)
	}

	exit, _, stderr := runGate(t, root, fixtureRoot)
	if exit == 0 {
		t.Fatalf("firing: expected coverage.go-consumer-dryrun to exit non-zero on injected failure, got 0\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("firing: gate stderr missing %q\nstderr:\n%s", gateKey, stderr)
	}
	// Parse-derived identity: the offending_file_or_artifact field must carry the
	// value the gate's anchored FIRST_FAIL parse produced for OUR injected failure —
	// not merely appear somewhere in stderr. A bare Contains of the import path
	// would also match the last-50-lines excerpt the SR-14 diagnostic embeds, so it
	// could not distinguish a healthy parse from one that regressed to "(unknown
	// package — tools/consumer-dryrun)". Anchoring on the JSON field asserts the
	// parse itself resolved the import path, so a degraded parse fails this test
	// (b.93m).
	if !strings.Contains(stderr, offendingArtifactField) {
		t.Fatalf("firing: diagnostic offending_file_or_artifact is not the parse-derived %q\nstderr:\n%s", offendingArtifactField, stderr)
	}

	// ── PASSING PROOF ───────────────────────────────────────────────────────
	// Remove the failure from the SAME tree and re-run the gate.
	if err := os.Remove(togglePath); err != nil {
		t.Fatalf("remove failure from fixture: %v", err)
	}

	exit, stdout, stderr := runGate(t, root, fixtureRoot)
	if exit != 0 {
		t.Fatalf("passing: expected coverage.go-consumer-dryrun to exit 0 after removing failure, got %d\nstderr:\n%s", exit, stderr)
	}
	// cd-fallback hazard closure: assert the FIXTURE module was actually tested.
	// A failed `cd "$1/tools/consumer-dryrun"` would have run `go test ./...` in
	// the original cwd and this ok-line would be absent (go-consumer-dryrun.sh:8
	// uses `set -uo pipefail` without -e). NON-NEGOTIABLE.
	if !strings.Contains(stdout, okLine) {
		t.Fatalf("passing: gate stdout missing %q — fixture module was not tested (cd-fallback?)\nstdout:\n%s", okLine, stdout)
	}
}
