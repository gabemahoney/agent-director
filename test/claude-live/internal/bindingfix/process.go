// Package bindingfix supplies harmless owned processes for offline binding and
// cleanup regressions. It never starts a provider or a tmux server.
// Binding and cleanup tests must reuse Child for real PID/start identity and
// exact-child teardown; see the reuse instructions in docs/architecture.md.
package bindingfix

import (
	"os/exec"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// Child owns one harmless process and its once-only teardown. Future binding
// and cleanup tests must reuse it for real PID/start identity, as required by
// docs/architecture.md; it grants no provider or unrelated-process ownership.
func Child(t testing.TB) (int, string, func()) {
	t.Helper()
	c := exec.Command("/bin/sleep", "600")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { _ = c.Process.Kill(); _ = c.Wait() }) }
	t.Cleanup(stop)
	start, alive, known := probe.NewProcChecker().StartTime(c.Process.Pid)
	if !alive || !known || start == "" {
		t.Fatal("fixture child process identity unavailable")
	}
	return c.Process.Pid, start, stop
}
