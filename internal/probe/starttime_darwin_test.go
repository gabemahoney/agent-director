//go:build darwin

package probe_test

import (
	"fmt"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// TestDarwinStartTimeRealSysctlLifecycle drives the production reader over the
// real KERN_PROC_PID sysctl: a child with no AGENT_DIRECTOR_* variable reads
// alive with its x/sys-decoded p_starttime (stable across reads), then gone as
// a zombie (x/sys confirms p_stat SZOMB) and gone after reaping.
func TestDarwinStartTimeRealSysctlLifecycle(t *testing.T) {
	pc := probe.NewProcChecker()
	cmd := startBareChild(t)
	pid := cmd.Process.Pid

	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		t.Fatalf("SysctlKinfoProc(%d): %v", pid, err)
	}
	// The canonical stored form "<tv_sec>.<tv_usec>".
	want := fmt.Sprintf("%d.%d", kp.Proc.P_starttime.Sec, kp.Proc.P_starttime.Usec)
	assertStartTime(t, "first read", pc, pid, want, true, true)
	assertStartTime(t, "second read", pc, pid, want, true, true)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	awaitGone(t, pc, pid)
	// Not yet reaped: the kernel still lists the pid, as a zombie.
	if kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid); err != nil {
		t.Fatalf("SysctlKinfoProc(%d) before reap: %v", pid, err)
	} else if kp.Proc.P_stat != 5 { // SZOMB
		t.Fatalf("p_stat before reap = %d; want 5 (SZOMB)", kp.Proc.P_stat)
	}

	_, _ = cmd.Process.Wait()
	assertStartTime(t, "after reap", pc, pid, "", false, true)
}

// TestDarwinStartTimeRealSysctlAbsentPid: XNU pids never exceed PID_MAX
// (99999), so 100000 reads gone.
func TestDarwinStartTimeRealSysctlAbsentPid(t *testing.T) {
	assertStartTime(t, "pid 100000", probe.NewProcChecker(), 100000, "", false, true)
}
