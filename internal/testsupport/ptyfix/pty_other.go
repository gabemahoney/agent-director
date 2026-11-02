//go:build !linux

package ptyfix

import (
	"os"
	"testing"
)

// Open skips the test: the real pseudo-terminal case runs on Linux only.
func Open(t testing.TB) (master, slave *os.File) {
	t.Helper()
	t.Skip("real pseudo-terminal case runs on Linux only")
	return nil, nil
}
