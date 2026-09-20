// Package fastforwardmainworktree_test is a synthetic-regression test for b.jqj.
//
// BACKGROUND (b.jqj incident class)
// =================================
// During the v0.7.8 release the publish-orchestrator's `fast-forward-main`
// substep failed: the old implementation ran `git checkout main` from inside
// the *release* worktree, but `main` was already checked out in the parent
// (primary) worktree, so git aborted with
//
//	fatal: 'main' is already checked out at '<parent>'   (exit 128)
//
// The operator had to complete the fast-forward by hand, defeating the
// "publish is fully scripted" goal of the b.wvr pipeline. The fix removed the
// checkout entirely: `_do_fast_forward_main` now derives the parent (primary)
// worktree from `git worktree list --porcelain` (first entry) and runs
// `git -C <parent> merge --ff-only <branch> && git -C <parent> push origin main`
// — never switching branches inside the release worktree.
//
// DESIGN
// ======
// The orchestrator executes its 6-substep pipeline at file-load time, and the
// substeps before fast-forward-main (gh-release, npm-publish) need tools/state
// this test cannot provide. So the happy-path and negative tests exercise the
// REAL `_do_fast_forward_main` function in isolation: they slice the function
// body out of the live publish-orchestrator.sh (between the `_do_fast_forward_main()`
// and the next `_do_*()` marker) and source that exact production text into a
// tiny bash harness with WORKTREE_ROOT/RELEASE_BRANCH set. If the source is
// reverted to the buggy `git checkout main` form, the extracted function runs
// the checkout and the happy-path test fails (exit 128) — that is the
// regression guarantee required by the ticket.
//
// FAKE ORIGIN (no network)
// ========================
// There is no precedent in the tests tree for faking a remote, so this test
// builds one: a local `git init --bare` repo wired in as `origin` via
// `git remote add origin <bare-path>`. `git fetch origin` and
// `git push origin main` therefore hit a path on disk — the test is physically
// incapable of reaching github.com. buildFakeRemoteLayout asserts the recorded
// remote URL is exactly the bare path.
//
// The dry-run test invokes the full orchestrator (safe: dry-run mutates nothing)
// and asserts the displayed fast-forward-main command contains the literal
// `<parent-worktree>` placeholder and NOT `checkout main`.
package fastforwardmainworktree_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the package working directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("repoRoot: os.Getwd: %v", err)
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

func orchestratorPath(root string) string {
	return filepath.Join(root, "skills", "release-agent-director",
		"gates", "publish", "publish-orchestrator.sh")
}

// extractFastForwardFn slices the exact `_do_fast_forward_main` function
// definition out of the live orchestrator script so the test sources the real
// production code. If the markers are gone the function was refactored — fail
// loudly (mirrors the helper-tag-replay marker-not-found convention) rather
// than silently testing nothing.
func extractFastForwardFn(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(orchestratorPath(root))
	if err != nil {
		t.Fatalf("read publish-orchestrator.sh: %v", err)
	}
	src := string(data)

	const startMarker = "_do_fast_forward_main() {"
	start := strings.Index(src, startMarker)
	if start < 0 {
		t.Fatalf("marker %q not found — update if _do_fast_forward_main was renamed", startMarker)
	}
	// The function ends at the next substep definition.
	const endMarker = "_do_delete_remote_branch() {"
	end := strings.Index(src[start:], endMarker)
	if end < 0 {
		t.Fatalf("end marker %q not found after _do_fast_forward_main — update if pipeline reordered", endMarker)
	}
	fnText := src[start : start+end]

	// Guard: the fixed function must NOT contain a bare `git checkout main`
	// against the release worktree. This is a belt-and-suspenders anchor: if
	// someone reintroduces the b.jqj bug, this extraction still runs it (so the
	// happy-path test fails on behavior), but this also documents intent.
	if strings.Contains(fnText, "checkout main") {
		t.Logf("NOTE: extracted _do_fast_forward_main still contains 'checkout main' — "+
			"the happy-path test below is expected to fail (b.jqj regression):\n%s", fnText)
	}
	return fnText
}

