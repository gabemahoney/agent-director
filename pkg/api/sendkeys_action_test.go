package api_test

// sendkeys_action_test.go: on live rows, which pane send-keys types into
// (SR-3.7), its failed pane listing and text or Enter call (SR-7.3, SR-1.4)
// and the adoption write after a lost create reply (SR-3.6).

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// skaText is the text every case sends (LF kept, no CR).
const skaText = "run the tests\nthen report"

// skaListedCalls is a refusal after the pane listing: nothing sent.
var skaListedCalls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes}

// skaParams is send-keys on r with skaText.
func skaParams(r killRow) api.SendKeysParams {
	return api.SendKeysParams{ClaudeInstanceID: r.ID, Text: skaText}
}

// skaWant is a pane case's answer: pane, the pane typed into by id, or err
// with its description case and the calls made, nothing sent.
type skaWant struct {
	pane  string
	err   error
	desc  apitest.DescCase
	calls []tmux.Call
}

// skaDelivered expects skaText and Enter typed into pane.
func skaDelivered(pane string) skaWant { return skaWant{pane: pane} }

// skaNotFound expects the pane-not-found conflict after the listing.
func skaNotFound(r killRow, lostReply bool) skaWant {
	return skaWant{err: api.ErrTmuxSessionConflict, calls: skaListedCalls,
		desc: apitest.DescPaneNotFound(apitest.PaneNotFound{Verb: apitest.PaneSendKeys, InstanceID: r.ID,
			Name: r.Name, LostReply: lostReply})}
}

// skaCheck runs send-keys on r and fails unless it answers want: delivered by
// pane id (assertDelivered), or want's error and calls with nothing sent.
func skaCheck(t *testing.T, e *killEnv, r killRow, want skaWant) {
	t.Helper()
	_, err := e.sendKeys(skaParams(r))
	if want.err == nil {
		if err != nil {
			t.Fatalf("SendKeys: %v; want delivery to %s", err, want.pane)
		}
		e.assertDelivered(t, r.Socket, want.pane, skaText)
		return
	}
	if !errors.Is(err, want.err) {
		t.Fatalf("SendKeys err = %v; want %v", err, want.err)
	}
	apitest.AssertDescription(t, err.Error(), want.desc, r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
	e.assertNothingSent(t)
	e.assertPaneCalls(t, want.calls...)
}

// skaOtherPane is a pane that is not the agent's: a new id and pid, no pane label.
func skaOtherPane(e *killEnv) tmuxfix.SeedPane { return tmuxfix.SeedPane{PID: e.newPID()} }

// TestSendKeysAgentPane: the keys go once to the agent's pane id wherever it
// now is; a missing or respawned pane is the pane-not-found conflict, unsent.
func TestSendKeysAgentPane(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(t *testing.T, e *killEnv, r *killRow) skaWant
	}{
		{name: "in its own session",
			setup: func(_ *testing.T, _ *killEnv, r *killRow) skaWant { return skaDelivered(r.Spawn.Identity.PaneID) }},
		{name: "moved to another window", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) skaWant {
				moved := r.pane()
				moved.Window, moved.Index = 3, 2
				e.seedOurs(t, r, skaOtherPane(e), moved)
				return skaDelivered(r.Spawn.Identity.PaneID)
			}},
		{name: "moved to another session", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) skaWant {
				e.seedOurs(t, r, skaOtherPane(e))
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "elsewhere", Panes: []tmuxfix.SeedPane{r.pane()}})
				return skaDelivered(r.Spawn.Identity.PaneID)
			}},
		{name: "shown in two sessions (grouped session or linked window)",
			setup: func(t *testing.T, e *killEnv, r *killRow) skaWant {
				e.seedViewer(t, *r)
				return skaDelivered(r.Spawn.Identity.PaneID)
			}},
		{name: "recorded pane id missing from the listing", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) skaWant {
				e.seedOurs(t, r, skaOtherPane(e))
				return skaNotFound(*r, false)
			}},
		{name: "recorded pane missing, another pane carries the row's token", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) skaWant {
				p := skaOtherPane(e)
				p.AdPane = r.Token
				e.seedOurs(t, r, p)
				return skaNotFound(*r, false)
			}},
		{name: "respawned pane: same id, another pid", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) skaWant {
				p := r.pane()
				p.PID = e.newPID()
				e.seedOurs(t, r, p)
				return skaNotFound(*r, false)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			skaCheck(t, e, r, tc.setup(t, e, &r))
		})
	}
}

