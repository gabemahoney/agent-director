//go:build linux || darwin

package inputjournal

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

// LockWriter nonblockingly acquires the original shared launch lock.
// Runtime and importer must reuse this lock, never replace its inode.
func LockWriter(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }

// LockObserver takes only the short shared admission gate, never the input
// writer lock. Release it as soon as the nonblocking input-lock try finishes.
func LockObserver(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB) }

// LockBusy distinguishes contention from an unusable lock or platform.
func LockBusy(err error) bool { return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) }

// A replaced evidence path must not turn a bounded observation into a FIFO
// open waiting for a writer. ReadEvidence verifies the descriptor is regular.
func openEvidence(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
