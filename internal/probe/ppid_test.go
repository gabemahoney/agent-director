package probe

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
)

// Every per-OS core must satisfy the parent-pid reader interface.
var (
	_ ParentPIDReader = linuxParentPIDReader{}
	_ ParentPIDReader = darwinParentPIDReader{}
	_ ParentPIDReader = unsupportedParentPIDReader{}
)

// TestLinuxParentPIDReader pins the Linux answer table over a fabricated proc root.
func TestLinuxParentPIDReader(t *testing.T) {
	const pid = 4242
	cases := []struct {
		name     string
		stat     *string // nil: no <pid>/stat file
		pid      int
		root     string // "" = the fabricated root
		wantPPID int
		wantOK   bool
	}{
		{name: "parent_pid", stat: commp(fakeStatLine(pid, 4241, "S", "12345")), pid: pid, wantPPID: 4241, wantOK: true},
		{name: "zombie_still_reports_parent", stat: commp(fakeStatLine(pid, 4241, "Z", "12345")), pid: pid, wantPPID: 4241, wantOK: true},
		{name: "parent_pid_zero", stat: commp(fakeStatLine(pid, 0, "S", "12345")), pid: pid},
		{name: "malformed_stat", stat: commp("4242 (claude) S\n"), pid: pid},
		{name: "empty_stat", stat: commp(""), pid: pid},
		{name: "missing_pid", pid: pid},
		{name: "missing_proc_root", stat: commp(fakeStatLine(pid, 4241, "S", "12345")), pid: pid, root: "/nonexistent-proc-root"},
		{name: "pid_zero", stat: commp(fakeStatLine(pid, 4241, "S", "12345")), pid: 0},
		{name: "pid_negative", stat: commp(fakeStatLine(pid, 4241, "S", "12345")), pid: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.stat != nil {
				// Plant under both the queried pid and pid, so the non-positive
				// cases prove the pid guard rather than a missing file.
				for _, p := range []int{pid, tc.pid} {
					dir := filepath.Join(root, strconv.Itoa(p))
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("mkdir %s: %v", dir, err)
					}
					if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(*tc.stat), 0o644); err != nil {
						t.Fatalf("write stat: %v", err)
					}
				}
			}
			if tc.root != "" {
				root = tc.root
			}

			got, ok := linuxParentPIDReader{procRoot: root}.PPID(tc.pid)
			if got != tc.wantPPID || ok != tc.wantOK {
				t.Errorf("PPID(%d) = (%d, %v); want (%d, %v)", tc.pid, got, ok, tc.wantPPID, tc.wantOK)
			}
		})
	}
}

// TestDarwinParentPIDReader pins the darwin answer table against fixed kinfo entries.
func TestDarwinParentPIDReader(t *testing.T) {
	const pid = 4242
	entry := func(ppid int32) []byte {
		buf := make([]byte, kinfoProcSize)
		plantEntry(buf, 0, ppid, 1700000000, 1)
		return buf
	}
	cases := []struct {
		name     string
		pid      int
		buf      []byte
		err      error
		wantPPID int
		wantOK   bool
	}{
		{name: "parent_pid", pid: pid, buf: entry(4241), wantPPID: 4241, wantOK: true},
		{name: "esrch", pid: pid, err: syscall.ESRCH},
		{name: "eperm", pid: pid, err: syscall.EPERM},
		{name: "empty_result", pid: pid, buf: []byte{}},
		{name: "short_entry", pid: pid, buf: entry(4241)[:kinfoProcSize-1]},
		{name: "drift_zero_ppid", pid: pid, buf: entry(0)},
		{name: "drift_negative_ppid", pid: pid, buf: entry(-5)},
		{name: "pid_zero", pid: 0},
		{name: "pid_negative", pid: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []int
			r := darwinParentPIDReader{fetchKinfo: func(p int) ([]byte, error) {
				calls = append(calls, p)
				return tc.buf, tc.err
			}}

			got, ok := r.PPID(tc.pid)
			if got != tc.wantPPID || ok != tc.wantOK {
				t.Errorf("PPID(%d) = (%d, %v); want (%d, %v)", tc.pid, got, ok, tc.wantPPID, tc.wantOK)
			}
			wantCalls := []int{tc.pid}
			if tc.pid <= 0 {
				wantCalls = nil
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Errorf("fetchKinfo calls = %v; want %v", calls, wantCalls)
			}
		})
	}
}

// TestUnsupportedParentPIDReader: every pid answers (0, false).
func TestUnsupportedParentPIDReader(t *testing.T) {
	for _, pid := range []int{-1, 0, 1, os.Getpid()} {
		if got, ok := (unsupportedParentPIDReader{}).PPID(pid); got != 0 || ok {
			t.Errorf("PPID(%d) = (%d, %v); want (0, false)", pid, got, ok)
		}
	}
}
