package probe

// fakeproc_test.go — UNTAGGED fabricated-/proc fixtures shared across the probe
// test suite.
//
// These builders write a Linux-format <root>/<pid>/{stat,environ} tree under a
// caller-supplied root (a t.TempDir), never the real /proc. The Linux readers
// (the checker linuxChecker, the start-time reader and the command-name
// reader) take an INJECTABLE procRoot, and their parsing/verdict logic is
// build-tag-free, so the tests that drive them over a fabricated tree compile
// and run on ANY OS. This file therefore carries NO //go:build linux tag, so
// the tag-free verdict-table tests in checker_linux_core_test.go compile
// off-linux (e.g. `GOOS=darwin go vet ./internal/probe/`).
//
// Only tests that require the REAL /proc mount stay under the linux tag.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeStatDefaultState is the field-3 state the state-less helpers write.
const fakeStatDefaultState = "S"

// fakeStatLine builds a stat line: pid, a comm with ')' and spaces, state
// (field 3, written verbatim so malformed tokens are possible), ppid, and starttime at field 22.
func fakeStatLine(pid, ppid int, state, starttime string) string {
	fields := make([]string, 0, 24)
	fields = append(fields, strconv.Itoa(pid))       // 1
	fields = append(fields, "(claude (weird) proc)") // 2 comm w/ ')' and spaces
	fields = append(fields, state)                   // 3 state
	fields = append(fields, strconv.Itoa(ppid))      // 4 ppid
	for f := 5; f <= 21; f++ {                       // 5..21 filler
		fields = append(fields, "0")
	}
	fields = append(fields, starttime) // 22 starttime
	fields = append(fields, "0", "0")  // tail
	return strings.Join(fields, " ") + "\n"
}

// writeStatWithState writes <root>/<pid>/stat only (no environ) with the given
// field-3 state and starttime. Returns the pid dir.
func writeStatWithState(t *testing.T, root string, pid, ppid int, state, starttime string) string {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	stat := fakeStatLine(pid, ppid, state, starttime)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	return dir
}

// writeFakeProc writes a fabricated <root>/<pid>/{stat,environ} pair with state
// "S". envVal, when non-empty, plants EnvKey=<envVal> in environ (NUL-separated,
// mixed with an unrelated var).
func writeFakeProc(t *testing.T, root string, pid, ppid int, starttime, envVal string) {
	t.Helper()
	dir := writeStatWithState(t, root, pid, ppid, fakeStatDefaultState, starttime)

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

// writeStatOnly writes <root>/<pid>/stat only (no environ, state "S") — for an
// environ-read failure (ENOENT) after a matched stat. Returns the pid dir.
func writeStatOnly(t *testing.T, root string, pid, ppid int, starttime string) string {
	t.Helper()
	return writeStatWithState(t, root, pid, ppid, fakeStatDefaultState, starttime)
}
