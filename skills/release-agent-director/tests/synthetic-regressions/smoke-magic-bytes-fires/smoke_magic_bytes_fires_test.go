// Package smokemagibytesfires_test is a synthetic-regression test for the
// smoke per-binary magic-bytes gate (b.6oj replay scenario).
//
// BACKGROUND
// ==========
// The smoke gate's magic-bytes sub-check reads the first 4 bytes of each
// release binary and compares them against the expected ELF or Mach-O magic
// (7f454c46 / cffaedfe).  If a binary is missing or contains garbage bytes,
// the sub-check must emit outcome "failed" and the gate must exit non-zero.
//
// DESIGN
// ======
// 1. Pre-req     : requires `make release-binaries` to succeed; test is
//                  skipped (not failed) if make exits non-zero.
// 2. Mutation    : overwrite the first 4 bytes of the host-arch binary with
//                  0x00000000 — guaranteed not to match ELF or Mach-O magic.
// 3. Gate        : bash skills/release-agent-director/gates/smoke/per-binary-smoke.sh
//                  is run from repo root.
// 4. Assertions  : (a) gate exits non-zero; (b) consolidated JSON stdout
//                  contains a sub-check named smoke.<host>.magic-bytes with
//                  outcome "failed".
// 5. Cleanup     : rm -rf dist/ — binaries are ephemeral build artifacts and
//                  dist/ is .gitignore-d.
//
// SLOW TEST
// =========
// This test runs `make release-binaries` (≈10s).
// It is skipped in -short mode to keep default `go test ./...` fast.
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

// repoRoot walks up from the package working directory until it finds go.mod.
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

// smokeOutput is the consolidated JSON emitted by per-binary-smoke.sh.
type smokeOutput struct {
	PhaseName string     `json:"phase_name"`
	Outcome   string     `json:"outcome"`
	SubChecks []subCheck `json:"sub_checks"`
}

type subCheck struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
}

// TestSmokeMagicBytesFires verifies that the smoke.per-binary gate fires
// (exit != 0, JSON sub-check outcome == "failed") when the host-arch binary's
// magic bytes are zeroed out.
func TestSmokeMagicBytesFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs make release-binaries")
	}

	root := repoRoot(t)

	// ── 1. Determine host triple ────────────────────────────────────────────
	// runtime.GOOS / runtime.GOARCH already use Go naming (linux/amd64/arm64),
	// which matches the release binary naming convention.
	hostTriple := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	binaryPath := filepath.Join(root, "dist", "agent-director-"+hostTriple)

	// ── 2. Build release binaries ───────────────────────────────────────────
	makeCmd := exec.Command("make", "release-binaries")
	makeCmd.Dir = root
	makeOut, err := makeCmd.CombinedOutput()
	if err != nil {
		t.Skipf("make release-binaries failed (skipping test): %v\n%s", err, makeOut)
	}

	// ── 3. Register cleanup: remove dist/ unconditionally ──────────────────
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Join(root, "dist")); err != nil {
			t.Errorf("t.Cleanup: remove dist/: %v", err)
		}
	})

	// ── 4. Save original magic bytes and mutate ─────────────────────────────
	f, err := os.OpenFile(binaryPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open host binary %s: %v", binaryPath, err)
	}

	var origMagic [4]byte
	if _, err := f.ReadAt(origMagic[:], 0); err != nil {
		_ = f.Close()
		t.Fatalf("read magic bytes from %s: %v", binaryPath, err)
	}

	// Overwrite with all-zeros: neither ELF (7f 45 4c 46) nor Mach-O (cf fa ed fe).
	zeros := [4]byte{}
	if _, err := f.WriteAt(zeros[:], 0); err != nil {
		_ = f.Close()
		t.Fatalf("write zero magic bytes to %s: %v", binaryPath, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s after mutation: %v", binaryPath, err)
	}

	// ── 5. Run the smoke gate ───────────────────────────────────────────────
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "smoke", "per-binary-smoke.sh")
	gateCmd := exec.Command("bash", gateScript)
	gateCmd.Dir = root

	var stdoutBuf bytes.Buffer
	gateCmd.Stdout = &stdoutBuf
	gateCmd.Stderr = os.Stderr // let diagnostics surface in test output

	_ = gateCmd.Run() // non-zero exit is expected; checked below

	// ── 6. Assertions ───────────────────────────────────────────────────────
	if gateCmd.ProcessState.ExitCode() == 0 {
		t.Fatal("smoke gate should exit non-zero when host binary magic bytes are zeroed; got exit 0")
	}

	var result smokeOutput
	if err := json.Unmarshal(stdoutBuf.Bytes(), &result); err != nil {
		t.Fatalf("parse smoke gate JSON stdout: %v\nraw stdout:\n%s", err, stdoutBuf.String())
	}

	if result.Outcome != "failed" {
		t.Errorf("top-level outcome: got %q, want %q", result.Outcome, "failed")
	}

	targetName := fmt.Sprintf("smoke.%s.magic-bytes", hostTriple)
	found := false
	for _, sc := range result.SubChecks {
		if sc.Name == targetName {
			found = true
			if sc.Outcome != "failed" {
				t.Errorf("sub-check %q outcome: got %q, want %q", sc.Name, sc.Outcome, "failed")
			}
			break
		}
	}
	if !found {
		names := make([]string, len(result.SubChecks))
		for i, sc := range result.SubChecks {
			names[i] = sc.Name
		}
		t.Errorf("sub-check %q not found in JSON output; available sub-checks: %v", targetName, names)
	}

	t.Logf("smoke.%s.magic-bytes fired correctly (exit %d)", hostTriple, gateCmd.ProcessState.ExitCode())
}
