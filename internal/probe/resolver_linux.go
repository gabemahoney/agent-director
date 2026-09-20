//go:build linux

package probe

import (
	"bytes"
	"os"
	"strconv"
)

// defaultProcRoot is the kernel procfs mount the production Linux resolver
// reads through. The injectable seam below lets tests substitute a fabricated
// proc tree.
const defaultProcRoot = "/proc"

// linuxResolver walks the parent chain through an INJECTABLE PROC ROOT
// (PM-mandated seam). procRoot defaults to /proc via newResolver; tests
// construct the reader with a fabricated tree root. Env matching reads
// <root>/<pid>/environ (NUL-separated KEY=VAL); ppid + verbatim starttime come
// from parseLinuxStat over <root>/<pid>/stat.
//
// Epic hp (SR-7.2) reuses this same per-pid <root>/<pid>/stat read for its
// liveness checker.
type linuxResolver struct {
	procRoot string
}

func newResolver() Resolver { return linuxResolver{procRoot: defaultProcRoot} }

func (r linuxResolver) Resolve(id string) (int, string, error) {
	return walkAncestors(&linuxProcReader{procRoot: r.procRoot, self: os.Getpid()}, id)
}

// linuxProcReader is the ancestorReader binding walkAncestors to a proc tree
// rooted at procRoot. It is separate from linuxResolver so the walk core stays
// build-tag-free and so the reader (with its injected root + starting pid) is
// directly constructible by tests.
type linuxProcReader struct {
	procRoot string
	self     int
}

func (r *linuxProcReader) selfPID() int { return r.self }

// instanceID reads <root>/<pid>/environ and returns the EnvKey value if
// present. An unreadable environ (foreign-uid ancestor under --pid=host, or the
// process gone mid-walk) is a routine non-match: (\"\", false, nil), not an
// error — the walk continues past it.
func (r *linuxProcReader) instanceID(pid int) (string, bool, error) {
	data, err := os.ReadFile(r.procRoot + "/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return "", false, nil
	}
	keyPrefix := []byte(EnvKey + "=")
	for _, kv := range bytes.Split(data, []byte{0}) {
		if !bytes.HasPrefix(kv, keyPrefix) {
			continue
		}
		return string(kv[len(keyPrefix):]), true, nil
	}
	return "", false, nil
}

// parent reads <root>/<pid>/stat and returns the parent pid + verbatim
// starttime via parseLinuxStat. A stat file gone mid-walk (the ancestor exited)
// is a clean end-of-chain: (0, \"\", false, nil). A malformed stat line is a
// hard error (ErrLinuxStatMalformed) surfaced fail-open.
func (r *linuxProcReader) parent(pid int) (int, string, bool, error) {
	data, err := os.ReadFile(r.procRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, "", false, nil
	}
	ppid, startTime, err := parseLinuxStat(string(data))
	if err != nil {
		return 0, "", false, err
	}
	return ppid, startTime, true, nil
}
