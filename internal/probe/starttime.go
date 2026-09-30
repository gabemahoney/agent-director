package probe

// ProcChecker is the start-time reader (SR-3.8, "The start-time reader (LFR
// C1)"; Appendix F.2): the one process reader every process judgement of this
// release is meant to use — the tmux server check (SR-3.3), the start times of
// the identity write and of adoption (SR-3.6), the SR-22.9 hook gate (the hook
// parent's start time compared with the row's recorded pane_starttime),
// SessionStart-identity and pane liveness in find-missing (SR-11.1), kill's
// wait (SR-6.1), expire's process_alive (SR-12.2) and resume's and reuse's
// check of a running agent process (SR-4.2).
//
// Its method set is exactly F.2's, so the lookup's ProcChecker interface (and
// pkg/api's later alias of it) is satisfied structurally, with no import from
// this package to the lookup's.
//
// Contract of StartTime(pid), exactly one of:
//
//   - alive:      (start, true, true) — the process runs with this start time.
//   - gone:       ("", false, true) — no such process, or a zombie. On Linux
//     states Z (zombie) and X (dead) both count as gone; on darwin
//     p_stat SZOMB does. "Another start time" is the caller's
//     comparison of start against its recorded value.
//   - unreadable: ("", false, false) — EACCES/EPERM, no /proc, a malformed
//     entry, layout drift, a non-positive pid, an unsupported OS.
//
// start is empty unless alive. Its form per OS is the proc_starttime the store
// already records: on Linux field 22 of /proc/<pid>/stat verbatim (clock ticks
// since boot, decimal); on darwin the KERN_PROC_PID entry's p_starttime as
// "<tv_sec>.<tv_usec>" (formatDarwinProcStartTime). Callers compare it
// byte-for-byte, never by clock.
//
// The reader never reads a process environment and never reads the clock: the
// environment tiebreaker of LivenessChecker is deliberately absent. Today's
// LivenessChecker / NewChecker stay, unchanged, for find-missing until Epic 14
// removes them. No verb, command or hook uses this reader yet.
type ProcChecker interface {
	StartTime(pid int) (start string, alive bool, known bool)
}

// NewProcChecker returns the per-OS start-time reader, selected by build tags
// at compile time (mirroring New / NewResolver / NewChecker):
//
//   - Linux:  linuxStartTimeReader over the default /proc root.
//   - darwin: darwinStartTimeReader over the real per-pid KERN_PROC_PID fetch
//     today's checker uses (no KERN_PROCARGS2 wiring).
//   - other:  unsupportedProcChecker, which answers unreadable for every pid.
func NewProcChecker() ProcChecker {
	return newProcChecker()
}

// unsupportedProcChecker is the start-time reader on an OS with no per-OS
// implementation. It answers unreadable (known false) for every input, never
// gone, so no caller ever treats a process it cannot evidence as ended. It is
// build-tag-free so its contract is unit-testable on any OS; newProcChecker
// returns it only on unsupported builds (probe_unsupported.go).
type unsupportedProcChecker struct{}

// StartTime always answers unreadable.
func (unsupportedProcChecker) StartTime(_ int) (start string, alive bool, known bool) {
	return "", false, false
}
