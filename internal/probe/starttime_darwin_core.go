package probe

// darwinStartTimeReader is the darwin start-time reader core (SR-3.8, "The
// start-time reader (LFR C1)"; Appendix F.2 ProcChecker). Its ONLY injected
// seam is the per-pid KERN_PROC_PID kinfo fetch (fetchKinfo, wired to
// fetchKinfoPID by newProcChecker on darwin — see checker_darwin.go), so the
// answer logic is build-tag-free and unit-testable off-darwin against
// synthetic kinfo_proc entries (the darwinChecker / resolver_darwin_core
// precedent).
//
// It is deliberately separate from darwinChecker, which keeps its
// KERN_PROCARGS2 environment tiebreaker for find-missing until Epic 14 removes
// it. This reader carries no environment or PROCARGS2 seam, so it structurally
// cannot read a process environment; it never reads the clock.
//
// Answers (the F.2 shape; start is empty unless alive):
//
//   - alive:      the single KERN_PROC_PID entry parses (p_stat and
//     p_starttime both pass their drift guards) and p_stat is not
//     SZOMB → (canonical "<tv_sec>.<tv_usec>", true, true),
//     byte-identical to the stored darwin proc_starttime.
//   - gone:       ESRCH, an empty KERN_PROC_PID result, or p_stat is SZOMB
//     → ("", false, true).
//   - unreadable: EACCES/EPERM, any other error, ErrKinfoLayoutDrift from
//     either extractor (a short entry included), or pid <= 0
//     → ("", false, false).
type darwinStartTimeReader struct {
	fetchKinfo func(pid int) ([]byte, error)
}

// StartTime implements the darwin answer table above.
func (r darwinStartTimeReader) StartTime(pid int) (start string, alive bool, known bool) {
	if pid <= 0 {
		return "", false, false
	}
	buf, err := r.fetchKinfo(pid)
	if err != nil {
		// Reuse today's darwin errno table (checker_darwin_core.go).
		if classifyDarwinErrno(err) == dispGone {
			return "", false, true
		}
		return "", false, false
	}
	if len(buf) == 0 {
		// Empty KERN_PROC_PID result: no such process.
		return "", false, true
	}

	// Single-entry parse at offset 0 (KERN_PROC_PID returns one entry). Both
	// fields are parsed before the zombie check, so drift in either one reads
	// unreadable (fail-open), never gone.
	stat, serr := parseKinfoStat(buf, 0)
	if serr != nil {
		return "", false, false
	}
	sec, usec, terr := parseKinfoStartTime(buf, 0)
	if terr != nil {
		return "", false, false
	}
	if stat == kinfoStatSZOMB {
		return "", false, true
	}
	return formatDarwinProcStartTime(sec, usec), true, true
}
