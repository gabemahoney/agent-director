// Package sourceoftruthdrift_test is a synthetic-regression test for SR-16.
//
// BACKGROUND (SR-16 incident class)
// ===================================
// Before SR-16, multiple files in the repo (Makefile, SKILL.md files,
// package.json siblings, Go internal/version constants) each hard-coded their
// own version strings independently.  This caused drift: a version bump in one
// place would not propagate to the others, leading to contradictory releases.
// SR-16 designates pkg/ts-bun-client/package.json as the single authoritative
// version site and enforces it via the check-source-of-truth.ts gate, which
// CI must run before any release step.  This test (AC-4) proves that:
//
//  A. the gate catches a NEW authoritative site (a package.json with "version"
//     in a directory that is not excluded);
//  B. the gate does NOT fire for prose mentions of a version in docs/ (false-
//     positive guard).
//
// DESIGN
// ======
//  Sub-case A (drift):
//   1. Create a temporary tools/test-violation-{rand}/package.json with
//      {"name":"violation","version":"9.9.9"}.
//   2. Register t.Cleanup that os.RemoveAll()s the temp directory.
//   3. Run bun run pkg/ts-bun-client/scripts/check-source-of-truth.ts from
//      repo root; capture stderr.
//   4. Assert exit code != 0.
//   5. Assert stderr contains the offending file path AND the gate key
//      "invariant.source-of-truth".
//
//  Sub-case B (false positive):
//   1. Create a temporary docs/_test-fixture-{rand}.md with prose mentioning
//      "version 9.9.9".
//   2. Register t.Cleanup that os.Remove()s the file.
//   3. Run the gate; assert exit 0 and that stderr is empty.
//
// CLEANUP-ON-FAILURE PATTERN
// ==========================
// t.Cleanup is registered before any mutation, so it fires unconditionally
// even when t.Fatalf (which calls runtime.Goexit) is hit mid-test.  After
// a test run, `git status` must show a clean tree.
//
// DEPENDENCY
// ==========
// The test requires `bun` and `git` in PATH; it skips gracefully if either
// is absent.
package sourceoftruthdrift_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// acquireSourceOfTruthLock serializes tests that mutate the repo tree and
// then run the source-of-truth gate. The companion test in
// source-of-truth-reference-prune/ also writes fixture files at the repo
// root; without serialization the two tests observe each other's fixtures
// and produce flaky results. The lock file is gitignored.
func acquireSourceOfTruthLock(t *testing.T, root string) {
	t.Helper()
	lockPath := filepath.Join(root, "pkg", "ts-bun-client", "scripts", ".source-of-truth-mutation.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("acquireSourceOfTruthLock: open %s: %v", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatalf("acquireSourceOfTruthLock: flock: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	})
}

// repoRoot walks up from the package's working directory (set by `go test` to
// the package directory) until it finds a go.mod file.
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

// runGate runs the SR-16 gate from repoRoot and returns (exitCode, stderr).
func runGate(t *testing.T, root string) (int, string) {
	t.Helper()
	cmd := exec.Command("bun", "run", "pkg/ts-bun-client/scripts/check-source-of-truth.ts")
	cmd.Dir = root
	// Capture stdout and stderr separately: the gate writes violations to stderr
	// and is silent on stdout.
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit is expected in sub-case A — ignore error here
	return cmd.ProcessState.ExitCode(), stderrBuf.String()
}

