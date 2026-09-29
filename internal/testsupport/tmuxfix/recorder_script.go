package tmuxfix

import "github.com/gabemahoney/agent-director/internal/tmux"

// Scripted typed results of the socket-taking calls (SRD SR-20.3): per call
// kind and socket, a test replaces the table's answer with a typed failure,
// once, N times or always. No reply text is involved: a script names the
// tmux.Failure and the CallError fields the production client would fill.

// AnySocket, as Script's socket, matches a call on every socket.
const AnySocket = ""

// Script is one scripted typed result.
type Script struct {
	// Failure is the call's failure; 0 answers from the table (it still
	// counts as one use of the script).
	Failure tmux.Failure
	// FirstLine is CallError.FirstLine (FailUnrecognized in production).
	FirstLine string
	// Socket is CallError.Socket for FailSocketDenied, FailNoServer and
	// FailNoSocket; "" takes the call's socket.
	Socket string
	// ExitStatus is CallError.ExitStatus. It is -1 for FailTimeout and
	// FailUnavailable whatever is given, and a 0 becomes 1 for every other
	// failure except FailUnrecognized, where 0 is the exit-0 unparseable
	// case (SR-3.5, SR-9.4).
	ExitStatus int
	// HadStdout is CallError.HadStdout; FailLabel always has it.
	HadStdout bool
	// Applied makes the call's table effect happen before the failure is
	// returned: a create adds its session (labelled by the chain rule), a
	// kill removes its target, a label by id sets the label. Without it a
	// failed call changes nothing. A FailLabel create always adds its
	// session, unlabelled, and returns its reply with the error.
	Applied bool
	// Times is how many matching calls the script answers; 0 answers every
	// one.
	Times int
}

// scriptState is a registered script and its remaining uses.
type scriptState struct {
	socket string
	call   tmux.Call
	s      Script
	left   int // remaining uses when s.Times > 0
}

// Script registers s for every call kind given on socket (AnySocket for any
// socket). Scripts answer in registration order: the first one that matches
// and has uses left answers the call, so an always-script shadows every
// later one for its call kind and socket. For SendKeysPane, script
// tmux.CallSendText and tmux.CallSendEnter separately; a failed text call
// makes no Enter call.
func (r *Recorder) Script(socket string, s Script, calls ...tmux.Call) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range calls {
		r.scripts = append(r.scripts, &scriptState{socket: socket, call: c, s: s, left: s.Times})
	}
	return r
}

// takeScript returns the script answering a call of kind call on socket and
// uses it up once. Callers hold r.mu.
func (r *Recorder) takeScript(socket string, call tmux.Call) (Script, bool) {
	for _, sc := range r.scripts {
		if sc.call != call || (sc.socket != AnySocket && sc.socket != socket) {
			continue
		}
		if sc.s.Times == 0 {
			return sc.s, true
		}
		if sc.left > 0 {
			sc.left--
			return sc.s, true
		}
	}
	return Script{}, false
}

// scriptedError is the *tmux.CallError a scripted failure gives for c.
// Callers hold r.mu.
func (r *Recorder) scriptedError(c SocketCall, s Script) *tmux.CallError {
	e := &tmux.CallError{Call: c.Call, Failure: s.Failure, FirstLine: s.FirstLine,
		ExitStatus: s.ExitStatus, HadStdout: s.HadStdout}
	switch s.Failure {
	case tmux.FailTimeout:
		e.Timeout, e.ExitStatus = r.classTimeout(c.Call), -1
	case tmux.FailUnavailable:
		e.ExitStatus = -1
	case tmux.FailSocketDenied, tmux.FailNoServer, tmux.FailNoSocket:
		e.Socket = s.Socket
		if e.Socket == "" {
			e.Socket = c.Socket
		}
	case tmux.FailLabel:
		e.HadStdout = true
	}
	if e.ExitStatus == 0 && s.Failure != tmux.FailUnrecognized {
		e.ExitStatus = 1
	}
	return e
}
