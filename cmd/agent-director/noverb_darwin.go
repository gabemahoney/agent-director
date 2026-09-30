//go:build darwin

package main

import "golang.org/x/sys/unix"

// ioctlReadTermios is the ioctl that reads a terminal's settings; isTerminal
// uses it to tell a terminal standard input from a pipe or file.
const ioctlReadTermios = unix.TIOCGETA