// TestSourceOfTruthDrift is the AC-4 demonstration for SR-16.
func TestSourceOfTruthDrift(t *testing.T) {
	// Dependency guards
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not in PATH — skipping SR-16 gate test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH — skipping SR-16 gate test")
	}

	root := repoRoot(t)

	// Serialize against the companion source-of-truth-reference-prune test,
	// which also mutates the repo tree.
	acquireSourceOfTruthLock(t, root)

	// ── Sub-case A: drift detection ───────────────────────────────────────────
	t.Run("A_drift_detected", func(t *testing.T) {
		// 1. Create a temporary package.json in tools/ that introduces a second
		//    authoritative version site.  The suffix makes it unique per run and
		//    avoids collisions when tests run in parallel.
		suffix := fmt.Sprintf("%d", os.Getpid())
		violationDir := filepath.Join(root, "tools", "test-violation-"+suffix)
		violationPkg := filepath.Join(violationDir, "package.json")

		if err := os.MkdirAll(violationDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", violationDir, err)
		}

		// 2. Register cleanup BEFORE writing, so the directory is removed even if
		//    a later t.Fatalf fires.
		t.Cleanup(func() {
			if err := os.RemoveAll(violationDir); err != nil {
				t.Errorf("t.Cleanup: RemoveAll %s: %v", violationDir, err)
			}
		})

		pkgContent := `{"name":"violation","version":"9.9.9"}` + "\n"
		if err := os.WriteFile(violationPkg, []byte(pkgContent), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", violationPkg, err)
		}

		// 3. Run the SR-16 gate.
		exitCode, stderr := runGate(t, root)

		// 4. Assert non-zero exit.
		if exitCode == 0 {
			t.Fatalf("gate exited 0 — expected non-zero after introducing drift file %s", violationPkg)
		}

		// 5a. Assert stderr references the offending file.
		relPath := filepath.Join("tools", "test-violation-"+suffix, "package.json")
		if !strings.Contains(stderr, relPath) {
			t.Fatalf("gate stderr does not name the offending file %q;\nstderr:\n%s", relPath, stderr)
		}

		// 5b. Assert stderr contains the gate key.
		const gateKey = "invariant.source-of-truth"
		if !strings.Contains(stderr, gateKey) {
			t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
		}

		// Validate that at least the first line is valid JSON with the expected gate field.
		firstLine := strings.SplitN(strings.TrimSpace(stderr), "\n", 2)[0]
		var v map[string]string
		if err := json.Unmarshal([]byte(firstLine), &v); err != nil {
			t.Fatalf("first stderr line is not valid JSON: %v\nline: %s", err, firstLine)
		}
		if v["gate"] != gateKey {
			t.Fatalf("JSON violation gate=%q, want %q", v["gate"], gateKey)
		}

		t.Logf("AC-4 sub-case A verified: gate caught drift.\nOffending file: %s\nSample stderr line: %s", relPath, firstLine)
	})

	// ── Sub-case B: false-positive guard ─────────────────────────────────────
	t.Run("B_false_positive_not_triggered", func(t *testing.T) {
		// 1. Create a temporary markdown file in docs/ with prose that mentions a
		//    version string.  The gate must NOT fire for docs/ content.
		suffix := fmt.Sprintf("%d", os.Getpid())
		fixtureFile := filepath.Join(root, "docs", "_test-fixture-"+suffix+".md")

		content := "# Test fixture\n\nThis document mentions version 9.9.9 in prose.\n" +
			"It also refers to version: 9.9.9 in a YAML-like comment for good measure.\n"
		if err := os.WriteFile(fixtureFile, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", fixtureFile, err)
		}

		// 2. Register cleanup before any assertion.
		t.Cleanup(func() {
			if err := os.Remove(fixtureFile); err != nil && !os.IsNotExist(err) {
				t.Errorf("t.Cleanup: Remove %s: %v", fixtureFile, err)
			}
		})

		// 3. Run the SR-16 gate.
		exitCode, stderr := runGate(t, root)

		// 4. Assert exit 0 (clean).
		if exitCode != 0 {
			t.Fatalf("gate exited %d for docs-only prose mention — false positive;\nstderr:\n%s", exitCode, stderr)
		}

		// 5. Assert stderr is silent (no violation lines).
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("gate produced unexpected stderr for docs-only prose mention;\nstderr:\n%s", stderr)
		}

		t.Logf("AC-4 sub-case B verified: prose mention in docs/ did not trigger gate.")
	})
}
