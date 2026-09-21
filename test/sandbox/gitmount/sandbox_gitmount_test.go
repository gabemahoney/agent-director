// Package sandboxgitmount_test is a regression test for bug b.kbe.
//
// BACKGROUND (b.kbe incident)
// ===========================
// On a plain clone, git <=2.34's `git rev-parse --git-common-dir` prints the
// RELATIVE path ".git" (relative to cwd) rather than an absolute path. The
// Makefile's sandbox plumbing assumed the value was absolute, so the guard
//
//	_SANDBOX_GIT_MOUNT := $(if $(filter-out $(CURDIR)/.git,$(GIT_COMMON_DIR)),-v "...",)
//
// compared the relative ".git" against the absolute "$(CURDIR)/.git", failed to
// match, and injected a bogus `-v ".git:.git"` mount. Docker rejected the
// relative volume ("mount path must be absolute") and every `make sandbox` /
// `make test-sandbox` run aborted before the container started.
//
// The fix absolutizes the value at capture:
//
//	_GIT_COMMON_DIR_RAW := $(shell git rev-parse --git-common-dir 2>/dev/null)
//	GIT_COMMON_DIR := $(if $(_GIT_COMMON_DIR_RAW),$(abspath $(_GIT_COMMON_DIR_RAW)),)
//
// so on a plain clone GIT_COMMON_DIR == $(CURDIR)/.git, the filter-out guard
// matches, and no mount is injected. The empty-string guard keeps the value
// empty outside a git repo (a bare $(abspath) of "" would collapse to CURDIR).
//
// DESIGN
// ======
// The real logic is not re-implemented here; it is EXTRACTED verbatim from the
// repo Makefile (the three lines defining _GIT_COMMON_DIR_RAW, GIT_COMMON_DIR
// and _SANDBOX_GIT_MOUNT) and evaluated in a throwaway mini-makefile with two
// print targets. Extracting keeps the test anchored to the source: if someone
// reverts the capture line, this test evaluates the reverted logic and fails.
//
// Three git scenarios are exercised as separate temp dirs, each with `make`
// run FROM that dir (so CURDIR and `git rev-parse`'s cwd are the scenario dir):
//
//	plain    a plain `git init` clone   → GIT_COMMON_DIR = $(CURDIR)/.git, no mount
//	worktree a linked git worktree      → GIT_COMMON_DIR = absolute parent .git, mount added
//	norepo   not a git repo             → GIT_COMMON_DIR empty, no mount
//
// FAILS-BEFORE / PASSES-AFTER
// ===========================
// TestGitMount_PreFixLineWouldRegress reconstructs the PRE-FIX single-line
// capture (`GIT_COMMON_DIR := $(shell git rev-parse --git-common-dir ...)`) on
// the same harness and asserts that, on a plain clone whose git prints a
// relative common dir, it injects the bogus `-v .git:.git` mount. That is the
// exact defect b.kbe fixed. If the host git already returns an absolute path
// (git that does not exhibit the bug), the test skips — the regression cannot
// be demonstrated on that git and there is nothing to prove.
//
// The Makefile whose logic is evaluated defaults to the repo Makefile but can
// be overridden with MAKEFILE_UNDER_TEST=<path> — used to demonstrate the
// fails-before direction against a pre-fix copy without breaking `make sandbox`
// on the real Makefile.
//
// This test only runs `git` and `make`; it never builds or execs the
// agent-director binary and never touches ~/.agent-director, so it needs no
// sandboxguard TestMain. It still runs under `make test-sandbox` (go test
// ./...); git, make and build-essential are present in the sandbox image.
package sandboxgitmount_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

// makefilePath returns the Makefile whose logic is under test. It defaults to
// the repo Makefile but honors MAKEFILE_UNDER_TEST so a reviewer can point the
// suite at a pre-fix copy to prove the fails-before direction WITHOUT editing
// (and thereby breaking `make sandbox` on) the real Makefile.
func makefilePath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MAKEFILE_UNDER_TEST"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("MAKEFILE_UNDER_TEST=%q: %v", p, err)
		}
		return p
	}
	return filepath.Join(sandboxtest.RepoRoot(t), "Makefile")
}

