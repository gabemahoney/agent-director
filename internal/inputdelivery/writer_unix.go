//go:build linux || darwin

package inputdelivery

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockWriter(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
