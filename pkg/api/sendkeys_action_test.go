package api_test

// sendkeys_action_test.go: the keys verbs' (send-keys and pause, through
// paneVerb) failed keys calls (SR-7.3, SR-1.4; pause's line clear, b.9o4) and
// the adoption write after a lost create reply (SR-3.6). Only a delivered
// /exit starts pause's wait.

import (
	"bytes"
	"fmt"
	"log"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// keysAction is a keys call a failure can hit, with the calls made up to and including it.
type keysAction struct {
	call  tmux.Call
	calls []tmux.Call
}

// actions are v's keys calls: send-keys' text call (no Enter after it) and
// Enter; pause's line clear (nothing typed after it), /exit call and Enter.
func (v paneVerb) actions() []keysAction {
	if v.verb == apitest.PanePause {
		return []keysAction{{tmux.CallSendKey, pauseClearCalls}, {tmux.CallSendText, pauseTextCalls},
			{tmux.CallSendEnter, pauseSendCalls}}
	}
	return []keysAction{{tmux.CallSendText, paneTextCalls}, {tmux.CallSendEnter, paneSendCalls}}
}

// assertTyped fails unless v's keys reached r's agent pane up to action, the
// failed call: pause's line cleared first, then the text (skaText or /exit)
// unless the line clear failed, and Enter exactly when action is Enter.
func (v paneVerb) assertTyped(t *testing.T, e *killEnv, r killRow, action tmux.Call) {
	t.Helper()
	pane, text := r.Spawn.Identity.PaneID, skaText
	if v.verb == apitest.PanePause {
		e.assertLineCleared(t, r.Socket, pane)
		if action == tmux.CallSendKey {
			e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendEnter)
			return
		}
		text = exitText
	}
	e.assertTextSent(t, r.Socket, pane, text)
	enters := e.rec.SocketCallsOf(tmux.CallSendEnter)
	if want := action == tmux.CallSendEnter; want != (len(enters) == 1) || len(enters) > 1 || (want && enters[0].Target != pane) {
		t.Errorf("Enter calls = %+v; want one to %s: %v", enters, pane, want)
	}
}

// assertKeysFailed fails unless err is errName's one name and desc (no token,
// store id or other store's id in it), the calls are calls, the keys reached
// r's pane up to action, the row is unchanged and no wait polled.
func assertKeysFailed(t *testing.T, e *killEnv, v paneVerb, r killRow, before apitest.SpawnColumns, err error,
	errName string, desc apitest.DescCase, action tmux.Call, calls []tmux.Call) {
	t.Helper()
	assertOneName(t, err, errName)
	apitest.AssertDescription(t, err.Error(), desc, r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
	e.assertPaneCalls(t, calls...)
	v.assertTyped(t, e, r, action)
	e.assertRowUnchanged(t, r.ID, before)
	if e.store.stateReads != 0 {
		t.Errorf("state reads = %d; want no wait after a failure", e.store.stateReads)
	}
}

// TestKeysVerbsActionTimeout: a timed-out keys call (pause's line clear too)
// is ErrTmuxUnresponsive naming it, the keys may have been delivered, with the
// verb's next step; no follow-up lookup, no later keys call, no wait.
func TestKeysVerbsActionTimeout(t *testing.T) {
	t.Parallel()
	for _, v := range keysVerbs() {
		for _, a := range v.actions() {
			t.Run(v.name+"/"+string(a.call), func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{})
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, a.call)
				before := e.columns(t, r.ID)
				_, err := v.run(t, e, r)
				assertKeysFailed(t, e, v, r, before, err, "ErrTmuxUnresponsive",
					apitest.DescKeysTimeout(v.verb, a.call, e.cfg.EffectiveActionTimeout()), a.call, a.calls)
			})
		}
	}
}

// skaAfter runs fn once the failed action call returns, shaping what the
// follow-up lookup finds.
func skaAfter(fn func(t *testing.T, e *killEnv, r killRow)) func(*testing.T, *killEnv, killRow, tmux.Call) {
	return func(t *testing.T, e *killEnv, r killRow, action tmux.Call) {
		e.rec.AfterCall(action, func(tmuxfix.SocketCall, error) { fn(t, e, r) })
	}
}

