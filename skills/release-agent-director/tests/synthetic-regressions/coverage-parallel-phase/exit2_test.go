// exit2_test.go — config-error exit-code semantics for run-parallel.sh
// (SR-1.3, SR-7.5; b.mgw parallel /release pipeline).
//
// run-parallel.sh's argument-validation section has three distinct exit-2
// paths: invocation with no config argument, a config path that does not
// exist, and a config file that exists but is not valid JSON. SR-1.3 requires
// that usage/configuration errors yield exit EXACTLY 2 — never a false pass (0)
// and never a mislabeled gate failure (1) — with an error message on stderr and
// no consolidated JSON. These invocations never run a gate, so they are
// sub-second and carry no testing.Short() guard.
//
// The failing-gate exit-1 path and the passing exit-0 path are covered by the
// sibling test files (single_failure_test.go, multi_failure_test.go,
// mechanism_test.go); this file owns only the config-error surface.

package coverageparallelphase_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runExecutorArgs invokes run-parallel.sh directly with an arbitrary argument
// list (zero or more), capturing stdout, stderr, and the exit code. The shared
// runExecutor helper always passes exactly one config path; the no-argument
// exit-2 case needs a bare invocation, so this file declares its own thin
// invoker rather than changing the shared helper. HOME is redirected to a
// throwaway dir for parity with runExecutor (b.93m trail-leak isolation),
// though these paths spawn no gate.
func runExecutorArgs(t *testing.T, args ...string) executorResult {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("bash", append([]string{runParallelPath(t, root)}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit is expected on every config-error path
	return executorResult{
		stdout:   stdoutBuf.String(),
		stderr:   stderrBuf.String(),
		exitCode: cmd.ProcessState.ExitCode(),
	}
}

func TestExit2ConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		// invoke performs the run-parallel.sh invocation for the case and
		// returns its result. Each case controls its own argument list because
		// the surfaces differ (no argument vs. a config-path argument).
		invoke func(t *testing.T) executorResult
	}{
		{
			name:   "no config argument",
			invoke: func(t *testing.T) executorResult { return runExecutorArgs(t) },
		},
		{
			name: "nonexistent config path",
			invoke: func(t *testing.T) executorResult {
				missing := filepath.Join(t.TempDir(), "does-not-exist.json")
				return runExecutorArgs(t, missing)
			},
		},
		{
			name: "existing file with invalid JSON",
			invoke: func(t *testing.T) executorResult {
				path := filepath.Join(t.TempDir(), "gates-config.json")
				if err := os.WriteFile(path, []byte("{ this is not valid json"), 0o600); err != nil {
					t.Fatalf("write invalid-JSON config: %v", err)
				}
				return runExecutorArgs(t, path)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.invoke(t)

			// Exit code EXACTLY 2: catches both a false pass (0) and a
			// mislabeled gate failure (1).
			if res.exitCode != 2 {
				t.Errorf("exit code = %d, want exactly 2\nstdout:\n%s\nstderr:\n%s",
					res.exitCode, res.stdout, res.stderr)
			}

			// An error message must reach stderr; the config-error paths emit
			// no consolidated JSON on stdout.
			if strings.TrimSpace(res.stderr) == "" {
				t.Errorf("expected an error message on stderr, got none\nstdout:\n%s", res.stdout)
			}
			// Contract: nothing is written to stdout on the exit-2 error paths (no
			// consolidated/config JSON). A partial or stray stdout write would let a
			// consumer parse a bogus report, so assert stdout is empty.
			if strings.TrimSpace(res.stdout) != "" {
				t.Errorf("expected empty stdout on the exit-2 error path, got:\n%s", res.stdout)
			}
		})
	}
}
