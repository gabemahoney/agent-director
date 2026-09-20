package probe

// fakeproc_test.go — UNTAGGED fabricated-/proc fixtures shared across the probe
// test suite.
//
// These builders write a Linux-format <root>/<pid>/{stat,environ} tree under a
// caller-supplied root (a t.TempDir), never the real /proc. Both the resolver
// (linuxProcReader) and the checker (linuxChecker) take an INJECTABLE procRoot,
// and their parsing/verdict logic is build-tag-free, so the tests that drive
// them over a fabricated tree compile and run on ANY OS. This file therefore
// carries NO //go:build linux tag: it lives here (not in resolver_linux_test.go)
// so the tag-free verdict-table tests in checker_linux_core_test.go compile
// off-linux (e.g. `GOOS=darwin go vet ./internal/probe/`).
//
// Only tests that require the REAL /proc mount (TestLinuxResolverRealProcHappyPath)
// stay under the linux tag in resolver_linux_test.go.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeFakeProc writes a fabricated <root>/<pid>/{stat,environ} pair. stat is
// composed so parseLinuxStat reads ppid from field 4 and starttime from field
// 22; the comm deliberately contains a ')' and spaces to prove the parser
// anchors on the LAST ')'. envVal, when non-empty, plants EnvKey=<envVal> in
// environ (NUL-separated, mixed with an unrelated var).
func writeFakeProc(t *testing.T, root string, pid, ppid int, starttime, envVal string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	// Build a stat line: field1 pid, field2 comm (with a nasty ')'), field3
	// state, field4 ppid, fields 5..21 filler, field22 starttime, plus tail.
	fields := make([]string, 0, 24)
	fields = append(fields, strconv.Itoa(pid))       // 1
	fields = append(fields, "(claude (weird) proc)") // 2 comm w/ ')' and spaces
	fields = append(fields, "S")                     // 3 state
	fields = append(fields, strconv.Itoa(ppid))      // 4 ppid
	for f := 5; f <= 21; f++ {                       // 5..21 filler
		fields = append(fields, "0")
	}
	fields = append(fields, starttime) // 22 starttime
	fields = append(fields, "0", "0")  // tail
	stat := strings.Join(fields, " ") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}

	var environ []byte
	if envVal != "" {
		parts := []string{
			"PATH=/usr/bin",
			EnvKey + "=" + envVal,
			"HOME=/home/x",
		}
		environ = []byte(strings.Join(parts, "\x00") + "\x00")
	} else {
		environ = []byte("PATH=/usr/bin\x00HOME=/home/x\x00")
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), environ, 0o644); err != nil {
		t.Fatalf("write environ: %v", err)
	}
}

// writeStatOnly writes <root>/<pid>/stat only (no environ) with the given
// starttime — used to fabricate an environ-read failure (ENOENT) after a matched
// stat, and as the base for permission-mode fixtures. Returns the pid dir.
func writeStatOnly(t *testing.T, root string, pid, ppid int, starttime string) string {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	fields := []string{
		strconv.Itoa(pid),       // 1 pid
		"(claude (weird) proc)", // 2 comm w/ ')' and spaces
		"S",                     // 3 state
		strconv.Itoa(ppid),      // 4 ppid
	}
	for f := 5; f <= 21; f++ {
		fields = append(fields, "0")
	}
	fields = append(fields, starttime, "0", "0") // 22 starttime + tail
	stat := strings.Join(fields, " ") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	return dir
}
