package probe

// darwinCommandNameReader is the darwin command-name reader core (SR-14's
// `parent_command`; see CommandNameReader). Its ONLY injected seam is the
// per-pid KERN_PROC_PID kinfo fetch (fetchKinfo, wired to fetchKinfoPID by
// newCommandNameReader on darwin — see commname_darwin.go), so the answer
// logic is build-tag-free and unit-testable off-darwin against synthetic
// kinfo_proc entries (the darwinStartTimeReader precedent). It carries no
// KERN_PROCARGS2 seam, so it structurally cannot read a process environment.
//
// Answers:
//
//   - (p_comm, true) when the single KERN_PROC_PID entry's p_comm passes
//     parseKinfoComm's drift guard;
//   - ("", false) for pid <= 0, any fetch error, an empty result (no such
//     process) or ErrKinfoLayoutDrift (a short entry included).
type darwinCommandNameReader struct {
	fetchKinfo func(pid int) ([]byte, error)
}

// CommandName implements the darwin answer table above.
func (r darwinCommandNameReader) CommandName(pid int) (name string, ok bool) {
	if pid <= 0 {
		return "", false
	}
	buf, err := r.fetchKinfo(pid)
	if err != nil || len(buf) == 0 {
		return "", false
	}
	// Single-entry parse at offset 0 (KERN_PROC_PID returns one entry).
	name, err = parseKinfoComm(buf, 0)
	if err != nil {
		return "", false
	}
	return name, true
}
