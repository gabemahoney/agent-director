//go:build !darwin

package adminapi

// journalKernelPath reports no kernel form off darwin: Linux's EvalSymlinks
// form is already one of a root's forms.
func journalKernelPath(string) ([]string, error) {
	return nil, nil
}
