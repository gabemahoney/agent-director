// Package coveragegorootfires_test is a synthetic-regression test for the
// coverage.go-root gate (b.wvr coverage phase).
//
// BACKGROUND
// ==========
// The coverage.go-root gate runs `go test ./... -race -count=1` at the repo
// root.  A compile-time bug (e.g. a wrong-arity call to an apitest helper)
// must cause the gate to exit non-zero and emit a structured SR-14 JSON
// diagnostic to stderr with "gate":"coverage.go-root".  This test injects
// exactly that class of defect and verifies the gate fires.
//
// DESIGN
// ======
// 1. Target file : pkg/api/apitest/seeds.go — same file used in the
//    helper-tag-replay regression, but here we exercise the coverage gate
//    path rather than `go build ./...` directly.
// 2. Mutation    : insert `_ = SeedSpawn("only-one-arg")` at the top of the
//    InitStore function body.  SeedSpawn requires 7 string args plus a bool;
//    a single-string call is a compile error that `go test ./...` must catch.
// 3. Gate        : bash skills/release-agent-director/gates/coverage/go-root.sh
//    is run from repo root.  The gate's stderr must contain the JSON
//    "gate":"coverage.go-root" diagnostic on failure.
// 4. Cleanup     : original bytes are captured before mutation; t.Cleanup
//    restores them unconditionally even when t.Fatalf fires mid-test.
// 5. Post-cleanup: after the test, `git status seeds.go` must be clean.
//
// SLOW TEST
// =========
// This test runs the full `go test ./... -race -count=1` suite (≈45s).
// It is skipped in -short mode to keep default `go test ./...` fast.
package coveragegorootfires_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestCoverageGoRootFires verifies that coverage.go-root fires (exit != 0,
// stderr contains "gate":"coverage.go-root") when the repo contains a
// wrong-arity call that fails compilation.
func TestCoverageGoRootFires(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs full coverage suite (go test ./... -race -count=1)")
	}

	root := repoRoot(t)
	targetFile := filepath.Join(root, "pkg", "api", "apitest", "seeds.go")

	// ── 1. Read original bytes ──────────────────────────────────────────────
	orig, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read seeds.go: %v", err)
	}
	origStat, err := os.Stat(targetFile)
	if err != nil {
		t.Fatalf("stat seeds.go: %v", err)
	}

	// ── 2. Register cleanup BEFORE mutating ────────────────────────────────
	t.Cleanup(func() {
		if err := os.WriteFile(targetFile, orig, origStat.Mode()); err != nil {
			t.Errorf("t.Cleanup: restore seeds.go: %v", err)
		}
	})

	// ── 3. Inject mutation ─────────────────────────────────────────────────
	// Insert a wrong-arity call to SeedSpawn at the top of InitStore's body.
	// Real signature: SeedSpawn(dbPath, id, state, cwd, relayMode, sessionID string, createStore bool)
	const marker = "func InitStore(dbPath string) (string, error) {\n\ts, err := store.OpenOrInit(dbPath)"
	const mutated = "func InitStore(dbPath string) (string, error) {\n" +
		"\t_ = SeedSpawn(\"only-one-arg\") // wrong-arity: SeedSpawn needs 7 args + bool (coverage.go-root regression)\n" +
		"\ts, err := store.OpenOrInit(dbPath)"

	if !bytes.Contains(orig, []byte(marker)) {
		t.Fatalf("mutation marker not found in seeds.go — update the marker if InitStore was refactored")
	}

	mutated2 := bytes.Replace(orig, []byte(marker), []byte(mutated), 1)
	if err := os.WriteFile(targetFile, mutated2, origStat.Mode()); err != nil {
		t.Fatalf("write mutated seeds.go: %v", err)
	}

	// ── 4. Run coverage.go-root gate ───────────────────────────────────────
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "coverage", "go-root.sh")
	cmd := exec.Command("bash", gateScript)
	cmd.Dir = root
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	// stdout flows to the test log for progress visibility
	cmd.Stdout = os.Stdout
	_ = cmd.Run() // non-zero exit is expected — ignore the returned error

	stderr := stderrBuf.String()

	// ── 5. Assertions ──────────────────────────────────────────────────────
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("expected coverage.go-root gate to exit non-zero after wrong-arity mutation, but it exited 0")
	}

	const gateKey = `"gate":"coverage.go-root"`
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	t.Logf("coverage.go-root fired correctly (exit %d).\nGate stderr: %s", cmd.ProcessState.ExitCode(), stderr)
}
