//go:build linux

package probe

import (
	"errors"

	"golang.org/x/sys/unix"
)

// terminateIdentity pins pid with a pidfd, checks its identity through that
// pin and signals through it, so a pid reused after the check is never hit.
func terminateIdentity(pid int, start string) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return ErrSignalTargetGone
	}
	if err != nil {
		return ErrSignalTargetUnverifiable
	}
	defer unix.Close(fd)
	if err := checkSignalIdentity(NewProcChecker(), pid, start); err != nil {
		return err
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrSignalTargetGone
		}
		return err
	}
	return nil
}
