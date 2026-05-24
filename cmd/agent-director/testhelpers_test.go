package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
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

// newTestClient constructs an isolated *pkgapi.Client for in-process tests.
// It creates a fresh HOME in t.TempDir(), writes a minimal (empty) config
// TOML, and opens the store with CreateIfMissing=true — matching the
// production setupClient Pin 1/2 contract. The client is closed via
// t.Cleanup so callers need not call Close themselves.
//
// Tests that invoke the CLI as a subprocess (the majority) should continue
// to use runCLI / runSpawnCLI; newTestClient is for tests that drive
// handler functions directly.
func newTestClient(t *testing.T) *pkgapi.Client {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Write an empty TOML so config.Load finds a valid (but default) file.
	cfgPath := filepath.Join(tmpHome, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatalf("newTestClient: write config: %v", err)
	}

	client, err := pkgapi.New(pkgapi.Options{
		ConfigPath:      cfgPath,
		CreateIfMissing: true,
		// StorePath omitted (Pin 2): resolves via cfg.Store.DbPath which
		// config.Load expands against HOME set above.
	})
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
