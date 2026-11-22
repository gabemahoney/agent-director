package tmux

import "golang.org/x/sys/unix"

// Linux bprm_stack_limits: max(ARG_MAX, min(3*_STK_LIM/4, rlim_stack/4)).
// ARG_MAX is 128 KiB and _STK_LIM is 8 MiB on the supported Linux ABI.
func runtimeLaunchBudget() (uint64, error) {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_STACK, &limit); err != nil {
		return 0, err
	}
	return max(uint64(128*1024), min(uint64(6*1024*1024), limit.Cur/4)), nil
}
