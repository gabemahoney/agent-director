package probe

// fakeproc_test.go — untagged fabricated-/proc fixtures (under a t.TempDir, never
// the real /proc) for the start-time and command-name reader tests on any OS.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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

// writeProcFile writes content as <root>/<pid>/<name> and returns the pid dir.
func writeProcFile(t *testing.T, root string, pid int, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return dir
}

// writeStatWithState writes <root>/<pid>/stat only (no environ) with the given
// field-3 state and starttime. Returns the pid dir.
func writeStatWithState(t *testing.T, root string, pid, ppid int, state, starttime string) string {
	t.Helper()
	return writeProcFile(t, root, pid, "stat", fakeStatLine(pid, ppid, state, starttime))
}

// writeFakeProc writes a fabricated <root>/<pid>/{stat,environ} pair with state
// "S". envVal, when non-empty, plants EnvKey=<envVal> in environ (NUL-separated,
// mixed with unrelated vars).
func writeFakeProc(t *testing.T, root string, pid, ppid int, starttime, envVal string) {
	t.Helper()
	writeStatWithState(t, root, pid, ppid, "S", starttime)
	environ := "PATH=/usr/bin\x00HOME=/home/x\x00"
	if envVal != "" {
		environ = "PATH=/usr/bin\x00" + EnvKey + "=" + envVal + "\x00HOME=/home/x\x00"
	}
	writeProcFile(t, root, pid, "environ", environ)
}
