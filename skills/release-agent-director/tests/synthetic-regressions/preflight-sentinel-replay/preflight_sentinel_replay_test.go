// Package preflightsentinelreplay_test is a synthetic-regression test for
// preflight.invariant-source-of-truth.
//
// BACKGROUND (AC r3.kx — E10 retirement anchor)
// ===============================================
// Before SR-16, multiple files in the repo each hard-coded version strings
// independently.  SR-16 designates pkg/ts-bun-client/package.json as the
// single authoritative version site and enforces it via the
// check-source-of-truth.ts gate (invoked by preflight.invariant-source-of-truth).
// This test re-anchors the legacy test-preflight-sentinel.sh contract: it
// proves the preflight shell gate fires when a stray package.json with a
// "version" field appears in the tools/ subtree.  It is the synthetic-regression
// counted in the E10 retirement table.
//
// DESIGN
// ======
// 1. Stage a throwaway git repo under t.TempDir() holding a copy of
//    check-source-of-truth.ts and the authoritative package.json, plus a
//    stray tools/_sentinel-test/package.json with
//    {"name":"sentinel-test","version":"0.0.0"}.  Any package.json with a
//    "version" field outside the designated canonical location triggers SR-16.
//    The sentinel never enters the real tree, where a sibling test or release
//    gate scanning it would see it (b.9qj).
// 2. Run the real wrapper,
//    bash skills/release-agent-director/gates/preflight/invariant-source-of-truth.sh,
//    from the staged repo's root: it resolves the repo with
//    `git rev-parse --show-toplevel` and runs the delegate there.
// 3. Assert exit code != 0.
// 4. Assert stderr contains "preflight.invariant-source-of-truth" (the
//    wrapper's own gate key) AND the sentinel file path (emitted by the
//    underlying SR-16 script).
//
// DEPENDENCY
// ==========
// Requires `bun` and `git` in PATH; skips gracefully if either is absent.
package preflightsentinelreplay_test

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
}

// writeFile writes content to rel (slash-separated) under dir, creating
// parent directories.
func writeFile(t *testing.T, dir, rel string, content []byte) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// gateRepo stages a git repo under t.TempDir() holding the real gate script and
// the authoritative package.json, and returns its root (b.9qj).
func gateRepo(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	dir := t.TempDir()
	for _, rel := range []string{
		"pkg/ts-bun-client/scripts/check-source-of-truth.ts",
		"pkg/ts-bun-client/package.json",
	} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		writeFile(t, dir, rel, data)
	}
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
	return dir
}

// TestPreflightSentinelReplay verifies that preflight.invariant-source-of-truth
// fires when a stray versioned package.json appears in the tools/ subtree
// (AC r3.kx — E10 retirement anchor).
func TestPreflightSentinelReplay(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not in PATH — skipping invariant-source-of-truth gate test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH — skipping invariant-source-of-truth gate test")
	}

	// 1. Stage the repo and plant the sentinel package.json.
	gateRoot := gateRepo(t)
	relSentinel := filepath.Join("tools", "_sentinel-test", "package.json")
	writeFile(t, gateRoot, relSentinel, []byte(`{"name":"sentinel-test","version":"0.0.0"}`+"\n"))

	// 2. Run the preflight gate wrapper (which in turn calls check-source-of-truth.ts).
	gateScript := filepath.Join(repoRoot(t), "skills", "release-agent-director", "gates", "preflight", "invariant-source-of-truth.sh")
	cmd := exec.Command("bash", gateScript)
	cmd.Dir = gateRoot
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit expected — ignore error

	// 3. Assert non-zero exit.
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("invariant-source-of-truth gate exited 0 — expected non-zero with sentinel file %s present", relSentinel)
	}

	stderr := stderrBuf.String()

	// 4a. Assert the wrapper's gate key is present.
	const wrapperKey = "preflight.invariant-source-of-truth"
	if !strings.Contains(stderr, wrapperKey) {
		t.Fatalf("gate stderr does not contain wrapper key %q;\nstderr:\n%s", wrapperKey, stderr)
	}

	// 4b. Assert the sentinel file path is named in the output.
	//     The SR-16 script emits the relative path of offending files.
	if !strings.Contains(stderr, relSentinel) {
		t.Fatalf("gate stderr does not name the sentinel file %q;\nstderr:\n%s", relSentinel, stderr)
	}

	// 5. Find and parse the wrapper's own JSON line to validate the gate field.
	var wrapperLine string
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if strings.Contains(line, wrapperKey) {
			wrapperLine = line
			break
		}
	}
	if wrapperLine == "" {
		t.Fatalf("could not find JSON line containing %q;\nstderr:\n%s", wrapperKey, stderr)
	}
	var diag map[string]interface{}
	if err := json.Unmarshal([]byte(wrapperLine), &diag); err != nil {
		t.Fatalf("wrapper stderr line is not valid JSON: %v\nline: %s", err, wrapperLine)
	}
	if diag["gate"] != wrapperKey {
		t.Fatalf("JSON gate=%q, want %q", diag["gate"], wrapperKey)
	}

	t.Logf("AC r3.kx verified: invariant-source-of-truth gate fired on sentinel file.\nSentinel: %s\nWrapper line: %s",
		relSentinel, wrapperLine)
}
