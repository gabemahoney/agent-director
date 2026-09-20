// runner_dryrun_test.go — dry-run and argument-handling tests for the coverage
// phase runner, skills/release-agent-director/gates/coverage/run-coverage-phase.sh
// (SR-7.5 first bullet, SR-1.2; b.mgw parallel /release pipeline).
//
// These tests cover ONLY what the runner adds on top of run-parallel.sh: the
// gates-config it builds and emits under --dry-run, and its argument handling.
// They never execute run-parallel.sh or any of the five real coverage gates —
// --dry-run emits the config and exits 0 without running a gate, and the
// unknown-argument path exits 2 before any config is materialized (SR-7.5
// package-scoped prohibition).
//
// The runner-under-test path is declared once here (runnerPath) and reused by
// the sibling structural-assertion test file (runner_structural_test.go), built
// on the package's shared repoRoot helper — no duplicated path literals.
//
// The five expected gate names, command strings, and cwd, plus the expected
// max_parallel, are declared once below (expectedRunnerGates / expectedMaxParallel)
// so every assertion reads the same literals and Epic t1.2mt.z4's max_parallel
// revision is a one-line update (SR-7.3).

package coverageparallelphase_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ─── runner-under-test path (declare-once; reused by runner_structural_test.go) ─

// runnerPath resolves the absolute path to the coverage-phase runner from
// repoRoot. Declared once and shared with the structural-assertion test file;
// never hardcoded relative to the test's cwd (SR-7.3, mirrors runParallelPath).
func runnerPath(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, "skills", "release-agent-director", "gates", "coverage", "run-coverage-phase.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("runnerPath: %s: %v", p, err)
	}
	return p
}

// ─── expected dry-run config (declare-once, SR-7.3) ───────────────────────────
//
// The runner's contracted config: phase_name "coverage", EXACTLY these five
// gates in order (name/command/cwd), and an explicit max_parallel. The gate
// commands invoke each gate script BARE by repo-root-relative path with no
// worktree-root argument (SR-1.2) — the go-root and go-consumer-dryrun commands
// end at ".sh" with nothing after it, proving no fixture-root argument leaks in.

const expectedRunnerPhaseName = "coverage"

// expectedMaxParallel is the single declared home for the runner's max_parallel.
// Epic t1.2mt.z4 may revise the runner's value; this constant is the one-line
// update that keeps the test in sync (SR-7.3).
const expectedMaxParallel = 5

// expectedRunnerGates is the five-entry coverage-phase contract, in order. The
// commands are BARE gate invocations (no trailing worktree-root argument).
var expectedRunnerGates = []toyGate{
	{Name: "coverage.go-root", Command: "bash skills/release-agent-director/gates/coverage/go-root.sh", Cwd: "."},
	{Name: "coverage.bun-test", Command: "bash skills/release-agent-director/gates/coverage/bun-test.sh", Cwd: "."},
	{Name: "coverage.docker-epics", Command: "bash skills/release-agent-director/gates/coverage/docker-epics.sh", Cwd: "."},
	{Name: "coverage.bun-extra-scripts", Command: "bash skills/release-agent-director/gates/coverage/bun-extra-scripts.sh", Cwd: "."},
	{Name: "coverage.go-consumer-dryrun", Command: "bash skills/release-agent-director/gates/coverage/go-consumer-dryrun.sh", Cwd: "."},
}

// ─── runner invocation ────────────────────────────────────────────────────────

