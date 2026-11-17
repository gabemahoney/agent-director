//go:build linux || darwin

package inputdelivery

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// RunProbe runs a ProbeCommand in its own process group and kills that group
// when the command ends or its context is done, so neither the probe nor its
// descendants outlive the call. It returns the run's error and, separately, a
// failure to kill the group, so a caller can report each.
// Use RunProbe for helper process-group cleanup.
func RunProbe(command *exec.Cmd) (runErr, cleanupErr error) {
	// A launcher may ignore its arguments and wait for children, or exit while
	// they keep its stdout open. Keep the whole probe separate from the caller's group.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := command.Start(); err != nil {
		return err, nil
	}
	runErr = command.Wait()
	// WaitDelay bounds inherited stdout pipes; cleanup is also necessary after
	// a successful exit when descendants closed or redirected their stdout.
	cleanupErr = command.Cancel()
	if errors.Is(cleanupErr, os.ErrProcessDone) {
		cleanupErr = nil
	}
	return runErr, cleanupErr
}
