//go:build linux

package probe

// defaultProcRoot is the kernel procfs mount the production Linux readers
// (the start-time reader and the command-name reader) read through. Each
// reader takes its root as an injectable seam, so tests substitute a
// fabricated tree.
const defaultProcRoot = "/proc"

// newProcChecker returns the production Linux start-time reader (SR-3.8) over
// the kernel procfs mount. Its answer logic lives in the build-tag-free
// linuxStartTimeReader (starttime_linux_core.go); this file only pins the
// default proc root.
func newProcChecker() ProcChecker {
	return linuxStartTimeReader{procRoot: defaultProcRoot}
}
