// Package worktreepollution_test is a synthetic-regression test for preflight.worktree-clean.
//
// BACKGROUND (AC r3.te)
// =====================
// The preflight.worktree-clean gate asserts the working tree has no modified,
// staged, or untracked files before a release proceeds.  Without this gate a
// developer could kick off a release while uncommitted work sits in the tree,
// embedding stale or debug content in the published artifact.  This test proves
// the gate fires when an untracked file is present in the working tree.
//
// DESIGN
// ======
// The gate runs `git status --porcelain` in its working directory, so the test
// points it at a throwaway repo and never writes into the real repo tree
// (b.ngj: a path appearing and vanishing at the real repo root can break the
// docker build context that parallel coverage.docker-epic-* gates tar).
//
//  1. `git init` a throwaway repo under t.TempDir().
//  2. Baseline: run the gate there and assert it passes (exit 0, empty
//     stderr), so the later failure is caused by the pollution file alone.
//  3. Write an untracked pollution file (_worktree_pollution.tmp) into the
//     throwaway repo.
//  4. Run bash skills/release-agent-director/gates/preflight/worktree-clean.sh
//     from the real repo, with cmd.Dir set to the throwaway repo.
//  5. Assert exit code 1.
//  6. Assert the stderr JSON line has "gate":"preflight.worktree-clean".
//  7. Assert offending_file_or_artifact names the pollution file exactly; the
//     throwaway repo holds nothing else, so no other path can be reported.
//
// Every git process (the init and the gate's `git status`) runs with GIT_*
// variables stripped, global/system git config disabled, and HOME and
// XDG_CONFIG_HOME pointed at an empty temp dir.  An inherited GIT_DIR therefore
// cannot redirect it to the real repo, and no user ignore rule can hide the
// pollution file: neither a configured core.excludesFile nor git's default
// global ignore file ($XDG_CONFIG_HOME/git/ignore, else
// $HOME/.config/git/ignore), which git reads even with global config disabled.
//
// CLEANUP
// =======
// Everything lives under t.TempDir(), which the testing package removes after
// the test, pass or fail.  Nothing is written into the real repo tree, so the
// test needs no t.Cleanup and no tree-write lock.
//
// DEPENDENCY
// ==========
// Requires `git` in PATH; skips gracefully if absent.
package worktreepollution_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const gateKey = "preflight.worktree-clean"

// pollutionName is the untracked file planted in the throwaway repo.  It sits
// at the repo root, so `git status --porcelain` reports it by this exact name.
const pollutionName = "_worktree_pollution.tmp"

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

// isolatedGitEnv returns the process environment with every GIT_* variable,
// HOME and XDG_CONFIG_HOME removed, global/system git config disabled, and
// HOME and XDG_CONFIG_HOME set to home (an empty dir).  A stray GIT_DIR or
// GIT_WORK_TREE (e.g. from a git hook) would otherwise point git at the real
// repo.  Disabling global config only stops a configured core.excludesFile;
// git still reads its default global ignore file ($XDG_CONFIG_HOME/git/ignore,
// else $HOME/.config/git/ignore), so both are moved to the empty dir to keep a
// user rule such as `*.tmp` from hiding the pollution file.
func isolatedGitEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
	)
}

// runGate runs the worktree-clean gate with gateRoot as its working directory
// and env as its environment, and returns (exitCode, stderr).
func runGate(t *testing.T, gateScript, gateRoot string, env []string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", gateScript)
	cmd.Dir = gateRoot
	cmd.Env = env
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit is expected once polluted — ignore error
	return cmd.ProcessState.ExitCode(), stderrBuf.String()
}

// TestWorktreePollution verifies that preflight.worktree-clean fires when an
// untracked file is present in the working tree (AC r3.te).
func TestWorktreePollution(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH — skipping worktree-clean gate test")
	}

	gateScript := filepath.Join(repoRoot(t), "skills", "release-agent-director", "gates", "preflight", "worktree-clean.sh")

	// 1. Create the throwaway repo.  An unborn branch is enough: `git status`
	//    needs no commit to list untracked files.
	gateRoot := t.TempDir()
	gitEnv := isolatedGitEnv(t.TempDir())
	initCmd := exec.Command("git", "init", "-q", gateRoot)
	initCmd.Env = gitEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", gateRoot, err, out)
	}

	// 2. Baseline: the gate passes on the clean throwaway repo.
	if code, stderr := runGate(t, gateScript, gateRoot, gitEnv); code != 0 || stderr != "" {
		t.Fatalf("baseline: gate on clean repo exited %d, want 0 with empty stderr;\nstderr:\n%s", code, stderr)
	}

	// 3. Plant the untracked pollution file.
	pollutionFile := filepath.Join(gateRoot, pollutionName)
	if err := os.WriteFile(pollutionFile, []byte("regression fixture — safe to delete\n"), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", pollutionFile, err)
	}

	// 4–5. Run the gate and assert it fails with exit 1.
	code, stderr := runGate(t, gateScript, gateRoot, gitEnv)
	if code != 1 {
		t.Fatalf("worktree-clean gate exited %d — expected 1 with pollution file %s present;\nstderr:\n%s", code, pollutionFile, stderr)
	}

	// 6. Parse the first stderr line as JSON and validate the gate field.
	firstLine := strings.SplitN(strings.TrimSpace(stderr), "\n", 2)[0]
	var diag map[string]interface{}
	if err := json.Unmarshal([]byte(firstLine), &diag); err != nil {
		t.Fatalf("first stderr line is not valid JSON: %v\nline: %s", err, firstLine)
	}
	if diag["gate"] != gateKey {
		t.Fatalf("JSON gate=%v, want %q;\nline: %s", diag["gate"], gateKey, firstLine)
	}

	// 7. Assert the gate names the pollution file exactly.
	if got := diag["offending_file_or_artifact"]; got != pollutionName {
		t.Fatalf("JSON offending_file_or_artifact=%v, want %q;\nline: %s", got, pollutionName, firstLine)
	}

	t.Logf("AC r3.te verified: worktree-clean gate fired on pollution file %s.\nSample stderr: %s",
		pollutionName, firstLine)
}
