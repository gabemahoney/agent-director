package api_test

// pause_action_test.go: on waiting rows, which pane pause types /exit into
// (SR-3.7), its failed pane listing and line clear (C-u, b.9o4), /exit or
// Enter call (SR-7.3, SR-1.4)
// and the adoption write after a lost create reply (SR-3.6). Only a
// delivered /exit starts the wait.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// pfRun is one pause call (answer and calls added) and whether its wait started.
type pfRun struct {
	verbRun[api.PauseResult]
	waited bool
}

// pfPause runs pause on r with its wait cut at the first sleep, which cancels
// the call's context: a started wait returns context.Canceled. Not parallel-safe.
func pfPause(e *killEnv, r killRow) pfRun {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interval, sleep := api.PauseTestKnobs()
	defer api.SetPauseTestKnobs(interval, sleep)
	waited := false
	api.SetPauseTestKnobs(interval, func(time.Duration) { waited = true; cancel() })
	run := runVerb(e, func() (api.PauseResult, error) {
		return e.pauseWithin(ctx, pauseTimeoutSeconds, pauseParams(r))
	})
	return pfRun{verbRun: run, waited: waited}
}

// assertWaited fails unless run delivered /exit and then started the wait.
func (run pfRun) assertWaited(t *testing.T) {
	t.Helper()
	if !errors.Is(run.err, context.Canceled) || !run.waited {
		t.Fatalf("Pause = %v (wait started %v); want /exit delivered, then the wait", run.err, run.waited)
	}
}

// assertFailed fails unless run failed with errName (assertOneName) without starting the wait.
func (run pfRun) assertFailed(t *testing.T, errName string) {
	t.Helper()
	assertOneName(t, run.err, errName)
	if run.waited {
		t.Errorf("Pause %v started the wait; want none after a failure", run.err)
	}
}

// pfWant is a pane case's answer: pane, /exit typed into it by id then the
// wait, or errName with its description case and the calls made, nothing sent.
type pfWant struct {
	pane    string
	errName string
	desc    apitest.DescCase
	calls   []tmux.Call
}

// pfNotFound expects pause's pane-not-found conflict after the listing.
func pfNotFound(r killRow, lostReply bool) pfWant {
	return pfWant{errName: "ErrTmuxSessionConflict", calls: skaListedCalls,
		desc: apitest.DescPaneNotFound(apitest.PaneNotFound{Verb: apitest.PanePause, InstanceID: r.ID, Name: r.Name,
			LostReply: lostReply})}
}

// pfCheck runs pause on r and fails unless it answers want; a refusal also
// leaves the row as it was.
func pfCheck(t *testing.T, e *killEnv, r killRow, want pfWant) {
	t.Helper()
	before := e.columns(t, r.ID)
	run := pfPause(e, r)
	if want.errName == "" {
		run.assertWaited(t)
		e.assertExitDelivered(t, r.Socket, want.pane)
		return
	}
	run.assertFailed(t, want.errName)
	apitest.AssertDescription(t, run.err.Error(), want.desc, r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
	e.assertNothingSent(t)
	e.assertPaneCalls(t, want.calls...)
	e.assertRowUnchanged(t, r.ID, before)
}

// TestPauseAgentPane: /exit goes once to the agent's pane id wherever it now
// is; a missing or respawned pane is the pane-not-found conflict, unsent.
func TestPauseAgentPane(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	recorded := func(_ *testing.T, _ *killEnv, r *killRow) pfWant { return pfWant{pane: r.Spawn.Identity.PaneID} }
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(t *testing.T, e *killEnv, r *killRow) pfWant
	}{
		{name: "in its own session", setup: recorded},
		{name: "moved to another window", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) pfWant {
				moved := r.pane()
				moved.Window, moved.Index = 2, 1
				e.seedOurs(t, r, skaOtherPane(e), moved)
				return recorded(t, e, r)
			}},
		{name: "moved to another session", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) pfWant {
				e.seedOurs(t, r, skaOtherPane(e))
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "moved-to", Panes: []tmuxfix.SeedPane{r.pane()}})
				return recorded(t, e, r)
			}},
		{name: "shown in two sessions (grouped session or linked window)",
			setup: func(t *testing.T, e *killEnv, r *killRow) pfWant {
				e.seedViewer(t, *r)
				return recorded(t, e, r)
			}},
		{name: "recorded pane id missing from the listing", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) pfWant {
				e.seedOurs(t, r, skaOtherPane(e))
				return pfNotFound(*r, false)
			}},
		{name: "recorded pane missing, another pane carries the row's token", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) pfWant {
				p := skaOtherPane(e)
				p.AdPane = r.Token
				e.seedOurs(t, r, p)
				return pfNotFound(*r, false)
			}},
		{name: "respawned pane: same id, another pid", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) pfWant {
				p := r.pane()
				p.PID = e.newPID()
				e.seedOurs(t, r, p)
				return pfNotFound(*r, false)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			pfCheck(t, e, r, tc.setup(t, e, &r))
		})
	}
}

