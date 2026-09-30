package probe

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// dispositionName renders a procDisposition for test failure messages (the
// production enum has no String method).
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

// TestClassifyLinuxErrnoTable pins every pinned Linux errno, bare and wrapped in
// the *PathError os.ReadFile returns: ENOENT/ESRCH gone, EACCES/EPERM permission.
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
		{"wrapped_EACCES_PathError", &os.PathError{Op: "open", Path: "/proc/1/stat", Err: syscall.EACCES}, dispPermission},
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

// TestClassifyLinuxErrnoUnexpected: any unpinned Linux error is dispUnexpected,
// which the start-time reader answers as unreadable, never gone.
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
				t.Errorf("classifyLinuxErrno(%v) = %s; want dispUnexpected (unreadable, never gone)",
					tc.err, dispositionName(got))
			}
		})
	}
}

// TestClassifyDarwinErrnoTable pins every pinned darwin errno, bare and wrapped:
// ESRCH gone, EACCES/EPERM permission (no ENOENT row on darwin).
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
		{"wrapped_EPERM", fmt.Errorf("sysctl kern.proc.pid: %w", syscall.EPERM), dispPermission},
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

// TestClassifyDarwinErrnoUnexpected: ErrKinfoLayoutDrift and every unpinned darwin
// error are dispUnexpected, which the start-time reader answers as unreadable, never gone.
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
				t.Errorf("classifyDarwinErrno(%v) = %s; want dispUnexpected (unreadable, never gone)",
					tc.err, dispositionName(got))
			}
		})
	}
}
