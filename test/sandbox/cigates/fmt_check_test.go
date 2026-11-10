// Regression tests for bug b.7ep: committed Go files drifted from gofmt because
// nothing in CI or the Makefile ran it. `make fmt-check` now fails when a
// tracked Go file is not gofmt-clean, and the lint workflow runs it.
//
// Fails before the fix: MAKEFILE_UNDER_TEST=<pre-fix Makefile> fails every
// TestFmtCheck case (no fmt-check target), WORKFLOWS_UNDER_TEST=<pre-fix
// .github/workflows directory> fails TestFmtCheck_HasCICaller, and the fixed
// Makefile against the pre-fix tree fails TestFmtCheck_RepoIsClean.

package sandboxcigates_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

const (
	formattedGo   = "package p\n\nfunc F() int {\n\treturn 1\n}\n"
	unformattedGo = "package p\n\nfunc G()   int {\n  return 2\n}\n"
)

// runFmtCheck runs `make fmt-check` in dir with the Makefile under test.
func runFmtCheck(t *testing.T, dir string) (string, error) {
	t.Helper()
	c := exec.Command("make", "-f", sandboxtest.MakefileUnderTest(t), "fmt-check")
	c.Dir = dir
	c.Env = sandboxtest.ScrubbedMakeEnv()
	out, err := c.CombinedOutput()
	return string(out), err
}

// TestFmtCheck (b.7ep): in a scratch git repo, fmt-check fails and names a
// tracked unformatted file, and ignores an untracked one.
func TestFmtCheck(t *testing.T) {
	sandboxtest.RequireTools(t, "skipping b.7ep fmt-check test", "make", "git", "gofmt")

	for _, tc := range []struct {
		name    string
		tracked map[string]string
		wantOK  bool
		wantOut string
	}{
		{name: "clean_passes", tracked: map[string]string{"a.go": formattedGo}, wantOK: true},
		{name: "unformatted_fails", tracked: map[string]string{"a.go": formattedGo, "sub/b.go": unformattedGo}, wantOut: "sub/b.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				c := exec.Command("git", append([]string{"-C", dir}, args...)...)
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
			git("init", "-q")
			for name, src := range tc.tracked {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			git("add", "-A")
			// Written after git add, so it stays untracked and must not fail the check.
			if err := os.WriteFile(filepath.Join(dir, "untracked.go"), []byte(unformattedGo), 0o644); err != nil {
				t.Fatalf("write untracked.go: %v", err)
			}

			out, err := runFmtCheck(t, dir)
			switch {
			case tc.wantOK && err != nil:
				t.Errorf("make fmt-check failed (%v), want exit 0; output:\n%s", err, out)
			case !tc.wantOK && err == nil:
				t.Errorf("make fmt-check exited 0, want non-zero; output:\n%s", out)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output does not contain %q:\n%s", tc.wantOut, out)
			}
			if strings.Contains(out, "untracked.go") {
				t.Errorf("output names the untracked file:\n%s", out)
			}
		})
	}
}

// TestFmtCheck_RepoIsClean (b.7ep): every tracked Go file in this repo is gofmt-clean.
func TestFmtCheck_RepoIsClean(t *testing.T) {
	sandboxtest.RequireTools(t, "skipping b.7ep fmt-check test", "make", "git", "gofmt")
	if out, err := runFmtCheck(t, sandboxtest.RepoRoot(t)); err != nil {
		t.Errorf("make fmt-check failed (%v); output:\n%s", err, out)
	}
}

// TestFmtCheck_HasCICaller (b.7ep): a workflow runs `make fmt-check` after an
// actions/setup-go step, so CI's gofmt comes from go.mod's Go.
func TestFmtCheck_HasCICaller(t *testing.T) {
	for _, lines := range workflowLines(t) {
		setupGo := false
		for _, l := range lines {
			if strings.Contains(l, "actions/setup-go@") {
				setupGo = true
			}
			if strings.Contains(l, "make fmt-check") && setupGo {
				return
			}
		}
	}
	t.Error("no workflow under .github/workflows runs `make fmt-check` after an actions/setup-go step")
}