// skaFollowUpLookup scripts s on every lookup after the failed action.
func skaFollowUpLookup(s tmuxfix.Script) func(*testing.T, *killEnv, killRow, tmux.Call) {
	return skaAfter(func(_ *testing.T, e *killEnv, r killRow) { e.rec.Script(r.Socket, s, tmux.CallLookup) })
}

// TestKeysVerbsActionFailureFollowUp: a keys call failing other than by
// timeout makes one follow-up lookup whose outcome picks the error (SR-7.3);
// a failed line clear is described as a failed /exit call is, naming the key
// send (b.9o4); after Enter the text may be typed but not submitted, and
// send-keys' ErrTmuxUnresponsive says send-keys with empty text submits it.
// The reply text never classifies. No wait.
func TestKeysVerbsActionFailureFollowUp(t *testing.T) {
	t.Parallel()
	gone := func(v apitest.PaneVerb, r killRow, action tmux.Call) apitest.DescCase {
		return apitest.DescPaneGone(apitest.PaneGone{Verb: v, InstanceID: r.ID, Name: r.Name, FailedCall: action})
	}
	unresponsive := func(_ apitest.PaneVerb, _ killRow, action tmux.Call) apitest.DescCase {
		return apitest.DescUnrecognisedReply(action, "")
	}
	noServer := tmuxfix.Script{Failure: tmux.FailNoServer}
	exited := callTableFirstLine()
	cases := []struct {
		name    string
		script  tmuxfix.Script
		world   func(t *testing.T, e *killEnv, r killRow, action tmux.Call)
		errName string
		desc    func(v apitest.PaneVerb, r killRow, action tmux.Call) apitest.DescCase
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
			func(_ apitest.PaneVerb, _ killRow, action tmux.Call) apitest.DescCase {
				return apitest.DescUnrecognisedReply(action, exited)
			}},
		{"unreadable, timeout", noServer, skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailTimeout}),
			"ErrTmuxUnresponsive", unresponsive},
		{"unreadable, unrecognised reply", noServer, skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailUnrecognized,
			FirstLine: exited, ExitStatus: 1}), "ErrTmuxUnresponsive", unresponsive},
		{"provenance conflict, scope value", noServer, skaAfter(func(_ *testing.T, e *killEnv, r killRow) {
			e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}), "ErrTmuxUnresponsive", unresponsive},
		{"provenance conflict, duplicate label", noServer, skaAfter(func(t *testing.T, e *killEnv, r killRow) {
			e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
		}), "ErrTmuxUnresponsive", unresponsive},
		{"different server", noServer, skaAfter(func(t *testing.T, e *killEnv, r killRow) {
			e.rec.RebindServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.seedBystander(t, r.Socket)
		}), "ErrTmuxNotAvailable", func(_ apitest.PaneVerb, r killRow, _ tmux.Call) apitest.DescCase {
			return apitest.DescDifferentServer(r.ID)
		}},
		{"tmux unavailable, missing binary", noServer, skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailUnavailable}),
			"ErrTmuxNotAvailable", func(apitest.PaneVerb, killRow, tmux.Call) apitest.DescCase { return apitest.DescTmuxNotRun() }},
		{"tmux unavailable, socket permission", noServer, skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			"ErrTmuxNotAvailable", func(_ apitest.PaneVerb, r killRow, _ tmux.Call) apitest.DescCase {
				return apitest.DescSocketPermission(r.Socket)
			}},
	}
	for _, v := range keysVerbs() {
		for _, a := range v.actions() {
			for _, tc := range cases {
				t.Run(v.name+"/"+string(a.call)+"/"+tc.name, func(t *testing.T) {
					t.Parallel()
					e := newKillEnv(t)
					r := e.seedRow(t, killRowSpec{})
					e.seedBystander(t, r.Socket)
					e.rec.Script(r.Socket, tc.script, a.call)
					if tc.world != nil {
						tc.world(t, e, r, a.call)
					}
					before := e.columns(t, r.ID)

					_, err := v.run(t, e, r)

					desc := tc.desc(v.verb, r, a.call).AfterTextFailed()
					if a.call == tmux.CallSendEnter {
						desc = tc.desc(v.verb, r, a.call).AfterEnterFailed(v.verb)
					}
					assertKeysFailed(t, e, v, r, before, err, tc.errName, desc, a.call, withFollowUp(a.calls))
				})
			}
		}
	}
}