// TestPauseLostReplyPane: a row recording no pane gets /exit in the one pane
// carrying its token, at any index; none or two is the lost-reply conflict.
func TestPauseLostReplyPane(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(t *testing.T, e *killEnv, r *killRow) pfWant
	}{
		{name: "pane and server identity lost", spec: killRowSpec{NoPane: true, NoServerIdentity: true},
			setup: func(t *testing.T, _ *killEnv, r *killRow) pfWant {
				return pfWant{pane: labelledPane(t, r.Session, r.Token)}
			}},
		{name: "a teammate at 0.0, the token pane at base-index 1", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) pfWant {
				e.seedOurs(t, r, skaOtherPane(e), tmuxfix.SeedPane{Window: 1, Index: 1, AdPane: r.Token})
				return pfWant{pane: labelledPane(t, r.Session, r.Token)}
			}},
		{name: "no pane carries the token", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) pfWant {
				e.seedOurs(t, r, skaOtherPane(e))
				return pfNotFound(*r, true)
			}},
		{name: "two panes carry the token", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) pfWant {
				e.seedOurs(t, r, tmuxfix.SeedPane{AdPane: r.Token}, tmuxfix.SeedPane{Index: 1, AdPane: r.Token})
				return pfNotFound(*r, true)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			pfCheck(t, e, r, tc.setup(t, e, &r))
		})
	}
}

// TestPauseListingFails: a pane listing that times out or is unrecognised is
// ErrTmuxUnresponsive naming the listing; one finding the server exited is Gone. Unsent.
func TestPauseListingFails(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	const firstLine = "list-panes: unexpected reply"
	listing := func(s tmuxfix.Script) func(*killEnv, killRow) {
		return func(e *killEnv, r killRow) { e.rec.Script(r.Socket, s, tmux.CallListPanes) }
	}
	cases := []struct {
		name    string
		world   func(e *killEnv, r killRow)
		errName string
		desc    func(e *killEnv, r killRow) apitest.DescCase
	}{
		{"timeout", listing(tmuxfix.Script{Failure: tmux.FailTimeout}), "ErrTmuxUnresponsive",
			func(e *killEnv, _ killRow) apitest.DescCase {
				return apitest.DescCallTimeout(tmux.CallListPanes, e.cfg.EffectiveQueryTimeout())
			}},
		{"unrecognised reply", listing(tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: firstLine, ExitStatus: 1,
			HadStdout: true}), "ErrTmuxUnresponsive", func(*killEnv, killRow) apitest.DescCase {
			return apitest.DescUnrecognisedReply(tmux.CallListPanes, firstLine)
		}},
		{"no server: it exited after the lookup", func(e *killEnv, r killRow) {
			e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
				e.rec.StopServer(r.Socket).SetNoServerFailure(r.Socket, tmux.FailNoServer)
				e.syncServers()
			})
		}, "ErrTmuxSendKeys", func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PanePause, InstanceID: r.ID, Name: r.Name})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			tc.world(e, r)
			pfCheck(t, e, r, pfWant{errName: tc.errName, desc: tc.desc(e, r), calls: skaListedCalls})
		})
	}
}

