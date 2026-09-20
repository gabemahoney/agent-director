package probe

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// dispositionName renders a procDisposition for test failure messages. It lives
// alongside the tests (the production enum has no String method — it never needs
// one) so the (errno → disposition) table assertions read clearly.
func dispositionName(d procDisposition) string {
	switch d {
	case dispGone:
		return "dispGone"
	case dispPermission:
		return "dispPermission"
	case dispUnexpected:
		return "dispUnexpected"
	default:
		return fmt.Sprintf("procDisposition(%d)", int(d))
	}
}

// TestClassifyLinuxErrnoTable pins EVERY entry of the SR-7.4 Linux errno table
// (classifyLinuxErrno) as data: ENOENT/ESRCH → dispGone, EACCES/EPERM →
// dispPermission. os.ErrNotExist / os.ErrPermission are covered too because the
// production reader (os.ReadFile) surfaces its errors wrapped as *PathError whose
// chain satisfies errors.Is against those sentinels — so the classifier must key
// off the abstract sentinels, not just the raw syscall.Errno.
func TestClassifyLinuxErrnoTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want procDisposition
	}{
		{"ENOENT", syscall.ENOENT, dispGone},
		{"os.ErrNotExist", os.ErrNotExist, dispGone},
		{"ESRCH", syscall.ESRCH, dispGone},
		{"EACCES", syscall.EACCES, dispPermission},
		{"os.ErrPermission", os.ErrPermission, dispPermission},
		{"EPERM", syscall.EPERM, dispPermission},
		// Wrapped forms (the production os.ReadFile path wraps in *PathError):
		{"wrapped_ENOENT_PathError", &os.PathError{Op: "open", Path: "/proc/1/stat", Err: syscall.ENOENT}, dispGone},
		{"wrapped_EACCES_PathError", &os.PathError{Op: "open", Path: "/proc/1/environ", Err: syscall.EACCES}, dispPermission},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLinuxErrno(tc.err); got != tc.want {
				t.Errorf("classifyLinuxErrno(%v) = %s; want %s",
					tc.err, dispositionName(got), dispositionName(tc.want))
			}
		})
	}
}

// TestClassifyLinuxErrnoUnexpected is the named exhaustiveness guard: any errno
// ABSENT from the pinned table classifies as dispUnexpected → the caller folds
// that to VerdictUnknown, NEVER VerdictProvablyDead. A drifted kernel that starts
// returning EINVAL/EIO (or a wrapped random error) must fail OPEN, not mark rows
// missing.
func TestClassifyLinuxErrnoUnexpected(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"EINVAL", syscall.EINVAL},
		{"EIO", syscall.EIO},
		{"ENOMEM", syscall.ENOMEM},
		{"wrapped_random_error", fmt.Errorf("scrambled: %w", errors.New("not an errno"))},
		{"bare_random_error", errors.New("totally unpinned")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLinuxErrno(tc.err); got != dispUnexpected {
				t.Errorf("classifyLinuxErrno(%v) = %s; want dispUnexpected (unknown never dead)",
					tc.err, dispositionName(got))
			}
		})
	}
}

// TestClassifyDarwinErrnoTable pins EVERY entry of the SR-7.4 macOS errno table
// (classifyDarwinErrno): ESRCH → dispGone, EACCES/EPERM → dispPermission. Note
// Darwin has NO ENOENT/ErrNotExist row — the sysctl seam surfaces a gone pid as
// ESRCH or an empty buffer, not a filesystem miss.
func TestClassifyDarwinErrnoTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want procDisposition
	}{
		{"ESRCH", syscall.ESRCH, dispGone},
		{"EACCES", syscall.EACCES, dispPermission},
		{"EPERM", syscall.EPERM, dispPermission},
		{"wrapped_ESRCH", fmt.Errorf("sysctl kern.proc.pid: %w", syscall.ESRCH), dispGone},
		{"wrapped_EPERM", fmt.Errorf("sysctl procargs2: %w", syscall.EPERM), dispPermission},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDarwinErrno(tc.err); got != tc.want {
				t.Errorf("classifyDarwinErrno(%v) = %s; want %s",
					tc.err, dispositionName(got), dispositionName(tc.want))
			}
		})
	}
}

// TestClassifyDarwinErrnoUnexpected is the named exhaustiveness guard for the
// macOS table. ErrKinfoLayoutDrift (a parse-layer sentinel that can reach the
// classifier) and every unpinned errno both classify as dispUnexpected → the
// caller folds them to VerdictUnknown, NEVER VerdictProvablyDead.
func TestClassifyDarwinErrnoUnexpected(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"ErrKinfoLayoutDrift", ErrKinfoLayoutDrift},
		{"wrapped_ErrKinfoLayoutDrift", fmt.Errorf("parse entry 0: %w", ErrKinfoLayoutDrift)},
		{"ENOENT_not_pinned_on_darwin", syscall.ENOENT},
		{"EINVAL", syscall.EINVAL},
		{"EIO", syscall.EIO},
		{"wrapped_random_error", fmt.Errorf("scrambled: %w", errors.New("not an errno"))},
		{"bare_random_error", errors.New("totally unpinned")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDarwinErrno(tc.err); got != dispUnexpected {
				t.Errorf("classifyDarwinErrno(%v) = %s; want dispUnexpected (unknown never dead)",
					tc.err, dispositionName(got))
			}
		})
	}
}
