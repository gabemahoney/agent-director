package api_test

// one_name_pane_verbs_test.go holds the pane verbs' SR-1.5 one-name rows
// (read-pane now; send-keys and pause append theirs here), each driven on the
// kill fixture with tmuxfix.Recorder failure kinds and checked by
// assertOneName through oneNameRows (one_name_per_error_test.go).

import (
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
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

// captureFails makes r's first capture fail other than by a timeout, then
// runs then (when set) to shape what the one follow-up lookup finds.
func captureFails(then func(t *testing.T, e *killEnv, r *killRow)) func(*testing.T, *killEnv, *killRow) {
	return func(t *testing.T, e *killEnv, r *killRow) {
		scriptKill(tmux.CallCapture, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1})(t, e, r)
		if then != nil {
			then(t, e, r)
		}
	}
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
