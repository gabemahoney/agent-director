// Package sandboxrequiresandbox_test is a regression test for bug b.8yq.
//
// make generate, all, surface-json, errnames-json, check-doccomments,
// nondet-coverage, err-coherence, release-smoke, envelope-diff-ts and test ran
// go generate, go run, go test or bun test straight on the host. Each now lists
// _require-sandbox first: outside the sandbox (no AGENT_DIRECTOR_TEST_SANDBOX)
// and without the CI bypass it exits 2 before anything runs and names the
// command that runs the same goals in the sandbox.
//
// b.4a1: that command put every goal but test in one make sandbox CMD=, also
// the goals that start a container (test-docker, sandbox, …), which cannot run
// there: the sandbox image has no container engine; and those that run a host
// tool it lacks or check the host's own setup (release-shellcheck, tla, …).
// Those now run on the host, as themselves, before or after the one sandbox
// command.
//
// Like the other test/sandbox suites it drives a copy of the Makefile under test
// in a temp tree, with logging fake go, bun and docker on PATH, and runs no repo
// binary, so it needs no sandboxguard TestMain.
//
// Fails before the fix: MAKEFILE_UNDER_TEST=<pre-fix Makefile> fails every
// TestRequireSandbox_RefusesOutsideSandbox cell and TestRequireSandbox_AdviceFollow;
// without the order-only `| _require-sandbox` block, the _j8 cells fail. Before
// b.4a1's fix, the cells that mix a guarded goal with a container goal fail.
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

// fakeEngine stands in for docker. It logs "docker <subcommand>". A run of the
// sandbox image runs the command after the image name in the current directory,
// as a container would: with only FAKE_TOOL_LOG and the run's -e variables set,
// and FAKE_SANDBOX_PATH as PATH, on which there is no working container engine.
// A run of any other image, such as the harness's test image, only logs.
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
[ $# -gt 0 ] || exit 0
shift
exec env -i PATH="$FAKE_SANDBOX_PATH" FAKE_TOOL_LOG="$FAKE_TOOL_LOG" "${envs[@]}" "$@"
`

// noEngine is docker and podman inside the fake sandbox, which, like the
// sandbox image, has no container engine (b.4a1).
const noEngine = `#!/bin/sh
echo "$(basename "$0"): no container engine in the sandbox" >&2
exit 127
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
	bin, sandboxBin := t.TempDir(), t.TempDir()
	for path, body := range map[string]string{
		filepath.Join(bin, "go"): fakeTool, filepath.Join(bin, "bun"): fakeTool, filepath.Join(bin, "docker"): fakeEngine,
		filepath.Join(sandboxBin, "docker"): noEngine, filepath.Join(sandboxBin, "podman"): noEngine,
	} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", path, err)
		}
	}
	logPath := filepath.Join(t.TempDir(), "calls.log")
	env := slices.DeleteFunc(sandboxtest.ScrubbedMakeEnv(), func(e string) bool {
		k, _, _ := strings.Cut(e, "=")
		return slices.Contains(unsetVars, k)
	})
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	env = append(env,
		"PATH="+path,
		"FAKE_SANDBOX_PATH="+sandboxBin+string(os.PathListSeparator)+path,
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
	// engineCall matches a recipe line that starts a container.
	engineCall = regexp.MustCompile(`(\bdocker|\bpodman|\$\(CONTAINER_ENGINE\)) (build|run)\b|\$\(_SANDBOX_RUN\)`)
	// subMake matches a sub-make in a recipe line and captures its goal.
	subMake = regexp.MustCompile(`\$\(MAKE\) ([^\s;|&]+)`)
)

// rule is one Makefile rule: its targets, prerequisites (order-only ones too)
// and recipe lines.
type rule struct{ targets, prereqs, recipe []string }

// rules returns the rules of makefile in order, continued lines joined.
func rules(t *testing.T, makefile string) []rule {
	t.Helper()
	data, err := os.ReadFile(makefile)
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	var rs []rule
	current := -1
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\\\n", " "), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "\t"):
			if current >= 0 {
				rs[current].recipe = append(rs[current].recipe, line)
			}
		case trimmed == "" || strings.HasPrefix(trimmed, "#"):
		default:
			current = -1
			if m := ruleLine.FindStringSubmatch(line); m != nil {
				prereqs := slices.DeleteFunc(strings.Fields(m[2]), func(p string) bool { return p == "|" })
				rs = append(rs, rule{targets: strings.Fields(m[1]), prereqs: prereqs})
				current = len(rs) - 1
			}
		}
	}
	return rs
}

