package probe

import (
	"os"
	"strconv"
	"strings"
)

// linuxCommandNameReader is the Linux command-name reader core (SR-14's
// `parent_command`; see CommandNameReader) over an INJECTABLE PROC ROOT
// (procRoot, default "/proc" via newCommandNameReader on Linux — see
// commname_linux.go). Like linuxStartTimeReader it is build-tag-free (plain
// os.ReadFile over the injected root), so tests drive it with a fabricated
// proc tree on any OS.
//
// It reads only <procRoot>/<pid>/comm — never environ or any other
// per-process file — and answers:
//
//   - (the file's content without its one trailing newline, true) when the
//     read succeeds and that content is non-empty;
//   - ("", false) for pid <= 0, any read error (no such process, a permission
//     wall, a missing proc root) or an empty comm.
type linuxCommandNameReader struct {
	procRoot string
}

// CommandName implements the Linux answer table above.
func (r linuxCommandNameReader) CommandName(pid int) (name string, ok bool) {
	if pid <= 0 {
		return "", false
	}
	data, err := os.ReadFile(r.procRoot + "/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "", false
	}
	name = strings.TrimSuffix(string(data), "\n")
	if name == "" {
		return "", false
	}
	return name, true
}
