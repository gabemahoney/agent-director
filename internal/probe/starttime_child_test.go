//go:build linux || darwin

package probe_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// startBareChild starts `sleep 10` with every AGENT_DIRECTOR_* variable
// stripped from its environment; cleanup kills and reaps it.
func startBareChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "10")
	cmd.Env = []string{} // non-nil: a nil Env would inherit the test's environment
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AGENT_DIRECTOR_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

// assertStartTime fails unless StartTime(pid) answers exactly (start, alive, known).
func assertStartTime(t *testing.T, label string, pc probe.ProcChecker, pid int, start string, alive, known bool) {
	t.Helper()
	gs, ga, gk := pc.StartTime(pid)
	if gs != start || ga != alive || gk != known {
		t.Errorf("%s: StartTime(%d) = (%q, %v, %v); want (%q, %v, %v)", label, pid, gs, ga, gk, start, alive, known)
	}
}

// awaitGone polls StartTime(pid) for up to 2s until it answers gone.
func awaitGone(t *testing.T, pc probe.ProcChecker, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s, a, k := pc.StartTime(pid)
		if s == "" && !a && k {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("StartTime(%d) never read gone within 2s; last = (%q, %v, %v)", pid, s, a, k)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
