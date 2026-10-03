package tmuxfix

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Session hooks, after-call hooks and virtual time (SRD SR-20.3, Appendix
// F.5). They apply to the socket-taking calls only: the name-based
// HasSession is never charged and runs no hook. Each socket-taking call runs
// in this order: its table effect (or scripted result), then the
// virtual-time charge, then the session hooks, then the after-call hooks
// (outside the Recorder's lock, so they may call back into the Recorder or
// the store), then it returns to the caller.

// AfterCallHook is a test function run when a socket-taking call returns,
// before the caller sees the result: c is the recorded call and err its
// error (nil on success).
type AfterCallHook func(c SocketCall, err error)

// afterHook is a registered after-call hook.
type afterHook struct {
	call tmux.Call
	fn   AfterCallHook
}

// AnyCall, as AfterCall's call, matches a call of every kind: one hook then
// sees the calls in order across kinds (for example an agent's input box,
// changed by text, Enter and key sends alike).
const AnyCall tmux.Call = ""

// AfterCall registers fn to run once per call of kind call (AnyCall for
// every kind), on any socket, in registration order with the other hooks
// that call matches, for example to apply a SessionStart through the store's
// hook path when a verb's lookup returns (AC-KILL-15).
func (r *Recorder) AfterCall(call tmux.Call, fn AfterCallHook) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.afterHooks = append(r.afterHooks, afterHook{call: call, fn: fn})
	return r
}

// sessionChange is a pending session hook.
type sessionChange struct {
	after     tmux.Call
	socket    string
	sessionID string
	remove    bool
	label     tmux.Label
}

// ReplaceSessionAfter arranges that, when the next call of kind call on
// socket (AnySocket for any) returns, the session sessionID is replaced by a
// new one with the same stored name, a new session id, new panes (same
// window and pane indices, new pane ids and pids, no pane label), the
// current second as its creation time and label as its label (LabelNone:
// unset). The hook
// fires once, whatever the call's result; it does nothing if the session is
// gone by then.
func (r *Recorder) ReplaceSessionAfter(call tmux.Call, socket, sessionID string, label tmux.Label) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, &sessionChange{after: call, socket: socket, sessionID: sessionID, label: label})
	return r
}

// RemoveSessionAfter arranges that, when the next call of kind call on
// socket (AnySocket for any) returns, the session sessionID is removed. The
// hook fires once, whatever the call's result.
func (r *Recorder) RemoveSessionAfter(call tmux.Call, socket, sessionID string) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, &sessionChange{after: call, socket: socket, sessionID: sessionID, remove: true})
	return r
}

// WithVirtualTime binds the Recorder to c (Appendix F.5): every later
// socket-taking call advances c by its class's timeout from t, whatever its
// result (query: lookup and pane listing; action: kills, text, Enter and key
// sends, capture, label by id; create: the create; SR-13.1), and a scripted
// FailTimeout carries that same value. SendKeysPane's text and Enter calls
// are charged separately; t.WaitDelay is never charged. A zero field of t
// takes the internal/config default. Seeded sessions and servers take their
// default times from c. Without it the Recorder never touches a clock.
func (r *Recorder) WithVirtualTime(c *Clock, t tmux.Timeouts) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock, r.timeouts = c, t
	return r
}

// classTimeout is call's class timeout: the WithVirtualTime value, or for a
// zero field the default of a zero config.Tmux (no duration is spelled
// here). Callers hold r.mu.
func (r *Recorder) classTimeout(call tmux.Call) time.Duration {
	var given, def time.Duration
	switch call {
	case tmux.CallLookup, tmux.CallListPanes:
		given, def = r.timeouts.Query, config.Tmux{}.EffectiveQueryTimeout()
	case tmux.CallCreate:
		given, def = r.timeouts.Create, config.Tmux{}.EffectiveCreateTimeout()
	default:
		given, def = r.timeouts.Action, config.Tmux{}.EffectiveActionTimeout()
	}
	if given != 0 {
		return given
	}
	return def
}

// afterCall applies the virtual-time charge and the session hooks for the
// call c that just returned, and returns the after-call hooks to run.
// Callers hold r.mu.
func (r *Recorder) afterCall(c SocketCall) []AfterCallHook {
	if r.clock != nil {
		r.clock.Advance(r.classTimeout(c.Call))
	}
	kept := r.changes[:0]
	for _, ch := range r.changes {
		if ch.after != c.Call || (ch.socket != AnySocket && ch.socket != c.Socket) {
			kept = append(kept, ch)
			continue
		}
		r.applyChange(c.Socket, ch)
	}
	r.changes = kept
	var hooks []AfterCallHook
	for _, h := range r.afterHooks {
		if h.call == c.Call || h.call == AnyCall {
			hooks = append(hooks, h.fn)
		}
	}
	return hooks
}

// applyChange applies one session hook on socket. Callers hold r.mu.
func (r *Recorder) applyChange(socket string, ch *sessionChange) {
	srv := r.socket(socket).server
	if srv == nil {
		return
	}
	old := srv.findSession(ch.sessionID)
	if old == nil {
		return
	}
	srv.removeSession(old.id)
	if ch.remove {
		return
	}
	panes := make([]SeedPane, 0, len(old.panes))
	for _, p := range old.panes {
		panes = append(panes, SeedPane{Window: p.Window, Index: p.Index})
	}
	r.addSession(srv, SeedSession{Name: old.name, Label: ch.label, Panes: panes})
}
