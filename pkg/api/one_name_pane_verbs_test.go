package api_test

// one_name_pane_verbs_test.go holds the pane verbs' SR-1.5 one-name rows
// (read-pane, send-keys and pause), each driven on the
// kill fixture with tmuxfix.Recorder failure kinds and checked by
// assertOneName through oneNameRows (one_name_per_error_test.go).

import (
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// oneNameReadPane is a row that runs Client.ReadPane on a row seeded with
// spec after setup (when set) prepares e and r.
func oneNameReadPane(name, want string, spec killRowSpec, setup func(t *testing.T, e *killEnv, r *killRow)) oneNameRow {
	return oneNameRow{name: "read-pane/" + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedRow(t, spec)
		if setup != nil {
			setup(t, e, &r)
		}
		_, err := e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID})
		return err
	}}
}

// actionFails makes r's first call of kind call (a capture, text or Enter)
// fail other than by a timeout, then runs then (when set) to shape what the
// one follow-up lookup finds.
func actionFails(call tmux.Call, then func(t *testing.T, e *killEnv, r *killRow)) func(*testing.T, *killEnv, *killRow) {
	return func(t *testing.T, e *killEnv, r *killRow) {
		scriptKill(call, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1})(t, e, r)
		if then != nil {
			then(t, e, r)
		}
	}
}

// captureFails is actionFails on the capture.
func captureFails(then func(t *testing.T, e *killEnv, r *killRow)) func(*testing.T, *killEnv, *killRow) {
	return actionFails(tmux.CallCapture, then)
}

// rebindServer binds a new server on r's socket (a different server, SR-3.3)
// holding only a bystander session.
func rebindServer(t *testing.T, e *killEnv, r *killRow) {
	e.rec.RebindServer(r.Socket, tmuxfix.Server{})
	e.syncServers()
	e.seedBystander(t, r.Socket)
}

