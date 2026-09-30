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
// or parses tmux reply text. The name-based methods (HasSession, SendKeys,
// CapturePane) only record their calls and return
// their two scripted answers, as before.
package tmuxfix

import (
	"sync"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// CallKind identifies which tmux method was invoked.
type CallKind string

const (
	CallHasSession  CallKind = "HasSession"
	CallSendKeys    CallKind = "SendKeys"
	CallCapturePane CallKind = "CapturePane"
)

// Call records a single invocation of a tmux method and its arguments.
type Call struct {
	// Kind is the name of the method that was called.
	Kind CallKind

	// Name is the session name passed to HasSession, SendKeys or
	// CapturePane.
	Name string

	// --- SendKeys fields ---

	// Text is the text argument of SendKeys.
	Text string
	// PressEnter is the pressEnter flag of SendKeys.
	PressEnter bool

	// --- CapturePane fields ---

	// NLines is the n_lines argument of CapturePane.
	NLines int
	// ANSI is the ansi flag of CapturePane.
	ANSI bool
}

// Recorder is a fake that satisfies the pkg/api TmuxClient interface. The
// name-based methods are no-ops that record every invocation (Calls,
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

	// paneOutput is the scripted response returned by CapturePane.
	// Defaults to empty string (no pane output) when not set.
	paneOutput string

	// hasSessionResult is the scripted return value for HasSession.
	// Defaults to false.
	hasSessionResult bool
}

// NewRecorder returns a new *Recorder with no recorded calls and default
// (empty/false) scripted responses.
func NewRecorder() *Recorder {
	return &Recorder{}
}

// WithPaneOutput sets the string that CapturePane returns for every call.
// Returns the receiver for chaining.
func (r *Recorder) WithPaneOutput(s string) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paneOutput = s
	return r
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
// the name-based scripted responses to defaults and discards every scripted
// typed result. Session tables, capture texts, hooks and virtual time stay.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = r.calls[:0]
	r.paneOutput = ""
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

// SendKeys records a SendKeys call and returns nil.
func (r *Recorder) SendKeys(name, text string, pressEnter bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, Call{
		Kind:       CallSendKeys,
		Name:       name,
		Text:       text,
		PressEnter: pressEnter,
	})
	return nil
}

// CapturePane records a CapturePane call and returns the scripted pane output.
func (r *Recorder) CapturePane(name string, nLines int, ansi bool) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, Call{
		Kind:   CallCapturePane,
		Name:   name,
		NLines: nLines,
		ANSI:   ansi,
	})
	return r.paneOutput, nil
}
