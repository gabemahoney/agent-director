//go:build !linux && !darwin

package probe

// newParentPIDReader on an unsupported OS returns the always-(0, false)
// parent-pid reader (ppid.go).
func newParentPIDReader() ParentPIDReader { return unsupportedParentPIDReader{} }