// targetsOf returns, in Makefile order and once each, the targets for which
// keep, given the target's rule, is true; special targets such as .PHONY left out.
func targetsOf(rs []rule, keep func(r rule, target string) bool) []string {
	var targets []string
	for _, r := range rs {
		for _, tg := range r.targets {
			if keep(r, tg) && !strings.HasPrefix(tg, ".") && !slices.Contains(targets, tg) {
				targets = append(targets, tg)
			}
		}
	}
	return targets
}

// toolTargets returns, in Makefile order, every target whose own recipe runs go
// generate, go run, go test or bun test outside $(_SANDBOX_RUN).
func toolTargets(t *testing.T, makefile string) []string {
	t.Helper()
	return targetsOf(rules(t, makefile), func(r rule, _ string) bool {
		return slices.ContainsFunc(r.recipe, func(l string) bool {
			return toolCall.MatchString(l) && !strings.Contains(l, "$(_SANDBOX_RUN)")
		})
	})
}

// engineTargets returns, in Makefile order, every target that starts a
// container: in its own recipe, or through a prerequisite or sub-make goal
// that does.
func engineTargets(t *testing.T, makefile string) []string {
	t.Helper()
	rs := rules(t, makefile)
	starts := map[string]bool{}
	startsContainer := func(r rule) bool {
		deps := slices.Clone(r.prereqs)
		for _, l := range r.recipe {
			for _, m := range subMake.FindAllStringSubmatch(l, -1) {
				deps = append(deps, m[1])
			}
		}
		return slices.ContainsFunc(r.recipe, engineCall.MatchString) ||
			slices.ContainsFunc(deps, func(d string) bool { return starts[d] })
	}
	for changed := true; changed; {
		changed = false
		for _, r := range rs {
			for _, tg := range r.targets {
				if !starts[tg] && startsContainer(r) {
					starts[tg], changed = true, true
				}
			}
		}
	}
	return targetsOf(rs, func(_ rule, tg string) bool { return starts[tg] })
}

