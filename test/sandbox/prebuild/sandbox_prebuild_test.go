// Package sandboxprebuild_test is a regression test for bug b.2b3.
//
// `make test-sandbox` used to run `go test ./...` and then `bun test` with no
// `bun install` or `bun run build` in pkg/ts-bun-client. node_modules/ and dist/
// are gitignored, so on a fresh worktree the tests that read dist/ failed, or
// passed only if an earlier run had left a build behind. The fixed recipe
// installs and builds first, and stops with "no tests ran" if either step fails.
//
// Like cmdinject (b.ay3), it drives the real Makefile recipe with a fake
// container engine on PATH that records the container `run` argv and runs
// nothing. The test then runs the recorded container script itself, with /work
// pointed at a temp tree and fake `bun` and `go` on PATH that only log each call
// and its directory. It never runs the real suites, bun or the agent-director
// binary, so it needs no sandboxguard TestMain.
//
// Fails before the fix: point MAKEFILE_UNDER_TEST at the pre-fix Makefile and
// every case fails, because the script never calls `bun install` or
// `bun run build`.
package sandboxprebuild_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

// fakeEngineScript stands in for docker. Each `run` overwrites run.argv
// (NUL-separated), so it ends with test-sandbox's run, not the pid probe's.
const fakeEngineScript = `#!/usr/bin/env bash
if [ "$1" = run ]; then printf '%s\0' "$@" > "$FAKE_ENGINE_DIR/run.argv"; fi
exit 0
`

// fakeToolScript stands in for bun and go: it logs "<tool> <args> @ <dir>" with
// the temp tree shown as /work, and fails a call that starts with $FAKE_FAIL.
const fakeToolScript = `#!/usr/bin/env bash
call="$(basename "$0") $*"
printf '%s @ /work%s\n' "$call" "${PWD#"$FAKE_WORK"}" >> "$FAKE_TOOL_LOG"
if [ -n "$FAKE_FAIL" ] && [[ "$call" == "$FAKE_FAIL"* ]]; then exit 1; fi
exit 0
`

// writeShim writes an executable script at dir/name.
func writeShim(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write %s shim: %v", name, err)
	}
}

// recordedScript runs `make test-sandbox` with the fake engine and returns the
// script the recipe hands the container (the argument after `bash -c`).
func recordedScript(t *testing.T) string {
	t.Helper()
	engineBin, engineDir := t.TempDir(), t.TempDir()
	writeShim(t, engineBin, "docker", fakeEngineScript)

	c := exec.Command("make", "-f", sandboxtest.MakefileUnderTest(t), "test-sandbox",
		"CONTAINER_ENGINE=docker", "GO_TEST_TIMEOUT=7m")
	c.Dir = sandboxtest.RepoRoot(t)
	c.Env = sandboxtest.ScrubbedMakeEnv(
		"PATH="+engineBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_ENGINE_DIR="+engineDir,
		"TMPDIR="+t.TempDir(), // fresh pid-probe marker, as cmdinject does
	)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("make test-sandbox (fake engine) failed: %v\noutput:\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(engineDir, "run.argv"))
	if err != nil {
		t.Fatalf("no container run recorded: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	n := len(argv)
	if n < 3 || argv[n-3] != "bash" || argv[n-2] != "-c" {
		t.Fatalf("test-sandbox's container run does not end in `bash -c <script>`: %q", argv)
	}
	return argv[n-1]
}

// runScript runs script with /work replaced by a temp tree and fake bun and go
// on PATH, failing the call that starts with fail. It returns the logged calls,
// stderr and the exit code.
func runScript(t *testing.T, script, fail string) (calls []string, stderr string, code int) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkg", "ts-bun-client"), 0o755); err != nil {
		t.Fatalf("make temp tree: %v", err)
	}
	toolBin := t.TempDir()
	writeShim(t, toolBin, "bun", fakeToolScript)
	writeShim(t, toolBin, "go", fakeToolScript)
	logPath := filepath.Join(t.TempDir(), "calls.log")

	c := exec.Command("bash", "-c", strings.ReplaceAll(script, "/work", root))
	c.Env = append(os.Environ(),
		"PATH="+toolBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_TOOL_LOG="+logPath, "FAKE_WORK="+root, "FAKE_FAIL="+fail,
	)
	var errBuf bytes.Buffer
	c.Stderr = &errBuf
	var exitErr *exec.ExitError
	switch err := c.Run(); {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("run recorded script: %v", err)
	}
	if data, err := os.ReadFile(logPath); err == nil {
		calls = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	return calls, errBuf.String(), code
}

// TestTestSandbox_InstallsAndBuildsTSClientFirst (b.2b3): the full suite runs
// `bun install` and `bun run build` in pkg/ts-bun-client before either suite.
func TestTestSandbox_InstallsAndBuildsTSClientFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make and shell shims")
	}
	sandboxtest.RequireTools(t, "skipping b.2b3 test-sandbox prebuild test", "make", "bash")
	script := recordedScript(t)

	install := "bun install --frozen-lockfile @ /work/pkg/ts-bun-client"
	build := "bun run build @ /work/pkg/ts-bun-client"
	goTest := "go test -timeout 7m ./... @ /work"
	bunTest := "bun test @ /work/pkg/ts-bun-client"

	for _, tc := range []struct {
		name, fail  string
		wantCalls   []string
		wantCode    int
		wantNoTests bool
	}{
		{name: "all_pass", wantCalls: []string{install, build, goTest, bunTest}},
		{name: "install_fails", fail: "bun install", wantCalls: []string{install}, wantCode: 1, wantNoTests: true},
		{name: "build_fails", fail: "bun run build", wantCalls: []string{install, build}, wantCode: 1, wantNoTests: true},
		{name: "go_test_fails_bun_test_still_runs", fail: "go test", wantCalls: []string{install, build, goTest, bunTest}, wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, stderr, code := runScript(t, script, tc.fail)
			if !slices.Equal(calls, tc.wantCalls) {
				t.Errorf("calls =\n  %s\nwant\n  %s", strings.Join(calls, "\n  "), strings.Join(tc.wantCalls, "\n  "))
			}
			if code != tc.wantCode {
				t.Errorf("exit = %d, want %d; stderr:\n%s", code, tc.wantCode, stderr)
			}
			if tc.wantNoTests && !strings.Contains(stderr, "no tests ran") {
				t.Errorf("stderr does not say %q:\n%s", "no tests ran", stderr)
			}
		})
	}
}
