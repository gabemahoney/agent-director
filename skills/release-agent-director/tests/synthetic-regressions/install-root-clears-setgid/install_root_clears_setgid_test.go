// Package installrootclearssetgid_test is a synthetic-regression test for b.29h.
//
// BACKGROUND (b.29h incident class)
// =================================
// debian:bookworm-slim ships /tmp as mode 3777 — the setgid bit is set. A
// `mkdir` under such a setgid parent inherits the setgid bit, so
// `~/.agent-director` was created as mode 2700 rather than 700. install.sh
// then ran `chmod 0700` on the install root, but a THREE-digit chmod mode does
// NOT clear an already-set setgid bit (coreutils 9.1) — the directory stayed
// 2700. The docker-epics child epic-12-install asserts the install root has
// mode exactly 700 and therefore failed on every run in the setgid-/tmp
// measurement environment.
//
// FIX
// ===
// install.sh switched to the FOUR-digit forms `chmod 00700` (install root) and
// `chmod 00755` (bin dir); the leading extra 0 explicitly clears any inherited
// setuid/setgid bit, so the install root lands at 700.
//
// DESIGN
// ======
// Running install.sh end-to-end needs a real built binary, jq, a claude/tmux
// preflight, and would touch a real state.db — far too heavy and unsafe. So,
// following the slice-the-real-production-code convention already used by the
// fast-forward-main-worktree regression, this test extracts the exact
// "Create install root + bin dir" block from the live install.sh and sources it
// into a tiny bash harness, with DEFAULT_INSTALL_ROOT / DEFAULT_BIN_DIR derived
// from an overridden HOME exactly as install.sh derives them (install.sh:55-56).
//
// FALSIFIABILITY / GENUINE SETGID PARENT
// ======================================
// The harness's HOME is placed under a fixture parent that this test explicitly
// makes setgid (chmod g+s → 2777). A plain 0755 tmpdir would inherit nothing,
// making the mode assertion vacuously pass even with the bug present. Here the
// parent is genuinely setgid, so `mkdir` yields a 2700 install root; only the
// four-digit chmod clears the setgid bit down to 700. Reverting install.sh to
// the three-digit `chmod 0700` leaves the install root at 2700 and this test
// fails — the required regression guarantee.
package installrootclearssetgid_test

import (
	"fmt"
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
		t.Fatalf("repoRoot: os.Getwd: %v", err)
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

func installScriptPath(root string) string {
	return filepath.Join(root, "skills", "install-agent-director", "install.sh")
}

// extractCreateRootBlock slices the exact "Create install root + bin dir" block
// out of the live install.sh so the test runs the real production chmod lines.
// If the section markers are gone the script was refactored — fail loudly
// rather than silently testing nothing.
func extractCreateRootBlock(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(installScriptPath(root))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	src := string(data)

	const startMarker = "# Create install root + bin dir"
	start := strings.Index(src, startMarker)
	if start < 0 {
		t.Fatalf("marker %q not found — update if the install-root section was renamed", startMarker)
	}
	// The block ends at the next section divider comment (the atomic-install
	// section begins with "# Atomic install:").
	const endMarker = "# Atomic install:"
	end := strings.Index(src[start:], endMarker)
	if end < 0 {
		t.Fatalf("end marker %q not found after the install-root section — update if install.sh was reordered", endMarker)
	}
	block := src[start : start+end]

	// Sanity: the block must actually run chmod on the install root; if it
	// doesn't, the extraction is wrong and the mode assertion would be vacuous.
	if !strings.Contains(block, "chmod") || !strings.Contains(block, "DEFAULT_INSTALL_ROOT") {
		t.Fatalf("extracted block does not chmod DEFAULT_INSTALL_ROOT — extraction markers are stale:\n%s", block)
	}
	return block
}

// makeSetgidHome creates a genuinely setgid parent directory and, beneath it, a
// HOME dir. Returns the HOME path. It verifies the setgid bit actually stuck
// (some filesystems can strip it) so the test can't silently degrade into a
// vacuous 0755-parent run.
func makeSetgidHome(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	parent := filepath.Join(base, "setgid-parent")
	if err := os.Mkdir(parent, 0o777); err != nil {
		t.Fatalf("mkdir setgid parent: %v", err)
	}
	// os.Chmod applies only the low 9 permission bits on some platforms; use the
	// chmod binary with an explicit g+s so the setgid bit is set regardless.
	if out, err := exec.Command("chmod", "g+s", parent).CombinedOutput(); err != nil {
		t.Fatalf("chmod g+s parent: %v\n%s", err, out)
	}
	// Confirm the parent is genuinely setgid — otherwise the fixture guarantees
	// nothing and the test would be vacuous.
	if mode := statMode(t, parent); mode&os.ModeSetgid == 0 {
		t.Fatalf("setgid parent %s did not retain the setgid bit (mode %o); "+
			"the underlying filesystem strips setgid — this test cannot run here", parent, mode)
	}

	home := filepath.Join(parent, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatalf("mkdir home under setgid parent: %v", err)
	}
	return home
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode()
}

// TestInstallRootClearsInheritedSetgid runs the real install.sh install-root
// creation block with HOME under a genuinely setgid parent and asserts the
// resulting ~/.agent-director is mode exactly 700 with no setgid bit (b.29h).
func TestInstallRootClearsInheritedSetgid(t *testing.T) {
	root := repoRoot(t)
	block := extractCreateRootBlock(t, root)
	home := makeSetgidHome(t)

	// Reproduce install.sh's own derivation of the install paths from HOME
	// (install.sh:55-56), then run the extracted production block verbatim.
	harness := "set -euo pipefail\n" +
		`DEFAULT_INSTALL_ROOT="${HOME}/.agent-director"` + "\n" +
		`DEFAULT_BIN_DIR="${DEFAULT_INSTALL_ROOT}/bin"` + "\n" +
		block + "\n"

	cmd := exec.Command("bash", "-c", harness)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running install-root block failed: %v\n%s", err, out)
	}

	installRoot := filepath.Join(home, ".agent-director")
	binDir := filepath.Join(installRoot, "bin")

	// The install root must be exactly 700: rwx for owner, nothing else, and
	// crucially NO setgid bit inherited from the setgid parent.
	assertPerm(t, installRoot, 0o700)
	assertPerm(t, binDir, 0o755)
}

// assertPerm fails unless path's mode is exactly want, considering the setuid,
// setgid, sticky, and permission bits — so an inherited setgid bit (mode 2700)
// is caught even though its low 9 bits match a bare 700.
func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	const specialAndPerm = os.ModeSetuid | os.ModeSetgid | os.ModeSticky | os.ModePerm
	got := fi.Mode() & specialAndPerm
	if got != want {
		t.Fatalf("%s: mode = %s (%s), want %s (%s) — an inherited setgid/setuid/sticky bit was not cleared (b.29h)",
			path, octal(got), got.String(), octal(want), want.String())
	}
}

// octal renders the special+permission bits as a 4-digit octal string for
// readable failure messages (e.g. 2700 vs 0700).
func octal(m os.FileMode) string {
	var special int
	if m&os.ModeSetuid != 0 {
		special |= 0o4000
	}
	if m&os.ModeSetgid != 0 {
		special |= 0o2000
	}
	if m&os.ModeSticky != 0 {
		special |= 0o1000
	}
	return fmt.Sprintf("%04o", special|int(m&os.ModePerm))
}
