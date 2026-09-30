package probe

import (
	"errors"
	"os"
	"syscall"
)

// procDisposition is the classification of a raw /proc (or sysctl) read error
// into abstract dispositions the start-time reader answers from. The per-OS
// errno tables are pure classification functions (classifyLinuxErrno /
// classifyDarwinErrno) returning one of these values, build-tag-free, so a
// test can iterate a table of (errno → disposition) pairs without any real
// process or syscall.
type procDisposition int

const (
	// dispUnexpected is the fail-open default: any errno not explicitly pinned
	// classifies here → unreadable, never gone.
	dispUnexpected procDisposition = iota
	// dispGone means the read proved the process is absent (ENOENT/ESRCH on the
	// /proc entry, or ESRCH from KERN_PROC_PID).
	dispGone
	// dispPermission means the read hit a permission wall (EACCES/EPERM).
	dispPermission
)

// classifyLinuxErrno maps a /proc read error into a procDisposition. A pure
// function a test iterates over a table of (errno → disposition) pairs, with
// no real /proc access.
//
//   - ENOENT / ESRCH ...... dispGone (process absent)
//   - EACCES / EPERM ...... dispPermission
//   - anything else ....... dispUnexpected (fail-open → unreadable)
func classifyLinuxErrno(err error) procDisposition {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ESRCH):
		return dispGone
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return dispPermission
	default:
		return dispUnexpected
	}
}

// classifyDarwinErrno maps a KERN_PROC_PID sysctl error into a
// procDisposition. A pure function a test iterates over (errno → disposition)
// pairs, no real sysctl.
//
//   - ESRCH ............... dispGone (process absent)
//   - EACCES / EPERM ...... dispPermission
//   - ErrKinfoLayoutDrift . dispUnexpected (fail-open → unreadable)
//   - anything else ....... dispUnexpected (fail-open → unreadable)
func classifyDarwinErrno(err error) procDisposition {
	switch {
	case errors.Is(err, syscall.ESRCH):
		return dispGone
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return dispPermission
	default:
		// Includes ErrKinfoLayoutDrift and every unpinned errno.
		return dispUnexpected
	}
}
