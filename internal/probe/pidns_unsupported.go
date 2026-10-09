//go:build !linux && !darwin

package probe

// selfPIDNamespace on an unsupported OS answers ("", false): the namespace
// cannot be read, as the start-time reader there reads nothing.
func selfPIDNamespace() (string, bool) {
	return "", false
}
