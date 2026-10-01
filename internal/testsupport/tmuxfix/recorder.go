// Package tmuxfix holds the in-process tmux fixtures (SRD SR-20.3, SR-20.4):
// the Recorder, a fake of the pkg/api tmux client interface that tests inject
// via Options.TmuxClient; the shared test Clock; and the replay catalogue of
// recorded tmux replies (replay*.go).
//
// The Recorder models tmux at the level of internal/tmux's typed API
// (Appendix F.1): per-socket session tables answer the socket-taking calls
// (recorder_table.go, recorder_calls.go), tests script a typed result per
// call kind (recorder_script.go), and session hooks, after-call hooks and
// virtual time sit around each call (recorder_hooks.go). It never produces
// or parses tmux reply text. The one name-based method, HasSession, only
// records its calls and returns its scripted answer, as before.
package tmuxfix

import (
	"sync"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// CallKind identifies which tmux method was invoked.
type CallKind string

const (
	CallHasSession CallKind = "HasSession"
)

// Call records a single invocation of a name-based tmux method and its
// arguments.
type Call struct {
	// Kind is the name of the method that was called.
	Kind CallKind

	// Name is the session name passed to HasSession.
	Name string
}

// Recorder is a fake that satisfies the pkg/api TmuxClient interface. The
// name-based HasSession is a no-op that records every invocation (Calls,
// CallsOfKind). The socket-taking methods, with exactly *tmux.Client's
// signatures, are answered from per-socket session tables or from scripted
// typed results, and are recorded separately (SocketCalls, SocketCallsOf).
//
// Recorder is safe for concurrent use.
type Recorder struct {
	mu    sync.Mutex
	calls []Call

	// Socket-taking side (SR-20.3): tables, scripts, recorded calls, hooks
	// and virtual time. See recorder_table.go and its siblings.
	sockets     map[string]*socketState
	servers     []*serverState // every server ever bound, in binding order
	scripts     []*scriptState
	socketCalls []SocketCall
	changes     []*sessionChange
	afterHooks  []afterHook
	captures    map[paneKey]string
	clock       *Clock
	timeouts    tmux.Timeouts
	nextPID     int // last auto-assigned server or pane pid

	// hasSessionResult is the scripted return value for HasSession.
	// Defaults to false.
	hasSessionResult bool
}

// NewRecorder returns a new *Recorder with no recorded calls and default
// (empty/false) scripted responses.
func NewRecorder() *Recorder {
	return &Recorder{}
}

// WithHasSession configures the bool that HasSession returns.
// Returns the receiver for chaining.
func (r *Recorder) WithHasSession(v bool) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hasSessionResult = v
	return r
}

// Calls returns a copy of all recorded invocations in call order.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Call, len(r.calls))
	copy(out, r.calls)
	return out
}

// CallsOfKind returns all recorded calls whose Kind matches kind.
func (r *Recorder) CallsOfKind(kind CallKind) []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Call
	for _, c := range r.calls {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// Reset discards all recorded calls (name-based and socket-taking), resets
// HasSession's scripted answer to false and discards every scripted
// typed result. Session tables, capture texts, hooks and virtual time stay.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = r.calls[:0]
	r.hasSessionResult = false
	r.socketCalls = nil
	r.scripts = nil
}

// HasSession records a HasSession call and returns the scripted result.
func (r *Recorder) HasSession(name string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, Call{Kind: CallHasSession, Name: name})
	return r.hasSessionResult, nil
}
