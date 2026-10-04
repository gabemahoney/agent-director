package probe

// darwinParentPIDReader is the darwin parent-pid reader core (the hook's
// launcher warning; see ParentPIDReader). Its ONLY injected seam is the
// per-pid KERN_PROC_PID kinfo fetch (fetchKinfo, wired to fetchKinfoPID by
// newParentPIDReader on darwin — see ppid_darwin.go), so the answer logic is
// build-tag-free and unit-testable off-darwin against synthetic kinfo_proc
// entries (the darwinStartTimeReader precedent). It carries no KERN_PROCARGS2
// seam, so it structurally cannot read a process environment.
//
// Answers:
//
//   - (e_ppid, true) when the single KERN_PROC_PID entry's e_ppid passes
//     parseKinfoPPID's drift guard (which also refuses 0);
//   - (0, false) for pid <= 0, any fetch error, an empty result (no such
//     process) or ErrKinfoLayoutDrift (a short entry included).
type darwinParentPIDReader struct {
	fetchKinfo func(pid int) ([]byte, error)
}

// PPID implements the darwin answer table above.
func (r darwinParentPIDReader) PPID(pid int) (ppid int, ok bool) {
	if pid <= 0 {
		return 0, false
	}
	buf, err := r.fetchKinfo(pid)
	if err != nil || len(buf) == 0 {
		return 0, false
	}
	// Single-entry parse at offset 0 (KERN_PROC_PID returns one entry).
	ppid, err = parseKinfoPPID(buf, 0)
	if err != nil {
		return 0, false
	}
	return ppid, true
}