// runFastForward sources the extracted function into a bash harness with the
// given WORKTREE_ROOT and RELEASE_BRANCH, then calls it. Returns exit code and
// combined output.
func runFastForward(t *testing.T, root, worktreeRoot, releaseBranch string) (int, string) {
	t.Helper()
	fnText := extractFastForwardFn(t, root)

	harness := "set -uo pipefail\n" +
		"WORKTREE_ROOT=" + shellQuote(worktreeRoot) + "\n" +
		"RELEASE_BRANCH=" + shellQuote(releaseBranch) + "\n" +
		fnText + "\n" +
		"_do_fast_forward_main\n"

	cmd := exec.Command("bash", "-c", harness)
	// Deterministic identity so `git merge`/commit metadata never depends on
	// host config, and no network/credential prompts are possible.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// git runs a git command in dir and fails the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fakeLayout holds the paths of a bare origin + primary clone + linked release
// worktree.
type fakeLayout struct {
	bare        string // bare repo acting as `origin`
	primary     string // clone with `main` checked out (the parent worktree)
	release     string // linked worktree on releaseBranch
	releaseSHA  string // commit that release is ahead of main by
	releaseName string // the release branch name
}

// buildFakeRemoteLayout constructs, entirely on the local filesystem:
//   - a bare repo used as `origin` (no network reachable)
//   - a primary clone with `main` checked out and pushed to origin
//   - a linked worktree checked out on releaseBranch, one commit ahead of main
//
// It asserts origin's URL is exactly the bare path so the layout cannot reach a
// real remote.
func buildFakeRemoteLayout(t *testing.T, releaseBranch string) fakeLayout {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "origin.git")
	primary := filepath.Join(base, "primary")
	release := filepath.Join(base, "release-wt")

	// Bare origin — a git remote that lives on disk.
	git(t, base, "init", "--bare", "-b", "main", bare)

	// Primary clone, main checked out.
	git(t, base, "clone", bare, primary)
	// Seed an initial commit on main and push it.
	if err := os.WriteFile(filepath.Join(primary, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	git(t, primary, "add", "README")
	git(t, primary, "commit", "-m", "base")
	git(t, primary, "push", "origin", "main")

	// Verify origin points ONLY at the local bare path — never a network host.
	url := git(t, primary, "remote", "get-url", "origin")
	if url != bare {
		t.Fatalf("origin URL must be the local bare path %q, got %q — test must not reach a real remote", bare, url)
	}

	// Create the release branch one commit ahead of main, as a LINKED worktree.
	// This is the exact layout that broke the old code: main is checked out in
	// `primary`, so `git checkout main` from `release` would abort with exit 128.
	git(t, primary, "worktree", "add", "-b", releaseBranch, release, "main")
	if err := os.WriteFile(filepath.Join(release, "SHIP"), []byte("v-ship\n"), 0o644); err != nil {
		t.Fatalf("seed SHIP: %v", err)
	}
	git(t, release, "add", "SHIP")
	git(t, release, "commit", "-m", "release commit")
	releaseSHA := git(t, release, "rev-parse", "HEAD")
	// Push the release branch to origin so `git fetch origin` in the substep has
	// something to fetch and the primary can merge --ff-only from local refs.
	git(t, release, "push", "origin", releaseBranch)

	return fakeLayout{
		bare: bare, primary: primary, release: release,
		releaseSHA: releaseSHA, releaseName: releaseBranch,
	}
}

// TestFastForwardMainFromReleaseWorktree is the b.jqj happy-path regression:
// running the real fast-forward-main substep from a linked release worktree
// advances the parent's main to the release commit and pushes it to the bare
// origin — WITHOUT any `git checkout main` (which the old code did, aborting
// with exit 128 in this exact layout).
func TestFastForwardMainFromReleaseWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow regression test in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := repoRoot(t)

	lay := buildFakeRemoteLayout(t, "release/v0.7.8-jqj")

	exit, out := runFastForward(t, root, lay.release, lay.releaseName)

	// Old buggy behavior: `git checkout main` from the release worktree aborts
	// with "already checked out" → exit 128. The fix must exit 0.
	if exit != 0 {
		t.Fatalf("fast-forward-main from release worktree exited %d, want 0 (b.jqj)\noutput:\n%s", exit, out)
	}
	if strings.Contains(out, "already checked out") {
		t.Fatalf("output mentions 'already checked out' — the b.jqj checkout bug is back:\n%s", out)
	}

	// Parent (primary) main must have advanced to the release commit.
	primaryMain := git(t, lay.primary, "rev-parse", "main")
	if primaryMain != lay.releaseSHA {
		t.Fatalf("parent main did not fast-forward: main=%s want release=%s", primaryMain, lay.releaseSHA)
	}

	// The push must have advanced origin's main ref too.
	originMain := git(t, lay.bare, "rev-parse", "main")
	if originMain != lay.releaseSHA {
		t.Fatalf("origin main not advanced (push did not happen): origin=%s want=%s", originMain, lay.releaseSHA)
	}

	t.Logf("b.jqj verified: fast-forward-main scripted merge+push from release worktree; main=%s", lay.releaseSHA)
}

// TestFastForwardMainGuardFiresOnBadLayout is the b.jqj negative case: when the
// derived parent worktree does not have `main` checked out, the substep must
// halt loudly with the SR-13.3 diagnostic and non-zero exit — no silent
// correction (acceptance criterion: unexpected layouts still halt).
func TestFastForwardMainGuardFiresOnBadLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow regression test in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := repoRoot(t)

	lay := buildFakeRemoteLayout(t, "release/v0.7.8-jqj-neg")

	// Move the PRIMARY worktree off main so the guard's "parent HEAD not on
	// main" branch fires. `git worktree list --porcelain` still lists primary
	// first, so the substep derives it as parent, then rejects it.
	git(t, lay.primary, "checkout", "-b", "not-main")

	exit, out := runFastForward(t, root, lay.release, lay.releaseName)

	if exit == 0 {
		t.Fatalf("guard did not fire: fast-forward-main exited 0 on a parent-not-on-main layout\noutput:\n%s", out)
	}
	if !strings.Contains(out, "SR-13.3") {
		t.Fatalf("expected SR-13.3 diagnostic on bad layout; got:\n%s", out)
	}

	// No silent correction: origin main must be unchanged from its seeded base.
	originMain := git(t, lay.bare, "rev-parse", "main")
	if originMain == lay.releaseSHA {
		t.Fatalf("origin main advanced despite guard firing — silent correction occurred")
	}

	t.Logf("b.jqj negative verified: SR-13.3 guard halted on parent-not-on-main layout (exit %d)", exit)
}

