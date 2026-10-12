//go:build linux

package api_test

import "syscall"

// dupOnto makes file descriptor to a copy of from.
func dupOnto(from, to int) error { return syscall.Dup3(from, to, 0) }
