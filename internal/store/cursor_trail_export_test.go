package store

import "testing"

// BeforeCursorTrailExecForTest reuses the transaction observation seam.
func BeforeCursorTrailExecForTest(t *testing.T, s *Store, path string, observe func(string)) {
	beforeEachExec(t, s, path, observe)
}
