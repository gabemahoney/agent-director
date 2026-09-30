//go:build darwin

package probe

// newCommandNameReader returns the production darwin command-name reader. It
// wires ONLY the real per-pid KERN_PROC_PID fetch (fetchKinfoPID, the one the
// start-time reader uses) into the build-tag-free darwinCommandNameReader
// (commname_darwin_core.go); there is no KERN_PROCARGS2 wiring, so it cannot
// read a process environment.
func newCommandNameReader() CommandNameReader {
	return darwinCommandNameReader{fetchKinfo: fetchKinfoPID}
}