// pfAssertTyped fails unless r's agent pane's line was cleared and, when
// action (the failed call) is not that line clear, /exit typed into it, with
// Enter sent to it exactly when action is the Enter call; a failed line
// clear is followed by no text or Enter call.
func pfAssertTyped(t *testing.T, e *killEnv, r killRow, action tmux.Call) {
	t.Helper()
	pane := r.Spawn.Identity.PaneID
	if action == tmux.CallSendKey {
		e.assertLineCleared(t, r.Socket, pane)
		e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendEnter)
		return
	}
	e.assertExitTyped(t, r.Socket, pane)
	enters := e.rec.SocketCallsOf(tmux.CallSendEnter)
	if want := action == tmux.CallSendEnter; want != (len(enters) == 1) || len(enters) > 1 ||
		(want && enters[0].Target != pane) {
		t.Errorf("Enter calls = %+v; want one to %s: %v", enters, pane, want)
	}
}

// TestPauseActionTimeout: a timed-out line clear (C-u), /exit or Enter is
// ErrTmuxUnresponsive naming the call, the keys may have been delivered,
// retry later; no follow-up lookup, no later keys call, no wait.
func TestPauseActionTimeout(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	for _, a := range pauseActions {
		t.Run(string(a.call), func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, a.call)
			before := e.columns(t, r.ID)

			run := pfPause(e, r)

			run.assertFailed(t, "ErrTmuxUnresponsive")
			apitest.AssertDescription(t, run.err.Error(),
				apitest.DescKeysTimeout(apitest.PanePause, a.call, e.cfg.EffectiveActionTimeout()), r.Token, r.StoreID)
			e.assertPaneCalls(t, a.calls...)
			pfAssertTyped(t, e, r, a.call)
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

// TestPauseActionFailureFollowUp: a line clear (C-u), /exit or Enter call
// failing other than by timeout makes one follow-up lookup whose outcome
// picks the error (SR-7.3); a failed line clear is described as a failed
// /exit call is, naming the key send (b.9o4); after Enter, /exit may be typed
// but not submitted. No wait.
func TestPauseActionFailureFollowUp(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	gone := func(r killRow, action tmux.Call) apitest.DescCase {
		return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PanePause, InstanceID: r.ID, Name: r.Name,
			FailedCall: action})
	}
	unresponsive := func(_ killRow, action tmux.Call) apitest.DescCase { return apitest.DescUnrecognisedReply(action, "") }
	noServer := tmuxfix.Script{Failure: tmux.FailNoServer}
	exited := callTableFirstLine()
	cases := []struct {
		name    string
		script  tmuxfix.Script
		world   func(t *testing.T, e *killEnv, r killRow, action tmux.Call)
		errName string
		desc    func(r killRow, action tmux.Call) apitest.DescCase
	}{
		{"gone, session ended", noServer, func(_ *testing.T, e *killEnv, r killRow, action tmux.Call) {
			e.rec.RemoveSessionAfter(action, r.Socket, r.Session.ID)
		}, "ErrTmuxSendKeys", gone},
		{"gone, server exited", noServer, skaAfter(func(_ *testing.T, e *killEnv, r killRow) {
			e.rec.StopServer(r.Socket).SetNoServerFailure(r.Socket, tmux.FailNoServer)
			e.syncServers()
		}), "ErrTmuxSendKeys", gone},
		{"leftover", noServer, func(_ *testing.T, e *killEnv, r killRow, action tmux.Call) {
			e.rec.ReplaceSessionAfter(action, r.Socket, r.Session.ID, r.old())
		}, "ErrTmuxSendKeys", gone},
		{"ours", noServer, nil, "ErrTmuxUnresponsive", unresponsive},
		{"ours after a reply that reads like a server exit",
			tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: exited, ExitStatus: 1}, nil, "ErrTmuxUnresponsive",
			func(_ killRow, action tmux.Call) apitest.DescCase {
				return apitest.DescUnrecognisedReply(action, exited)
			}},
		{"unreadable, timeout", noServer, skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailTimeout}),
			"ErrTmuxUnresponsive", unresponsive},
		{"provenance conflict, duplicate label", noServer, skaAfter(func(t *testing.T, e *killEnv, r killRow) {
			e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
		}), "ErrTmuxUnresponsive", unresponsive},
		{"different server", noServer, skaAfter(func(t *testing.T, e *killEnv, r killRow) {
			e.rec.RebindServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.seedBystander(t, r.Socket)
		}), "ErrTmuxNotAvailable", func(r killRow, _ tmux.Call) apitest.DescCase { return apitest.DescDifferentServer(r.ID) }},
		{"tmux unavailable, missing binary", noServer, skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailUnavailable}),
			"ErrTmuxNotAvailable", func(killRow, tmux.Call) apitest.DescCase { return apitest.DescTmuxNotRun() }},
		{"tmux unavailable, socket permission", noServer, skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			"ErrTmuxNotAvailable", func(r killRow, _ tmux.Call) apitest.DescCase { return apitest.DescSocketPermission(r.Socket) }},
	}
	for _, a := range pauseActions {
		for _, tc := range cases {
			t.Run(string(a.call)+"/"+tc.name, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{})
				e.seedBystander(t, r.Socket)
				e.rec.Script(r.Socket, tc.script, a.call)
				if tc.world != nil {
					tc.world(t, e, r, a.call)
				}
				before := e.columns(t, r.ID)

				run := pfPause(e, r)

				run.assertFailed(t, tc.errName)
				desc := tc.desc(r, a.call).AfterTextFailed()
				if a.call == tmux.CallSendEnter {
					desc = tc.desc(r, a.call).AfterEnterFailed(apitest.PanePause)
				}
				apitest.AssertDescription(t, run.err.Error(), desc, r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
				e.assertPaneCalls(t, withFollowUp(a.calls)...)
				pfAssertTyped(t, e, r, a.call)
				e.assertRowUnchanged(t, r.ID, before)
			})
		}
	}
}

