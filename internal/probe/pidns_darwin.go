//go:build darwin

package probe

// selfPIDNamespace on darwin answers ("", true): darwin has no pid
// namespaces, so every process shares the one namespace and a pid means the
// same process to every reader.
func selfPIDNamespace() (string, bool) {
	return "", true
}