// runRunner invokes the coverage-phase runner from the repo root with the given
// argument list, capturing stdout, stderr, and the exit code. --dry-run runs no
// gate and the unknown-argument path exits before materializing any config, so
// no invocation here touches a real gate or run-parallel.sh. HOME is redirected
// to a throwaway dir for parity with the executor tests (b.93m trail-leak
// isolation), though these paths spawn no gate.
func runRunner(t *testing.T, args ...string) executorResult {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("bash", append([]string{runnerPath(t, root)}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit is expected on the unknown-argument path
	return executorResult{
		stdout:   stdoutBuf.String(),
		stderr:   stderrBuf.String(),
		exitCode: cmd.ProcessState.ExitCode(),
	}
}

// TestRunnerDryRunConfig proves --dry-run emits the contracted gates-config as
// valid JSON on stdout and exits 0: phase_name "coverage", EXACTLY five gates
// with the expected name/command/cwd (commands BARE, no worktree-root argument),
// and the explicit max_parallel. It runs no gate.
func TestRunnerDryRunConfig(t *testing.T) {
	res := runRunner(t, "--dry-run")

	if res.exitCode != 0 {
		t.Fatalf("--dry-run exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, res.stdout, res.stderr)
	}

	// Decode into the toy gates-config shape (the runner's INPUT contract to
	// run-parallel.sh). A decode error here means the dry-run output is not
	// valid JSON of the expected shape.
	var got toyConfig
	if err := json.Unmarshal([]byte(res.stdout), &got); err != nil {
		t.Fatalf("--dry-run stdout is not valid gates-config JSON: %v\nstdout:\n%s", err, res.stdout)
	}

	if got.PhaseName != expectedRunnerPhaseName {
		t.Errorf("phase_name = %q, want %q", got.PhaseName, expectedRunnerPhaseName)
	}

	if got.MaxParallel != expectedMaxParallel {
		t.Errorf("max_parallel = %d, want %d", got.MaxParallel, expectedMaxParallel)
	}

	// EXACTLY five entries — not "at least five".
	if len(got.Gates) != len(expectedRunnerGates) {
		t.Fatalf("gate count = %d, want exactly %d\ngates: %+v",
			len(got.Gates), len(expectedRunnerGates), got.Gates)
	}

	// Each entry matches the expected name/command/cwd in order. The exact
	// command match is what proves the BARE invocation: any trailing
	// worktree-root argument on go-root or go-consumer-dryrun would fail here.
	for i, want := range expectedRunnerGates {
		g := got.Gates[i]
		if g.Name != want.Name {
			t.Errorf("gate[%d].name = %q, want %q", i, g.Name, want.Name)
		}
		if g.Command != want.Command {
			t.Errorf("gate[%d].command = %q, want %q", i, g.Command, want.Command)
		}
		if g.Cwd != want.Cwd {
			t.Errorf("gate[%d].cwd = %q, want %q", i, g.Cwd, want.Cwd)
		}
	}

	// max_parallel must be PRESENT as an explicit key, not merely zero-valued by
	// a typed decode of a missing field. Re-decode raw and assert the key exists.
	var rawTop map[string]json.RawMessage
	if err := json.Unmarshal([]byte(res.stdout), &rawTop); err != nil {
		t.Fatalf("--dry-run stdout raw decode: %v\nstdout:\n%s", err, res.stdout)
	}
	if _, ok := rawTop["max_parallel"]; !ok {
		// rawKeys is the package's shared key-dump helper (single_failure_test.go).
		t.Errorf("dry-run config is missing an explicit max_parallel key; keys present: %v", rawKeys(rawTop))
	}
}

// TestRunnerUnknownArgumentExits2 proves an unrecognized argument yields exit
// EXACTLY 2 (usage/config error), catching both a false pass (0) and a
// mislabeled gate failure (1). No gate runs.
func TestRunnerUnknownArgumentExits2(t *testing.T) {
	res := runRunner(t, "--no-such-flag")

	if res.exitCode != 2 {
		t.Errorf("unknown-argument exit code = %d, want exactly 2\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, res.stdout, res.stderr)
	}

	// A usage message must reach stderr; the error path emits no config JSON on
	// stdout.
	if strings.TrimSpace(res.stderr) == "" {
		t.Errorf("expected a usage message on stderr, got none\nstdout:\n%s", res.stdout)
	}
	// Contract: nothing is written to stdout on the exit-2 usage-error path (no
	// config JSON). A partial or stray stdout write would let a consumer parse a
	// bogus config, so assert stdout is empty.
	if strings.TrimSpace(res.stdout) != "" {
		t.Errorf("expected empty stdout on the exit-2 usage-error path, got:\n%s", res.stdout)
	}
}
