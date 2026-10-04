package probe

import (
	"os"
	"strconv"
)

// linuxParentPIDReader is the Linux parent-pid reader core (the hook's
// launcher warning; see ParentPIDReader) over an INJECTABLE PROC ROOT
// (procRoot, default "/proc" via newParentPIDReader on Linux — see
// ppid_linux.go). Like linuxStartTimeReader it is build-tag-free (plain
// os.ReadFile over the injected root), so tests drive it with a fabricated
// proc tree on any OS.
//
// It reads only <procRoot>/<pid>/stat — never environ or any other
// per-process file — and parses it with the start-time reader's parser
// (parseLinuxStatWithState). It answers:
//
//   - (field 4, true) when the read succeeds, the line parses and field 4 is
//     positive;
//   - (0, false) for pid <= 0, any read error (no such process, a permission
//     wall, a missing proc root), a malformed stat line or a parent pid of 0.
type linuxParentPIDReader struct {
	procRoot string
}

// PPID implements the Linux answer table above.
func (r linuxParentPIDReader) PPID(pid int) (ppid int, ok bool) {
	if pid <= 0 {
		return 0, false
	}
	data, err := os.ReadFile(r.procRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	_, ppid, _, err = parseLinuxStatWithState(string(data))
	if err != nil || ppid <= 0 {
		return 0, false
	}
	return ppid, true
}
