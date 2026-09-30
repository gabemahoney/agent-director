//go:build !linux && !darwin

package probe

// newProcChecker on an unsupported OS returns the always-unreadable start-time
// reader (starttime.go): known false for every pid, never gone.
func newProcChecker() ProcChecker { return unsupportedProcChecker{} }
