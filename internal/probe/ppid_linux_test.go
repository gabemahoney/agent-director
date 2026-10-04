//go:build linux

package probe_test

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// TestLinuxParentPIDRealProc: the production reader reports this process's
// parent and a child's parent (this process) through the real /proc, and
// answers unreadable once the child is reaped.
func TestLinuxParentPIDRealProc(t *testing.T) {
	skipWithoutProc(t)
	r := probe.NewParentPIDReader()

	if got, ok := r.PPID(os.Getpid()); !ok || got != os.Getppid() {
		t.Errorf("PPID(self) = (%d, %v); want (%d, true)", got, ok, os.Getppid())
	}
	cmd := startBareChild(t)
	pid := cmd.Process.Pid
	if got, ok := r.PPID(pid); !ok || got != os.Getpid() {
		t.Errorf("PPID(child %d) = (%d, %v); want (%d, true)", pid, got, ok, os.Getpid())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_, _ = cmd.Process.Wait()
	if got, ok := r.PPID(pid); got != 0 || ok {
		t.Errorf("PPID(reaped child %d) = (%d, %v); want (0, false)", pid, got, ok)
	}
}
