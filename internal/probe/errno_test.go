package probe

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// dispositionName renders a procDisposition for failure messages (the
// production enum has no String method).
func dispositionName(d procDisposition) string {
	switch d {
	case dispGone:
		return "dispGone"
	case dispPermission:
		return "dispPermission"
	case dispUnexpected:
		return "dispUnexpected"
	}
	return fmt.Sprintf("procDisposition(%d)", int(d))
}

// TestClassifyErrno pins each OS's errno table, bare and wrapped (os.ReadFile's
// *PathError on Linux, the sysctl error on darwin): Linux ENOENT/ESRCH and
// darwin ESRCH are gone, EACCES/EPERM permission, and everything else,
// ErrKinfoLayoutDrift and darwin ENOENT included, dispUnexpected, which the
// start-time reader answers as unreadable, never gone.
func TestClassifyErrno(t *testing.T) {
	random := errors.New("totally unpinned")
	wrappedRandom := fmt.Errorf("scrambled: %w", errors.New("not an errno"))
	pathErr := func(e error) error { return &os.PathError{Op: "open", Path: "/proc/1/stat", Err: e} }
	sysctlErr := func(e error) error { return fmt.Errorf("sysctl kern.proc.pid: %w", e) }
	cases := []struct {
		os       string
		classify func(error) procDisposition
		want     map[error]procDisposition
	}{
		{"linux", classifyLinuxErrno, map[error]procDisposition{
			syscall.ENOENT: dispGone, os.ErrNotExist: dispGone, syscall.ESRCH: dispGone, pathErr(syscall.ENOENT): dispGone,
			syscall.EACCES: dispPermission, os.ErrPermission: dispPermission, syscall.EPERM: dispPermission, pathErr(syscall.EACCES): dispPermission,
			syscall.EINVAL: dispUnexpected, syscall.EIO: dispUnexpected, syscall.ENOMEM: dispUnexpected, wrappedRandom: dispUnexpected, random: dispUnexpected,
		}},
		{"darwin", classifyDarwinErrno, map[error]procDisposition{
			syscall.ESRCH: dispGone, sysctlErr(syscall.ESRCH): dispGone,
			syscall.EACCES: dispPermission, syscall.EPERM: dispPermission, sysctlErr(syscall.EPERM): dispPermission,
			ErrKinfoLayoutDrift: dispUnexpected, fmt.Errorf("parse entry 0: %w", ErrKinfoLayoutDrift): dispUnexpected,
			syscall.ENOENT: dispUnexpected, syscall.EINVAL: dispUnexpected, syscall.EIO: dispUnexpected,
			wrappedRandom: dispUnexpected, random: dispUnexpected,
		}},
	}
	for _, tc := range cases {
		for err, want := range tc.want {
			if got := tc.classify(err); got != want {
				t.Errorf("%s: classify(%v) = %s; want %s", tc.os, err, dispositionName(got), dispositionName(want))
			}
		}
	}
}
