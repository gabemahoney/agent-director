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
// Each sub-case runs the real gate script in its own throwaway git repo
// (gateRepo): a copy of check-source-of-truth.ts and the authoritative
// package.json under t.TempDir(). The gate scans the repo its own copy sits
// in, so the fixtures never enter the real tree, where a sibling test or
// release gate scanning it would see them (b.9qj).
//
//  Sub-case A (drift):
//   1. Create tools/test-violation/package.json with
//      {"name":"violation","version":"9.9.9"}.
//   2. Run the gate from the repo root; capture stderr.
//   3. Assert exit code != 0, and that stderr names the offending file and
//      carries the gate key "invariant.source-of-truth".
//
//  Sub-case B (false positive):
//   1. Create docs/_test-fixture.md with prose mentioning "version 9.9.9".
//   2. Run the gate; assert exit 0 and that stderr is empty.
//
// DEPENDENCY
// ==========
// The test requires `bun` and `git` in PATH; it skips gracefully if either
// is absent.
package sourceoftruthdrift_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
}

// writeFile writes content to rel (slash-separated) under dir, creating
// parent directories.
func writeFile(t *testing.T, dir, rel string, content []byte) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// gateRepo stages a git repo under t.TempDir() holding the real gate script and
// the authoritative package.json, and returns its root (b.9qj).
func gateRepo(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	dir := t.TempDir()
	for _, rel := range []string{
		"pkg/ts-bun-client/scripts/check-source-of-truth.ts",
		"pkg/ts-bun-client/package.json",
	} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		writeFile(t, dir, rel, data)
	}
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
	return dir
}

// runGate runs the SR-16 gate from gateRoot and returns (exitCode, stderr).
func runGate(t *testing.T, gateRoot string) (int, string) {
	t.Helper()
	cmd := exec.Command("bun", "run", "pkg/ts-bun-client/scripts/check-source-of-truth.ts")
	cmd.Dir = gateRoot
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

	// ── Sub-case A: drift detection ───────────────────────────────────────────
	t.Run("A_drift_detected", func(t *testing.T) {
		gateRoot := gateRepo(t)

		// 1. A package.json in tools/ introduces a second authoritative version
		//    site.
		relPath := filepath.Join("tools", "test-violation", "package.json")
		writeFile(t, gateRoot, relPath, []byte(`{"name":"violation","version":"9.9.9"}`+"\n"))

		// 2. Run the SR-16 gate.
		exitCode, stderr := runGate(t, gateRoot)

		// 3. Assert non-zero exit.
		if exitCode == 0 {
			t.Fatalf("gate exited 0 — expected non-zero after introducing drift file %s", relPath)
		}

		// 4a. Assert stderr references the offending file.
		if !strings.Contains(stderr, relPath) {
			t.Fatalf("gate stderr does not name the offending file %q;\nstderr:\n%s", relPath, stderr)
		}

		// 4b. Assert stderr contains the gate key.
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
		gateRoot := gateRepo(t)

		// 1. A markdown file in docs/ with prose that mentions a version string.
		//    The gate must NOT fire for docs/ content.
		content := "# Test fixture\n\nThis document mentions version 9.9.9 in prose.\n" +
			"It also refers to version: 9.9.9 in a YAML-like comment for good measure.\n"
		writeFile(t, gateRoot, "docs/_test-fixture.md", []byte(content))

		// 2. Run the SR-16 gate.
		exitCode, stderr := runGate(t, gateRoot)

		// 3. Assert exit 0 (clean).
		if exitCode != 0 {
			t.Fatalf("gate exited %d for docs-only prose mention — false positive;\nstderr:\n%s", exitCode, stderr)
		}

		// 4. Assert stderr is silent (no violation lines).
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("gate produced unexpected stderr for docs-only prose mention;\nstderr:\n%s", stderr)
		}

		t.Logf("AC-4 sub-case B verified: prose mention in docs/ did not trigger gate.")
	})
}
