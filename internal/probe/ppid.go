package probe

// ParentPIDReader reads a process's parent pid (b.9n6, the hook's launcher
// warning: `ad.hook.launcher_detected` and `ad.hook.ignored`'s
// `launcher_pid`).
//
// It is used ONLY by that warning: after the SR-22.9 gate refuses a hook with
// pid_mismatch, the hook reads its parent's parent pid once, to tell whether
// the row's recorded pane process started the hook's parent as a child (a
// `claude` launcher that does not exec, or a hook run through a shell instead
// of in exec form). It is NEVER evidence: no ownership, launch,
// liveness or hook-gate decision reads it — the hook gate compares only the
// parent's pid and start time (SR-22.9, via ProcChecker.StartTime).
//
// Contract of PPID(pid):
//
//   - (ppid, true): the process's parent pid as the kernel reports it
//     (Linux: field 4 of /proc/<pid>/stat; darwin: the KERN_PROC_PID entry's
//     e_ppid), always positive.
//   - (0, false):   anything else — a non-positive pid, no such process, a
//     permission wall, an unreadable or malformed entry, a parent pid of 0,
//     layout drift, an unsupported OS.
//
// The reader never fails loudly (no error, no panic, no log), never reads a
// process environment and never reads the clock.
type ParentPIDReader interface {
	PPID(pid int) (ppid int, ok bool)
}

// NewParentPIDReader returns the per-OS parent-pid reader, selected by build
// tags at compile time (mirroring NewProcChecker and NewCommandNameReader):
//
//   - Linux:  linuxParentPIDReader over the default /proc root.
//   - darwin: darwinParentPIDReader over the real per-pid KERN_PROC_PID fetch
//     the start-time reader uses.
//   - other:  unsupportedParentPIDReader, which answers (0, false) for every
//     pid.
func NewParentPIDReader() ParentPIDReader {
	return newParentPIDReader()
}

// unsupportedParentPIDReader is the parent-pid reader on an OS with no per-OS
// implementation: (0, false) for every pid. Build-tag-free so its contract is
// unit-testable on any OS; newParentPIDReader returns it only on unsupported
// builds (ppid_unsupported.go).
type unsupportedParentPIDReader struct{}

// PPID always answers (0, false).
func (unsupportedParentPIDReader) PPID(_ int) (ppid int, ok bool) {
	return 0, false
}
