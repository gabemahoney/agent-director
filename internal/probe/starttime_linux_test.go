//go:build linux

package probe_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstat"
)

// TestLinuxStartTimeRealProcLifecycle drives the production reader over the
// real /proc: a child with no AGENT_DIRECTOR_* variable reads alive with its
// field 22 (stable across reads), then gone as a zombie and gone after reaping.
func TestLinuxStartTimeRealProcLifecycle(t *testing.T) {
	skipWithoutProc(t)
	pc := probe.NewProcChecker()
	cmd := startBareChild(t)
	pid := cmd.Process.Pid
	want := procstat.ReadStarttime(t, pid)

	assertStartTime(t, "first read", pc, pid, want, true, true)
	assertStartTime(t, "second read", pc, pid, want, true, true)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	awaitGone(t, pc, pid)
	// Not yet reaped, so the entry is still present: gone came from the zombie state.
	if _, err := os.Stat("/proc/" + strconv.Itoa(pid) + "/stat"); err != nil {
		t.Fatalf("zombie's /proc/%d/stat missing before reap: %v", pid, err)
	}

	_, _ = cmd.Process.Wait()
	assertStartTime(t, "after reap", pc, pid, "", false, true)
}

// TestLinuxStartTimeRealProcAbsentPid: pid_max is never a live pid, so it reads gone.
func TestLinuxStartTimeRealProcAbsentPid(t *testing.T) {
	skipWithoutProc(t)
	raw, err := os.ReadFile("/proc/sys/kernel/pid_max")
	if err != nil {
		t.Skipf("read pid_max: %v", err)
	}
	pidMax, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse pid_max %q: %v", raw, err)
	}
	assertStartTime(t, "pid_max", probe.NewProcChecker(), pidMax, "", false, true)
}

func skipWithoutProc(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("/proc not mounted; skipping")
	}
}