// TestSendKeysLostReplyPane: a row recording no pane types into the one pane
// carrying its token, at any index; none or two is the lost-reply conflict.
func TestSendKeysLostReplyPane(t *testing.T) {
	t.Parallel()
	token := func(t *testing.T, _ *killEnv, r *killRow) skaWant {
		return skaDelivered(labelledPane(t, r.Session, r.Token))
	}
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(t *testing.T, e *killEnv, r *killRow) skaWant
	}{
		{name: "pane and server identity lost", spec: killRowSpec{NoPane: true, NoServerIdentity: true}, setup: token},
		{name: "only the pane lost", spec: killRowSpec{NoPane: true}, setup: token},
		{name: "a teammate split from the agent's pane", spec: killRowSpec{NoPane: true, NoServerIdentity: true, Teammates: 1},
			setup: token},
		{name: "a teammate at 0.0, the token pane at base-index 1", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) skaWant {
				e.seedOurs(t, r, skaOtherPane(e), tmuxfix.SeedPane{Window: 1, Index: 1, AdPane: r.Token})
				return skaDelivered(labelledPane(t, r.Session, r.Token))
			}},
		{name: "no pane carries the token", spec: killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) skaWant {
				e.seedOurs(t, r, skaOtherPane(e))
				return skaNotFound(*r, true)
			}},
		{name: "two panes carry the token", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) skaWant {
				e.seedOurs(t, r, tmuxfix.SeedPane{AdPane: r.Token}, tmuxfix.SeedPane{Index: 1, AdPane: r.Token})
				return skaNotFound(*r, true)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			skaCheck(t, e, r, tc.setup(t, e, &r))
		})
	}
}

// TestSendKeysListingFails: a pane listing that times out or gives an
// unrecognised reply is ErrTmuxUnresponsive naming the listing; nothing sent.
func TestSendKeysListingFails(t *testing.T) {
	t.Parallel()
	const firstLine = "list-panes: unexpected reply"
	cases := []struct {
		name   string
		script tmuxfix.Script
		desc   func(e *killEnv) apitest.DescCase
	}{
		{"timeout", tmuxfix.Script{Failure: tmux.FailTimeout}, func(e *killEnv) apitest.DescCase {
			return apitest.DescCallTimeout(tmux.CallListPanes, e.cfg.EffectiveQueryTimeout())
		}},
		{"unrecognised reply", tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: firstLine, ExitStatus: 1,
			HadStdout: true}, func(*killEnv) apitest.DescCase {
			return apitest.DescUnrecognisedReply(tmux.CallListPanes, firstLine)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.rec.Script(r.Socket, tc.script, tmux.CallListPanes)
			skaCheck(t, e, r, skaWant{err: api.ErrTmuxUnresponsive, desc: tc.desc(e), calls: skaListedCalls})
		})
	}
}

// skaActions are the keys calls a failure can hit, with the calls made up to
// and including it: the text call (no Enter), or Enter after the text.
var skaActions = []struct {
	call  tmux.Call
	calls []tmux.Call
}{
	{tmux.CallSendText, paneTextCalls},
	{tmux.CallSendEnter, paneSendCalls},
}

// skaAssertTyped fails unless the text call typed skaText into r's agent pane
// and Enter was sent to it exactly when action is the Enter call.
func skaAssertTyped(t *testing.T, e *killEnv, r killRow, action tmux.Call) {
	t.Helper()
	e.assertTextSent(t, r.Socket, r.Spawn.Identity.PaneID, skaText)
	enters := e.rec.SocketCallsOf(tmux.CallSendEnter)
	if want := action == tmux.CallSendEnter; want != (len(enters) == 1) || len(enters) > 1 ||
		(want && enters[0].Target != r.Spawn.Identity.PaneID) {
		t.Errorf("Enter calls = %+v; want one to %s: %v", enters, r.Spawn.Identity.PaneID, want)
	}
}

// TestSendKeysActionTimeout: a timed-out text or Enter call is
// ErrTmuxUnresponsive, the keys may have been delivered, with send-keys'
// next step (b.9o4) and no follow-up.
func TestSendKeysActionTimeout(t *testing.T) {
	t.Parallel()
	for _, a := range skaActions {
		t.Run(string(a.call), func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, a.call)

			_, err := e.sendKeys(skaParams(r))

			assertOneName(t, err, "ErrTmuxUnresponsive")
			apitest.AssertDescription(t, err.Error(),
				apitest.DescKeysTimeout(apitest.PaneSendKeys, a.call, e.cfg.EffectiveActionTimeout()), r.Token, r.StoreID)
			e.assertPaneCalls(t, a.calls...)
			skaAssertTyped(t, e, r, a.call)
		})
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

// TestSendKeysActionFailureFollowUp: a text or Enter call failing other than
// by timeout makes one follow-up lookup whose outcome picks the error (SR-7.3);
// after Enter the text may be typed but not submitted, and an
// ErrTmuxUnresponsive says send-keys with empty text submits it (b.9o4).
// Reply text never classifies.
func TestSendKeysActionFailureFollowUp(t *testing.T) {
	t.Parallel()
	gone := func(r killRow, action tmux.Call) apitest.DescCase {
		return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneSendKeys, InstanceID: r.ID, Name: r.Name,
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
		}), "ErrTmuxNotAvailable", func(r killRow, _ tmux.Call) apitest.DescCase { return apitest.DescDifferentServer(r.ID) }},
		{"tmux unavailable, missing binary", noServer, skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailUnavailable}),
			"ErrTmuxNotAvailable", func(killRow, tmux.Call) apitest.DescCase { return apitest.DescTmuxNotRun() }},
		{"tmux unavailable, socket permission", noServer, skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			"ErrTmuxNotAvailable", func(r killRow, _ tmux.Call) apitest.DescCase { return apitest.DescSocketPermission(r.Socket) }},
	}
	for _, a := range skaActions {
		for _, tc := range cases {
			t.Run(string(a.call)+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{})
				e.seedBystander(t, r.Socket)
				e.rec.Script(r.Socket, tc.script, a.call)
				if tc.world != nil {
					tc.world(t, e, r, a.call)
				}

				_, err := e.sendKeys(skaParams(r))

				assertOneName(t, err, tc.errName)
				desc := tc.desc(r, a.call).AfterTextFailed()
				if a.call == tmux.CallSendEnter {
					desc = tc.desc(r, a.call).AfterEnterFailed(apitest.PaneSendKeys)
				}
				apitest.AssertDescription(t, err.Error(), desc, r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
				e.assertPaneCalls(t, withFollowUp(a.calls)...)
				skaAssertTyped(t, e, r, a.call)
			})
		}
	}
}

