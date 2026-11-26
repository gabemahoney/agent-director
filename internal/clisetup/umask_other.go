//go:build !unix

package clisetup

// EnsureOwnerUmask is a no-op on platforms without a Unix umask.
func EnsureOwnerUmask() {}
