//go:build linux

package probe

// newChecker returns the production Linux LivenessChecker reading through the
// kernel procfs mount. The verdict logic + errno table live in the
// build-tag-free linuxChecker (checker_linux_core.go); this file only pins the
// default proc root, mirroring newResolver's use of defaultProcRoot so tests
// can substitute a fabricated tree.
func newChecker() LivenessChecker {
	return linuxChecker{procRoot: defaultProcRoot}
}
