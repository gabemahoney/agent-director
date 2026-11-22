package tmux

import "golang.org/x/sys/unix"

func runtimeLaunchBudget() (uint64, error) {
	limit, err := unix.SysctlUint32("kern.argmax")
	return uint64(limit), err
}
