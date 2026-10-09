// starttime_linux_core_test.go — UNTAGGED answer-table tests for the build-tag-free
// linuxStartTimeReader, driven over a fabricated t.TempDir() proc root (never the
// real /proc) so they compile and run on any OS. Cases that need a real EACCES skip
// under root.

package probe

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
)

// chmodLinuxStartTime sets mode on path and restores 0o755 at cleanup so t.TempDir can remove it.
func chmodLinuxStartTime(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s %o: %v", path, mode, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

// symlinkProcSelf plants <root>/self -> target, the mounted-procfs witness the
// reader requires before an absent pid entry reads gone.
func symlinkProcSelf(t *testing.T, root, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(root, "self")); err != nil {
		t.Fatalf("symlink self -> %s: %v", target, err)
	}
}

// TestLinuxStartTimeReader covers the alive / gone / unreadable answers, each as a
// full (start, alive, known) triple; start is empty on every answer that is not alive.
func TestLinuxStartTimeReader(t *testing.T) {
	const pid = 100
	start := procstarttimefix.LinuxProcStarttime

	type answer struct {
		start        string
		alive, known bool
	}
	alive := answer{start, true, true}
	gone := answer{"", false, true}
	unreadable := answer{"", false, false}

	// stat plants pid's stat with state and starttime; raw plants a verbatim line.
	stat := func(state, st string) func(t *testing.T, root string) string {
		return func(t *testing.T, root string) string { writeStatWithState(t, root, pid, 1, state, st); return root }
	}
	raw := func(line string) func(t *testing.T, root string) string {
		return func(t *testing.T, root string) string { writeProcFile(t, root, pid, "stat", line); return root }
	}
	cases := []struct {
		name    string
		nonRoot bool // needs a real EACCES; skipped as root
		// setup fabricates the tree under root and returns the procRoot to read.
		setup func(t *testing.T, root string) string
		want  answer
	}{
		{name: "alive state S returns field 22 verbatim", setup: stat("S", start), want: alive},
		{name: "alive state R returns field 22 verbatim", setup: stat("R", start), want: alive},
		{name: "uninterruptible sleep D is alive", setup: stat("D", start), want: alive},
		{name: "stopped T is alive", setup: stat("T", start), want: alive},
		{name: "alive keeps leading zeros verbatim", setup: stat("S", "000123"), want: answer{"000123", true, true}},
		{name: "zombie Z is gone", setup: stat("Z", start), want: gone},
		{name: "dead X is gone", setup: stat("X", start), want: gone},
		{name: "pid dir absent under readable root is gone", setup: func(t *testing.T, root string) string {
			writeStatWithState(t, root, pid+1, 1, "S", start) // a neighbour, not pid
			symlinkProcSelf(t, root, "101")                   // mounted-procfs witness
			return root
		}, want: gone},
		{name: "pid dir absent with self a plain directory is gone", setup: func(t *testing.T, root string) string {
			if err := os.Mkdir(filepath.Join(root, "self"), 0o755); err != nil {
				t.Fatalf("mkdir self: %v", err)
			}
			return root
		}, want: gone},
		{name: "empty readable root without self (unmounted /proc) is unreadable",
			setup: func(t *testing.T, root string) string { return root }, want: unreadable},
		{name: "pid dir absent with self a dangling symlink is unreadable", setup: func(t *testing.T, root string) string {
			writeStatWithState(t, root, pid+1, 1, "S", start)
			symlinkProcSelf(t, root, "no-such-pid")
			return root
		}, want: unreadable},
		{name: "missing proc root is unreadable",
			setup: func(t *testing.T, root string) string { return filepath.Join(root, "no-such-proc") }, want: unreadable},
		{name: "proc root not listable with pid absent is unreadable", nonRoot: true, setup: func(t *testing.T, root string) string {
			symlinkProcSelf(t, root, ".")       // self resolves, so only the root open fails
			chmodLinuxStartTime(t, root, 0o311) // search but no read: stat ENOENT, root open EACCES
			return root
		}, want: unreadable},
		{name: "stat mode 000 (EACCES) is unreadable", nonRoot: true, setup: func(t *testing.T, root string) string {
			dir := writeStatWithState(t, root, pid, 1, "S", start)
			chmodLinuxStartTime(t, filepath.Join(dir, "stat"), 0o000)
			return root
		}, want: unreadable},
		{name: "stat entry is a directory (unexpected errno) is unreadable", setup: func(t *testing.T, root string) string {
			if err := os.MkdirAll(filepath.Join(root, "100", "stat"), 0o755); err != nil {
				t.Fatalf("mkdir stat dir: %v", err)
			}
			return root
		}, want: unreadable},
		{name: "malformed line without ')' is unreadable", setup: raw("garbage without a paren\n"), want: unreadable},
		{name: "line ending before field 22 is unreadable", setup: raw("100 (x) S 1 0 0\n"), want: unreadable},
		{name: "missing state (fields shift left) is unreadable", setup: stat("", start), want: unreadable},
		{name: "multi-letter state ZZ is unreadable, not gone", setup: stat("ZZ", start), want: unreadable},
		// No environment read: whatever environ holds (or none, as above), a matching stat reads alive.
		{name: "environ lacking the instance id still alive", setup: func(t *testing.T, root string) string {
			writeFakeProc(t, root, pid, 1, start, "")
			return root
		}, want: alive},
		{name: "environ mode 000 still alive", setup: func(t *testing.T, root string) string {
			writeFakeProc(t, root, pid, 1, start, "inst-linux-xyz")
			chmodLinuxStartTime(t, filepath.Join(root, "100", "environ"), 0o000)
			return root
		}, want: alive},
		{name: "environ is a directory (unreadable even as root) still alive", setup: func(t *testing.T, root string) string {
			dir := writeStatWithState(t, root, pid, 1, "S", start)
			if err := os.Mkdir(filepath.Join(dir, "environ"), 0o755); err != nil {
				t.Fatalf("mkdir environ: %v", err)
			}
			return root
		}, want: alive},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.nonRoot && os.Geteuid() == 0 {
				t.Skip("running as root: mode bits do not produce EACCES")
			}
			r := linuxStartTimeReader{procRoot: tc.setup(t, t.TempDir())}
			assertLinuxStartTime(t, r, pid, tc.want.start, tc.want.alive, tc.want.known)
		})
	}
}

// TestLinuxStartTimeReaderNonPositivePID: pid <= 0 is unreadable even when a
// readable stat sits under the root (pid 0 has its own stat entry here).
func TestLinuxStartTimeReaderNonPositivePID(t *testing.T) {
	for _, p := range []int{0, -1} {
		root := t.TempDir()
		writeStatWithState(t, root, 0, 1, "S", procstarttimefix.LinuxProcStarttime)
		writeStatWithState(t, root, 1, 0, "S", procstarttimefix.LinuxProcStarttime)
		assertLinuxStartTime(t, linuxStartTimeReader{procRoot: root}, p, "", false, false)
	}
}

// assertLinuxStartTime checks the full (start, alive, known) triple from r.StartTime(pid).
func assertLinuxStartTime(t *testing.T, r ProcChecker, pid int, start string, alive, known bool) {
	t.Helper()
	gs, ga, gk := r.StartTime(pid)
	if gs != start || ga != alive || gk != known {
		t.Errorf("StartTime(%d) = (%q, %v, %v); want (%q, %v, %v)", pid, gs, ga, gk, start, alive, known)
	}
}
