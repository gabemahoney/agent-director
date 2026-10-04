// Package sourceoftruthreleaseworkprune_test is a synthetic-regression test
// for bug b.vvv: the SR-16 source-of-truth gate must NOT fire on the
// release skill's per-version bump worktree under .release-work/, while
// still firing on real in-tree authoritative version sites.
//
// BACKGROUND (b.vvv incident)
// ============================
// The retried v0.7.5 release aborted in Phase 5 at the coverage.go-root gate
// because the source-of-truth gate scanned .release-work/release-v0.7.5/
// (the worktree the release skill's branch-and-bump phase creates to host
// the bumped package.json) and reported the bumped
// .release-work/release-v0.7.5/pkg/ts-bun-client/package.json as a
// secondary authoritative version site. The fix extends the prune list in
// check-source-of-truth.ts so any path component named `.release-work` is
// skipped during traversal, the same way `node_modules`, `.git`, `dist`,
// `testdata`, `fixtures`, and `reference` are already skipped.
//
// DESIGN
// ======
// Each sub-case runs the real gate script in its own throwaway git repo
// (gateRepo): a copy of check-source-of-truth.ts and the authoritative
// package.json under t.TempDir(). The gate scans the repo its own copy sits
// in, so the .release-work/ and tools/ fixtures never enter the real tree,
// where a sibling test or release gate scanning it would see them (b.9qj).
//
// Sub-case A (.release-work/ pruned, no false positive):
//   1. Create .release-work/release-v9.9.9/pkg/ts-bun-client/package.json
//      with a "version" field (this is exactly what the release skill's
//      bump commit produces inside the worktree — would have triggered P1
//      pre-fix).
//   2. Run the gate from the repo root; assert exit 0 and silent stderr.
//
// Sub-case B (sibling violation OUTSIDE .release-work/ still fires):
//   1. Create tools/test-bvvv/package.json with a "version" field
//      (this is OUTSIDE .release-work/, so the gate MUST still fire).
//   2. Run the gate; assert non-zero exit, stderr names the offending
//      relative path, stderr contains "invariant.source-of-truth", and the
//      first stderr line is valid JSON with gate == that key.
//
// DEPENDENCY
// ==========
// Requires `bun` and `git` in PATH; skips gracefully if either is absent.
package sourceoftruthreleaseworkprune_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func runGate(t *testing.T, gateRoot string) (int, string) {
	t.Helper()
	cmd := exec.Command("bun", "run", "pkg/ts-bun-client/scripts/check-source-of-truth.ts")
	cmd.Dir = gateRoot
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero is expected in sub-case B
	return cmd.ProcessState.ExitCode(), stderrBuf.String()
}

func TestSourceOfTruthReleaseWorkPrune(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not in PATH — skipping b.vvv regression test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH — skipping b.vvv regression test")
	}

	// ── Sub-case A: .release-work/ subtree must be pruned ────────────────────
	t.Run("A_release_work_subtree_pruned", func(t *testing.T) {
		gateRoot := gateRepo(t)

		writeFile(t, gateRoot, ".release-work/release-v9.9.9/pkg/ts-bun-client/package.json",
			[]byte(`{"name":"agent-director-bumped","version":"9.9.9"}`+"\n"))

		exitCode, stderr := runGate(t, gateRoot)

		if exitCode != 0 {
			t.Fatalf("gate exited %d for files under .release-work/ — expected 0 (pruned);\nstderr:\n%s", exitCode, stderr)
		}
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("gate produced unexpected stderr for files under .release-work/;\nstderr:\n%s", stderr)
		}

		t.Logf("b.vvv sub-case A verified: .release-work/ subtree was pruned (no false positive).")
	})

	// ── Sub-case B: sibling violation OUTSIDE .release-work/ still fires ────
	t.Run("B_sibling_violation_outside_release_work_still_fires", func(t *testing.T) {
		gateRoot := gateRepo(t)

		relPath := filepath.Join("tools", "test-bvvv", "package.json")
		writeFile(t, gateRoot, relPath, []byte(`{"name":"sibling","version":"9.9.9"}`+"\n"))

		exitCode, stderr := runGate(t, gateRoot)

		if exitCode == 0 {
			t.Fatalf("gate exited 0 — expected non-zero for sibling package.json with version at %s", relPath)
		}

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

		t.Logf("b.vvv sub-case B verified: sibling package.json outside .release-work/ still fires.\nOffending file: %s\nSample stderr line: %s", relPath, firstLine)
	})
}
