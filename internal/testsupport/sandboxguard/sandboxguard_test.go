package sandboxguard

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// envHelper flips this test binary into helper-process mode: TestMain calls
// Require() and, if it returns, prints proceedMarker and exits 0. Require()
// calls os.Exit(1) on refusal, so the behaviour is only observable from a
// subprocess (same trick as internal/store's fresh-init helper).
const envHelper = "AD_SANDBOXGUARD_TEST_HELPER"

// proceedMarker is what the helper prints when Require() lets it through.
const proceedMarker = "GUARD-PROCEEDED"

func TestMain(m *testing.M) {
	if os.Getenv(envHelper) != "" {
		Require()
		os.Stdout.WriteString(proceedMarker + "\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runGuard re-execs this test binary in helper mode with exactly the guard
// env vars listed in set (both are stripped from the inherited environment
// first — the sandbox itself exports EnvVar, which would otherwise leak in).
func runGuard(t *testing.T, set map[string]string) (stdout, stderr string, exitCode int) {
	t.Helper()

	env := []string{envHelper + "=1"}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == EnvVar || name == BypassEnvVar || name == envHelper {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^$") //nolint:gosec // path is the test binary itself
	cmd.Env = env
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		exitCode = 0
	case errors.As(err, &exitErr):
		exitCode = exitErr.ExitCode()
	default:
		t.Fatalf("running guard helper: %v\nstderr:\n%s", err, errBuf.String())
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// Require() proceeds when either escape hatch is set, in any combination.
func TestRequireProceedsWhenAnEscapeHatchIsSet(t *testing.T) {
	cases := map[string]map[string]string{
		"sandbox marker only": {EnvVar: "1"},
		"bypass only":         {BypassEnvVar: "1"},
		"both":                {EnvVar: "1", BypassEnvVar: "1"},
	}
	for name, env := range cases {
		env := env
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := runGuard(t, env)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, stderr)
			}
			if !strings.Contains(stdout, proceedMarker) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, proceedMarker)
			}
			if strings.Contains(stderr, "refusing to run") {
				t.Errorf("guard refused despite %v being set; stderr:\n%s", env, stderr)
			}
		})
	}
}

// With neither variable set the guard is fail-closed: exit 1, nothing runs,
// and the refusal names both escape hatches (b.175 AC4/AC7).
func TestRequireRefusesWhenNeitherVariableIsSet(t *testing.T) {
	stdout, stderr, code := runGuard(t, nil)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, proceedMarker) {
		t.Fatalf("helper proceeded past Require(); stdout:\n%s", stdout)
	}
	for _, want := range []string{
		"refusing to run",
		EnvVar,
		BypassEnvVar,
		"make test-sandbox",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal message missing %q; stderr:\n%s", want, stderr)
		}
	}
	// The bypass must be described as CI-only, not as a host escape hatch.
	if !strings.Contains(stderr, "Do NOT set "+BypassEnvVar) {
		t.Errorf("refusal message does not warn against setting %s here; stderr:\n%s", BypassEnvVar, stderr)
	}
}

// An empty value is not "set": the guard keys on non-empty, so blank vars
// must still refuse rather than silently opening the gate.
func TestRequireRefusesOnEmptyValues(t *testing.T) {
	_, stderr, code := runGuard(t, map[string]string{EnvVar: "", BypassEnvVar: ""})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (empty values must not count as set)\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "refusing to run") {
		t.Errorf("expected refusal message; stderr:\n%s", stderr)
	}
}
