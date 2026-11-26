//go:build unix

package clisetup

import "syscall"

// EnsureOwnerUmask preserves owner access without changing group or other
// restrictions. Every binary entry point must call it first in main, before
// starting workers or creating files; it changes the process-wide umask.
func EnsureOwnerUmask() {
	old := syscall.Umask(0)
	syscall.Umask(old &^ 0o700)
}
