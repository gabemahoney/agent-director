//go:build linux

package probe

import (
	"os"
	"strconv"
	"strings"
)

// TerminalProcess is one procfs observation of a live process's identity and
// controlling terminal. TTY is Linux's encoded device number, comparable to
// stat's st_rdev; PGRP and Foreground are in the reader's PID namespace.
type TerminalProcess struct {
	Starttime  string
	TTY        uint64
	PGRP       int
	Foreground int
}

// ReadTerminalProcess observes foreground ownership without requiring the
// reader to share the target's controlling terminal (unlike TIOCGPGRP on a
// slave tty). Missing, malformed or terminal-less observations fail closed.
// It reads only stat, never process arguments or environment.
func ReadTerminalProcess(pid int) (TerminalProcess, error) {
	if pid <= 0 {
		return TerminalProcess{}, ErrLinuxStatMalformed
	}
	data, err := os.ReadFile(defaultProcRoot + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return TerminalProcess{}, err
	}
	line := string(data)
	state, _, start, err := parseLinuxStatWithState(line)
	if err != nil || state == 'Z' || state == 'X' || state == 'x' {
		return TerminalProcess{}, ErrLinuxStatMalformed
	}
	// The shared parser has validated the tail through field 22. Reuse its
	// last-parenthesis anchor so spaces or parentheses in comm cannot shift it.
	fields := strings.Fields(line[strings.LastIndexByte(line, ')')+1:])
	pg, pgErr := strconv.Atoi(fields[5-3])
	tty, ttyErr := strconv.ParseInt(fields[7-3], 10, 32)
	fg, fgErr := strconv.Atoi(fields[8-3])
	if pgErr != nil || ttyErr != nil || fgErr != nil || pg <= 0 || tty == 0 || fg <= 0 {
		return TerminalProcess{}, ErrLinuxStatMalformed
	}
	// procfs prints tty_nr as a signed 32-bit number; preserve its device bits.
	return TerminalProcess{Starttime: start, TTY: uint64(uint32(tty)), PGRP: pg, Foreground: fg}, nil
}
