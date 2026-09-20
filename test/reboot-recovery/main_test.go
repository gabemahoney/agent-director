// Package rebootrecovery_test is the end-to-end reboot-recovery acceptance test
// for agent-director (Part E, Epic t1.93m.5j). It exercises the plan's culminating
// flow against REAL tmux and a stub `claude` on PATH:
//
//	spawn (with extra_env CLAUDE_CONFIG_DIR=<custom>) → kill the tmux server and
//	every stub process → ONE find-missing marks every dead row missing with no
//	refusal → resume succeeds with the custom config dir restored and the
//	transcript continuing the pre-kill session.
//
// It must run ONLY inside the sandbox container: the tests build and exec the
// real binary and open agent-director state, which can rewrite the developer's
// real ~/.agent-director on the host (b.8dr). TestMain enforces the sandbox
// marker via sandboxguard and additionally skips cleanly when tmux is absent
// from PATH (defense-in-depth — the deny rules already block host runs). The
// sandbox image installs tmux + procps, and internal/probe reads /proc, so
// find-missing works against real processes there.
package rebootrecovery_test

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// tmuxAvailable is set by TestMain: true iff `tmux` resolves on PATH. Every
// test skips cleanly when it is false so an off-sandbox invocation that somehow
// bypasses the sandbox marker still does not hard-fail.
var tmuxAvailable bool

func TestMain(m *testing.M) {
	sandboxguard.Require()
	if _, err := exec.LookPath("tmux"); err == nil {
		tmuxAvailable = true
	}
	// Become a child-subreaper so the tmux server + stub panes (started by the
	// agent-director CLI subprocess, our grandchildren) reparent to THIS process
	// when the CLI exits — not to the pid-namespace init. That lets the test
	// reap them with Wait4 after the kill step, so a SIGKILLed pane's /proc
	// entry disappears (ENOENT) instead of lingering as a <defunct> zombie that
	// still carries a matching starttime. Without this, the container's init is
	// the go-test-driving bash, which does not process SIGCHLD until the test
	// returns, and zombies would keep find-missing's checker from seeing the
	// provably-dead ENOENT the acceptance describes. Best-effort: a failure just
	// falls back to parent-kill reaping.
	_ = prSetChildSubreaper()
	os.Exit(m.Run())
}

// prSetChildSubreaper sets PR_SET_CHILD_SUBREAPER(1) via prctl.
func prSetChildSubreaper() error {
	const prSetChildSubreaper = 36 // linux/prctl.h
	_, _, errno := syscall.Syscall(syscall.SYS_PRCTL, uintptr(prSetChildSubreaper), 1, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// reapZombies non-blockingly wait()s for every currently-exited reparented
// child (WNOHANG), clearing <defunct> entries from /proc. Because this process
// is a child-subreaper, the tmux server + stub panes reparent here when the CLI
// subprocess exits, so their post-SIGKILL zombies are waitable here. Synchronous
// runCLI calls have already reaped their own direct children via cmd.Wait before
// this runs, so there is no contention with os/exec's wait. Returns when no
// child is currently reapable.
func reapZombies() {
	var ws syscall.WaitStatus
	for {
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

// requireTmux skips the calling test when tmux is not on PATH.
func requireTmux(t *testing.T) {
	t.Helper()
	if !tmuxAvailable {
		t.Skip("tmux not on PATH; reboot-recovery E2E requires real tmux (sandbox image installs it)")
	}
}
