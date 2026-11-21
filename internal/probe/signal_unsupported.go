//go:build !linux && !darwin

package probe

// terminateIdentity on an unsupported OS sends nothing.
func terminateIdentity(int, string) error { return ErrProbeUnsupported }
