// Package tmuxfix provides a recording fake for the pkg/api tmux client
// interface. Tests inject a *Recorder via Options.TmuxClient to capture every
// tmux call without launching a real tmux process.
package tmuxfix

import "sync"

// CallKind identifies which tmux method was invoked.
type CallKind string

const (
	CallNewSession  CallKind = "NewSession"
	CallHasSession  CallKind = "HasSession"
	CallKillSession CallKind = "KillSession"
	CallSendKeys    CallKind = "SendKeys"
	CallCapturePane CallKind = "CapturePane"
)

// Call records a single invocation of a tmux method and its arguments.
type Call struct {
	// Kind is the name of the method that was called.
	Kind CallKind

	// --- NewSession fields ---

	// Name is the session name passed to NewSession, HasSession, KillSession,
	// SendKeys, or CapturePane.
	Name string
	// Cwd is the working directory passed to NewSession.
	Cwd string
	// Envs are the environment variables passed to NewSession.
	Envs map[string]string
	// Command is the argv passed to NewSession.
	Command []string

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

// Recorder is a recording fake that satisfies the pkg/api TmuxClient
// interface. All methods are no-ops that record every invocation.
// Use Calls() to inspect recorded calls in tests.
//
// Recorder is safe for concurrent use.
type Recorder struct {
	mu    sync.Mutex
	calls []Call

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

// Reset discards all recorded calls and resets scripted responses to defaults.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = r.calls[:0]
	r.paneOutput = ""
	r.hasSessionResult = false
}

// NewSession records a NewSession call and returns nil.
func (r *Recorder) NewSession(name, cwd string, envs map[string]string, command []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, Call{
		Kind:    CallNewSession,
		Name:    name,
		Cwd:     cwd,
		Envs:    envs,
		Command: command,
	})
	return nil
}

// HasSession records a HasSession call and returns the scripted result.
func (r *Recorder) HasSession(name string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, Call{Kind: CallHasSession, Name: name})
	return r.hasSessionResult, nil
}

// KillSession records a KillSession call and returns nil.
func (r *Recorder) KillSession(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, Call{Kind: CallKillSession, Name: name})
	return nil
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