// oneNameReadPaneRows are read-pane's tmux-caused errors (SR-7.2, SR-7.3):
// its gone error, each ErrTmuxSessionConflict refusal, each
// ErrTmuxNotAvailable and ErrTmuxUnresponsive cause, and each follow-up
// outcome of a failed capture.
func oneNameReadPaneRows() []oneNameRow {
	lookup, timeout := tmux.CallLookup, tmuxfix.Script{Failure: tmux.FailTimeout}
	noSession := killRowSpec{NoSession: true}
	lostReply := killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true}
	gone, conflict := "ErrTmuxCaptureFailed", "ErrTmuxSessionConflict"
	unavailable, unresponsive := "ErrTmuxNotAvailable", "ErrTmuxUnresponsive"
	return []oneNameRow{
		oneNameReadPane("session not there", gone, noSession, func(t *testing.T, e *killEnv, r *killRow) {
			e.ensureServer(r)
			e.seedBystander(t, r.Socket)
		}),
		oneNameReadPane("only another store's session names the row", gone, noSession,
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.otherStore(r.Token), true))
			}),
		oneNameReadPane("capture failed, follow-up gone", gone, killRowSpec{},
			captureFails(func(t *testing.T, e *killEnv, r *killRow) {
				e.seedBystander(t, r.Socket)
				e.rec.RemoveSessionAfter(tmux.CallCapture, r.Socket, r.Session.ID)
			})),
		oneNameReadPane("capture failed, follow-up leftover", gone, killRowSpec{},
			captureFails(func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.ReplaceSessionAfter(tmux.CallCapture, r.Socket, r.Session.ID, r.old())
			})),
		oneNameReadPane("agent's pane not found", conflict, noSession, func(t *testing.T, e *killEnv, r *killRow) {
			e.seedOurs(t, r)
		}),
		oneNameReadPane("lost reply, no pane carries the row's token", conflict, lostReply,
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedOurs(t, r)
			}),
		oneNameReadPane("lone leftover, no pane carries its token", conflict, noSession,
			func(t *testing.T, e *killEnv, r *killRow) {
				e.ensureServer(r)
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "leftover-" + uuid.NewString()[:8], Label: r.old()})
			}),
		oneNameReadPane("two leftovers", conflict, noSession, func(t *testing.T, e *killEnv, r *killRow) {
			e.seedLeftover(t, *r, newToken())
			e.seedLeftover(t, *r, newToken())
		}),
		oneNameReadPane("conflicting labels: scope value", conflict, killRowSpec{},
			func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
			}),
		oneNameReadPane("conflicting labels: duplicate label", conflict, killRowSpec{},
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
			}),
		oneNameReadPane("different server", unavailable, killRowSpec{}, rebindServer),
		oneNameReadPane("binary unavailable", unavailable, killRowSpec{},
			scriptKill(lookup, tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameReadPane("socket permission", unavailable, killRowSpec{},
			scriptKill(lookup, tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameReadPane("capture failed, follow-up different server", unavailable, killRowSpec{},
			captureFails(func(t *testing.T, e *killEnv, r *killRow) {
				e.rec.AfterCall(tmux.CallCapture, func(tmuxfix.SocketCall, error) { rebindServer(t, e, r) })
			})),
		oneNameReadPane("capture failed, follow-up binary unavailable", unavailable, killRowSpec{},
			captureFails(scriptKill(lookup, tmuxfix.Script{Times: 1}, tmuxfix.Script{Failure: tmux.FailUnavailable}))),
		oneNameReadPane("lookup timeout", unresponsive, killRowSpec{}, scriptKill(lookup, timeout)),
		oneNameReadPane("lookup unrecognised reply", unresponsive, killRowSpec{}, scriptKill(lookup,
			tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: callTableFirstLine(), ExitStatus: 1})),
		oneNameReadPane("pane-listing timeout", unresponsive, killRowSpec{}, scriptKill(tmux.CallListPanes, timeout)),
		oneNameReadPane("capture timeout", unresponsive, killRowSpec{}, scriptKill(tmux.CallCapture, timeout)),
		oneNameReadPane("capture failed, follow-up finds the session", unresponsive, killRowSpec{}, captureFails(nil)),
		oneNameReadPane("capture failed, follow-up timeout", unresponsive, killRowSpec{},
			captureFails(scriptKill(lookup, tmuxfix.Script{Times: 1}, timeout))),
	}
}

// oneNameSendKeys is a row that runs Client.SendKeys (AllowPending on a
// pending row) on a row seeded with spec(e) after setup (when set) prepares e and r.
func oneNameSendKeys(name, want string, spec func(e *killEnv) killRowSpec, setup func(t *testing.T, e *killEnv, r *killRow)) oneNameRow {
	return oneNameRow{name: "send-keys/" + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		s := spec(e)
		r := e.seedRow(t, s)
		if setup != nil {
			setup(t, e, &r)
		}
		_, _, err := e.sendKeysClient(t, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hello",
			AllowPending: s.State == store.StatePending})
		return err
	}}
}

// liveSpec is spec whatever the fixture.
func liveSpec(spec killRowSpec) func(*killEnv) killRowSpec {
	return func(*killEnv) killRowSpec { return spec }
}

// pendingNoSession is a fresh spawn's pending row with opts and no session
// seeded (a row with no launch token cannot be given one).
func pendingNoSession(opts ...apitest.SpawnOption) func(*killEnv) killRowSpec {
	return func(e *killEnv) killRowSpec {
		spec := e.pendingSpec(pendingFresh, pendingOurs, opts...)
		spec.NoSession = true
		return spec
	}
}

// seedOld seeds r's session under this store's label of an earlier launch (a leftover).
func seedOld(t *testing.T, e *killEnv, r *killRow) {
	e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
}

// followUpGone makes r's first call of kind call fail and r's session go
// with it, so the one follow-up lookup finds it Gone.
func followUpGone(call tmux.Call) func(*testing.T, *killEnv, *killRow) {
	return actionFails(call, func(t *testing.T, e *killEnv, r *killRow) {
		e.seedBystander(t, r.Socket)
		e.rec.RemoveSessionAfter(call, r.Socket, r.Session.ID)
	})
}

