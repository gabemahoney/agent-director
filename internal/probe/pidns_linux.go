//go:build linux

package probe

// selfPIDNamespace on Linux reads the kernel procfs mount's self entry. Its
// answer logic lives in the build-tag-free linuxSelfPIDNamespace
// (pidns_linux_core.go); this file only pins the default proc root, as
// newProcChecker does.
func selfPIDNamespace() (string, bool) {
	return linuxSelfPIDNamespace(defaultProcRoot)
}