// captureLines returns the exact Makefile lines matching pattern, in file
// order. It fails the test if none match (the Makefile no longer contains the
// logic this test anchors to and must be re-anchored).
func captureLines(t *testing.T, makefile string, pattern *regexp.Regexp) []string {
	t.Helper()
	data, err := os.ReadFile(makefile)
	if err != nil {
		t.Fatalf("read Makefile %s: %v", makefile, err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if pattern.MatchString(line) {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		t.Fatalf("captureLines: no lines matched %s in %s", pattern, makefile)
	}
	return out
}

// writeMiniMake writes a throwaway makefile containing logicLines plus
// print-GIT_COMMON_DIR and print-MOUNT targets, and returns its path.
func writeMiniMake(t *testing.T, dir string, logicLines []string) string {
	t.Helper()
	body := strings.Join(logicLines, "\n") + "\n" +
		"print-GIT_COMMON_DIR:\n\t@printf '%s' \"$(GIT_COMMON_DIR)\"\n" +
		"print-MOUNT:\n\t@printf '%s' \"$(_SANDBOX_GIT_MOUNT)\"\n"
	path := filepath.Join(dir, "mini.mk")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write mini.mk: %v", err)
	}
	return path
}

// runMakeVar runs `make -s -f makefilePath target` from workdir and returns
// the printed value.
func runMakeVar(t *testing.T, makefilePath, workdir, target string) string {
	t.Helper()
	cmd := exec.Command("make", "-s", "-f", makefilePath, target)
	cmd.Dir = workdir
	// Scrub inherited make state so an ancestor make's variable overrides
	// (e.g. `make sandbox CMD=...`) can't skew the captured values.
	cmd.Env = sandboxtest.ScrubbedMakeEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make %s in %s: %v\noutput:\n%s", target, workdir, err, out)
	}
	return string(out)
}

// git runs a git subcommand in workdir, failing the test on error.
func git(t *testing.T, workdir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workdir
	// Deterministic identity so `git commit` works in a bare temp env.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), workdir, err, out)
	}
}

// requireTools skips if make or git is unavailable.
func requireTools(t *testing.T) {
	t.Helper()
	sandboxtest.RequireTools(t, "skipping b.kbe sandbox git-mount test", "make", "git")
}