// oneNameSendKeysRows are send-keys' returned errors (SR-7.1 to SR-7.3): the
// pending refusals before and after the lookup, its gone error, each
// ErrTmuxSessionConflict refusal, each ErrTmuxNotAvailable and
// ErrTmuxUnresponsive cause, and each follow-up outcome of a failed text or Enter call.
func oneNameSendKeysRows() []oneNameRow {
	lookup, text, enter := tmux.CallLookup, tmux.CallSendText, tmux.CallSendEnter
	timeout := tmuxfix.Script{Failure: tmux.FailTimeout}
	live, noSession := liveSpec(killRowSpec{}), liveSpec(killRowSpec{NoSession: true})
	lostReply := liveSpec(killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true})
	pendingLeft := func(e *killEnv) killRowSpec { return e.pendingSpec(pendingFresh, pendingLeftover) }
	notInteractive, gone, conflict := "ErrSpawnNotInteractive", "ErrTmuxSendKeys", "ErrTmuxSessionConflict"
	unavailable, unresponsive := "ErrTmuxNotAvailable", "ErrTmuxUnresponsive"
	return []oneNameRow{
		oneNameSendKeys("pending, leftover only", notInteractive, pendingLeft, seedOld),
		oneNameSendKeys("pending, no launch start", notInteractive, pendingNoSession(apitest.WithNoLaunchStartedAt()), nil),
		oneNameSendKeys("pending, unreadable launch start", notInteractive,
			pendingNoSession(apitest.WithRawLaunchStartedAt("not a time")), nil),
		oneNameSendKeys("pending, no launch token", notInteractive, pendingNoSession(apitest.WithNoLaunchToken()), nil),
		oneNameSendKeys("session not there", gone, noSession, func(t *testing.T, e *killEnv, r *killRow) {
			e.ensureServer(r)
			e.seedBystander(t, r.Socket)
		}),
		oneNameSendKeys("text failed, follow-up gone", gone, live, followUpGone(text)),
		oneNameSendKeys("text failed, follow-up leftover", gone, live, actionFails(text, func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.ReplaceSessionAfter(text, r.Socket, r.Session.ID, r.old())
		})),
		oneNameSendKeys("Enter failed, follow-up gone", gone, live, followUpGone(enter)),
		oneNameSendKeys("agent's pane not found", conflict, noSession, func(t *testing.T, e *killEnv, r *killRow) {
			e.seedOurs(t, r)
		}),
		oneNameSendKeys("lost reply, no pane carries the row's token", conflict, lostReply,
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedOurs(t, r)
			}),
		oneNameSendKeys("live row, leftover only", conflict, noSession, seedOld),
		oneNameSendKeys("conflicting labels: scope value", conflict, live, func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}),
		oneNameSendKeys("different server", unavailable, live, rebindServer),
		oneNameSendKeys("binary unavailable", unavailable, live, scriptKill(lookup, tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNameSendKeys("socket permission", unavailable, live, scriptKill(lookup, tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNameSendKeys("text failed, follow-up different server", unavailable, live,
			actionFails(text, func(t *testing.T, e *killEnv, r *killRow) {
				e.rec.AfterCall(text, func(tmuxfix.SocketCall, error) { rebindServer(t, e, r) })
			})),
		oneNameSendKeys("lookup timeout", unresponsive, live, scriptKill(lookup, timeout)),
		oneNameSendKeys("pane-listing timeout", unresponsive, live, scriptKill(tmux.CallListPanes, timeout)),
		oneNameSendKeys("text timeout", unresponsive, live, scriptKill(text, timeout)),
		oneNameSendKeys("Enter timeout", unresponsive, live, scriptKill(enter, timeout)),
		oneNameSendKeys("text failed, follow-up finds the session", unresponsive, live, actionFails(text, nil)),
		oneNameSendKeys("Enter failed, follow-up finds the session", unresponsive, live, actionFails(enter, nil)),
	}
}

// oneNamePause is a row that runs Client.Pause on a row seeded with spec
// after setup (when set) prepares e and r.
func oneNamePause(name, want string, spec killRowSpec, setup func(t *testing.T, e *killEnv, r *killRow)) oneNameRow {
	return oneNameRow{name: "pause/" + name, want: want, run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedRow(t, spec)
		if setup != nil {
			setup(t, e, &r)
		}
		_, _, err := e.pauseClient(t, pauseParams(r))
		return err
	}}
}

// oneNamePauseTimeout is pause's wait running out: /exit delivered to a row
// nothing ends, through api.Pause with the fixture's pauseTimeoutSeconds.
func oneNamePauseTimeout() oneNameRow {
	return oneNameRow{name: "pause/wait timed out", want: "ErrPauseTimeout", run: func(t *testing.T) error {
		e := newKillEnv(t)
		r := e.seedRow(t, killRowSpec{})
		_, err := e.pause(pauseParams(r))
		return err
	}}
}

// oneNamePauseRows are pause's returned errors (SR-7.1 to SR-7.3): the state
// refusal, the wait's timeout, its gone error, each ErrTmuxSessionConflict
// refusal, each ErrTmuxNotAvailable and ErrTmuxUnresponsive cause, and each
// follow-up outcome of a failed /exit or Enter call.
func oneNamePauseRows() []oneNameRow {
	lookup, text, enter := tmux.CallLookup, tmux.CallSendText, tmux.CallSendEnter
	timeout := tmuxfix.Script{Failure: tmux.FailTimeout}
	live, noSession := killRowSpec{}, killRowSpec{NoSession: true}
	lostReply := killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true}
	notPausable, gone, conflict := "ErrSpawnNotPausable", "ErrTmuxSendKeys", "ErrTmuxSessionConflict"
	unavailable, unresponsive := "ErrTmuxNotAvailable", "ErrTmuxUnresponsive"
	return []oneNameRow{
		oneNamePause("pending", notPausable, killRowSpec{State: store.StatePending, NoSession: true}, nil),
		oneNamePause("working", notPausable, killRowSpec{State: store.StateWorking}, nil),
		oneNamePauseTimeout(),
		oneNamePause("session not there", gone, noSession, func(t *testing.T, e *killEnv, r *killRow) {
			e.ensureServer(r)
			e.seedBystander(t, r.Socket)
		}),
		oneNamePause("exit text failed, follow-up gone", gone, live, followUpGone(text)),
		oneNamePause("exit text failed, follow-up leftover", gone, live, actionFails(text, func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.ReplaceSessionAfter(text, r.Socket, r.Session.ID, r.old())
		})),
		oneNamePause("Enter failed, follow-up gone", gone, live, followUpGone(enter)),
		oneNamePause("agent's pane not found", conflict, noSession, func(t *testing.T, e *killEnv, r *killRow) {
			e.seedOurs(t, r)
		}),
		oneNamePause("lost reply, no pane carries the row's token", conflict, lostReply,
			func(t *testing.T, e *killEnv, r *killRow) {
				e.seedOurs(t, r)
			}),
		oneNamePause("leftover only", conflict, noSession, seedOld),
		oneNamePause("conflicting labels: scope value", conflict, live, func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}),
		oneNamePause("different server", unavailable, live, rebindServer),
		oneNamePause("binary unavailable", unavailable, live, scriptKill(lookup, tmuxfix.Script{Failure: tmux.FailUnavailable})),
		oneNamePause("socket permission", unavailable, live, scriptKill(lookup, tmuxfix.Script{Failure: tmux.FailSocketDenied})),
		oneNamePause("exit text failed, follow-up different server", unavailable, live,
			actionFails(text, func(t *testing.T, e *killEnv, r *killRow) {
				e.rec.AfterCall(text, func(tmuxfix.SocketCall, error) { rebindServer(t, e, r) })
			})),
		oneNamePause("lookup timeout", unresponsive, live, scriptKill(lookup, timeout)),
		oneNamePause("pane-listing timeout", unresponsive, live, scriptKill(tmux.CallListPanes, timeout)),
		oneNamePause("exit text timeout", unresponsive, live, scriptKill(text, timeout)),
		oneNamePause("Enter timeout", unresponsive, live, scriptKill(enter, timeout)),
		oneNamePause("exit text failed, follow-up finds the session", unresponsive, live, actionFails(text, nil)),
		oneNamePause("Enter failed, follow-up finds the session", unresponsive, live, actionFails(enter, nil)),
	}
}
