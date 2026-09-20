// checker_linux_core_test.go — UNTAGGED verdict-table tests for the build-tag-free
// linuxChecker. The checker takes an INJECTABLE procRoot and its parsing/verdict
// logic carries no //go:build linux tag, so these tests drive it over a
// fabricated /proc tree (writeFakeProc / writeStatOnly, in the untagged
// fakeproc_test.go) and compile on ANY OS — verified by
// `GOOS=darwin go vet ./internal/probe/`.
//
// The two chmod-000 EACCES cases are ALSO untagged: chmod 000 produces a genuine
// EACCES on any unix at uid!=0, the fabricated tree is OS-agnostic, and the
// checker is tag-free — so they need no linux tag. They skip cleanly under root
// (where 000 does not deny). The ONLY test requiring the real /proc mount lives
// under the linux tag in resolver_linux_test.go (TestLinuxResolverRealProcHappyPath).

package probe

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

const linuxTestID = "inst-linux-xyz"

// TestLinuxCheckerMatchEnvHasIDAlive: stat present with matching starttime AND
// environ carries the id → verified-alive.
func TestLinuxCheckerMatchEnvHasIDAlive(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, 100, 1, procstarttimefix.LinuxProcStarttime, linuxTestID)
	c := linuxChecker{procRoot: root}
	if got := c.CheckLiveness(100, procstarttimefix.LinuxProcStarttime, linuxTestID); got != VerdictVerifiedAlive {
		t.Errorf("verdict = %v; want verified-alive", got)
	}
}

// TestLinuxCheckerMatchEnvLacksIDDead: stat matches, environ readable but LACKS
// the id → provably-dead (SR-7.3 tiebreaker). writeFakeProc with envVal=="" plants
// an environ with only unrelated vars.
func TestLinuxCheckerMatchEnvLacksIDDead(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, 100, 1, procstarttimefix.LinuxProcStarttime, "")
	c := linuxChecker{procRoot: root}
	if got := c.CheckLiveness(100, procstarttimefix.LinuxProcStarttime, linuxTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (environ lacks id tiebreaker)", got)
	}
}

// TestLinuxCheckerStarttimeMismatchDead: stat present but field-22 starttime
// differs from stored → pid reuse → provably-dead.
func TestLinuxCheckerStarttimeMismatchDead(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, 100, 1, "99999999", linuxTestID) // != LinuxProcStarttime
	c := linuxChecker{procRoot: root}
	if got := c.CheckLiveness(100, procstarttimefix.LinuxProcStarttime, linuxTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (starttime mismatch)", got)
	}
}

// TestLinuxCheckerAbsentPIDDead: no <root>/<pid> dir → stat ENOENT → dispGone →
// provably-dead.
func TestLinuxCheckerAbsentPIDDead(t *testing.T) {
	root := t.TempDir() // empty tree, pid 100 absent
	c := linuxChecker{procRoot: root}
	if got := c.CheckLiveness(100, procstarttimefix.LinuxProcStarttime, linuxTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (absent pid dir)", got)
	}
}

// TestLinuxCheckerStatEACCESUnknown: stat file mode-000 → a REAL EACCES from the
// kernel (the sandbox runs as uid 1000, so 000 is unreadable) → dispPermission →
// VerdictUnknown. Skipped when running as root (root bypasses the permission
// bits, so the fixture would not produce EACCES).
func TestLinuxCheckerStatEACCESUnknown(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode-000 does not produce EACCES")
	}
	root := t.TempDir()
	dir := writeStatOnly(t, root, 100, 1, procstarttimefix.LinuxProcStarttime)
	if err := os.Chmod(filepath.Join(dir, "stat"), 0o000); err != nil {
		t.Fatalf("chmod stat 000: %v", err)
	}
	c := linuxChecker{procRoot: root}
	if got := c.CheckLiveness(100, procstarttimefix.LinuxProcStarttime, linuxTestID); got != VerdictUnknown {
		t.Errorf("verdict = %v; want unknown (stat EACCES)", got)
	}
}

// TestLinuxCheckerEnvironEACCESAfterMatchAlive: stat present + matching starttime,
// but environ is mode-000 → REAL EACCES on the environ read → verified-alive
// (starttime already proved the process live). Skipped as root.
func TestLinuxCheckerEnvironEACCESAfterMatchAlive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode-000 does not produce EACCES")
	}
	root := t.TempDir()
	writeFakeProc(t, root, 100, 1, procstarttimefix.LinuxProcStarttime, linuxTestID)
	environPath := filepath.Join(root, "100", "environ")
	if err := os.Chmod(environPath, 0o000); err != nil {
		t.Fatalf("chmod environ 000: %v", err)
	}
	c := linuxChecker{procRoot: root}
	if got := c.CheckLiveness(100, procstarttimefix.LinuxProcStarttime, linuxTestID); got != VerdictVerifiedAlive {
		t.Errorf("verdict = %v; want verified-alive (environ EACCES after starttime match)", got)
	}
}

// TestLinuxCheckerEnvironAbsentAfterMatchDead: stat present + matching starttime,
// but the environ file is absent → ENOENT on the environ read → dispGone →
// provably-dead (the process exited between the stat and environ reads).
func TestLinuxCheckerEnvironAbsentAfterMatchDead(t *testing.T) {
	root := t.TempDir()
	writeStatOnly(t, root, 100, 1, procstarttimefix.LinuxProcStarttime) // no environ
	c := linuxChecker{procRoot: root}
	if got := c.CheckLiveness(100, procstarttimefix.LinuxProcStarttime, linuxTestID); got != VerdictProvablyDead {
		t.Errorf("verdict = %v; want provably-dead (environ vanished after stat)", got)
	}
}

// TestLinuxCheckerMalformedStatUnknown: a stat line with no ')' delimiter fails
// parseLinuxStat. A malformed stat is NOT evidence of death → VerdictUnknown.
func TestLinuxCheckerMalformedStatUnknown(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "100")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte("garbage without a paren\n"), 0o644); err != nil {
		t.Fatalf("write malformed stat: %v", err)
	}
	c := linuxChecker{procRoot: root}
	if got := c.CheckLiveness(100, procstarttimefix.LinuxProcStarttime, linuxTestID); got != VerdictUnknown {
		t.Errorf("verdict = %v; want unknown (malformed stat, never dead)", got)
	}
}
