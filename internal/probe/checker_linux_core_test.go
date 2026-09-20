//go:build linux

package probe

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

const linuxTestID = "inst-linux-xyz"

// writeStatOnly writes <root>/<pid>/stat only (no environ) with the given
// starttime — used to fabricate an environ-read failure (ENOENT) after a matched
// stat, and as the base for permission-mode fixtures.
func writeStatOnly(t *testing.T, root string, pid, ppid int, starttime string) string {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	fields := []string{
		strconv.Itoa(pid),      // 1 pid
		"(claude (weird) proc)", // 2 comm w/ ')' and spaces
		"S",                     // 3 state
		strconv.Itoa(ppid),      // 4 ppid
	}
	for f := 5; f <= 21; f++ {
		fields = append(fields, "0")
	}
	fields = append(fields, starttime, "0", "0") // 22 starttime + tail
	stat := ""
	for i, f := range fields {
		if i > 0 {
			stat += " "
		}
		stat += f
	}
	stat += "\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	return dir
}

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
