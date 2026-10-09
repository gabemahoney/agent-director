//go:build linux

package probe_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// TestSelfPIDNamespaceRealProc (b.kdf): on Linux this process's pid namespace
// is the real /proc/self/ns/pid link's target, "pid:[<inode>]", known, and
// the same on every read.
func TestSelfPIDNamespaceRealProc(t *testing.T) {
	skipWithoutProc(t)
	want, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		t.Skipf("readlink /proc/self/ns/pid: %v", err)
	}
	for i := 0; i < 2; i++ {
		got, known := probe.SelfPIDNamespace()
		if got != want || !known || !regexp.MustCompile(`^pid:\[\d+\]$`).MatchString(got) {
			t.Errorf("read %d: SelfPIDNamespace = %q, %v; want %q, known", i+1, got, known, want)
		}
	}
}
