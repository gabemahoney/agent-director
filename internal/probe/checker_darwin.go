//go:build darwin

package probe

import "golang.org/x/sys/unix"

// newChecker returns the production darwin LivenessChecker. It wires the real
// per-pid KERN_PROC_PID + KERN_PROCARGS2 sysctl fetches into the build-tag-free
// darwinChecker, whose verdict logic + errno table are unit-testable off-darwin
// (the resolver_darwin_core precedent: syscalls here, byte parsing + logic in
// the tag-free core).
func newChecker() LivenessChecker {
	return darwinChecker{
		fetchKinfo: fetchKinfoPID,
		fetchEnv:   procArgs,
	}
}

// fetchKinfoPID returns the raw kinfo_proc bytes for a single live pid via
// sysctl(kern.proc.pid.<pid>) — the KERN_PROC_PID surface. A live pid yields
// exactly one kinfoProcSize entry; a gone pid yields an empty buffer (the
// caller maps that to provably-dead) or ESRCH. The RAW bytes flow straight
// into the entry-granular kinfo parsers (no whole-buffer plausibility pass);
// those parsers apply the fail-open ErrKinfoLayoutDrift guard.
func fetchKinfoPID(pid int) ([]byte, error) {
	return unix.SysctlRaw("kern.proc.pid", pid)
}
