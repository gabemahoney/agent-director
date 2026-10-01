package api_test

// readpane_pane_test.go: which pane read-pane reads (SR-3.7, SR-7.2): the
// agent's pane by its recorded pane id and pid wherever it now is; after a
// lost create reply, the one pane carrying the row's token, for the call
// only (SR-3.6, SR-7.5); a lone leftover's pane by its label's token; never
// a pane of another agent-director store's session (SR-3.4, AC-LKP-20).

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rppLines is the n_lines every case asks for (not the default).
const rppLines = 7

// The refusal call sequences: after the listing, after the lookup alone (a
// read is paneReadCalls).
var (
	rppListedCalls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes}
	rppLookupCalls = []tmux.Call{tmux.CallLookup}
)

// rppWant is a case's expected answer: pane, the pane id captured, or err
// with its description case; and the socket-taking calls, in order.
type rppWant struct {
	pane  string
	err   error
	desc  apitest.DescCase
	calls []tmux.Call
}

// rppCaptured expects pane to be read, by its id.
func rppCaptured(pane string) rppWant { return rppWant{pane: pane, calls: paneReadCalls} }

// rppNotFound expects the pane-not-found conflict naming session name.
func rppNotFound(r killRow, name string, lostReply bool) rppWant {
	return rppWant{err: api.ErrTmuxSessionConflict, calls: rppListedCalls,
		desc: apitest.DescPaneNotFound(apitest.PaneNotFound{Verb: apitest.PaneReadPane, InstanceID: r.ID,
			Name: name, LostReply: lostReply})}
}

// rppGone expects read-pane's gone error for r, with no listing.
func rppGone(r killRow) rppWant {
	return rppWant{err: api.ErrTmuxCaptureFailed, calls: rppLookupCalls,
		desc: apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneReadPane, InstanceID: r.ID, Name: r.Name})}
}

// rppRead runs read-pane on r through a Client (e.pc as its reader).
func rppRead(t *testing.T, e *killEnv, r killRow) verbRun[api.ReadPaneResult] {
	t.Helper()
	return e.readPaneRun(t, api.ReadPaneParams{ClaudeInstanceID: r.ID, NLines: rppLines, ANSI: true})
}

// rppCheck gives every pane on r's socket its own text, runs read-pane on r
// and fails unless it answers want with want's calls, captures at most want's
// pane, and changes no row, session, tmux state or trail. It returns the run.
func rppCheck(t *testing.T, e *killEnv, r killRow, want rppWant) verbRun[api.ReadPaneResult] {
	t.Helper()
	e.setPaneTexts(r.Socket)
	before, sessions, mark := e.columns(t, r.ID), e.rec.Sessions(r.Socket), trailMark(t)

	run := rppRead(t, e, r)

	if want.err == nil {
		if run.err != nil || run.res.Pane != paneText(r.Socket, want.pane) {
			t.Errorf("ReadPane = %+v, %v; want pane %s's text", run.res, run.err, want.pane)
		}
		e.assertCaptured(t, want.pane, rppLines, true)
	} else {
		if !errors.Is(run.err, want.err) {
			t.Fatalf("ReadPane err = %v; want %v", run.err, want.err)
		}
		apitest.AssertDescription(t, run.err.Error(), want.desc, r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
		e.assertNoCalls(t, tmux.CallCapture)
	}
	e.assertPaneCalls(t, want.calls...)
	e.assertNoCalls(t, paneWriteCalls...)
	e.assertRowUnchanged(t, r.ID, before)
	if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
		t.Errorf("sessions after read-pane = %+v; want %+v", got, sessions)
	}
	assertNoTrailSince(t, mark, r.ID)
	return run
}

// rppOtherPane is a pane that is not the agent's: a new id and pid, no pane label.
func rppOtherPane(e *killEnv) tmuxfix.SeedPane { return tmuxfix.SeedPane{PID: e.newPID()} }

// TestReadPaneAgentPane: the agent's pane is read once by its id wherever it
// now is; a missing or respawned pane is the pane-not-found conflict, unread.
func TestReadPaneAgentPane(t *testing.T) {
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(t *testing.T, e *killEnv, r *killRow) rppWant
	}{
		{name: "in its own session",
			setup: func(_ *testing.T, _ *killEnv, r *killRow) rppWant { return rppCaptured(r.Spawn.Identity.PaneID) }},
		{name: "moved to another window", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				moved := r.pane()
				moved.Window, moved.Index = 3, 2
				e.seedOurs(t, r, rppOtherPane(e), moved)
				return rppCaptured(r.Spawn.Identity.PaneID)
			}},
		{name: "moved to another session", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				e.seedOurs(t, r, rppOtherPane(e))
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "elsewhere", Panes: []tmuxfix.SeedPane{r.pane()}})
				return rppCaptured(r.Spawn.Identity.PaneID)
			}},
		{name: "shown in two sessions (grouped session or linked window)",
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				e.seedViewer(t, *r)
				return rppCaptured(r.Spawn.Identity.PaneID)
			}},
		{name: "recorded pane id missing from the listing", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				e.seedOurs(t, r, rppOtherPane(e))
				return rppNotFound(*r, r.Name, false)
			}},
		{name: "recorded pane missing, another pane carries the row's token", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				p := rppOtherPane(e)
				p.AdPane = r.Token
				e.seedOurs(t, r, p)
				return rppNotFound(*r, r.Name, false)
			}},
		{name: "respawned pane: same id, another pid", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				p := r.pane()
				p.PID = e.newPID()
				e.seedOurs(t, r, p)
				return rppNotFound(*r, r.Name, false)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			rppCheck(t, e, r, tc.setup(t, e, &r))
		})
	}
}

