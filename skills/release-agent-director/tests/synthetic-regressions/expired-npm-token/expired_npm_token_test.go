// Package expirednpmtoken_test is a synthetic-regression test for preflight.npm-whoami.
//
// BACKGROUND (AC r3.r4)
// =====================
// The preflight.npm-whoami gate verifies that the configured npm token is valid
// against the registry before a publish is attempted.  Without this gate a
// release could begin and then fail mid-flight when npm publish rejects the
// expired credential.  This test proves the gate fires when an invalid token is
// in effect.
//
// DESIGN
// ======
// 1. Write a temp .npmrc containing a clearly-invalid auth token to a
//    t.TempDir() path.
// 2. Pass the path via NPM_CONFIG_USERCONFIG in the subprocess environment
//    so `npm whoami` uses that config instead of the developer's ~/.npmrc.
//    This approach is preferred over mutating NPM_TOKEN because the system's
//    ~/.npmrc may override it via the npmrc cascade.
// 3. Run bash skills/release-agent-director/gates/preflight/npm-whoami.sh.
// 4. Assert exit code != 0.
// 5. Assert stderr JSON has "gate":"preflight.npm-whoami".
//
// The subprocess env is built explicitly from os.Environ(), with
// NPM_CONFIG_USERCONFIG overridden, so the parent process environment is
// never mutated and no t.Cleanup is needed for env restoration.
//
// DEPENDENCY
// ==========
// Requires `npm` in PATH; skips gracefully if absent.
package expirednpmtoken_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the package's working directory (set by `go test` to
// the package directory) until it finds a go.mod file.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRoot: could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
	panic("unreachable")
}

// TestExpiredNpmToken verifies that preflight.npm-whoami fires when the
// configured npm token is invalid (AC r3.r4).
func TestExpiredNpmToken(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not in PATH — skipping npm-whoami gate test")
	}

	root := repoRoot(t)

	// 1. Write a temp .npmrc with an invalid token to a directory that
	//    t.TempDir() will clean up after the test.
	badRc := filepath.Join(t.TempDir(), ".npmrc")
	if err := os.WriteFile(badRc, []byte("//registry.npmjs.org/:_authToken=invalid-regression-token-9999\n"), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", badRc, err)
	}

	// 2. Build a subprocess environment that overrides NPM_CONFIG_USERCONFIG
	//    without mutating the parent process environment.  Strip any existing
	//    NPM_CONFIG_USERCONFIG so the override is unambiguous.
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "NPM_CONFIG_USERCONFIG=") {
			env = append(env, e)
		}
	}
	env = append(env, "NPM_CONFIG_USERCONFIG="+badRc)

	// 3. Run the gate.
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "preflight", "npm-whoami.sh")
	cmd := exec.Command("bash", gateScript)
	cmd.Dir = root
	cmd.Env = env
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit expected — ignore error

	// 4. Assert non-zero exit.
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("npm-whoami gate exited 0 — expected non-zero with invalid token in %s", badRc)
	}

	stderr := stderrBuf.String()

	// 5. Assert gate key present in stderr.
	const gateKey = "preflight.npm-whoami"
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	// 6. Parse the first stderr line as JSON and validate the gate field.
	firstLine := strings.SplitN(strings.TrimSpace(stderr), "\n", 2)[0]
	var diag map[string]interface{}
	if err := json.Unmarshal([]byte(firstLine), &diag); err != nil {
		t.Fatalf("first stderr line is not valid JSON: %v\nline: %s", err, firstLine)
	}
	if diag["gate"] != gateKey {
		t.Fatalf("JSON gate=%q, want %q", diag["gate"], gateKey)
	}

	t.Logf("AC r3.r4 verified: npm-whoami gate fired on invalid token.\nBad npmrc: %s\nSample stderr: %s",
		badRc, firstLine)
}
