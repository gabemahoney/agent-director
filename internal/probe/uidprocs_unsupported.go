//go:build !darwin

package probe

// newUIDProcessReader off darwin returns the always-erroring uid selection
// reader (uidprocs.go).
func newUIDProcessReader() UIDProcessReader { return unsupportedUIDProcessReader{} }
