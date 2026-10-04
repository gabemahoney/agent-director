//go:build linux

package probe

// newParentPIDReader returns the production Linux parent-pid reader over the
// kernel procfs mount. Its answer logic lives in the build-tag-free
// linuxParentPIDReader (ppid_linux_core.go); this file only pins the default
// proc root, as newProcChecker does.
func newParentPIDReader() ParentPIDReader {
	return linuxParentPIDReader{procRoot: defaultProcRoot}
}
