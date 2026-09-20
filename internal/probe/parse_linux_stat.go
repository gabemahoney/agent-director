package probe

import (
	"errors"
	"strconv"
	"strings"
)

// ErrLinuxStatMalformed is returned by parseLinuxStat when a /proc/<pid>/stat
// line cannot be parsed into the fields we need (missing ')' delimiter,
// too few fields after it, or a non-numeric ppid). It is a distinct sentinel
// so callers can fail-open to NULL identity (SR-6.3) without conflating a
// parse miss with probe.ErrProbeUnsupported's fail-closed meaning.
var ErrLinuxStatMalformed = errors.New("ErrLinuxStatMalformed")

// parseLinuxStat parses a single /proc/<pid>/stat line and returns the
// parent pid (field 4) and the process start time (field 22). The line
// format is (proc(5)):
//
//	pid (comm) state ppid pgrp session tty_nr tpgid flags ... starttime ...
//	  1   2      3    4    ...                                  22
//
// The comm field (2) is the executable basename wrapped in parentheses and
// may itself contain spaces, parentheses, and digits (e.g. "(a b) c)"), so a
// naive Fields()[3] split is unsafe. Per the canonical proc(5) technique we
// locate the LAST ')' in the line: everything after it is the fixed,
// space-delimited tail whose first token is field 3 (state). Fields 4 and 22
// are then counted from that anchor, immune to whatever the comm contains.
//
// This parser is build-tag-free (pure string handling, no /proc I/O) so it
// is unit-testable on any OS.
//
// starttime is returned VERBATIM as its decimal string — the value is
// opaque clock-ticks-since-boot and NO unit conversion is applied here. This
// is the AUTHORITATIVE production counterpart to the Linux fixture constant
// procstarttimefix.LinuxProcStarttime ("12345678"): a stat line whose
// field 22 is "12345678" yields exactly that string. Keep the two in sync —
// see internal/testsupport/procstarttimefix/procstarttimefix.go (SR-6.3).
func parseLinuxStat(line string) (ppid int, starttime string, err error) {
	// Anchor on the last ')' so a comm containing ')' can't shift the count.
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+1 >= len(line) {
		return 0, "", ErrLinuxStatMalformed
	}

	// Fields from field 3 (state) onward, space-delimited. rest[0] is
	// field 3, so field N (N>=3) is rest[N-3].
	rest := strings.Fields(line[rparen+1:])

	// Need at least through field 22 → index 22-3 = 19.
	const (
		ppidIdx      = 4 - 3  // = 1
		starttimeIdx = 22 - 3 // = 19
	)
	if len(rest) <= starttimeIdx {
		return 0, "", ErrLinuxStatMalformed
	}

	ppid, convErr := strconv.Atoi(rest[ppidIdx])
	if convErr != nil {
		return 0, "", ErrLinuxStatMalformed
	}

	starttime = rest[starttimeIdx]
	if starttime == "" {
		return 0, "", ErrLinuxStatMalformed
	}

	return ppid, starttime, nil
}
