//go:build linux

package main_test

import (
	"fmt"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// b.hd8: strace scheduling must not turn a signal-0 probe into runtime access.
func TestVersionForbiddenAccessProbe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		trace string
	}{
		{"whole", "80732 pidfd_send_signal(12<pid:80728>, 0, NULL, 0) = 0\n"},
		// The unfinished line is verbatim from the failing unit gate.
		{"split-captured", "80732 pidfd_send_signal(12<pid:80728>, 0, NULL, 0 <unfinished ...>\n80733 write(0x1, 0xc000012345, 0x1) = 0x1\n80732 <... pidfd_send_signal resumed>) = 0\n"},
		{"unfinished", "80732 pidfd_send_signal(12<pid:80728>, 0, NULL, 0 <unfinished ...>\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if hits := versionForbiddenAccess(tc.trace); len(hits) != 0 {
				t.Fatalf("signal-0 probe reported as runtime access: %q", hits)
			}
		})
	}
}

// b.hd8: rejoining must preserve real accesses, even calls that never resume.
func TestVersionForbiddenAccessRealCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		call string
		args string
	}{
		{"kill", "kill", "80728, SIGTERM"},
		{"tkill", "tkill", "80728, SIGTERM"},
		{"pidfd-signal", "pidfd_send_signal", "12<pid:80728>, 9, NULL, 0"},
		{"connect", "connect", `3<UNIX:[1]>, {sa_family=AF_UNIX, sun_path="/tmp/x"}, 110`},
		{"sendto", "sendto", `3, "x", 1, 0, NULL, 0`},
		{"sendmsg", "sendmsg", "3, {msg_name=NULL}, 0"},
		{"stdin-read", "read", "0x0, 0xc000100000, 0x1000"},
		{"state-db", "openat", `AT_FDCWD</w>, "/h/state.db", O_RDONLY|O_CLOEXEC`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			whole := fmt.Sprintf("80732 %s(%s) = 0", tc.call, tc.args)
			unfinished := fmt.Sprintf("80732 %s(%s <unfinished ...>\n", tc.call, tc.args)
			for _, form := range []struct {
				name  string
				trace string
				want  string
			}{
				{"whole", whole + "\n", whole},
				{"split", unfinished + "80733 write(0x1, 0xc000012345, 0x1) = 0x1\n" + fmt.Sprintf("80732 <... %s resumed>) = 0\n", tc.call), whole},
				{"unfinished", unfinished, ""},
			} {
				t.Run(form.name, func(t *testing.T) {
					hits := versionForbiddenAccess(form.trace)
					if len(hits) != 1 {
						t.Fatalf("runtime access hits = %q, want exactly one", hits)
					}
					if form.want != "" && hits[0] != form.want {
						t.Fatalf("runtime access = %q, want %q", hits[0], form.want)
					}
				})
			}
		})
	}
}

// b.hd8: calibration must see ENOENT on the resumed half of an openat.
func TestVersionAccessCalibrationSplitLines(t *testing.T) {
	input := "/h/.config/agent-director/input/0123456789abcdef/fedcba9876543210"
	dbCall := `openat(AT_FDCWD</w>, "/h/state.db-wal", O_RDONLY`
	inputCall := `openat(AT_FDCWD</w>, "` + input + `/missing", O_RDONLY`
	wake := `92 utimensat(AT_FDCWD</w>, "/h/launch/` + store.BusySendWakeFile + `", NULL, 0) = 0` + "\n"
	for _, tc := range []struct {
		name  string
		trace string
	}{
		{"whole", "90 " + dbCall + ") = -1 ENOENT (No such file or directory)\n91 " + inputCall + ") = -1 ENOENT (No such file or directory)\n"},
		// Both calls are pending at once, then resume in reverse order.
		{"split", "90 " + dbCall + " <unfinished ...>\n91 " + inputCall + " <unfinished ...>\n91 <... openat resumed>) = -1 ENOENT (No such file or directory)\n90 <... openat resumed>) = -1 ENOENT (No such file or directory)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, touched, in := versionAccessCalibration(tc.trace+wake, input)
			if !db || !touched || !in {
				t.Fatalf("calibration db=%v wake=%v input=%v, want all true", db, touched, in)
			}
		})
	}
}
