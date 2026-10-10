// Package sandboxcigates_test is a regression test for bug b.ug8: two release
// checks that either never ran or passed without running.
//
//   - `make release-shellcheck` printed "skipping" and exited 0 when shellcheck
//     was not on PATH, and no workflow or /release phase called it. It now fails
//     unless SHELLCHECK_OPTIONAL=1 is set, and the lint workflow calls it.
//   - integration.yml ran one hard-coded Docker-harness group, harness-smoke. Its
//     docker-epics job now builds the harness matrix from
//     `make list-test-docker-epics`, so a slug added to test/docker-epics.txt
//     gets a job with no workflow edit, and only the harness-smoke job gets
//     the Claude credentials.
//
// Like the other test/sandbox suites it drives the real Makefile with fake tools
// on PATH and runs no repo binary, so it needs no sandboxguard TestMain.
//
// Fails before the fix: MAKEFILE_UNDER_TEST=<pre-fix Makefile> fails the strict
// and opt-in TestReleaseShellcheck cases, and WORKFLOWS_UNDER_TEST=<pre-fix
// .github/workflows directory> fails every other test.
package sandboxcigates_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

// fakeShellcheck logs each call's arguments as one line and exits $FAKE_SHELLCHECK_EXIT.
const fakeShellcheck = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_SHELLCHECK_LOG"
exit "${FAKE_SHELLCHECK_EXIT:-0}"
`

// pathWithoutShellcheck returns a PATH with every directory holding a shellcheck
// dropped, led by dir, which links the tools the recipe needs besides shellcheck.
func pathWithoutShellcheck(t *testing.T, dir string) string {
	t.Helper()
	for _, tool := range []string{"find", "sort", "xargs"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("look up %s: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("link %s: %v", tool, err)
		}
	}
	keep := []string{dir}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, "shellcheck")); errors.Is(err, fs.ErrNotExist) {
			keep = append(keep, d)
		}
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

// gateScripts lists the repo's *.sh files under the /release gates directory, repo-relative and sorted.
func gateScripts(t *testing.T) []string {
	t.Helper()
	root := sandboxtest.RepoRoot(t)
	var scripts []string
	err := filepath.WalkDir(filepath.Join(root, "skills", "release-agent-director", "gates"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".sh") {
			rel, _ := filepath.Rel(root, p)
			scripts = append(scripts, rel)
		}
		return err
	})
	if err != nil || len(scripts) == 0 {
		t.Fatalf("list gate scripts: %v (found %d)", err, len(scripts))
	}
	slices.Sort(scripts)
	return scripts
}

// TestReleaseShellcheck (b.ug8): with shellcheck absent the target fails unless
// SHELLCHECK_OPTIONAL=1; with it present it checks every gate script, SHELLCHECK_OPTIONAL=1 or not.
//
// The absent_fails advice is followed literally: the optional cases set
// SHELLCHECK_OPTIONAL=1, and the present cases install a shellcheck.
func TestReleaseShellcheck(t *testing.T) {
	sandboxtest.RequireTools(t, "skipping b.ug8 release-shellcheck test", "make", "find", "sort", "xargs")
	const advice = "Install shellcheck, or set SHELLCHECK_OPTIONAL=1 to skip this check on purpose."
	const skipped = "SKIPPED, nothing was checked"

	for _, tc := range []struct {
		name       string
		shellcheck string // the fake's exit code; "" leaves shellcheck off PATH
		args, env  []string
		wantOK     bool
		wantOut    string
	}{
		{name: "absent_fails", wantOut: advice},
		{name: "absent_optional_on_cmdline_skips", args: []string{"SHELLCHECK_OPTIONAL=1"}, wantOK: true, wantOut: skipped},
		{name: "absent_optional_in_env_skips", env: []string{"SHELLCHECK_OPTIONAL=1"}, wantOK: true, wantOut: skipped},
		{name: "absent_optional_other_value_fails", args: []string{"SHELLCHECK_OPTIONAL=yes"}, wantOut: advice},
		{name: "present_clean_passes", shellcheck: "0", wantOK: true},
		{name: "present_findings_fail", shellcheck: "1"},
		{name: "present_optional_still_checks", shellcheck: "1", args: []string{"SHELLCHECK_OPTIONAL=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "shellcheck.log")
			if tc.shellcheck != "" {
				if err := os.WriteFile(filepath.Join(bin, "shellcheck"), []byte(fakeShellcheck), 0o755); err != nil {
					t.Fatalf("write fake shellcheck: %v", err)
				}
			}
			c := exec.Command("make", append([]string{"-f", sandboxtest.MakefileUnderTest(t), "release-shellcheck"}, tc.args...)...)
			c.Dir = sandboxtest.RepoRoot(t)
			c.Env = sandboxtest.ScrubbedMakeEnv(append([]string{
				"PATH=" + pathWithoutShellcheck(t, bin), "SHELLCHECK_OPTIONAL=",
				"FAKE_SHELLCHECK_LOG=" + logPath, "FAKE_SHELLCHECK_EXIT=" + tc.shellcheck,
			}, tc.env...)...)
			out, err := c.CombinedOutput()

			var exitErr *exec.ExitError
			switch {
			case tc.wantOK && err != nil:
				t.Errorf("make release-shellcheck failed (%v), want exit 0; output:\n%s", err, out)
			case !tc.wantOK && err == nil:
				t.Errorf("make release-shellcheck exited 0, want non-zero; output:\n%s", out)
			case err != nil && !errors.As(err, &exitErr):
				t.Fatalf("run make: %v", err)
			}
			if !strings.Contains(string(out), tc.wantOut) {
				t.Errorf("output does not contain %q:\n%s", tc.wantOut, out)
			}
			if tc.shellcheck == "" {
				return
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("shellcheck was never called: %v; output:\n%s", err, out)
			}
			var checked []string
			for _, arg := range strings.Fields(string(data)) {
				if strings.HasSuffix(arg, ".sh") {
					checked = append(checked, arg)
				}
			}
			slices.Sort(checked)
			if want := gateScripts(t); !slices.Equal(checked, want) {
				t.Errorf("shellcheck checked\n  %s\nwant every gate script\n  %s", strings.Join(checked, "\n  "), strings.Join(want, "\n  "))
			}
		})
	}
}

// TestReleaseShellcheck_HasCICaller (b.ug8): a workflow runs `make release-shellcheck`, and none sets SHELLCHECK_OPTIONAL.
func TestReleaseShellcheck_HasCICaller(t *testing.T) {
	var callers []string
	for name, lines := range workflowLines(t) {
		for _, l := range lines {
			if strings.Contains(l, "SHELLCHECK_OPTIONAL") {
				t.Errorf("%s sets SHELLCHECK_OPTIONAL, so its shellcheck run may pass without checking: %s", name, strings.TrimSpace(l))
			}
			if strings.Contains(l, "make release-shellcheck") {
				callers = append(callers, name)
			}
		}
	}
	if len(callers) == 0 {
		t.Error("no workflow under .github/workflows runs `make release-shellcheck`; the target has no automated caller")
	}
}
