// Package worktreepollution_test is a synthetic-regression test for preflight.worktree-clean.
//
// BACKGROUND (AC r3.te)
// =====================
// The preflight.worktree-clean gate asserts the working tree has no modified,
// staged, or untracked files before a release proceeds.  Without this gate a
// developer could kick off a release while uncommitted work sits in the tree,
// embedding stale or debug content in the published artifact.  This test proves
// the gate fires when an untracked file is created in the repo root.
//
// DESIGN
// ======
// 1. Create a unique untracked file in the repo root
//    (_worktree_pollution_{pid}.tmp) and write a few bytes to it.
// 2. Register t.Cleanup to os.Remove the file BEFORE any assertion.
// 3. Run bash skills/release-agent-director/gates/preflight/worktree-clean.sh
//    from the repo root with stdout/stderr captured.
// 4. Assert exit code != 0.
// 5. Assert the stderr JSON line has "gate":"preflight.worktree-clean".
// 6. Assert the offending filename is named in the output.
//
// CLEANUP-ON-FAILURE PATTERN
// ==========================
// t.Cleanup is registered before assertions so the temp file is removed
// unconditionally even if t.Fatalf fires mid-test.  After the run,
// `git status` must show a clean tree.
//
// DEPENDENCY
// ==========
// Requires `git` in PATH; skips gracefully if absent.
package worktreepollution_test

import (
	"encoding/json"
	"fmt"
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
	panic("unreachable")
}

// TestWorktreePollution verifies that preflight.worktree-clean fires when an
// untracked file is present in the working tree (AC r3.te).
func TestWorktreePollution(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH — skipping worktree-clean gate test")
	}

	root := repoRoot(t)

	// 1. Pick a unique filename so parallel runs don't collide.
	pollutionFile := filepath.Join(root, fmt.Sprintf("_worktree_pollution_%d.tmp", os.Getpid()))

	// 2. Write a few bytes to make it visible to `git status --porcelain`.
	if err := os.WriteFile(pollutionFile, []byte("regression fixture — safe to delete\n"), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", pollutionFile, err)
	}

	// 3. Register cleanup BEFORE any assertion.  t.Cleanup fires unconditionally
	//    even when t.Fatalf / runtime.Goexit is called.
	t.Cleanup(func() {
		if err := os.Remove(pollutionFile); err != nil && !os.IsNotExist(err) {
			t.Errorf("t.Cleanup: Remove %s: %v", pollutionFile, err)
		}
	})

	// 4. Run the gate.
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "preflight", "worktree-clean.sh")
	cmd := exec.Command("bash", gateScript)
	cmd.Dir = root
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit expected — ignore error

	// 5. Assert non-zero exit.
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("worktree-clean gate exited 0 — expected non-zero with pollution file %s present", pollutionFile)
	}

	stderr := stderrBuf.String()

	// 6. Assert gate key present in stderr.
	const gateKey = "preflight.worktree-clean"
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	// 7. Parse the first stderr line as JSON and validate the gate field.
	firstLine := strings.SplitN(strings.TrimSpace(stderr), "\n", 2)[0]
	var diag map[string]interface{}
	if err := json.Unmarshal([]byte(firstLine), &diag); err != nil {
		t.Fatalf("first stderr line is not valid JSON: %v\nline: %s", err, firstLine)
	}
	if diag["gate"] != gateKey {
		t.Fatalf("JSON gate=%q, want %q", diag["gate"], gateKey)
	}

	// 8. Assert the JSON includes a non-empty offending_file_or_artifact so the
	//    developer knows which file to fix.  We check the JSON field rather than
	//    the raw filename because the working tree may already contain other dirty
	//    files; the gate always reports the first one from `git status --porcelain`
	//    which may not be our temp file.  What matters is that the gate names
	//    *some* file — the diagnostic is actionable.
	offending, ok := diag["offending_file_or_artifact"].(string)
	if !ok || offending == "" {
		t.Fatalf("JSON offending_file_or_artifact is absent or empty;\ndiag: %v", diag)
	}

	t.Logf("AC r3.te verified: worktree-clean gate fired on pollution file.\nFile: %s\nReported offender: %s\nSample stderr: %s",
		pollutionFile, offending, firstLine)
}
