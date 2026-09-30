//go:build !linux && !darwin

package probe

// newCommandNameReader on an unsupported OS returns the always-("", false)
// command-name reader (commname.go).
func newCommandNameReader() CommandNameReader { return unsupportedCommandNameReader{} }
