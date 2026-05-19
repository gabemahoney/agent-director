package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// binaryPath is set by TestMain to the path of a freshly-built binary used by
// all tests in this file. Per the test-writing guide we use real exec rather
// than mocking.
var binaryPath string

// TestMain builds the CLI binary once and shares it across all tests in this
// package. Building once avoids per-test compile overhead.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "claude-director-test-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	binaryPath = filepath.Join(tmp, "claude-director")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// runCLI invokes the built binary with args and returns stdout, stderr, exit
// code. Env is cleared except for PATH so tests are hermetic.
func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
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

// errorEnvelope mirrors the JSON shape main.go emits on dispatch errors.
type errorEnvelope struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
}

func TestHelpVerbExitsZeroAndWritesStdout(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Errorf("exit=%d want 0; stderr=%q", code, stderr)
	}
	if stdout == "" {
		t.Errorf("stdout empty; want some help output")
	}
}

func TestHelpFlagAndHelpVerbProduceIdenticalOutput(t *testing.T) {
	stdoutVerb, _, codeVerb := runCLI(t, "help")
	stdoutFlag, _, codeFlag := runCLI(t, "--help")
	if codeVerb != 0 || codeFlag != 0 {
		t.Fatalf("expected exit 0 for both: verb=%d flag=%d", codeVerb, codeFlag)
	}
	if stdoutVerb != stdoutFlag {
		t.Errorf("help and --help produced different output:\nhelp=%q\n--help=%q",
			stdoutVerb, stdoutFlag)
	}
}

func TestNoArgsRoutesToHelpAndExitsZero(t *testing.T) {
	stdout, _, code := runCLI(t)
	if code != 0 {
		t.Errorf("exit=%d want 0", code)
	}
	helpStdout, _, _ := runCLI(t, "help")
	if stdout != helpStdout {
		t.Errorf("no-args output differs from help:\nno-args=%q\nhelp=%q",
			stdout, helpStdout)
	}
}

func TestUnknownVerbWritesErrorEnvelope(t *testing.T) {
	cases := []struct {
		name string
		verb string
	}{
		{name: "bogusverb", verb: "bogusverb"},
		{name: "nope", verb: "nope"},
		{name: "nonexistent", verb: "nonexistent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runCLI(t, tc.verb)
			if code == 0 {
				t.Errorf("exit=0 want non-zero")
			}
			if stdout != "" {
				t.Errorf("stdout=%q want empty", stdout)
			}
			var env errorEnvelope
			if err := json.Unmarshal([]byte(stderr), &env); err != nil {
				t.Fatalf("stderr is not JSON-parseable: %v\nstderr=%q", err, stderr)
			}
			if env.ErrName == "" {
				t.Errorf("err_name empty in %q", stderr)
			}
			if env.ErrDescription == "" {
				t.Errorf("err_description empty in %q", stderr)
			}
		})
	}
}
