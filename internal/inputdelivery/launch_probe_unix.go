//go:build linux || darwin

package inputdelivery

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func runVersionProbe(command *exec.Cmd) error {
	// A launcher may ignore --version and wait for children, or exit while they
	// keep its stdout open. Keep the whole probe separate from the caller's group.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	err := command.Wait()
	// WaitDelay bounds inherited stdout pipes; cleanup is also necessary after
	// a successful exit when descendants closed or redirected their stdout.
	cleanupErr := command.Cancel()
	if errors.Is(cleanupErr, os.ErrProcessDone) {
		cleanupErr = nil
	}
	return errors.Join(err, cleanupErr)
}
