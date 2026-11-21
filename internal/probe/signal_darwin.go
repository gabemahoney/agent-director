//go:build darwin

package probe

import (
	"errors"

	"golang.org/x/sys/unix"
)

// terminateIdentity checks pid's identity and signals it at once. Darwin has
// no pidfd to pin the process between the two, so this is a known platform
// limitation: if the checked process exits and its pid is reused between the
// check and kill(2), the SIGTERM reaches the new process. SRD t1.kii.kn
// `## Guide exceptions` ("Darwin keyless pause may signal a reused PID in a
// narrow gap", E2 decision 17) accepts it as an exception to SR-6.4's
// ownership safeguards. The gap is kept to the two calls: the start-time
// reader is made before the check, and nothing runs between the check's
// return and the syscall.
func terminateIdentity(pid int, start string) error {
	pc := NewProcChecker()
	if err := checkSignalIdentity(pc, pid, start); err != nil {
		return err
	}
	if err := unix.Kill(pid, unix.SIGTERM); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrSignalTargetGone
		}
		return err
	}
	return nil
}
