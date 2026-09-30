//go:build linux

package probe_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// TestLinuxCommandNameRealProc: the production reader names the test binary itself through the real /proc.
func TestLinuxCommandNameRealProc(t *testing.T) {
	skipWithoutProc(t)
	// The kernel's comm is the exec'd file's base name, cut to 15 bytes.
	want := filepath.Base(os.Args[0])
	if len(want) > 15 {
		want = want[:15]
	}

	got, ok := probe.NewCommandNameReader().CommandName(os.Getpid())
	if !ok || got != want {
		t.Errorf("CommandName(self) = (%q, %v); want (%q, true)", got, ok, want)
	}
}
