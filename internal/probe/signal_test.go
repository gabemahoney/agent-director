//go:build linux || darwin

package probe_test

// signal_test.go: TerminateIdentity's real-process boundary (SR-6.4) on a
// test-owned child: only its own identity is signalled, and the production
// start-time reader reads it gone once reaped. It proves the OS primitive,
// not what an agent does on SIGTERM.

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

func TestTerminateIdentity(t *testing.T) {
	cmd := startBareChild(t)
	pid := cmd.Process.Pid
	pc := probe.NewProcChecker()
	start, alive, known := pc.StartTime(pid)
	if start == "" || !alive || !known {
		t.Fatalf("child StartTime(%d) = (%q, %v, %v); want its start time", pid, start, alive, known)
	}
	for _, tc := range []struct {
		name  string
		pid   int
		start string
		want  error
	}{
		{"another start time on the pid", pid, start + "1", probe.ErrSignalTargetGone},
		{"no start time", pid, "", probe.ErrSignalTargetUnverifiable},
		{"no pid", 0, start, probe.ErrSignalTargetUnverifiable},
	} {
		if err := probe.TerminateIdentity(tc.pid, tc.start); !errors.Is(err, tc.want) {
			t.Errorf("%s: TerminateIdentity = %v; want %v", tc.name, err, tc.want)
		}
	}
	assertStartTime(t, "after the mismatched signals", pc, pid, start, true, true)

	if err := probe.TerminateIdentity(pid, start); err != nil {
		t.Fatalf("TerminateIdentity(own identity): %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("child not reaped within 10 s of its SIGTERM")
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("child ended %v; want killed by SIGTERM", cmd.ProcessState)
	}
	assertStartTime(t, "after reaping", pc, pid, "", false, true)
	if err := probe.TerminateIdentity(pid, start); !errors.Is(err, probe.ErrSignalTargetGone) {
		t.Errorf("TerminateIdentity after reaping = %v; want ErrSignalTargetGone", err)
	}
}