// TestPauseAdoptionWrite: a lost reply's identity is written once and logged
// adopted only when applied; never when it adds nothing or pause refuses
// first; the write's outcome never changes where /exit goes (SR-3.6).
func TestPauseAdoptionWrite(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs); it redirects
	// the process-wide standard logger (log.SetOutput).
	lost := killRowSpec{NoPane: true, NoServerIdentity: true}
	tokenPane := func(t *testing.T, r killRow) string { return labelledPane(t, r.Session, r.Token) }
	recorded := func(_ *testing.T, r killRow) string { return r.Spawn.Identity.PaneID }
	noTokenPane := func(t *testing.T, e *killEnv, r *killRow) { e.seedOurs(t, r, skaOtherPane(e)) }
	cases := []struct {
		name    string
		spec    killRowSpec
		setup   func(t *testing.T, e *killEnv, r *killRow)
		pane    func(t *testing.T, r killRow) string // /exit's pane, then the wait; nil: refused with errName
		errName string
		tries   int // adoption writes attempted
		applied bool
	}{
		{name: "pane and server identity lost", spec: lost, pane: tokenPane, tries: 1, applied: true},
		{name: "only the server identity lost", spec: killRowSpec{NoServerIdentity: true}, pane: recorded, tries: 1,
			applied: true},
		{name: "only the pane lost", spec: killRowSpec{NoPane: true}, pane: tokenPane, tries: 1, applied: true},
		{name: "lost reply, no pane carries the token: the server identity alone",
			spec: killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true}, setup: noTokenPane,
			errName: "ErrTmuxSessionConflict", tries: 1, applied: true},
		{name: "identity recorded: nothing to add", pane: recorded},
		{name: "only the pane lost, no pane carries the token: nothing to add",
			spec: killRowSpec{NoPane: true, NoSession: true}, setup: noTokenPane, errName: "ErrTmuxSessionConflict"},
		{name: "row written between the lookup and the adoption", spec: lost, pane: tokenPane, tries: 1,
			setup: func(t *testing.T, e *killEnv, r *killRow) { e.rowWriteAfter(t, tmux.CallLookup, *r) }},
		{name: "SessionStart between the lookup and the adoption", spec: killRowSpec{NoServerIdentity: true},
			pane: recorded, tries: 1,
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.sessionStartAfter(t, tmux.CallLookup, *r, "sess-pf-"+r.ID)
			}},
		{name: "adoption write fails in the store", spec: lost, pane: tokenPane, tries: 1,
			setup: func(_ *testing.T, e *killEnv, _ *killRow) { e.store.failAdopt(nil) }},
		{name: "refused before the lookup: pending row",
			spec:    killRowSpec{State: store.StatePending, NoPane: true, NoServerIdentity: true},
			errName: "ErrSpawnNotPausable"},
		{name: "refused on Leftover",
			spec:    killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true, Agent: agentNotRecorded},
			setup:   func(t *testing.T, e *killEnv, r *killRow) { e.seedLeftover(t, *r, tmuxfix.OtherToken) },
			errName: "ErrTmuxSessionConflict"},
		{name: "refused on conflicting labels", spec: lost,
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
			}, errName: "ErrTmuxSessionConflict"},
		{name: "refused on a timed-out pane listing", spec: lost,
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallListPanes)
			}, errName: "ErrTmuxUnresponsive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			if tc.setup != nil {
				tc.setup(t, e, &r)
			}
			logs := &bytes.Buffer{}
			prev := log.Writer()
			log.SetOutput(logs)
			t.Cleanup(func() { log.SetOutput(prev) })
			before := e.adoption(t, r.ID)

			first := pfPause(e, r)

			pane := ""
			if tc.pane == nil {
				first.assertFailed(t, tc.errName)
				e.assertNothingSent(t)
			} else {
				pane = tc.pane(t, r)
				first.assertWaited(t)
				e.assertExitDelivered(t, r.Socket, pane)
			}
			if e.store.adoptTries != tc.tries {
				t.Errorf("adoption writes attempted = %d; want %d", e.store.adoptTries, tc.tries)
			}
			after := e.adoption(t, r.ID)
			wantAdopted := 0
			switch {
			case tc.applied:
				wantAdopted = 1
				if after.RowVersion != before.RowVersion+1 || after.ServerPID == nil ||
					(pane == "") != (after.PaneID == nil) || (pane != "" && fmt.Sprint(after.PaneID) != pane) {
					t.Errorf("adoption columns %+v (before %+v); want written once, server set, pane %q", after, before, pane)
				}
			case tc.tries == 0:
				if after != before {
					t.Errorf("adoption columns %+v; want unchanged %+v", after, before)
				}
			default:
				if after.RowVersion = before.RowVersion; after != before {
					t.Errorf("adoption columns %+v; want the identity unchanged %+v", after, before)
				}
			}
			if got := adoptedRecords(t, "pause", r.ID); got != wantAdopted {
				t.Errorf("adopted records = %d; want %d", got, wantAdopted)
			}
			if logs.Len() != 0 {
				t.Errorf("log output %q; want none", logs.String())
			}
			if tc.applied {
				assertSameRun(t, pfPause(e, r).verbRun, first.verbRun)
				if e.store.adoptTries != tc.tries || adoptedRecords(t, "pause", r.ID) != 1 {
					t.Errorf("re-issued: %d writes attempted, %d adopted records; want %d and 1", e.store.adoptTries,
						adoptedRecords(t, "pause", r.ID), tc.tries)
				}
			}
		})
	}
}
