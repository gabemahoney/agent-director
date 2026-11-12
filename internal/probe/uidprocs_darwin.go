//go:build darwin

package probe

import "golang.org/x/sys/unix"

// fetchKinfoUID returns the raw kinfo_proc entries of every process whose
// effective uid is uid, via sysctl(kern.proc.uid.<uid>) — the KERN_PROC_UID
// surface. x/sys resolves the name through the kernel's own name-to-MIB
// sysctl and calls libSystem without cgo, as fetchKinfoPID does. The RAW
// bytes flow straight into parseKinfoPIDs.
func fetchKinfoUID(uid int) ([]byte, error) {
	return unix.SysctlRaw("kern.proc.uid", uid)
}

// newUIDProcessReader returns the production darwin uid selection reader. It
// wires ONLY the real KERN_PROC_UID fetch (fetchKinfoUID) into the
// build-tag-free darwinUIDProcessReader (uidprocs.go); there is no
// KERN_PROCARGS2 wiring, so it cannot read a process's arguments or
// environment.
func newUIDProcessReader() UIDProcessReader {
	return darwinUIDProcessReader{fetchKinfoUID: fetchKinfoUID}
}