// TestSendKeysAdoptionWrite: a lost reply's identity is written once and
// logged adopted only when applied; no write when it adds nothing; the
// write's outcome never changes where the keys go (SR-3.6).
func TestSendKeysAdoptionWrite(t *testing.T) {
	// Serial: it redirects the process-wide standard logger (log.SetOutput).
	lost := killRowSpec{NoPane: true, NoServerIdentity: true}
	tokenPane := func(t *testing.T, r killRow) string { return labelledPane(t, r.Session, r.Token) }
	recorded := func(_ *testing.T, r killRow) string { return r.Spawn.Identity.PaneID }
	noTokenPane := func(t *testing.T, e *killEnv, r *killRow) { e.seedOurs(t, r, skaOtherPane(e)) }
	cases := []struct {
		name    string
		spec    killRowSpec
		setup   func(t *testing.T, e *killEnv, r *killRow)
		pane    func(t *testing.T, r killRow) string // the pane typed into; nil: the pane-not-found conflict
		tries   int                                  // adoption writes attempted
		applied bool
	}{
		{name: "pane and server identity lost", spec: lost, pane: tokenPane, tries: 1, applied: true},
		{name: "only the server identity lost", spec: killRowSpec{NoServerIdentity: true}, pane: recorded, tries: 1,
			applied: true},
		{name: "only the pane lost", spec: killRowSpec{NoPane: true}, pane: tokenPane, tries: 1, applied: true},
		{name: "lost reply, no pane carries the token: the server identity alone",
			spec: killRowSpec{NoPane: true, NoServerIdentity: true, NoSession: true}, setup: noTokenPane, tries: 1,
			applied: true},
		{name: "identity recorded: nothing to add", pane: recorded},
		{name: "only the pane lost, no pane carries the token: nothing to add",
			spec: killRowSpec{NoPane: true, NoSession: true}, setup: noTokenPane},
		{name: "row written between the lookup and the adoption", spec: lost, pane: tokenPane, tries: 1,
			setup: func(t *testing.T, e *killEnv, r *killRow) { e.rowWriteAfter(t, tmux.CallLookup, *r) }},
		{name: "SessionStart between the lookup and the adoption", spec: killRowSpec{NoServerIdentity: true},
			pane: recorded, tries: 1,
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.sessionStartAfter(t, tmux.CallLookup, *r, "sess-ska-"+r.ID)
			}},
		{name: "adoption write fails in the store", spec: lost, pane: tokenPane, tries: 1,
			setup: func(_ *testing.T, e *killEnv, _ *killRow) { e.store.failAdopt(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			if tc.setup != nil {
				tc.setup(t, e, &r)
			}
			send := func() verbRun[api.SendKeysResult] { return e.sendKeysRun(skaParams(r)) }
			logs := &bytes.Buffer{}
			prev := log.Writer()
			log.SetOutput(logs)
			t.Cleanup(func() { log.SetOutput(prev) })
			before := e.adoption(t, r.ID)

			first := send()

			pane := ""
			if tc.pane == nil {
				if !errors.Is(first.err, api.ErrTmuxSessionConflict) {
					t.Fatalf("SendKeys err = %v; want the pane-not-found conflict", first.err)
				}
				e.assertNothingSent(t)
			} else {
				pane = tc.pane(t, r)
				if first.err != nil {
					t.Fatalf("SendKeys: %v; want delivery to %s", first.err, pane)
				}
				e.assertDelivered(t, r.Socket, pane, skaText)
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
			if got := adoptedRecords(t, "send-keys", r.ID); got != wantAdopted {
				t.Errorf("adopted records = %d; want %d", got, wantAdopted)
			}
			if logs.Len() != 0 {
				t.Errorf("log output %q; want none", logs.String())
			}
			if tc.applied {
				assertSameRun(t, send(), first)
				if e.store.adoptTries != tc.tries || adoptedRecords(t, "send-keys", r.ID) != 1 {
					t.Errorf("re-issued: %d writes attempted, %d adopted records; want %d and 1", e.store.adoptTries,
						adoptedRecords(t, "send-keys", r.ID), tc.tries)
				}
			}
		})
	}
}
