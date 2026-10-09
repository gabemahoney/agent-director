package probe

import "os"

// linuxSelfPIDNamespace is the Linux pid-namespace reader core over an
// INJECTABLE PROC ROOT (procRoot, "/proc" in production; see pidns_linux.go).
// It is build-tag-free (a plain os.Readlink under the injected root, no
// Linux-only syscall), so tests drive it with a fabricated proc tree on any
// OS, as they drive linuxStartTimeReader.
//
// It reads only the link <procRoot>/self/ns/pid and answers its target
// verbatim ("pid:[<inode>]"), known. Any read error (no such link, a
// permission wall, a missing or unmounted proc root) or an empty target is
// ("", false): a namespace that cannot be read is never guessed.
func linuxSelfPIDNamespace(procRoot string) (string, bool) {
	ns, err := os.Readlink(procRoot + "/self/ns/pid")
	if err != nil || ns == "" {
		return "", false
	}
	return ns, true
}
