// Package sourceoftruthreferenceprune_test is a synthetic-regression test
// for bug b.7v4: the SR-16 source-of-truth gate must NOT fire on vendored
// read-only clones under reference/, while still firing on real in-tree
// authoritative version sites.
//
// BACKGROUND (b.7v4 incident)
// ============================
// Phase 5 of release v0.7.6 aborted at preflight because the gate scanned
// reference/ (which holds vendored clones of other projects with their own
// pinned versions) and reported 4 false-positive violations. The fix extends
// the prune list in check-source-of-truth.ts so any path component named
// `reference` is skipped during traversal, the same way `node_modules`,
// `.git`, `dist`, `testdata`, and `fixtures` are already skipped.
//
// DESIGN
// ======
// Sub-case A (reference/ pruned, no false positive):
//   1. Create reference/test-clone-{rand}/package.json with a "version" field
//      (would have triggered P1 pre-fix).
//   2. Create reference/test-clone-{rand}/SKILL.md with `version:` in YAML
//      frontmatter (would have triggered P2 pre-fix).
//   3. Run the gate from repo root; assert exit 0 and silent stderr.
//
// Sub-case B (real in-tree SKILL.md still fires):
//   1. Create skills/test-skill-{rand}/SKILL.md with `version:` frontmatter
//      (this is OUTSIDE reference/, so the gate MUST still fire).
//   2. Run the gate; assert non-zero exit and that stderr names the file.
//
// CLEANUP-ON-FAILURE PATTERN
// ==========================
// t.Cleanup is registered before any mutation so it fires unconditionally
// even when t.Fatalf (which calls runtime.Goexit) is hit mid-test.
//
// DEPENDENCY
// ==========
// Requires `bun` and `git` in PATH; skips gracefully if either is absent.
package sourceoftruthreferenceprune_test

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
// then run the source-of-truth gate. Multiple tests under
// synthetic-regressions/ create fixture files (under tools/, skills/,
// reference/) and invoke the gate — without serialization they observe each
// other's fixtures and produce false positives/negatives. The lock file is
// gitignored.
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

func runGate(t *testing.T, root string) (int, string) {
	t.Helper()
	cmd := exec.Command("bun", "run", "pkg/ts-bun-client/scripts/check-source-of-truth.ts")
	cmd.Dir = root
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero is expected in sub-case B
	return cmd.ProcessState.ExitCode(), stderrBuf.String()
}

func TestSourceOfTruthReferencePrune(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not in PATH — skipping b.7v4 regression test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH — skipping b.7v4 regression test")
	}

	root := repoRoot(t)

	// Serialize against the other source-of-truth gate test, which also
	// mutates the repo tree.
	acquireSourceOfTruthLock(t, root)

	// ── Sub-case A: reference/ subtree must be pruned ────────────────────────
	t.Run("A_reference_subtree_pruned", func(t *testing.T) {
		suffix := fmt.Sprintf("%d", os.Getpid())
		refDir := filepath.Join(root, "reference", "test-clone-"+suffix)

		if err := os.MkdirAll(refDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", refDir, err)
		}

		// Cleanup removes the whole reference/ root if we created it, so the
		// tree is clean even if the test aborts mid-way. Use RemoveAll on the
		// nearest ancestor we own (refDir, not reference/ itself).
		t.Cleanup(func() {
			if err := os.RemoveAll(refDir); err != nil {
				t.Errorf("t.Cleanup: RemoveAll %s: %v", refDir, err)
			}
			// If reference/ is now empty, remove it too — it didn't exist
			// before this test ran and we shouldn't leave it behind.
			referenceRoot := filepath.Join(root, "reference")
			if entries, err := os.ReadDir(referenceRoot); err == nil && len(entries) == 0 {
				_ = os.Remove(referenceRoot)
			}
		})

		pkgPath := filepath.Join(refDir, "package.json")
		pkgContent := `{"name":"vendored","version":"9.9.9"}` + "\n"
		if err := os.WriteFile(pkgPath, []byte(pkgContent), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", pkgPath, err)
		}

		skillPath := filepath.Join(refDir, "SKILL.md")
		skillContent := "---\nname: vendored-skill\nversion: 1.0.0\n---\n\n# Vendored\n"
		if err := os.WriteFile(skillPath, []byte(skillContent), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", skillPath, err)
		}

		exitCode, stderr := runGate(t, root)

		if exitCode != 0 {
			t.Fatalf("gate exited %d for files under reference/ — expected 0 (pruned);\nstderr:\n%s", exitCode, stderr)
		}
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("gate produced unexpected stderr for files under reference/;\nstderr:\n%s", stderr)
		}

		t.Logf("b.7v4 sub-case A verified: reference/ subtree was pruned (no false positive).")
	})

	// ── Sub-case B: real in-tree SKILL.md must still fire ───────────────────
	t.Run("B_in_tree_skill_still_fires", func(t *testing.T) {
		suffix := fmt.Sprintf("%d", os.Getpid())
		skillDir := filepath.Join(root, "skills", "test-skill-"+suffix)

		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", skillDir, err)
		}

		t.Cleanup(func() {
			if err := os.RemoveAll(skillDir); err != nil {
				t.Errorf("t.Cleanup: RemoveAll %s: %v", skillDir, err)
			}
		})

		skillPath := filepath.Join(skillDir, "SKILL.md")
		skillContent := "---\nname: in-tree-skill\nversion: 9.9.9\n---\n\n# In-tree\n"
		if err := os.WriteFile(skillPath, []byte(skillContent), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", skillPath, err)
		}

		exitCode, stderr := runGate(t, root)

		if exitCode == 0 {
			t.Fatalf("gate exited 0 — expected non-zero for in-tree SKILL.md with version frontmatter at %s", skillPath)
		}

		relPath := filepath.Join("skills", "test-skill-"+suffix, "SKILL.md")
		if !strings.Contains(stderr, relPath) {
			t.Fatalf("gate stderr does not name the offending file %q;\nstderr:\n%s", relPath, stderr)
		}

		const gateKey = "invariant.source-of-truth"
		if !strings.Contains(stderr, gateKey) {
			t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
		}

		firstLine := strings.SplitN(strings.TrimSpace(stderr), "\n", 2)[0]
		var v map[string]string
		if err := json.Unmarshal([]byte(firstLine), &v); err != nil {
			t.Fatalf("first stderr line is not valid JSON: %v\nline: %s", err, firstLine)
		}
		if v["gate"] != gateKey {
			t.Fatalf("JSON violation gate=%q, want %q", v["gate"], gateKey)
		}

		t.Logf("b.7v4 sub-case B verified: in-tree SKILL.md with version: frontmatter still fires.\nOffending file: %s\nSample stderr line: %s", relPath, firstLine)
	})
}
