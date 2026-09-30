//go:build !linux

package main_test

import (
	"os"
	"testing"
)

// openPTY skips the test: the real pseudo-terminal case runs on Linux only.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	t.Skip("real pseudo-terminal case runs on Linux only")
	return nil, nil
}
