// Package sandboxreleaseversion_test is a regression test for bug b.x7z.
//
// release-binaries set its version with a target-specific `:=`, which make
// expands while it reads the Makefile. So every make invocation, whatever the
// target, ran `jq -r .version pkg/ts-bun-client/package.json`: a tree without
// that file or a host without jq printed a `jq:` error on every target, and
// release-binaries stamped an empty version instead of failing. Now no target
// but release-binaries runs jq, and release-binaries stops before building
// anything when it cannot read one version string.
//
// Like the other test/sandbox suites it drives a copy of the real Makefile, in
// a temp tree, with a logging jq wrapper on PATH (or, for the no-jq case, a PATH
// without any jq), and runs no repo binary, so it needs no sandboxguard TestMain.
//
// Fails before the fix: MAKEFILE_UNDER_TEST=<pre-fix Makefile> fails every
// TestMakeParse_RunsNoJq case and the TestReleaseBinaries_Version *_fails cases.
// So does a Makefile whose version check only warns ($(warning) for $(error)).
package sandboxreleaseversion_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

// fakeJq logs each call's arguments as one line, then runs the real jq.
const fakeJq = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_JQ_LOG"
exec "$REAL_JQ" "$@"
`

// tree returns a temp dir holding a copy of the Makefile under test and, unless
// pkgJSON is "", pkg/ts-bun-client/package.json with that content.
func tree(t *testing.T, pkgJSON string) string {
	t.Helper()
	root := t.TempDir()
	data, err := os.ReadFile(sandboxtest.MakefileUnderTest(t))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), data, 0o644); err != nil {
		t.Fatalf("copy Makefile: %v", err)
	}
	if pkgJSON != "" {
		pkg := filepath.Join(root, "pkg", "ts-bun-client")
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatalf("make package dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(pkgJSON), 0o644); err != nil {
			t.Fatalf("write package.json: %v", err)
		}
	}
	return root
}

// pathWithoutJq returns this PATH with jq taken out and every other tool kept:
// each directory holding a jq is swapped, in place, for a temp dir that links
// everything else in it. So the no-jq case is a host with all the tools but jq
// (mkdir, go, id, git…): a recipe that went on with an unread version would get
// as far as creating dist/, which the case checks for.
func pathWithoutJq(t *testing.T) string {
	t.Helper()
	var dirs []string
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(d, "jq")); errors.Is(err, os.ErrNotExist) {
			dirs = append(dirs, d)
			continue
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			t.Fatalf("list %s: %v", d, err)
		}
		mirror := t.TempDir()
		for _, e := range entries {
			if e.Name() == "jq" {
				continue
			}
			if err := os.Symlink(filepath.Join(d, e.Name()), filepath.Join(mirror, e.Name())); err != nil {
				t.Fatalf("link %s: %v", e.Name(), err)
			}
		}
		dirs = append(dirs, mirror)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// runMake runs make with args and the extra env in root, the logging jq first on PATH
// or, with noJq, the PATH of pathWithoutJq. It returns make's output, the logged jq calls and make's error.
func runMake(t *testing.T, root string, noJq bool, extra []string, args ...string) (out string, jqCalls []string, err error) {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "jq.log")
	// Unset, not empty: an empty RELEASE_PKG_DIR or RELEASE_DIST_DIR would override the Makefile's ?= default.
	env := slices.DeleteFunc(sandboxtest.ScrubbedMakeEnv(), func(e string) bool {
		k, _, _ := strings.Cut(e, "=")
		return k == "AGENT_DIRECTOR_BUILD_VERSION" || k == "RELEASE_PKG_DIR" || k == "RELEASE_DIST_DIR"
	})
	var path string
	if noJq {
		path = pathWithoutJq(t)
	} else {
		realJq, lerr := exec.LookPath("jq")
		if lerr != nil {
			t.Fatalf("look up jq: %v", lerr)
		}
		if werr := os.WriteFile(filepath.Join(bin, "jq"), []byte(fakeJq), 0o755); werr != nil {
			t.Fatalf("write fake jq: %v", werr)
		}
		path = bin + string(os.PathListSeparator) + os.Getenv("PATH")
		env = append(env, "REAL_JQ="+realJq)
	}
	c := exec.Command("make", args...)
	c.Dir = root
	c.Env = append(append(env, "PATH="+path, "FAKE_JQ_LOG="+logPath), extra...)
	b, err := c.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run make %v: %v", args, err)
	}
	if data, rerr := os.ReadFile(logPath); rerr == nil {
		jqCalls = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	return string(b), jqCalls, err
}

// TestMakeParse_RunsNoJq (b.x7z): a dry run of a non-release target, in a tree
// without pkg/ts-bun-client/package.json, runs no jq and prints no jq error.
func TestMakeParse_RunsNoJq(t *testing.T) {
	sandboxtest.RequireTools(t, "skipping b.x7z Makefile parse test", "make", "jq")
	root := tree(t, "")
	for _, target := range []string{"list-test-docker-epics", "build", "all", "lint"} {
		t.Run(target, func(t *testing.T) {
			out, calls, err := runMake(t, root, false, nil, "-n", target)
			if err != nil {
				t.Errorf("make -n %s failed (%v), want exit 0; output:\n%s", target, err, out)
			}
			if len(calls) != 0 {
				t.Errorf("make -n %s ran jq %d time(s) (%q), want none", target, len(calls), calls)
			}
			if strings.Contains(out, "jq:") {
				t.Errorf("make -n %s printed a jq error:\n%s", target, out)
			}
		})
	}
}

// TestReleaseBinaries_Version (b.x7z): release-binaries stamps package.json's
// version, AGENT_DIRECTOR_BUILD_VERSION wins with no jq call, and a version it
// cannot read stops make with an error before anything is built.
//
// env_override_wins_without_jq follows the error's advice to set AGENT_DIRECTOR_BUILD_VERSION.
// no_jq_fails drops only jq from PATH, so it fails for the version, not for a missing mkdir or go.
func TestReleaseBinaries_Version(t *testing.T) {
	sandboxtest.RequireTools(t, "skipping b.x7z release-binaries version test", "make", "jq", "mkdir")
	const advice = "or set AGENT_DIRECTOR_BUILD_VERSION=X.Y.Z. No binary was built"

	for _, tc := range []struct {
		name, pkgJSON string // pkgJSON "": no package.json
		env           []string
		noJq          bool
		wantVersion   string // "": make fails with the version error and builds nothing
		wantJqCalls   int
	}{
		{name: "stamps_from_package_json", pkgJSON: `{"version":"1.2.3"}`, wantVersion: "1.2.3", wantJqCalls: 1},
		{name: "env_override_wins_without_jq", env: []string{"AGENT_DIRECTOR_BUILD_VERSION=9.8.7"}, wantVersion: "9.8.7"},
		{name: "no_jq_fails", pkgJSON: `{"version":"1.2.3"}`, noJq: true},
		{name: "no_package_json_fails", wantJqCalls: 1},
		{name: "no_version_field_fails", pkgJSON: `{"name":"x"}`, wantJqCalls: 1},
		{name: "empty_version_fails", pkgJSON: `{"version":""}`, wantJqCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tree(t, tc.pkgJSON)
			args := []string{"release-binaries"}
			if tc.wantVersion != "" {
				args = []string{"-n", "release-binaries"} // no real cross-compile
			}
			out, calls, err := runMake(t, root, tc.noJq, tc.env, args...)

			if len(calls) != tc.wantJqCalls {
				t.Errorf("jq ran %d time(s) (%q), want %d", len(calls), calls, tc.wantJqCalls)
			}
			if tc.wantVersion != "" {
				if err != nil {
					t.Errorf("make -n release-binaries failed (%v), want exit 0; output:\n%s", err, out)
				}
				if want := "/internal/version.Version=" + tc.wantVersion + " "; !strings.Contains(out, want) {
					t.Errorf("make -n release-binaries does not stamp %q:\n%s", want, out)
				}
				return
			}
			if err == nil {
				t.Errorf("make release-binaries exited 0, want non-zero; output:\n%s", out)
			}
			for _, want := range []string{"release-binaries: cannot read the release version", advice} {
				if !strings.Contains(out, want) {
					t.Errorf("output does not contain %q:\n%s", want, out)
				}
			}
			if _, serr := os.Stat(filepath.Join(root, "dist")); !errors.Is(serr, os.ErrNotExist) {
				t.Errorf("make release-binaries created dist/ (stat: %v), want nothing built; output:\n%s", serr, out)
			}
		})
	}
}
