package tmux_test

import (
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Shared scripted-runner fixture for the external-package replay, parse and
// argv tests: a client over a runner that records every invocation and
// answers each one from a script.

// testSocket is the socket path the scripted tests pass to every call.
const testSocket = "/tmp/tmux-1000/default"

// testTimeouts has a distinct value per call class so the class applied to an
// invocation is observable from its recorded Timeout.
var testTimeouts = tmux.Timeouts{
	Query:     1 * time.Second,
	Action:    2 * time.Second,
	Create:    3 * time.Second,
	WaitDelay: 4 * time.Millisecond,
}

// scriptedRunner answers invocations in order from its script and records
// each one. An invocation past the end of the script fails the test.
type scriptedRunner struct {
	t      testing.TB
	mu     sync.Mutex
	script []tmux.RunResult
	calls  []tmux.Invocation
}

// run is the tmux.Runner the client calls.
func (s *scriptedRunner) run(inv tmux.Invocation) tmux.RunResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.calls)
	s.calls = append(s.calls, inv)
	if n >= len(s.script) {
		s.t.Errorf("unscripted tmux call %d: %q", n+1, inv.Args)
		return notStarted()
	}
	return s.script[n]
}

// Calls returns a copy of every invocation recorded so far, in order.
func (s *scriptedRunner) Calls() []tmux.Invocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tmux.Invocation(nil), s.calls...)
}

// Only returns the single recorded invocation, failing the test otherwise.
func (s *scriptedRunner) Only() tmux.Invocation {
	s.t.Helper()
	calls := s.Calls()
	if len(calls) != 1 {
		s.t.Fatalf("want exactly 1 tmux call, got %d: %v", len(calls), calls)
	}
	return calls[0]
}

// newScripted builds a client with testTimeouts whose socket-taking calls are
// answered, in order, by script.
func newScripted(t testing.TB, script ...tmux.RunResult) (*tmux.Client, *scriptedRunner) {
	return newScriptedWith(t, "", testTimeouts, script...)
}

// newScriptedWith is newScripted with a chosen binary and timeouts.
func newScriptedWith(t testing.TB, binary string, to tmux.Timeouts, script ...tmux.RunResult) (*tmux.Client, *scriptedRunner) {
	s := &scriptedRunner{t: t, script: script}
	return tmux.NewWithRunner(binary, to, s.run), s
}

// newReplay builds a client with testTimeouts whose calls are answered, in
// order, by the recorded bytes and exit status of the catalogue entries.
func newReplay(t testing.TB, entries ...tmuxfix.Entry) (*tmux.Client, *scriptedRunner) {
	return newScripted(t, replayed(entries...)...)
}

// replayed turns catalogue entries into script steps, to mix with the
// timeout, exec-failure and pipe-close-wait steps below.
func replayed(entries ...tmuxfix.Entry) []tmux.RunResult {
	out := make([]tmux.RunResult, len(entries))
	for i, e := range entries {
		out[i] = e.Result()
	}
	return out
}

// exited answers with a process that exited on its own.
func exited(status int, stdout, stderr string) tmux.RunResult {
	return tmux.RunResult{Status: tmux.RunExited, ExitStatus: status, Stdout: []byte(stdout), Stderr: []byte(stderr)}
}

// exitZero answers with an exit 0 printing stdout and nothing on stderr.
func exitZero(stdout string) tmux.RunResult { return exited(0, stdout, "") }

// pipesCut answers with an exit 0 whose pipes the pipe-close wait closed.
func pipesCut(stdout string) tmux.RunResult {
	return tmux.RunResult{Status: tmux.RunPipesCut, ExitStatus: 0, Stdout: []byte(stdout)}
}

// timedOut answers with a client terminated at its class timeout.
func timedOut() tmux.RunResult { return tmux.RunResult{Status: tmux.RunTimedOut, ExitStatus: -1} }

// notStarted answers with a binary that could not be run.
func notStarted() tmux.RunResult { return tmux.RunResult{Status: tmux.RunNotStarted, ExitStatus: -1} }