// TestKeysVerbsAdoptionWrite: a lost reply's identity is written once and
// logged adopted only when applied; never when it adds nothing or the verb
// refuses first; the write's outcome never changes where the keys go; re-issued,
// an applied adoption gives the same answer and calls with no further write (SR-3.6).
func TestKeysVerbsAdoptionWrite(t *testing.T) {
	// Serial: it redirects the process-wide standard logger (log.SetOutput).
	lost := killRowSpec{NoPane: true, NoServerIdentity: true}
	tokenPane := func(t *testing.T, r killRow) string { return labelledPane(t, r.Session, r.Token) }
	recorded := func(_ *testing.T, r killRow) string { return r.Spawn.Identity.PaneID }
	noTokenPane := func(t *testing.T, e *killEnv, r *killRow) { e.seedOurs(t, r, e.otherPane()) }
	cases := []struct {
		name    string
		spec    killRowSpec
		setup   func(t *testing.T, e *killEnv, r *killRow)
		pane    func(t *testing.T, r killRow) string // the pane the keys go to; nil: refused with errName
		errName string                               // "": the verb's state refusal (a pending row)
		tries   int                                  // adoption writes attempted
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
				e.sessionStartAfter(t, tmux.CallLookup, *r, "sess-ad-"+r.ID)
			}},
		{name: "adoption write fails in the store", spec: lost, pane: tokenPane, tries: 1,
			setup: func(_ *testing.T, e *killEnv, _ *killRow) { e.store.failAdopt(nil) }},
		{name: "refused before the lookup: pending row",
			spec: killRowSpec{State: store.StatePending, NoPane: true, NoServerIdentity: true}},
		{name: "refused on Leftover",
			spec:    killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true, Agent: agentNotRecorded},
			setup:   func(t *testing.T, e *killEnv, r *killRow) { e.seedLeftover(t, *r, tmuxfix.OtherToken) },
			errName: "ErrTmuxSessionConflict"},
		{name: "refused on conflicting labels", spec: lost, errName: "ErrTmuxSessionConflict",
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
			}},
		{name: "refused on a timed-out pane listing", spec: lost, errName: "ErrTmuxUnresponsive",
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallListPanes)
			}},
	}
	logs := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	for _, v := range keysVerbs() {
		for _, tc := range cases {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedRow(t, tc.spec)
				if tc.setup != nil {
					tc.setup(t, e, &r)
				}
				logs.Reset()
				before := e.adoption(t, r.ID)

				first := runVerb(e, func() (string, error) { return v.run(t, e, r) })

				pane := ""
				if tc.pane == nil {
					errName := tc.errName
					if errName == "" {
						errName = map[apitest.PaneVerb]string{apitest.PaneSendKeys: "ErrSpawnNotInteractive",
							apitest.PanePause: "ErrSpawnNotPausable"}[v.verb]
					}
					assertOneName(t, first.err, errName)
					e.assertNothingSent(t)
				} else {
					pane = tc.pane(t, r)
					v.acted(t, e, r, pane, first.res, first.err)
				}
				if e.store.adoptTries != tc.tries || e.store.stateReads != 0 {
					t.Errorf("adoption writes attempted = %d, state reads %d; want %d, none", e.store.adoptTries,
						e.store.stateReads, tc.tries)
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
				if got := adoptedRecords(t, v.name, r.ID); got != wantAdopted {
					t.Errorf("adopted records = %d; want %d", got, wantAdopted)
				}
				if logs.Len() != 0 {
					t.Errorf("log output %q; want none", logs.String())
				}
				if tc.applied {
					assertSameRun(t, runVerb(e, func() (string, error) { return v.run(t, e, r) }), first)
					if e.store.adoptTries != tc.tries || adoptedRecords(t, v.name, r.ID) != 1 {
						t.Errorf("re-issued: %d writes attempted, %d adopted records; want %d and 1", e.store.adoptTries,
							adoptedRecords(t, v.name, r.ID), tc.tries)
					}
				}
			})
		}
	}
}
