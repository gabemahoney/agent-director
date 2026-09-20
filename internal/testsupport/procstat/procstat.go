// Package procstat holds the canonical test-side reader for a live process's
// proc_starttime — field 22 of /proc/<pid>/stat — so the byte-identical
// last-')' anchor logic lives exactly once instead of being copied into each
// test package that seeds a row against a real child.
//
// This is a LEAF test-support package: it imports only the standard library
// (notably NOTHING from internal/store), so it can be pulled into any test
// without dragging in the store graph. It is Linux-only in effect (the
// /proc/<pid>/stat layout is a Linux kernel interface); callers are the Linux
// resolver test and the find-missing acceptance test, both of which already
// run only where /proc exists.
package procstat

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// ReadStarttime reads field 22 (starttime, clock-ticks-since-boot) from
// /proc/<pid>/stat and returns it verbatim — the exact decimal form the store
// records and the checker compares against. It anchors on the LAST ')' so a
// comm containing spaces or parens cannot shift the field count, matching the
// production parser. It t.Fatalf's on a read error or a malformed line.
func ReadStarttime(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("read /proc/%d/stat: %v", pid, err)
	}
	line := string(data)
	rparen := strings.LastIndexByte(line, ')')
	if rparen < 0 || rparen+1 >= len(line) {
		t.Fatalf("malformed /proc/%d/stat: %q", pid, line)
	}
	// Fields from field 3 (state) onward; field 22 is index 22-3 = 19.
	fields := strings.Fields(line[rparen+1:])
	const starttimeIdx = 22 - 3
	if len(fields) <= starttimeIdx {
		t.Fatalf("/proc/%d/stat has too few fields: %q", pid, line)
	}
	return fields[starttimeIdx]
}