// TestRequireSandbox_RefusesOutsideSandbox (b.8yq): outside the sandbox every
// target whose recipe runs a go or bun tool (found by toolTargets, so a new one
// is covered too), all, the default goal and several goals at once exit 2 with
// the refusal and its advice, and no go, bun or container engine runs, also
// under make -j8. The advice is built from all the goals, whichever target
// reaches the guard first: make test-sandbox for test, which cannot run in the
// sandbox, and make sandbox CMD="make <the others>" for the rest, joined by &&
// with test-sandbox first only when test is the first goal. A goal that works
// only on the host never goes in the CMD= but runs on the host as itself,
// keeping its place before or after the sandbox (b.4a1): each goal that starts
// a container (found by engineTargets) and each in hostOnly (a host tool or the
// host's own setup) has a cell after a guarded goal.
func TestRequireSandbox_RefusesOutsideSandbox(t *testing.T) {
	root := tree(t)
	targets := toolTargets(t, filepath.Join(root, "Makefile"))
	for _, want := range []string{"test", "generate", "surface-json", "errnames-json", "err-coherence",
		"check-doccomments", "nondet-coverage", "envelope-diff-ts", "release-smoke"} {
		if !slices.Contains(targets, want) {
			t.Fatalf("toolTargets = %q, missing %q: the recipe scan no longer finds the known tool targets", targets, want)
		}
	}
	engine := engineTargets(t, filepath.Join(root, "Makefile"))
	for _, want := range []string{"test", "test-install-sh", "_sandbox-build", "sandbox", "test-sandbox",
		"test-image", "test-docker", "measure-exit", "verify-prerelease-linux"} {
		if !slices.Contains(engine, want) {
			t.Fatalf("engineTargets = %q, missing %q: the recipe scan no longer finds the known container targets", engine, want)
		}
	}
	// hostOnly are the goals that work only on the host without starting a
	// container, so engineTargets cannot find them: they run a host tool the
	// sandbox image lacks, or check or print the host's own setup. Listed by
	// hand, so that dropping one from the Makefile's _REQUIRE_SANDBOX_HOST_GOALS
	// fails its cell.
	hostOnly := []string{"_sandbox-preflight", "measure-exit-print", "_measure-exit-credentials",
		"release-shellcheck", "tla", "tla-print"}
	makeTargets := targetsOf(rules(t, filepath.Join(root, "Makefile")), func(rule, string) bool { return true })
	hostGoals := slices.Clone(engine)
	for _, tg := range hostOnly {
		if !slices.Contains(makeTargets, tg) {
			t.Fatalf("Makefile targets = %q, missing %q: the hand-listed host-only goal is no longer a target", makeTargets, tg)
		}
		if !slices.Contains(hostGoals, tg) {
			hostGoals = append(hostGoals, tg)
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
		// b.4a1: container goals run on the host, before or after the sandbox.
		cell{name: "test_docker_generate_test", args: []string{"test-docker", "generate", "test"},
			goals:  "test-docker generate test",
			advice: `make test-docker && make sandbox CMD="make generate" && make test-sandbox`, j8: true},
		cell{name: "test_image_generate_test_test_docker", args: []string{"test-image", "generate", "test", "test-docker"},
			goals:  "test-image generate test test-docker",
			advice: `make test-image && make sandbox CMD="make generate" && make test-sandbox test-docker`, j8: true},
		cell{name: "test_test_docker", args: []string{"test", "test-docker"},
			goals: "test test-docker", advice: "make test-sandbox test-docker", j8: true},
	)
	// b.4a1: each host-only goal after a guarded one: the container goals, and
	// hostOnly. No _j8 copy: with no test, all or envelope-diff-ts among the
	// goals, test-image's go build and docker build do not wait for the guard.
	for _, tg := range hostGoals {
		if tg != "test" { // advised as make test-sandbox, in the cells above
			cells = append(cells, cell{name: "generate_" + tg, args: []string{"generate", tg},
				goals: "generate " + tg, advice: `make sandbox CMD="make generate" && make ` + tg})
		}
	}
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

// TestRequireSandbox_AdviceFollow (b.8yq, b.4a1): the refusal's advice, run as
// printed, exits 0 and runs the goals' go calls: go generate in the (fake)
// container, where the guard passes, and test-docker's builds on the host,
// where its container engine is (docker fails in the fake container).
func TestRequireSandbox_AdviceFollow(t *testing.T) {
	root := tree(t)
	for _, tc := range []struct {
		name       string
		goals, env []string
		advice     string
		wantCalls  []string
	}{
		{name: "generate", goals: []string{"generate"}, advice: `make sandbox CMD="make generate"`,
			wantCalls: []string{"go generate ./..."}},
		// EPIC is set in the environment: the advice names the goals only.
		{name: "generate_test_docker", goals: []string{"generate", "test-docker"}, env: []string{"EPIC=harness-smoke"},
			advice:    `make sandbox CMD="make generate" && make test-docker`,
			wantCalls: []string{"go generate ./...", "go build ./cmd/agent-director", "go build ./cmd/agent-director-admin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := run(t, root, tc.env, append([]string{"make"}, tc.goals...)...)
			if code != 2 || !strings.Contains(out, "Run it in the sandbox: "+tc.advice+"\n") {
				t.Fatalf("make %s: exit %d, want 2 with the advice %q; output:\n%s", strings.Join(tc.goals, " "), code, tc.advice, out)
			}

			code, out, calls := run(t, root, tc.env, "bash", "-c", tc.advice)
			calls = slices.DeleteFunc(calls, func(c string) bool { return strings.HasPrefix(c, "docker ") })
			if code != 0 {
				t.Errorf("%s: exit %d, want 0; output:\n%s", tc.advice, code, out)
			}
			if got := briefCalls(calls); !slices.Equal(got, briefCalls(tc.wantCalls)) {
				t.Errorf("%s ran %q, want %q; output:\n%s", tc.advice, calls, tc.wantCalls, out)
			}
		})
	}
}
