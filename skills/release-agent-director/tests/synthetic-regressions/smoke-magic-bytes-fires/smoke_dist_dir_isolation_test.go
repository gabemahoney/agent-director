// Regression guard for b.aur: per-binary-smoke.sh must read release binaries
// from the directory named by SMOKE_DIST_DIR, not the hardcoded repo-root
// dist/. Before the b.aur fix the gate ignored the env var and always looked in
// dist/, so release synthetic-regression tests raced on that shared directory.
//
// This is a fully deterministic before/after guard (no `make` build): we plant
// a fake host binary with valid ELF magic in an isolated SMOKE_DIST_DIR and
// assert the gate's magic-bytes sub-check reports "passed" for the host triple.
// A gate that hardcodes dist/ (pre-fix) finds no such file and reports the
// magic-bytes sub-check as "failed" (file not found) — so this test fails
// before the fix and passes after.
package smokemagibytesfires_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSmokeHonorsDistDir verifies per-binary-smoke.sh reads from SMOKE_DIST_DIR.
func TestSmokeHonorsDistDir(t *testing.T) {
	root := repoRoot(t)

	hostTriple := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)

	// Only linux/amd64 and linux/arm64 use ELF magic (7f454c46); darwin-arm64
	// uses Mach-O. The gate's magic-bytes check keys off the file's real first
	// four bytes, so plant the correct magic for the host.
	var magic []byte
	switch runtime.GOOS {
	case "linux":
		magic = []byte{0x7f, 0x45, 0x4c, 0x46} // ELF
	case "darwin":
		magic = []byte{0xcf, 0xfa, 0xed, 0xfe} // Mach-O
	default:
		t.Skipf("no known magic for host OS %q", runtime.GOOS)
	}

	// Plant a fake host binary in an isolated dist dir. Only the host binary's
	// magic-bytes sub-check needs to pass; the other two platforms' binaries
	// may be absent (that only affects their own sub-checks, not the host's).
	distDir := t.TempDir()
	hostBinary := filepath.Join(distDir, "agent-director-"+hostTriple)
	if err := os.WriteFile(hostBinary, magic, 0o755); err != nil {
		t.Fatalf("plant fake host binary: %v", err)
	}

	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "smoke", "per-binary-smoke.sh")
	gateCmd := exec.Command("bash", gateScript)
	gateCmd.Dir = root
	gateCmd.Env = append(os.Environ(), "SMOKE_DIST_DIR="+distDir)

	var stdoutBuf bytes.Buffer
	gateCmd.Stdout = &stdoutBuf
	gateCmd.Stderr = os.Stderr
	_ = gateCmd.Run() // overall exit may be non-zero (sibling binaries absent)

	var result smokeOutput
	if err := json.Unmarshal(stdoutBuf.Bytes(), &result); err != nil {
		t.Fatalf("parse smoke gate JSON stdout: %v\nraw stdout:\n%s", err, stdoutBuf.String())
	}

	// The host magic-bytes sub-check must be "passed" — provable only if the
	// gate read our planted binary from SMOKE_DIST_DIR. Pre-fix it looked in
	// repo-root dist/, found nothing, and marked it "failed".
	targetName := fmt.Sprintf("smoke.%s.magic-bytes", hostTriple)
	found := false
	for _, sc := range result.SubChecks {
		if sc.Name == targetName {
			found = true
			if sc.Outcome != "passed" {
				t.Errorf("sub-check %q outcome: got %q, want %q — gate did not read the "+
					"planted binary from SMOKE_DIST_DIR=%s (b.aur)", sc.Name, sc.Outcome, "passed", distDir)
			}
			break
		}
	}
	if !found {
		names := make([]string, len(result.SubChecks))
		for i, sc := range result.SubChecks {
			names[i] = sc.Name
		}
		t.Errorf("sub-check %q not found in JSON output; available: %v", targetName, names)
	}
}
