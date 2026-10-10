// Package sandboxrequiresandbox_test is a regression test for bug b.8yq.
//
// make generate, all, surface-json, errnames-json, check-doccomments,
// nondet-coverage, err-coherence, release-smoke, envelope-diff-ts and test ran
// go generate, go run, go test or bun test straight on the host. Each now lists
// _require-sandbox first: outside the sandbox (no AGENT_DIRECTOR_TEST_SANDBOX)
// and without the CI bypass it exits 2 before anything runs and names the
// command that runs the same goals in the sandbox.
//
// Like the other test/sandbox suites it drives a copy of the Makefile under test
// in a temp tree, with logging fake go, bun and docker on PATH, and runs no repo
// binary, so it needs no sandboxguard TestMain.
//
// Fails before the fix: MAKEFILE_UNDER_TEST=<pre-fix Makefile> fails every
// TestRequireSandbox_RefusesOutsideSandbox cell and TestRequireSandbox_AdviceFollow;
// without the order-only `| _require-sandbox` block, the _j8 cells fail.
package sandboxrequiresandbox_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

// fakeTool stands in for go and bun: it logs "<tool> <args>" and succeeds.
const fakeTool = `#!/bin/sh
printf '%s %s\n' "$(basename "$0")" "$*" >> "$FAKE_TOOL_LOG"
`

// fakeEngine stands in for docker. It logs "docker <subcommand>". A run runs
// the command after the image name in the current directory, as a container
// would: with only PATH, FAKE_TOOL_LOG and the run's -e variables set.
const fakeEngine = `#!/usr/bin/env bash
printf 'docker %s\n' "$1" >> "$FAKE_TOOL_LOG"
[ "$1" = run ] || exit 0
shift
envs=()
while [ $# -gt 0 ] && [ "$1" != "$SANDBOX_IMAGE" ]; do
	if [ "$1" = -e ]; then
		case "$2" in
		*=*) envs+=("$2") ;;
		*) envs+=("$2=${!2-}") ;;
		esac
		shift
	fi
	shift
done
[ $# -gt 0 ] || { echo "fake docker: image $SANDBOX_IMAGE not in the run" >&2; exit 125; }
shift
exec env -i PATH="$PATH" FAKE_TOOL_LOG="$FAKE_TOOL_LOG" "${envs[@]}" "$@"
`

// unsetVars are dropped from the inherited environment: the sandbox marker
// (set where this suite runs), the bypass, GitHub runner variables and the
// Makefile variables the runs pin themselves.
var unsetVars = []string{
	"AGENT_DIRECTOR_TEST_SANDBOX", "BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS",
	"GITHUB_ACTIONS", "RUNNER_ENVIRONMENT", "CI",
	"CONTAINER_ENGINE", "SANDBOX_IMAGE", "SANDBOX_FLAGS", "GO_TEST_TIMEOUT",
}

