//go:build linux

// Package ptyfix provides real pseudo-terminals for CLI and terminal-mode
// tests (reusable test fixture), extracted from the CLI's openPTY for b.qcf.
package ptyfix

import (
	"fmt"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// Open opens a real pseudo-terminal pair through /dev/ptmx and fails the
// test unless the slave reads as a terminal; both ends close at cleanup.
func Open(t testing.TB) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("pty number: %v", err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open pty slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	if _, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS); err != nil {
		t.Fatalf("pty slave is not a terminal: %v", err)
	}
	return master, slave
}