// makePlainClone creates a plain (non-worktree) git repo with one commit and
// returns its path.
func makePlainClone(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// realLogicLines extracts the Makefile's git-common-dir capture + mount lines
// verbatim, in file order. It matches whatever capture form is present (the
// fixed 3-line _GIT_COMMON_DIR_RAW/abspath form, or a pre-fix single-line
// form), so the BEHAVIOR of whatever the Makefile actually defines is what
// gets evaluated — a reverted fix produces a behavioral failure, not just a
// shape mismatch.
func realLogicLines(t *testing.T) []string {
	t.Helper()
	pat := regexp.MustCompile(`^(_GIT_COMMON_DIR_RAW|GIT_COMMON_DIR|_SANDBOX_GIT_MOUNT)\s*:?=`)
	return captureLines(t, makefilePath(t), pat)
}

// TestGitMount_RealMakefileLogic evaluates the repo Makefile's actual capture
// logic across the three b.kbe scenarios and asserts the documented contract.
func TestGitMount_RealMakefileLogic(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make and git subprocesses")
	}
	requireTools(t)
	logic := realLogicLines(t)

	t.Run("plain_clone_no_mount", func(t *testing.T) {
		work := makePlainClone(t)
		mini := writeMiniMake(t, work, logic)

		gotCommon := runMakeVar(t, mini, work, "print-GIT_COMMON_DIR")
		wantCommon := filepath.Join(work, ".git")
		if gotCommon != wantCommon {
			t.Fatalf("plain clone GIT_COMMON_DIR = %q, want %q (must be absolute $(CURDIR)/.git)", gotCommon, wantCommon)
		}

		if mount := runMakeVar(t, mini, work, "print-MOUNT"); mount != "" {
			t.Fatalf("plain clone injected a git mount %q; b.kbe requires it empty", mount)
		}
	})

	t.Run("worktree_mount_added", func(t *testing.T) {
		main := makePlainClone(t)
		wt := filepath.Join(t.TempDir(), "wt")
		git(t, main, "worktree", "add", "-q", wt, "-b", "wtbranch")
		mini := writeMiniMake(t, wt, logic)

		gotCommon := runMakeVar(t, mini, wt, "print-GIT_COMMON_DIR")
		wantCommon := filepath.Join(main, ".git")
		if gotCommon != wantCommon {
			t.Fatalf("worktree GIT_COMMON_DIR = %q, want %q (absolute parent .git)", gotCommon, wantCommon)
		}
		if !filepath.IsAbs(gotCommon) {
			t.Fatalf("worktree GIT_COMMON_DIR %q is not absolute", gotCommon)
		}

		mount := runMakeVar(t, mini, wt, "print-MOUNT")
		wantMount := "-v " + wantCommon + ":" + wantCommon
		if mount != wantMount {
			t.Fatalf("worktree mount = %q, want %q", mount, wantMount)
		}
	})

	t.Run("non_repo_empty", func(t *testing.T) {
		work := t.TempDir() // no git init
		mini := writeMiniMake(t, work, logic)

		if got := runMakeVar(t, mini, work, "print-GIT_COMMON_DIR"); got != "" {
			t.Fatalf("outside a git repo GIT_COMMON_DIR = %q, want empty", got)
		}
		if mount := runMakeVar(t, mini, work, "print-MOUNT"); mount != "" {
			t.Fatalf("outside a git repo injected mount %q, want empty", mount)
		}
	})
}

// TestGitMount_PreFixLineWouldRegress proves the fails-before direction: the
// original single-line capture (no $(abspath)) injects the bogus `-v .git:.git`
// mount on a plain clone whose git prints a relative common dir. Skips on a git
// that already returns an absolute path (the bug cannot manifest there).
func TestGitMount_PreFixLineWouldRegress(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make and git subprocesses")
	}
	requireTools(t)

	work := makePlainClone(t)

	// Precondition: this git must exhibit the relative-common-dir behavior for
	// the regression to be demonstrable.
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = work
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --git-common-dir: %v", err)
	}
	rawStr := strings.TrimSpace(string(raw))
	if filepath.IsAbs(rawStr) {
		t.Skipf("git returns an absolute common dir (%q) — b.kbe regression not reproducible on this git", rawStr)
	}

	// Reconstruct the PRE-FIX logic: capture WITHOUT $(abspath), same guard.
	mountLine := captureLines(t, makefilePath(t),
		regexp.MustCompile(`^_SANDBOX_GIT_MOUNT\s*:?=`))[0]
	preFix := []string{
		`GIT_COMMON_DIR := $(shell git rev-parse --git-common-dir 2>/dev/null)`,
		mountLine,
	}
	mini := writeMiniMake(t, work, preFix)

	mount := runMakeVar(t, mini, work, "print-MOUNT")
	if !strings.Contains(mount, "-v .git:.git") {
		t.Fatalf("pre-fix line did not reproduce the b.kbe defect; mount = %q, expected to contain %q", mount, "-v .git:.git")
	}

	// And confirm the FIXED logic on the same repo/git does NOT regress.
	fixed := writeMiniMake(t, work, realLogicLines(t))
	if got := runMakeVar(t, fixed, work, "print-MOUNT"); got != "" {
		t.Fatalf("fixed logic still injects a mount %q on the same plain clone; b.kbe not fixed", got)
	}
}
