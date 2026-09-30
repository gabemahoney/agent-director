//go:build darwin

package probe

import "golang.org/x/sys/unix"

// fetchKinfoPID returns the raw kinfo_proc bytes for a single live pid via
// sysctl(kern.proc.pid.<pid>) — the KERN_PROC_PID surface. A live pid yields
// exactly one kinfoProcSize entry; a gone pid yields an empty buffer or
// ESRCH. The RAW bytes flow straight into the entry-granular kinfo parsers
// (parse_kinfo.go), which apply the fail-open ErrKinfoLayoutDrift guard. The
// start-time reader and the command-name reader both use it.
func fetchKinfoPID(pid int) ([]byte, error) {
	return unix.SysctlRaw("kern.proc.pid", pid)
}

// newProcChecker returns the production darwin start-time reader (SR-3.8). It
// wires ONLY the real per-pid KERN_PROC_PID fetch (fetchKinfoPID) into the
// build-tag-free darwinStartTimeReader (starttime_darwin_core.go); there is no
// KERN_PROCARGS2 wiring, so it cannot read a process environment.
func newProcChecker() ProcChecker {
	return darwinStartTimeReader{fetchKinfo: fetchKinfoPID}
}
