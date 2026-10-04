//go:build darwin

package probe

// newParentPIDReader returns the production darwin parent-pid reader. It wires
// ONLY the real per-pid KERN_PROC_PID fetch (fetchKinfoPID, the one the
// start-time reader uses) into the build-tag-free darwinParentPIDReader
// (ppid_darwin_core.go); there is no KERN_PROCARGS2 wiring, so it cannot read
// a process environment.
func newParentPIDReader() ParentPIDReader {
	return darwinParentPIDReader{fetchKinfo: fetchKinfoPID}
}
