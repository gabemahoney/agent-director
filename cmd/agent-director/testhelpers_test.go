package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// binaryPath is set by TestMain to the path of a freshly-built binary used
// by all tests in this package. Per the test-writing guide we exec the real
// binary rather than mocking dispatch.
var binaryPath string

// TestMain builds the CLI binary once and shares it across every test in
// this package. Building once per package run avoids per-test compile cost
// and keeps the race-detector run cheap.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "agent-director-test-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	binaryPath = filepath.Join(tmp, "agent-director")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// errorEnvelope mirrors the JSON shape main.go emits on dispatch errors.
type errorEnvelope struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
}

// runCLI invokes the built binary with args under a HOME=t.TempDir()
// override and returns stdout, stderr, exit code. The HOME override keeps
// every invocation from touching the developer's real ~/.agent-director/
// when state.db is opened during startup.
func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runCLIWithHome(t, t.TempDir(), args...)
}

// runCLIWithHome is the explicit-HOME variant of runCLI. Tests that need to
// observe filesystem side effects (e.g. mode bits on a directory the binary
// created) pass the same home into multiple invocations.
func runCLIWithHome(t *testing.T, home string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("unexpected exec error: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

// parseEnvelope unmarshals a JSON error envelope from raw and fails the test
// on parse error. Returns the parsed envelope for further field assertions.
func parseEnvelope(t *testing.T, raw string) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("stderr is not JSON-parseable: %v\nstderr=%q", err, raw)
	}
	return env
}
