package probe

import (
	"errors"
	"strconv"
	"strings"
)

// ErrLinuxStatMalformed is returned by parseLinuxStat (and
// parseLinuxStatWithState) when a /proc/<pid>/stat line cannot be parsed into
// the fields we need (missing ')' delimiter, too few fields after it, a
// non-numeric ppid, or, for parseLinuxStatWithState, a missing or malformed
// state). It is a distinct sentinel
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
	_, ppid, starttime, err = parseLinuxStatTail(line)
	return ppid, starttime, err
}

// parseLinuxStatWithState parses a single /proc/<pid>/stat line like
// parseLinuxStat and additionally returns the process state (field 3, the
// first token after the last ')'), which the start-time reader needs to count
// a zombie as gone (SR-3.8). It shares parseLinuxStat's last-')' anchoring, so
// a comm containing spaces, digits or parentheses cannot shift the count.
//
// The state must be exactly one ASCII letter (proc(5): R, S, D, Z, T, t, W,
// X, x, K, P, I). A missing or malformed state is ErrLinuxStatMalformed (the
// same fail-open family), never a guessed state. A line missing the state
// also shifts every later field left by one, so it fails the field-22 count
// as well.
//
// parseLinuxStat itself deliberately does NOT validate the state: its callers
// (the resolver's ancestor walk and today's LivenessChecker) keep their
// behaviour exactly.
func parseLinuxStatWithState(line string) (state byte, ppid int, starttime string, err error) {
	stateTok, ppid, starttime, err := parseLinuxStatTail(line)
	if err != nil {
		return 0, 0, "", err
	}
	if len(stateTok) != 1 || !isASCIILetter(stateTok[0]) {
		return 0, 0, "", ErrLinuxStatMalformed
	}
	return stateTok[0], ppid, starttime, nil
}

// parseLinuxStatTail is the shared body of parseLinuxStat and
// parseLinuxStatWithState: it anchors on the last ')' and returns the raw
// field-3 token (unvalidated), the parent pid (field 4) and the verbatim
// start time (field 22).
func parseLinuxStatTail(line string) (stateTok string, ppid int, starttime string, err error) {
	// Anchor on the last ')' so a comm containing ')' can't shift the count.
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+1 >= len(line) {
		return "", 0, "", ErrLinuxStatMalformed
	}

	// Fields from field 3 (state) onward, space-delimited. rest[0] is
	// field 3, so field N (N>=3) is rest[N-3].
	rest := strings.Fields(line[rparen+1:])

	// Need at least through field 22 → index 22-3 = 19.
	const (
		stateIdx     = 3 - 3  // = 0
		ppidIdx      = 4 - 3  // = 1
		starttimeIdx = 22 - 3 // = 19
	)
	if len(rest) <= starttimeIdx {
		return "", 0, "", ErrLinuxStatMalformed
	}

	ppid, convErr := strconv.Atoi(rest[ppidIdx])
	if convErr != nil {
		return "", 0, "", ErrLinuxStatMalformed
	}

	starttime = rest[starttimeIdx]
	if starttime == "" {
		return "", 0, "", ErrLinuxStatMalformed
	}

	return rest[stateIdx], ppid, starttime, nil
}

// isASCIILetter reports whether b is an ASCII letter (A-Z or a-z).
func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
