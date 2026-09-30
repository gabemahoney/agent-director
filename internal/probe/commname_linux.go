//go:build linux

package probe

// newCommandNameReader returns the production Linux command-name reader over
// the kernel procfs mount. Its answer logic lives in the build-tag-free
// linuxCommandNameReader (commname_linux_core.go); this file only pins the
// default proc root, as newProcChecker does.
func newCommandNameReader() CommandNameReader {
	return linuxCommandNameReader{procRoot: defaultProcRoot}
}