// TestReadPaneLostReply: a row recording no pane reads the one pane carrying
// its token, never a teammate's, with no write; re-issued, the same calls.
func TestReadPaneLostReply(t *testing.T) {
	lost := killRowSpec{NoPane: true, NoServerIdentity: true}
	seeded := func(t *testing.T, _ *killEnv, r *killRow) rppWant {
		return rppCaptured(labelledPane(t, r.Session, r.Token))
	}
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(t *testing.T, e *killEnv, r *killRow) rppWant
	}{
		{name: "pane and server identity lost, one pane carries the token", spec: lost, setup: seeded},
		{name: "only the pane lost", spec: killRowSpec{NoPane: true}, setup: seeded},
		{name: "a teammate split from the agent's pane", spec: killRowSpec{NoPane: true, NoServerIdentity: true, Teammates: 1},
			setup: seeded},
		{name: "a teammate at 0.0, the token pane at 1.1", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				e.seedOurs(t, r, rppOtherPane(e), tmuxfix.SeedPane{Window: 1, Index: 1, AdPane: r.Token})
				return rppCaptured(labelledPane(t, r.Session, r.Token))
			}},
		{name: "no pane carries the token", spec: killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				e.seedOurs(t, r, rppOtherPane(e))
				return rppNotFound(*r, r.Name, true)
			}},
		{name: "two panes carry the token", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				e.seedOurs(t, r, tmuxfix.SeedPane{AdPane: r.Token}, tmuxfix.SeedPane{Index: 1, AdPane: r.Token})
				return rppNotFound(*r, r.Name, true)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			first := rppCheck(t, e, r, tc.setup(t, e, &r))

			assertSameRun(t, rppRead(t, e, r), first)
		})
	}
}

// rppFinished is a finished row's spec: ended longer ago than the stopping window.
func rppFinished(e *killEnv) killRowSpec {
	ended := e.clock.Now().Add(-e.cfg.EffectiveStoppingWindow() - time.Second)
	return killRowSpec{State: store.StateEnded, NoSession: true, Opts: []apitest.SpawnOption{apitest.WithEndedAt(ended)}}
}

// TestReadPaneLeftover: a lone leftover's pane carrying its label's token is
// read on a pending, live or finished row; none such, or two leftovers, refuse.
func TestReadPaneLeftover(t *testing.T) {
	lone := func(t *testing.T, e *killEnv, r *killRow) rppWant {
		e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
		return rppCaptured(labelledPane(t, r.Session, tmuxfix.OtherToken))
	}
	cases := []struct {
		name  string
		spec  func(e *killEnv) killRowSpec
		setup func(t *testing.T, e *killEnv, r *killRow) rppWant
	}{
		{name: "pending row, launching process stopped before its create",
			spec: func(*killEnv) killRowSpec { return killPendSpec(store.StatePending) }, setup: lone},
		{name: "live row", setup: lone},
		{name: "finished row", spec: rppFinished, setup: lone},
		{name: "its pane moved to window 2, pane 1",
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				e.ensureServer(r)
				s := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "leftover-moved", Label: r.old(),
					Panes: []tmuxfix.SeedPane{rppOtherPane(e), {Window: 2, Index: 1, AdPane: tmuxfix.OtherToken}}})
				return rppCaptured(labelledPane(t, s, tmuxfix.OtherToken))
			}},
		{name: "no pane carries its token",
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				e.ensureServer(r)
				s := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "leftover-bare", Label: r.old(),
					Panes: []tmuxfix.SeedPane{rppOtherPane(e)}})
				return rppNotFound(*r, s.Name, false)
			}},
		{name: "two leftovers",
			setup: func(t *testing.T, e *killEnv, r *killRow) rppWant {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
				second := e.seedLeftover(t, *r, newToken())
				return rppWant{err: api.ErrTmuxSessionConflict, calls: rppLookupCalls,
					desc: apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: apitest.PaneReadPane, InstanceID: r.ID,
						Sessions: []apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}, {Name: second.Name, ID: second.ID}}})}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			spec := killRowSpec{NoSession: true}
			if tc.spec != nil {
				spec = tc.spec(e)
			}
			r := e.seedRow(t, spec)
			rppCheck(t, e, r, tc.setup(t, e, &r))
		})
	}
}

// TestReadPaneOtherStore: another store's session naming the row's id is
// never read nor counted as a leftover (SR-3.4, AC-LKP-20).
func TestReadPaneOtherStore(t *testing.T) {
	cases := []struct {
		name     string
		token    func(r killRow) string // the other store's label token
		leftover bool                   // also one leftover of this store
	}{
		{name: "with this row's token, no session of this store", token: func(r killRow) string { return r.Token }},
		{name: "with another token, no session of this store", token: func(killRow) string { return newToken() }},
		{name: "with this row's token, one leftover of this store", token: func(r killRow) string { return r.Token },
			leftover: true},
		{name: "with another token, one leftover of this store", token: func(killRow) string { return newToken() },
			leftover: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{NoSession: true})
			e.seedSession(t, &r, tmuxfix.WithRowSessionLabel(r.otherStore(tc.token(r)), true))
			want := rppGone(r)
			if tc.leftover {
				lo := e.seedLeftover(t, r, tmuxfix.OtherToken)
				want = rppCaptured(lo.Panes[0].ID)
			}
			rppCheck(t, e, r, want)
		})
	}
}