// tree returns a temp dir holding a copy of the Makefile under test, and the
// go.mod and go.sum that bin/ts-helper and test/fake-tmux/tmux depend on.
func tree(t *testing.T) string {
	t.Helper()
	sandboxtest.RequireTools(t, "skipping b.8yq _require-sandbox test", "make", "bash", "env")
	if testing.Short() {
		t.Skip("slow: runs make and shell shims")
	}
	data, err := os.ReadFile(sandboxtest.MakefileUnderTest(t))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	root := t.TempDir()
	for name, body := range map[string][]byte{"Makefile": data, "go.mod": nil, "go.sum": nil} {
		if err := os.WriteFile(filepath.Join(root, name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// run runs argv in root with the fakes first on PATH, none of unsetVars set
// and extra added. It returns the exit code, the output and the logged calls.
func run(t *testing.T, root string, extra []string, argv ...string) (code int, out string, calls []string) {
	t.Helper()
	bin := t.TempDir()
	for name, body := range map[string]string{"go": fakeTool, "bun": fakeTool, "docker": fakeEngine} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	logPath := filepath.Join(t.TempDir(), "calls.log")
	env := slices.DeleteFunc(sandboxtest.ScrubbedMakeEnv(), func(e string) bool {
		k, _, _ := strings.Cut(e, "=")
		return slices.Contains(unsetVars, k)
	})
	env = append(env,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_TOOL_LOG="+logPath,
		"TMPDIR="+t.TempDir(), // fresh pid-probe marker, as cmdinject does
		"CONTAINER_ENGINE=docker", "SANDBOX_IMAGE=fake-sandbox-image", "GO_TEST_TIMEOUT=7m",
	)
	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = root
	c.Env = append(env, extra...)
	b, err := c.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("run %q: %v", argv, err)
	}
	if data, rerr := os.ReadFile(logPath); rerr == nil {
		calls = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	return code, string(b), calls
}

var (
	// ruleLine matches a rule line (targets and prerequisites, no assignment).
	ruleLine = regexp.MustCompile(`^([A-Za-z0-9_./%-][^:=#]*):([^=]*)$`)
	// toolCall matches a recipe line that runs a go or bun tool itself.
	toolCall = regexp.MustCompile(`\b(go (generate|run|test)|bun test)\b`)
)

// toolTargets returns, in Makefile order, every target whose own recipe runs go
// generate, go run, go test or bun test outside $(_SANDBOX_RUN).
func toolTargets(t *testing.T, makefile string) []string {
	t.Helper()
	data, err := os.ReadFile(makefile)
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	var targets, current []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\\\n", " "), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "\t"):
			if toolCall.MatchString(line) && !strings.Contains(line, "$(_SANDBOX_RUN)") {
				for _, tg := range current {
					if !slices.Contains(targets, tg) {
						targets = append(targets, tg)
					}
				}
			}
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
		default:
			current = nil
			if m := ruleLine.FindStringSubmatch(line); m != nil {
				current = strings.Fields(m[1])
			}
		}
	}
	return targets
}

// TestRequireSandbox_RefusesOutsideSandbox (b.8yq): outside the sandbox every
// target whose recipe runs a go or bun tool (found by toolTargets, so a new one
// is covered too), all, the default goal and several goals at once exit 2 with
// the refusal and its advice, and no go, bun or container engine runs, also
// under make -j8. The advice is built from all the goals, whichever target
// reaches the guard first: make test-sandbox for test, which cannot run in the
// sandbox, and make sandbox CMD="make <the others>" for the rest, joined by &&
// with test-sandbox first only when test is the first goal.
func TestRequireSandbox_RefusesOutsideSandbox(t *testing.T) {
	root := tree(t)
	targets := toolTargets(t, filepath.Join(root, "Makefile"))
	for _, want := range []string{"test", "generate", "surface-json", "errnames-json", "err-coherence",
		"check-doccomments", "nondet-coverage", "envelope-diff-ts", "release-smoke"} {
		if !slices.Contains(targets, want) {
			t.Fatalf("toolTargets = %q, missing %q: the recipe scan no longer finds the known tool targets", targets, want)
		}
	}

	type cell struct {
		name          string
		args, env     []string
		goals, advice string
		// j8 adds a copy of the cell run under make -j8, which starts a goal's
		// prerequisites together: the go builds and docker build that test, all
		// and envelope-diff-ts need must still wait for the guard.
		j8 bool
	}
	var cells []cell
	for _, tg := range targets {
		advice := `make sandbox CMD="make ` + tg + `"`
		if tg == "test" {
			advice = "make test-sandbox" // make test needs a container engine, which the sandbox lacks
		}
		cells = append(cells, cell{name: tg, args: []string{tg}, goals: tg, advice: advice,
			j8: slices.Contains([]string{"test", "envelope-diff-ts"}, tg)})
	}
	cells = append(cells,
		cell{name: "all", args: []string{"all"}, goals: "all", advice: `make sandbox CMD="make all"`, j8: true},
		cell{name: "default_goal", goals: "all", advice: `make sandbox CMD="make all"`, j8: true},
		cell{name: "two_goals", args: []string{"check-doccomments", "nondet-coverage"},
			goals: "check-doccomments nondet-coverage", advice: `make sandbox CMD="make check-doccomments nondet-coverage"`},
		cell{name: "generate_surface_json", args: []string{"generate", "surface-json"},
			goals: "generate surface-json", advice: `make sandbox CMD="make generate surface-json"`},
		// test with other goals: test-sandbox for test, the sandbox for the rest.
		cell{name: "generate_test", args: []string{"generate", "test"},
			goals: "generate test", advice: `make sandbox CMD="make generate" && make test-sandbox`, j8: true},
		cell{name: "test_generate", args: []string{"test", "generate"},
			goals: "test generate", advice: `make test-sandbox && make sandbox CMD="make generate"`, j8: true},
		cell{name: "generate_test_surface_json", args: []string{"generate", "test", "surface-json"},
			goals:  "generate test surface-json",
			advice: `make sandbox CMD="make generate surface-json" && make test-sandbox`, j8: true},
		cell{name: "github_hosted_runner_vars_alone", args: []string{"generate"},
			env:   []string{"GITHUB_ACTIONS=true", "RUNNER_ENVIRONMENT=github-hosted", "CI=true"},
			goals: "generate", advice: `make sandbox CMD="make generate"`},
	)
	for _, c := range cells {
		if c.j8 {
			c.name, c.args = c.name+"_j8", append([]string{"-j8"}, c.args...)
			cells = append(cells, c)
		}
	}

	for _, tc := range cells {
		t.Run(tc.name, func(t *testing.T) {
			code, out, calls := run(t, root, tc.env, append([]string{"make"}, tc.args...)...)
			if code != 2 {
				t.Errorf("exit = %d, want 2; output:\n%s", code, out)
			}
			if len(calls) != 0 {
				t.Errorf("ran %q outside the sandbox, want nothing run", calls)
			}
			for _, want := range []string{
				"ERROR: make " + tc.goals + " runs go or bun tools, which run only in the sandbox, never on the host (b.8yq).",
				"Nothing was run.",
				"Run it in the sandbox: " + tc.advice + "\n",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("output does not contain %q:\n%s", want, out)
				}
			}
		})
	}
}

// briefCalls reduces each logged call to "<tool> <subcommand> <last arg>" (the
// -ldflags of a go build vary) and sorts them (make -j runs in any order).
func briefCalls(calls []string) []string {
	var brief []string
	for _, c := range calls {
		f := strings.Fields(c)
		if len(f) > 3 {
			f = []string{f[0], f[1], f[len(f)-1]}
		}
		brief = append(brief, strings.Join(f, " "))
	}
	slices.Sort(brief)
	return brief
}

// TestRequireSandbox_Passes (b.8yq): the guard lets the tool run in the sandbox
// or with the CI bypass, a dry run outside the sandbox still exits 0, and
// targets that only build (build, ts-helper) still run on the host, also under -j8.
func TestRequireSandbox_Passes(t *testing.T) {
	root := tree(t)
	builds := []string{"go build ./cmd/agent-director", "go build ./cmd/agent-director-admin"}
	for _, tc := range []struct {
		name            string
		args, env       []string
		wantCalls       []string
		wantOutContains string
	}{
		{name: "sandbox_marker", args: []string{"generate"}, env: []string{"AGENT_DIRECTOR_TEST_SANDBOX=1"},
			wantCalls: []string{"go generate ./..."}},
		{name: "ci_bypass", args: []string{"generate"}, env: []string{"BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS=1"},
			wantCalls: []string{"go generate ./..."}},
		{name: "dry_run_outside_sandbox", args: []string{"-n", "generate"}, wantOutContains: "go generate ./..."},
		{name: "build_outside_sandbox", args: []string{"build"}, wantCalls: builds},
		{name: "build_ts_helper_j8_outside_sandbox", args: []string{"-j8", "build", "ts-helper"},
			wantCalls: append([]string{"go build ./test/smoke/ts-helper/"}, builds...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, calls := run(t, root, tc.env, append([]string{"make"}, tc.args...)...)
			if code != 0 {
				t.Errorf("exit = %d, want 0; output:\n%s", code, out)
			}
			if got := briefCalls(calls); !slices.Equal(got, briefCalls(tc.wantCalls)) {
				t.Errorf("calls = %q, want %q; output:\n%s", calls, tc.wantCalls, out)
			}
			if !strings.Contains(out, tc.wantOutContains) {
				t.Errorf("output lacks %q:\n%s", tc.wantOutContains, out)
			}
		})
	}
}

// TestRequireSandbox_AdviceFollow (b.8yq): the refusal's advice
// `make sandbox CMD="make generate"`, run as printed, runs go generate in the
// (fake) container, where the guard passes.
func TestRequireSandbox_AdviceFollow(t *testing.T) {
	root := tree(t)
	const advice = `make sandbox CMD="make generate"`
	code, out, _ := run(t, root, nil, "make", "generate")
	if code != 2 || !strings.Contains(out, "Run it in the sandbox: "+advice+"\n") {
		t.Fatalf("make generate: exit %d, want 2 with the advice %q; output:\n%s", code, advice, out)
	}

	code, out, calls := run(t, root, nil, "bash", "-c", advice)
	calls = slices.DeleteFunc(calls, func(c string) bool { return strings.HasPrefix(c, "docker ") })
	if code != 0 {
		t.Errorf("%s: exit %d, want 0; output:\n%s", advice, code, out)
	}
	if want := []string{"go generate ./..."}; !slices.Equal(calls, want) {
		t.Errorf("%s ran %q, want %q; output:\n%s", advice, calls, want, out)
	}
}
