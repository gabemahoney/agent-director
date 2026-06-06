// Package existingtag_test is a synthetic-regression test for preflight.version-novelty-tag.
//
// BACKGROUND (AC r3.yh)
// =====================
// The preflight.version-novelty-tag gate prevents re-releasing a version that
// has already been tagged on origin.  Re-tagging the same version would
// overwrite or conflict with the published artifact and confuse downstream
// consumers.  This test proves the gate fires when the requested version
// matches an existing remote tag.
//
// DESIGN
// ======
// 1. Query origin for existing tags via `git ls-remote --tags origin`.
//    Skip gracefully if `git` is absent or if origin has no tags yet.
// 2. Extract the first plain tag ref (excluding peeled ^{} entries) and
//    strip the leading "v" to get a bare version number.
// 3. Invoke bash skills/release-agent-director/gates/preflight/version-novelty-tag.sh
//    <existing-version> from the repo root.
// 4. Assert exit code != 0.
// 5. Assert stderr JSON has "gate":"preflight.version-novelty-tag".
//
// This is a READ-ONLY test — no repo mutations are made and no cleanup is
// required.
//
// DEPENDENCY
// ==========
// Requires `git` in PATH and network access to origin; skips gracefully if
// either is unavailable or if origin has no tags.
package existingtag_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"os"
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

// firstRemoteTag queries origin for tags and returns the bare version number
// (without the leading "v") of the first tag found.  Calls t.Skip if git
// is unavailable, origin is unreachable, or no tags exist.
func firstRemoteTag(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("git", "ls-remote", "--tags", "origin")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git ls-remote --tags origin failed (%v) — skipping existing-tag test", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// Skip peeled dereference entries (e.g. refs/tags/v0.1.0^{}) and
		// non-version refs.
		if strings.Contains(line, "^{}") || !strings.Contains(line, "refs/tags/v") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		tag := strings.TrimPrefix(parts[1], "refs/tags/")
		return strings.TrimPrefix(tag, "v")
	}
	t.Skip("no tags found on origin — skipping existing-tag test")
	return "" // unreachable
}

// TestExistingTag verifies that preflight.version-novelty-tag fires when the
// requested version already exists as a tag on origin (AC r3.yh).
func TestExistingTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH — skipping version-novelty-tag gate test")
	}

	root := repoRoot(t)
	existingVersion := firstRemoteTag(t, root)

	// Run the gate with the known-existing version.
	gateScript := filepath.Join(root, "skills", "release-agent-director", "gates", "preflight", "version-novelty-tag.sh")
	cmd := exec.Command("bash", gateScript, existingVersion)
	cmd.Dir = root
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero exit expected — ignore error

	// Assert non-zero exit.
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("version-novelty-tag gate exited 0 for existing tag v%s — expected non-zero", existingVersion)
	}

	stderr := stderrBuf.String()

	// Assert gate key present in stderr.
	const gateKey = "preflight.version-novelty-tag"
	if !strings.Contains(stderr, gateKey) {
		t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
	}

	// Parse the first stderr line as JSON and validate the gate field.
	firstLine := strings.SplitN(strings.TrimSpace(stderr), "\n", 2)[0]
	var diag map[string]interface{}
	if err := json.Unmarshal([]byte(firstLine), &diag); err != nil {
		t.Fatalf("first stderr line is not valid JSON: %v\nline: %s", err, firstLine)
	}
	if diag["gate"] != gateKey {
		t.Fatalf("JSON gate=%q, want %q", diag["gate"], gateKey)
	}

	t.Logf("AC r3.yh verified: version-novelty-tag gate fired for existing tag v%s.\nSample stderr: %s",
		existingVersion, firstLine)
}