// TestFastForwardMainDryRunDisplay asserts the dry-run path (acceptance
// criterion: no change to dry-run behavior) shows the new command form with the
// literal `<parent-worktree>` placeholder and never `checkout main`, and that
// dry-run performs no git mutations against a real layout.
func TestFastForwardMainDryRunDisplay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow: invokes publish-orchestrator.sh")
	}
	root := repoRoot(t)
	reportDir := t.TempDir()

	// A real primary+release layout so we can prove dry-run mutates nothing.
	lay := buildFakeRemoteLayout(t, "release/v0.7.8-jqj-dry")
	originMainBefore := git(t, lay.bare, "rev-parse", "main")
	primaryMainBefore := git(t, lay.primary, "rev-parse", "main")

	sha := git(t, root, "rev-parse", "HEAD")

	cmd := exec.Command("bash", orchestratorPath(root),
		"--target", "0.0.0-jqj-dry",
		"--bump-sha", sha,
		"--tarball", "/tmp/fake.tgz",
		"--notes", "/tmp/fake-notes.md",
		"--binaries", "/tmp/b1,/tmp/b2",
		"--dry-run",
		"--worktree-root", lay.release,
		"--release-branch", lay.releaseName,
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "RELEASE_REPORT_DIR="+reportDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("orchestrator --dry-run failed (exit %d):\n%s", cmd.ProcessState.ExitCode(), out)
	}

	// The fast-forward-main "would do:" line must carry the new command form.
	var ffLine string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "publish.fast-forward-main") && strings.Contains(line, "would do:") {
			ffLine = line
			break
		}
	}
	if ffLine == "" {
		t.Fatalf("no dry-run 'would do:' line for publish.fast-forward-main in output:\n%s", out)
	}
	if !strings.Contains(ffLine, "<parent-worktree>") {
		t.Errorf("dry-run command missing <parent-worktree> placeholder:\n%s", ffLine)
	}
	if strings.Contains(ffLine, "checkout main") {
		t.Errorf("dry-run command still shows 'checkout main' (b.jqj bug):\n%s", ffLine)
	}

	// Dry-run must not mutate any git ref.
	if got := git(t, lay.bare, "rev-parse", "main"); got != originMainBefore {
		t.Errorf("dry-run mutated origin main: before=%s after=%s", originMainBefore, got)
	}
	if got := git(t, lay.primary, "rev-parse", "main"); got != primaryMainBefore {
		t.Errorf("dry-run mutated primary main: before=%s after=%s", primaryMainBefore, got)
	}

	t.Logf("b.jqj dry-run verified: %s", ffLine)
}
