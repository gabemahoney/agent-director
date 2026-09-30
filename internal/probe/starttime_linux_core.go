package probe

import (
	"os"
	"strconv"
)

// linuxStartTimeReader is the Linux start-time reader core (SR-3.8, "The
// start-time reader (LFR C1)"; Appendix F.2 ProcChecker) over an INJECTABLE
// PROC ROOT (procRoot, default "/proc" via newProcChecker on Linux — see
// checker_linux.go). Like linuxChecker it is build-tag-free
// (plain os.ReadFile over the injected root, no Linux-only syscalls), so tests
// drive it with a fabricated proc tree on any OS.
//
// It is deliberately separate from linuxChecker, which keeps its environment
// tiebreaker for find-missing until Epic 14 removes it. This reader has NO
// environment tiebreaker: it reads only <procRoot>/<pid>/stat, never environ
// or any other per-process file, never consults AGENT_DIRECTOR_*, and never
// reads the clock.
//
// Answers (the F.2 shape; start is empty unless alive):
//
//   - alive:      the stat line parses and the state is neither Z (zombie)
//     nor X (dead) → (field 22 verbatim, true, true). The start
//     time is byte-identical to the proc_starttime form the
//     store records, so callers compare it exactly, never by clock.
//   - gone:       the pid's stat entry is absent (ENOENT/ESRCH) while the proc
//     root itself is present and readable AND <procRoot>/self
//     resolves (procfs is mounted), or the state is Z or X
//     → ("", false, true).
//   - unreadable: EACCES/EPERM, any other errno, a malformed stat line, a
//     missing or unreadable proc root, a proc root without a
//     resolvable self entry (an empty, unmounted /proc directory),
//     or pid <= 0 → ("", false, false). A missing /proc never reads
//     as every process gone.
type linuxStartTimeReader struct {
	procRoot string
}

// StartTime implements the Linux answer table above.
func (r linuxStartTimeReader) StartTime(pid int) (start string, alive bool, known bool) {
	if pid <= 0 {
		return "", false, false
	}
	data, err := os.ReadFile(r.procRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// Reuse today's Linux errno table (checker_linux_core.go).
		if classifyLinuxErrno(err) == dispGone && r.procRootReadable() {
			return "", false, true
		}
		// Permission wall, any other errno, or an absent/unreadable proc
		// root behind the ENOENT → unreadable, never gone.
		return "", false, false
	}

	state, _, starttime, perr := parseLinuxStatWithState(string(data))
	if perr != nil {
		// A malformed stat line is not evidence of anything → unreadable.
		return "", false, false
	}
	if state == 'Z' || state == 'X' {
		// A zombie (or a dead task still visible) counts as gone.
		return "", false, true
	}
	return starttime, true, true
}

// procRootReadable reports whether the proc root itself is present, a
// directory, openable for reading, AND has a resolvable <procRoot>/self entry.
// It gates the "gone" answer: an ENOENT under a missing proc root (a wrong
// root) or under an empty /proc directory (procfs not mounted, so the
// directory exists but holds nothing) must read as unreadable, never as every
// process gone. A mounted procfs always carries self, so requiring it to
// resolve (Stat follows the symlink to the reader's own pid directory) is the
// mounted-procfs witness.
func (r linuxStartTimeReader) procRootReadable() bool {
	f, err := os.Open(r.procRoot)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.IsDir() {
		return false
	}
	_, err = os.Stat(r.procRoot + "/self")
	return err == nil
}
