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
// 1. Create a temporary directory tools/_sentinel-test-{pid}/ and write a
//    minimal package.json with {"name":"sentinel-test","version":"0.0.0"} into
//    it.  Any package.json with a "version" field outside the designated
//    canonical location triggers SR-16.
// 2. Register t.Cleanup to os.RemoveAll the temp directory BEFORE any
//    assertion.
// 3. Run bash skills/release-agent-director/gates/preflight/invariant-source-of-truth.sh
//    from the repo root; capture stderr.
// 4. Assert exit code != 0.
// 5. Assert stderr contains "preflight.invariant-source-of-truth" (the
//    wrapper's own gate key) AND the sentinel file path (emitted by the
//    underlying SR-16 script).
//
// CLEANUP-ON-FAILURE PATTERN
// ==========================
// t.Cleanup is registered before assertions so the sentinel directory is
// removed unconditionally even if t.Fatalf fires.  After the run,
// `git status` must show a clean tree.
//
// DEPENDENCY
// ==========
// Requires `bun` and `git` in PATH; skips gracefully if either is absent.
package preflightsentinelreplay_test

import (
	"encoding/json"
	"fmt"
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

	root := repoRoot(t)

	// 1. Create the sentinel directory and package.json.
	//    The suffix makes the path unique per run and avoids collisions when
	//    tests run in parallel.
	suffix := fmt.Sprintf("%d", os.Getpid())
	sentinelDir := filepath.Join(root, "tools", "_sentinel-test-"+suffix)
	sentinelPkg := filepath.Join(sentinelDir, "package.json")

	if err := os.MkdirAll(sentinelDir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", sentinelDir, err)
	}

	// 2. Register cleanup BEFORE writing or asserting.  t.Cleanup fires
	//    unconditionally even when t.Fatalf / runtime.Goexit is called.
	t.Cleanup(func() {
		if err := os.RemoveAll(sentinelDir); err != nil {
			t.Errorf("t.Cleanup: RemoveAll %s: %v", sentinelDir, err)
		}
	})

	pkgContent := `{"name":"sentinel-test","version":"0.0.0"}` + "\n"
	if err := os.WriteFile(sentinelPkg, []byte(pkgContent), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", sentinelPkg, err)
	}

	// 3. Run the preflight gate wrapper (which in turn calls check-source-of-truth.ts).
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "preflight", "invariant-source-of-truth.sh")
	cmd := exec.Command("bash", gateScript)
	cmd.Dir = root
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit expected — ignore error

	// 4. Assert non-zero exit.
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("invariant-source-of-truth gate exited 0 — expected non-zero with sentinel file %s present", sentinelPkg)
	}

	stderr := stderrBuf.String()

	// 5a. Assert the wrapper's gate key is present.
	const wrapperKey = "preflight.invariant-source-of-truth"
	if !strings.Contains(stderr, wrapperKey) {
		t.Fatalf("gate stderr does not contain wrapper key %q;\nstderr:\n%s", wrapperKey, stderr)
	}

	// 5b. Assert the sentinel file path is named in the output.
	//     The SR-16 script emits the relative path of offending files.
	relSentinel := filepath.Join("tools", "_sentinel-test-"+suffix, "package.json")
	if !strings.Contains(stderr, relSentinel) {
		t.Fatalf("gate stderr does not name the sentinel file %q;\nstderr:\n%s", relSentinel, stderr)
	}

	// 6. Find and parse the wrapper's own JSON line to validate the gate field.
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
		sentinelPkg, wrapperLine)
}
