package api_test

// advice_follow_pane_test.go (b.fji E1-E9, F6; b.kdf F7): each refusal of send-keys,
// read-pane, pause and decide that says what to do next is followed
// literally, on the kill and pane-verb fixtures (fake tmux, injected clock).
// The agent's input box is modelled from the keys calls (paneInput,
// pane_input_fixture_test.go), so a retry that types or submits twice is seen.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// advPaneNothingRetry is the unreadable refusal's advice before any keys went (E1, E4).
const advPaneNothingRetry = "nothing was done; retry later"

// advPaneRetryLater follows "retry later" on call: refused with advice and
// nothing sent, refused unchanged while the scripted condition holds, then
// (the script used up) it must succeed.
func advPaneRetryLater(t *testing.T, e *killEnv, advice string, call func() error) {
	t.Helper()
	first := call()
	adviceAssertAdvice(t, first, api.ErrTmuxUnresponsive, advice)
	if again := call(); errText(again) != errText(first) {
		t.Errorf("retried while tmux still fails: %v; want the same refusal %v", again, first)
	}
	e.assertNothingSent(t)
	if err := call(); err != nil {
		t.Fatalf("retried once tmux answers: %v; want success", err)
	}
}

// advPaneVerb starts a pane verb on r: it returns the call and the check of
// its effect once the call succeeded.
type advPaneVerb func(t *testing.T, e *killEnv, r killRow) (call func() error, done func(*testing.T))

// advPaneSendKeys is send-keys of skaText; done: the agent got it submitted once.
func advPaneSendKeys(_ *testing.T, e *killEnv, r killRow) (func() error, func(*testing.T)) {
	in := watchPaneInput(e, r.Spawn.Identity.PaneID, false)
	return func() error { _, err := e.sendKeys(skaParams(r)); return err },
		func(t *testing.T) { in.assertSubmittedOnce(t, skaText) }
}

// advPaneReadPane is read-pane; done: the last call read the agent's pane.
func advPaneReadPane(t *testing.T, e *killEnv, r killRow) (func() error, func(*testing.T)) {
	e.setPaneTexts(r.Socket)
	var got api.ReadPaneResult
	return func() (err error) {
			got, err = e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID})
			return err
		}, func(t *testing.T) {
			if want := paneText(r.Socket, r.Spawn.Identity.PaneID); got.Pane != want {
				t.Errorf("read-pane = %q; want the agent's pane %q", got.Pane, want)
			}
		}
}

// advPanePause is pause on an agent that exits on its first /exit; done: /exit submitted once, row ended.
func advPanePause(t *testing.T, e *killEnv, r killRow) (func() error, func(*testing.T)) {
	fastPausePolls(t)
	in := paneAgentExiting(t, e, r, false, 1)
	return func() error { _, err := e.pause(pauseParams(r)); return err },
		func(t *testing.T) {
			in.assertSubmittedOnce(t, exitText)
			pauAssertState(t, e, r.ID, store.StateEnded)
		}
}

// TestAdviceFollow_E1_PaneVerbLookupUnreadableRetryLater: E1 "nothing was
// done; retry later" after an unreadable lookup or pane listing; the same
// call re-issued is refused alike while tmux fails, then does its work.
func TestAdviceFollow_E1_PaneVerbLookupUnreadableRetryLater(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	failures := []struct {
		name   string
		call   tmux.Call
		script tmuxfix.Script
	}{
		{"lookup timed out", tmux.CallLookup, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 2}},
		{"pane listing gave an unrecognised reply", tmux.CallListPanes, tmuxfix.Script{Failure: tmux.FailUnrecognized,
			FirstLine: "list-panes: unexpected reply", ExitStatus: 1, HadStdout: true, Times: 2}},
	}
	verbs := []struct {
		name  string
		start advPaneVerb
	}{{"send-keys", advPaneSendKeys}, {"read-pane", advPaneReadPane}, {"pause", advPanePause}}
	for _, v := range verbs {
		for _, f := range failures {
			t.Run(v.name+"/"+f.name, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{})
				call, done := v.start(t, e, r)
				e.rec.Script(r.Socket, f.script, f.call)

				advPaneRetryLater(t, e, advPaneNothingRetry, call)

				done(t)
			})
		}
	}
}

// TestAdviceFollow_E4_ReadPaneCaptureFailedRetryLater: E4 "nothing was done;
// retry later" after a failed capture; read-pane re-issued reads the pane once tmux answers.
func TestAdviceFollow_E4_ReadPaneCaptureFailedRetryLater(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		script tmuxfix.Script
	}{
		{"capture timed out", tmuxfix.Script{Failure: tmux.FailTimeout, Times: 2}},
		{"capture failed, the follow-up lookup found the session", tmuxfix.Script{Failure: tmux.FailNoServer, Times: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			call, done := advPaneReadPane(t, e, r)
			e.rec.Script(r.Socket, tc.script, tmux.CallCapture)

			advPaneRetryLater(t, e, advPaneNothingRetry, call)

			done(t)
		})
	}
}

// advPaneKeysFailure is a failed keys call: the call scripted to fail once,
// whether a timed-out call reached the pane, the advice its description
// carries and what the agent's input box holds before the first call.
type advPaneKeysFailure struct {
	name    string
	call    tmux.Call
	script  tmuxfix.Script
	reached bool
	advice  string
	typed   string
}

// The keys-failure advice sentences (SR-1.4, SR-7.3; b.9o4): send-keys names
// its follow-up (read-pane, and send-keys with empty text, which presses
// Enter only); pause keeps "retry later", its retry clearing the line first.
const (
	advSendKeysTextTimeout    = "the keys may have been delivered; read-pane; if the text is typed, send-keys with empty text, otherwise the same send-keys"
	advSendKeysEnterTimeout   = "the keys may have been delivered; the text may be typed but not submitted; send-keys with empty text submits it"
	advSendKeysNotSubmitted   = "the text may be typed but not submitted; send-keys with empty text submits it"
	advPauseKeysMayHaveRetry  = "the keys may have been delivered; retry later"
	advPauseEnterTimeoutRetry = "the keys may have been delivered; the text may be typed but not submitted; retry later"
	advPauseNotSubmittedRetry = "the text may be typed but not submitted; retry later"
)

// advSendKeysEmptyTextManifest is what send-keys' manifest text (help, MCP
// tools/list) says empty text does, which "send-keys with empty text" relies on.
const advSendKeysEmptyTextManifest = "(empty text: Enter only)"

// pause's line clear (C-u) failing before /exit (b.9o4): its descriptions
// name the key send, word for word at the default 2 s action timeout, and
// end "retry later"; a draft the agent's input box holds then is cleared by
// the retry's own line clear.
const (
	advPauseClearTimeout = "tmux key send failed: no answer within 2 s; the keys may have been delivered; retry later"
	advPauseClearReply   = "key send: unexpected reply"
	advPauseClearFailed  = "tmux key send failed: unrecognized reply: " + advPauseClearReply +
		"; tmux gave a reply agent-director does not recognise; the follow-up lookup found this launch's session; " +
		"nothing was done; retry later"
	advPauseDraft = "/mcp reconnect github"
)

// advPaneEnterFailures are a failed Enter after the text went through, its
// description carrying timedOut after a timeout and failed otherwise.
func advPaneEnterFailures(timedOut, failed string) []advPaneKeysFailure {
	once := func(f tmux.Failure) tmuxfix.Script { return tmuxfix.Script{Failure: f, Times: 1} }
	return []advPaneKeysFailure{
		{name: "Enter timed out before reaching the pane", call: tmux.CallSendEnter, script: once(tmux.FailTimeout),
			advice: timedOut},
		{name: "Enter failed, the follow-up lookup found the session", call: tmux.CallSendEnter,
			script: once(tmux.FailNoServer), advice: failed},
	}
}

// advPaneEnterSubmitted is a timed-out Enter that submitted what was typed,
// its description carrying advice.
func advPaneEnterSubmitted(what, advice string) advPaneKeysFailure {
	return advPaneKeysFailure{name: "Enter timed out after submitting " + what, call: tmux.CallSendEnter,
		script: tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, reached: true, advice: advice}
}

// advPaneKeysRefused seeds a live row watching its agent's input box, fails
// f's call once and sends skaText, which must be refused with f's advice.
func advPaneKeysRefused(t *testing.T, f advPaneKeysFailure) (*killEnv, killRow, *paneInput) {
	t.Helper()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	in := watchPaneInput(e, r.Spawn.Identity.PaneID, f.reached)
	e.rec.Script(r.Socket, f.script, f.call)
	_, err := e.sendKeys(skaParams(r))
	adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, f.advice)
	return e, r, in
}

// TestAdviceFollow_E2_SendKeysTextTimeoutReadPaneThenEmptyText: E2 "the keys
// may have been delivered; read-pane; if the text is typed, send-keys with
// empty text, otherwise the same send-keys" after the text call timed out;
// followed literally, the agent has the text submitted once.
func TestAdviceFollow_E2_SendKeysTextTimeoutReadPaneThenEmptyText(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "send-keys", "", advSendKeysEmptyTextManifest)
	timeout := tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}
	for _, f := range []advPaneKeysFailure{
		{name: "the timed-out text call did not reach the pane", call: tmux.CallSendText, script: timeout,
			advice: advSendKeysTextTimeout},
		{name: "the timed-out text call typed the text", call: tmux.CallSendText, script: timeout, reached: true,
			advice: advSendKeysTextTimeout},
	} {
		t.Run(f.name, func(t *testing.T) {
			e, r, in := advPaneKeysRefused(t, f)

			read, err := e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID})
			if err != nil {
				t.Fatalf("read-pane: %v; want the agent's pane", err)
			}
			p := skaParams(r)
			if strings.Contains(read.Pane, skaText) {
				p.Text = ""
			}
			if _, err := e.sendKeys(p); err != nil {
				t.Fatalf("send-keys of %q after read-pane: %v; want delivery", p.Text, err)
			}
			in.assertSubmittedOnce(t, skaText)
		})
	}
}

// TestAdviceFollow_E3_SendKeysEnterFailedEmptyTextSubmits: E3 "the text may
// be typed but not submitted; send-keys with empty text submits it" (after a
// timeout, "the keys may have been delivered; " first) after the Enter call
// failed; send-keys with empty text must leave the text submitted once, also
// when the timed-out Enter had submitted it (Enter on an empty input submits
// nothing).
func TestAdviceFollow_E3_SendKeysEnterFailedEmptyTextSubmits(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "send-keys", "", advSendKeysEmptyTextManifest)
	failures := append(advPaneEnterFailures(advSendKeysEnterTimeout, advSendKeysNotSubmitted),
		advPaneEnterSubmitted("the text", advSendKeysEnterTimeout))
	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			e, r, in := advPaneKeysRefused(t, f)

			if _, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID}); err != nil {
				t.Fatalf("send-keys with empty text: %v; want Enter delivered", err)
			}
			in.assertSubmittedOnce(t, skaText)
		})
	}
}

// TestAdviceFollow_E5_PauseKeysFailedRetryLater: E5 "the keys may have been
// delivered; retry later" (and pause's Enter failures, "the text may be typed
// but not submitted; retry later", and its line clear's, "tmux key send
// failed: ... retry later"); pause re-issued must end the row and never
// submit /exit/exit or /exit joined to a draft: a no-op on a finished row,
// else /exit submitted once, the input line cleared before it is typed
// (b.9o4).
func TestAdviceFollow_E5_PauseKeysFailedRetryLater(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	timeout := tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}
	failures := append([]advPaneKeysFailure{
		{name: "the line clear timed out before reaching the pane, a draft typed", call: tmux.CallSendKey,
			script: timeout, advice: advPauseClearTimeout, typed: advPauseDraft},
		{name: "the line clear timed out after clearing a draft", call: tmux.CallSendKey, script: timeout,
			reached: true, advice: advPauseClearTimeout, typed: advPauseDraft},
		{name: "the line clear failed, the follow-up lookup found the session, a draft typed", call: tmux.CallSendKey,
			script: tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: advPauseClearReply, ExitStatus: 1, Times: 1},
			advice: advPauseClearFailed, typed: advPauseDraft},
		{name: "the /exit text call timed out before reaching the pane", call: tmux.CallSendText, script: timeout,
			advice: advPauseKeysMayHaveRetry},
		{name: "the /exit text call timed out after typing /exit", call: tmux.CallSendText, script: timeout,
			reached: true, advice: advPauseKeysMayHaveRetry},
		advPaneEnterSubmitted("/exit", advPauseEnterTimeoutRetry),
	}, advPaneEnterFailures(advPauseEnterTimeoutRetry, advPauseNotSubmittedRetry)...)
	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			fastPausePolls(t)
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			in := paneAgentExiting(t, e, r, f.reached, 1)
			in.box = f.typed
			e.rec.Script(r.Socket, f.script, f.call)

			_, err := e.pause(pauseParams(r))
			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, f.advice)

			if _, err := e.pause(pauseParams(r)); err != nil {
				t.Fatalf("re-issued pause: %v; want the row ended (agent got %q, %q left typed)", err, in.submitted, in.box)
			}
			in.assertSubmittedOnce(t, exitText)
			pauAssertState(t, e, r.ID, store.StateEnded)
		})
	}
}

// The relay guard's advice (b.2b8, b.ah6, b.ceq): send-keys' refusal says only
// to answer the open request it names with decide, to retry send-keys later
// when the request it names is decided, or, with none recorded yet, to answer
// it with decide once get lists it (E6); decide's fallen-back refusal says only
// to answer at the pane with send-keys (E7, b.pzy), or, when the Spawn is not
// shown sitting on that request alone, not to answer it at the pane (F6,
// b.t6e). A request the relay hook denied at its deadline is ErrAlreadyDecided
// naming that deny.
const (
	advSendKeysNoRequestYet = "is awaiting a relayed permission decision whose request is not yet recorded; " +
		"answer it with decide once get lists it"
	advDecideFallenBack = "fell back — too late; its record is still open and its relay hook can no longer answer it; " +
		"answer at the pane with send-keys"
	advDecideNotShown     = "so its permission dialog cannot be shown to be on screen; do not answer it at the pane"
	advDecideHookTimedOut = `already decided as "deny" (decision_reason "timeout")`
)

// advSendKeysAnswerWithDecide is E6's advice naming token, the open request holding the relay guard.
func advSendKeysAnswerWithDecide(token string) string {
	return "is awaiting a relayed permission decision on request " + token + "; answer it with decide"
}

// advSendKeysRetryLater is E6's advice naming token, the decided request holding the relay guard (b.ceq).
func advSendKeysRetryLater(token string) string {
	return "the relayed permission verdict on request " + token +
		" is recorded and its relay hook may still be delivering it; retry send-keys later"
}

// advRelayAdvice is adviceAssertAdvice for a relay refusal, which ends with its
// advice and states no release time or margin: agent-director absorbs them (b.ah6).
func advRelayAdvice(t *testing.T, err, want error, advice string) {
	t.Helper()
	adviceAssertAdvice(t, err, want, advice)
	desc := errText(err)
	if !strings.HasSuffix(desc, advice) {
		t.Errorf("description %q\nwant it to end with %q", desc, advice)
	}
	for _, w := range []string{"releases", "1 s", "delivery window", "margin", "already has"} {
		if strings.Contains(desc, w) {
			t.Errorf("description %q states a release time (%q); want only what to call (b.ah6)", desc, w)
		}
	}
}

// advRequestCreatedAt is the stored created_at of r's request A.
func advRequestCreatedAt(t *testing.T, e *killEnv, r killRow) time.Time {
	t.Helper()
	pr, err := e.st.GetPermissionRequest(r.ID, storefix.TestRequestTokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	return pr.CreatedAt
}

// advRelayHookClock is a forward-only clock from createdAt, request A's stored created_at; with deny > 0, A's live
// relay hook records its timeout deny, which closes the dialog, as the clock reaches createdAt + deny.
func advRelayHookClock(t *testing.T, e *killEnv, r killRow, createdAt time.Time, deny time.Duration) (*time.Time, func(time.Time)) {
	now, at := createdAt, createdAt.Add(deny)
	return &now, func(to time.Time) {
		if deny > 0 && now.Before(at) && !to.Before(at) {
			relayHookTimeout(t, e, r.ID, storefix.TestRequestTokenA)
		}
		now = to
	}
}

// TestAdviceFollow_E6_SendKeysWhileRelayedAnswerWithDecide: E6 "on request <token>; answer it with decide"; decide on
// that token records the verdict, is ErrAlreadyDecided by a live relay hook's late timeout deny with nothing typed
// (b.z6g), or is ErrRelayFallenBack and send-keys then delivers at once (b.ah6).
func TestAdviceFollow_E6_SendKeysWhileRelayedAnswerWithDecide(t *testing.T) {
	t.Parallel()
	// The hook's timeout path (deny, then working) ends 1.5 s past its poll deadline, created_at + window.
	lateDeny := relayGuardWindow + 1500*time.Millisecond
	cases := []struct {
		name string
		age  time.Duration // the request's age when send-keys is refused
		deny time.Duration // after created_at, when a live relay hook's timeout deny lands; 0: none does
		want error         // decide's outcome; nil: the verdict recorded
	}{
		{"request in its window", relayGuardWindow / 2, 0, nil},
		{"request just before decide starts refusing it", relayGuardWindow - api.RelayKillSafetyMargin - time.Nanosecond, 0, nil},
		{"request as decide starts refusing it", relayGuardWindow - api.RelayKillSafetyMargin, 0, api.ErrRelayFallenBack},
		{"request just before its relay hook settles", relayGuardSettled - time.Nanosecond, 0, api.ErrRelayFallenBack},
		{"request just before its live relay hook's late timeout deny", lateDeny - time.Nanosecond, lateDeny,
			store.ErrAlreadyDecided},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := seedRelayRow(t, e, storefix.TestRequestTokenA)
			createdAt := advRequestCreatedAt(t, e, r)
			now, advance := advRelayHookClock(t, e, r, createdAt, tc.deny)
			advance(createdAt.Add(tc.age))
			send := func() error {
				_, err := e.sendKeysAt(relayGuardWindow, *now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
				return err
			}
			advRelayAdvice(t, send(), api.ErrSendKeysWhileRelayed, advSendKeysAnswerWithDecide(storefix.TestRequestTokenA))
			e.assertNoTmuxCall(t)

			// The advice, literally: decide the request it names; decide's wait moves the clock.
			_, err := api.DecideWithSleep(e.st, api.RelayView{Window: relayGuardWindow, Now: func() time.Time { return *now }}, func(d time.Duration) { advance(now.Add(d)) },
				api.DecideParams{ClaudeInstanceID: r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})

			switch tc.want {
			case nil:
				if err != nil {
					t.Fatalf("decide: %v; want the verdict recorded for the relay hook to deliver", err)
				}
				if pr, err := e.st.GetPermissionRequest(r.ID, storefix.TestRequestTokenA); err != nil || pr.Decision != "allow" {
					t.Errorf("request after decide: decision %q, %v; want allow recorded", pr.Decision, err)
				}
			case store.ErrAlreadyDecided:
				// The hook's deny closed the dialog; no keys reached the pane while it was open.
				assertOneSentinel(t, err, store.ErrAlreadyDecided)
				adviceAssertPhrase(t, err, advDecideHookTimedOut)
				e.assertNoTmuxCall(t)
			default:
				assertOneSentinel(t, err, api.ErrRelayFallenBack)
				if err := send(); err != nil {
					t.Fatalf("send-keys at once after decide's ErrRelayFallenBack: %v; want delivery", err)
				}
				e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
			}
		})
	}
}

// TestAdviceFollow_E6_SendKeysWhileRelayedDecidedRequestRetryLater: E6 "the relayed permission verdict on request
// <token> is recorded and its relay hook may still be delivering it; retry send-keys later" naming the one request,
// allowed in its window (b.ceq). send-keys retried is refused alike while the row stays check_permission, and delivers
// once the relay hook's delivery takes the row out of it, or, the hook dead, once that hook is presumed settled (b.z6g).
func TestAdviceFollow_E6_SendKeysWhileRelayedDecidedRequestRetryLater(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	for _, tc := range []struct {
		name      string
		delivered bool // the relay hook delivers the allow; otherwise it died after the allow was recorded
	}{
		{"the relay hook delivers the verdict", true},
		{"the relay hook died before delivering the verdict", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := seedRelayRow(t, e, tok)
			createdAt := advRequestCreatedAt(t, e, r)
			now := createdAt.Add(relayGuardWindow / 2)
			if _, err := api.Decide(e.st, api.RelayView{Window: relayGuardWindow, Now: func() time.Time { return now }},
				api.DecideParams{ClaudeInstanceID: r.ID, RequestToken: tok, Decision: "allow"}); err != nil {
				t.Fatalf("decide in the window: %v; want the allow recorded", err)
			}
			send := func() error {
				_, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
				return err
			}

			first := send()
			advRelayAdvice(t, first, api.ErrSendKeysWhileRelayed, advSendKeysRetryLater(tok))
			e.assertNoTmuxCall(t)

			// The advice, literally: send-keys again later. Up to its relay hook's
			// settling the row stays check_permission.
			now = createdAt.Add(relayGuardSettled - time.Nanosecond)
			if again := send(); errText(again) != errText(first) {
				t.Errorf("send-keys retried while the row stays check_permission: %v; want the same refusal %v", again, first)
			}
			e.assertNoTmuxCall(t)

			if tc.delivered {
				// The agent runs the tool; no other request is open, so its move to
				// working takes the row out of check_permission.
				if err := seedAgentState(e.st, e.dbPath, r.ID, store.StateWorking); err != nil {
					t.Fatalf("agent's move to working: %v", err)
				}
			} else {
				now = now.Add(time.Nanosecond) // the request's relay hook is presumed settled
				pauAssertState(t, e, r.ID, store.StateCheckPermission)
			}
			if err := send(); err != nil {
				t.Fatalf("send-keys retried later: %v; want delivery", err)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
		})
	}
}

// TestAdviceFollow_E6_SendKeysNoRequestYetDecideOnceGetLists: E6 "whose request is not yet recorded;
// answer it with decide once get lists it"; refused alike until then, decide on get's token records the verdict.
func TestAdviceFollow_E6_SendKeysNoRequestYetDecideOnceGetLists(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := seedRelayRow(t, e)
	send := func() error {
		_, err := e.sendKeysAt(relayGuardWindow, time.Now(), api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
		return err
	}
	listed := func() []api.PermissionRequestInfo {
		t.Helper()
		row, err := api.Get(e.st, api.RelayView{}, r.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		return row.PermissionRequests
	}
	first := send()
	advRelayAdvice(t, first, api.ErrSendKeysWhileRelayed, advSendKeysNoRequestYet)
	if got := listed(); len(got) != 0 {
		t.Fatalf("get lists %+v with no request recorded; want none", got)
	}
	if again := send(); errText(again) != errText(first) {
		t.Errorf("retried before get lists the request: %v; want the same refusal %v", again, first)
	}
	e.assertNoTmuxCall(t)

	storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA}) // the relay hook records it
	got := listed()
	if len(got) != 1 {
		t.Fatalf("get lists %+v once the request is recorded; want it", got)
	}
	token := got[0].RequestToken
	if _, err := api.Decide(e.st, api.RelayView{Window: relayGuardWindow},
		api.DecideParams{ClaudeInstanceID: r.ID, RequestToken: token, Decision: "allow"}); err != nil {
		t.Fatalf("decide on get's request_token %s: %v; want the verdict recorded for the relay hook to deliver", token, err)
	}
	if pr, err := e.st.GetPermissionRequest(r.ID, token); err != nil || pr.Decision != "allow" {
		t.Errorf("request after decide: decision %q, %v; want allow recorded", pr.Decision, err)
	}
}

// TestAdviceFollow_E7_DecideFallenBackOtherRequestHoldsGuard: E7's Go doc "... the refusal names that request;
// answer that one with decide", for a request recorded after decide's last read (one recorded before it makes decide
// refuse with F6, b.t6e); followed, the pane answer lands once that request's verdict is delivered.
func TestAdviceFollow_E7_DecideFallenBackOtherRequestHoldsGuard(t *testing.T) {
	t.Parallel()
	adviceAssertGoDoc(t, "decide.go", "ErrRelayFallenBack", "If send-keys still refuses with ErrSendKeysWhileRelayed, "+
		"another of the Spawn's requests holds it and the refusal names that request; answer that one with decide, as that error says.")
	tokA, tokB := storefix.TestRequestTokenA, storefix.TestRequestTokenB
	e := newKillEnv(t)
	r := seedRelayRow(t, e, tokA)
	storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, tokA, 2*relayGuardWindow)
	now := time.Now() // A is an hour past its window
	decide := func(token string) error {
		_, err := api.DecideWithSleep(e.st, api.RelayView{Window: relayGuardWindow, Now: func() time.Time { return now }}, func(d time.Duration) { now = now.Add(d) },
			api.DecideParams{ClaudeInstanceID: r.ID, RequestToken: token, Decision: "allow"})
		return err
	}
	send := func() error {
		_, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
		return err
	}

	advRelayAdvice(t, decide(tokA), api.ErrRelayFallenBack, advDecideFallenBack)
	storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{tokB}) // a subagent's request, in its own window
	advRelayAdvice(t, send(), api.ErrSendKeysWhileRelayed, advSendKeysAnswerWithDecide(tokB))
	e.assertNoTmuxCall(t)
	if err := decide(tokB); err != nil {
		t.Fatalf("decide %s, which the refusal names: %v; want the verdict recorded", tokB, err)
	}
	// B's relay hook delivers the allow and the agent runs that tool; A's open
	// record holds its move to working, so the row stays check_permission.
	if err := seedAgentState(e.st, e.dbPath, r.ID, store.StateWorking); err != nil {
		t.Fatalf("agent's move to working: %v", err)
	}
	pauAssertState(t, e, r.ID, store.StateCheckPermission)

	if err := send(); err != nil {
		t.Fatalf("send-keys (A's answer at the pane) once B's verdict is delivered: %v; want delivery", err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
}

// TestAdviceFollow_E7_DecideFallenBackAnswerAtPane: E7 "fell back — too late;
// ... answer at the pane with send-keys", for a request recorded before schema
// v7 (judged by time). A caller that branches on decide's
// error name and answers at the pane at once, at the instant decide returned,
// is not refused by the relay guard (b.ah6) and never types into Claude's
// prompt after a live relay hook denied the request at its poll deadline
// (b.pzy). Decide's earliest refusal, window - 1 s (SR-4.2, SR-4.4), is
// TestDecideDeliverabilityBoundary's.
func TestAdviceFollow_E7_DecideFallenBackAnswerAtPane(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "decide", "", "first call wins (else ErrAlreadyDecided)",
		"refused at once with ErrRelayFallenBack: only a pane answer can close it")
	settled := relayGuardSettled // decide's last read of the request and the guard's release
	ages := []struct {
		name string
		age  time.Duration // the request's age when decide is called
	}{
		{"request an hour past its window", 2 * relayGuardWindow},
		{"request at decide's last read of it", settled},
		{"request just before decide's last read of it", settled - time.Nanosecond},
		{"request a margin past its window", relayGuardWindow + api.RelayKillSafetyMargin},
		{"request at the end of its window", relayGuardWindow},
		{"request as decide starts refusing it", relayGuardWindow - api.RelayKillSafetyMargin},
	}
	hooks := []struct {
		name string
		deny time.Duration // after created_at, when the live relay hook's timeout deny lands; 0: the hook is dead
	}{
		{"relay hook dead", 0},
		{"relay hook alive to its poll deadline", relayGuardWindow},
		// The hook's poll deadline is created_at + window (b.z6g); its timeout
		// path (deny, then working) may land up to just before decide's last read.
		{"relay hook alive, its timeout deny landing just before decide's last read", settled - time.Nanosecond},
	}
	for _, a := range ages {
		for _, h := range hooks {
			hookAlive := h.deny > 0
			if hookAlive && a.age >= h.deny {
				continue // the hook denied before decide was called: TestDecideFirstCallWins's path
			}
			t.Run(a.name+"/"+h.name, func(t *testing.T) {
				e := newKillEnv(t)
				r := seedRelayRow(t, e, storefix.TestRequestTokenA)
				createdAt := advRequestCreatedAt(t, e, r)
				now, advance := advRelayHookClock(t, e, r, createdAt, h.deny)
				advance(createdAt.Add(a.age))

				_, err := api.DecideWithSleep(e.st, api.RelayView{Window: relayGuardWindow, Now: func() time.Time { return *now }}, func(d time.Duration) { advance(now.Add(d)) },
					api.DecideParams{ClaudeInstanceID: r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})
				answered := false
				if errors.Is(err, api.ErrRelayFallenBack) {
					// At once: the clock stays at the instant decide returned.
					if _, err := e.sendKeysAt(relayGuardWindow, *now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"}); err != nil {
						t.Fatalf("answer at the pane (send-keys) at once, %v after created_at: %v; want it accepted",
							now.Sub(createdAt), err)
					}
					answered = true
				}

				if !hookAlive {
					advRelayAdvice(t, err, api.ErrRelayFallenBack, advDecideFallenBack)
					e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
					return
				}
				assertOneSentinel(t, err, store.ErrAlreadyDecided)
				adviceAssertPhrase(t, err, advDecideHookTimedOut)
				if answered {
					t.Errorf("the answer was sent at the pane, but the live relay hook's timeout deny (%v after created_at) "+
						"closes the dialog: the answer lands in Claude's prompt", h.deny)
				}
				e.assertNoTmuxCall(t)
			})
		}
	}
}

// TestAdviceFollow_F6_DecideNotShownDoNotAnswerAtPane: F6 "so its permission dialog cannot be shown to be on screen;
// do not answer it at the pane". Request A, recorded before schema v7, had its dialog answered at the pane after its
// relay hook was killed, leaving its record open; a caller that branches on decide's error name types nothing into
// Claude's prompt (b.t6e). The advice is in the error text only: decide's manifest Description no longer carries it.
func TestAdviceFollow_F6_DecideNotShownDoNotAnswerAtPane(t *testing.T) {
	t.Parallel()
	tokA, tokB := storefix.TestRequestTokenA, storefix.TestRequestTokenB
	cases := []struct {
		name  string
		after func(t *testing.T, e *killEnv, r killRow, now time.Time) // what the agent did after A's pane answer
	}{
		{"the agent's Stop", func(t *testing.T, e *killEnv, r killRow, _ time.Time) {
			if err := seedAgentState(e.st, e.dbPath, r.ID, store.StateWaiting); err != nil {
				t.Fatalf("agent's Stop: %v", err)
			}
		}},
		// B's decided verdict no longer holds the send-keys guard once A has fallen back (b.ceq).
		{"the agent's next request decided", func(t *testing.T, e *killEnv, r killRow, now time.Time) {
			storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{tokB})
			if _, err := api.Decide(e.st, api.RelayView{Window: relayGuardWindow, Now: func() time.Time { return now }},
				api.DecideParams{ClaudeInstanceID: r.ID, RequestToken: tokB, Decision: "allow"}); err != nil {
				t.Fatalf("decide %s in its window: %v", tokB, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := seedRelayRow(t, e, tokA)
			storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, tokA, 2*relayGuardWindow)
			now := time.Now() // A is an hour past its window
			// The agent ran A's tool; its move to working is held while A's record is open.
			if err := seedAgentState(e.st, e.dbPath, r.ID, store.StateWorking); err != nil {
				t.Fatalf("agent's move to working: %v", err)
			}
			tc.after(t, e, r, now)

			_, err := api.DecideWithSleep(e.st, api.RelayView{Window: relayGuardWindow, Now: func() time.Time { return now }}, func(d time.Duration) { now = now.Add(d) },
				api.DecideParams{ClaudeInstanceID: r.ID, RequestToken: tokA, Decision: "allow"})
			if errors.Is(err, api.ErrRelayFallenBack) { // E7's advice, as a caller branching on the name follows it
				_, _ = e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
			}

			advRelayAdvice(t, err, store.ErrNoOpenPermissionRequest, advDecideNotShown)
			e.assertNoTmuxCall(t) // the advice followed: nothing typed into Claude's prompt
		})
	}
}

// decideStep is one arrangement of a decide case's store, before decide runs.
type decideStep func(t *testing.T, s *store.Store, dbPath string)

// decideSpawnEnds is a DecideStore that runs end once, right after decide's first read of the Spawn: the Spawn
// finishes between that read and the guarded write.
type decideSpawnEnds struct {
	*store.Store
	end  func()
	done *bool
}

func (d decideSpawnEnds) GetSpawn(id string) (store.Spawn, error) {
	sp, err := d.Store.GetSpawn(id)
	if !*d.done {
		*d.done = true
		d.end()
	}
	return sp, err
}

// TestAdviceFollow_F7_DecideFinishedSpawnNothingRecorded: F7 "nothing was recorded; do not answer it at the pane"
// (b.146 rule 12). A request of an ended or missing Spawn is closed: decide records nothing (request A stays as it
// was) under an existing error name, ErrAlreadyDecided for a decided request (find-missing's deny when it marked the
// Spawn missing), else ErrNoOpenPermissionRequest carrying F7 when the request is open, or when the mark closed it
// before its relay hook acked the verdict recorded on it; also when the Spawn finishes after decide read it.
func TestAdviceFollow_F7_DecideFinishedSpawnNothingRecorded(t *testing.T) {
	t.Parallel()
	var end decideStep = func(t *testing.T, s *store.Store, dbPath string) {
		if err := seedAgentState(s, dbPath, "id-d-1", store.StateEnded); err != nil {
			t.Fatalf("SessionEnd: %v", err)
		}
	}
	var markMissing decideStep = func(t *testing.T, s *store.Store, _ string) {
		sp, err := s.GetSpawn("id-d-1")
		if err != nil {
			t.Fatalf("GetSpawn: %v", err)
		}
		if _, res, err := s.MarkMissingIfSameLife("id-d-1", sp.Snapshot); err != nil || res != store.CondApplied {
			t.Fatalf("mark = %v, %v", res, err)
		}
	}
	var allowA decideStep = func(t *testing.T, s *store.Store, _ string) {
		if ok, err := s.DecidePermissionRequest("id-d-1", storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide); err != nil || !ok {
			t.Fatalf("decide A = %v, %v", ok, err)
		}
	}
	var openA decideStep = func(t *testing.T, s *store.Store, _ string) { apitest.SeedPermissionRow(t, s, "id-d-1") }
	// relayAllowA is request A recorded by a relay hook (schema v7) and decide's allow on it, not acked.
	var relayAllowA decideStep = func(t *testing.T, s *store.Store, _ string) {
		storefix.SeedRelayRequest(t, s, "id-d-1", store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
			ToolInput: `{}`, Hook: relayEnvHook, SettledAt: time.Now().Add(time.Hour)})
		if ok, err := s.DecideRelayRequest("id-d-1", storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide,
			time.Time{}, store.DefaultLockWait); err != nil || !ok {
			t.Fatalf("decide A = %v, %v", ok, err)
		}
	}
	cases := []struct {
		name         string
		steps        []decideStep // in order, before decide
		race         bool         // the Spawn ends after decide's read instead
		want         error
		phrase       string // carried by the refusal with F7 ("": neither checked)
		decision     string // request A's decision afterwards ("": open)
		noRequestRow bool
	}{
		{name: "ended, request open", steps: []decideStep{openA, end}, want: store.ErrNoOpenPermissionRequest, phrase: "the spawn is ended"},
		{name: "ended, request decided", steps: []decideStep{openA, allowA, end}, want: store.ErrAlreadyDecided, decision: "allow"},
		{name: "ended, no such request", steps: []decideStep{end}, want: store.ErrNoOpenPermissionRequest, noRequestRow: true},
		{name: "missing, request denied by the mark", steps: []decideStep{openA, markMissing}, want: store.ErrAlreadyDecided, decision: "deny"},
		{name: "missing, decided before the mark, not acked", steps: []decideStep{relayAllowA, markMissing}, want: store.ErrNoOpenPermissionRequest,
			phrase: "find-missing marked the spawn missing before the request's relay hook delivered a verdict", decision: "allow"},
		{name: "missing, request recorded after the mark", steps: []decideStep{markMissing, openA}, want: store.ErrNoOpenPermissionRequest,
			phrase: "the spawn is missing"},
		{name: "ended after decide read the spawn", steps: []decideStep{openA}, race: true, want: store.ErrNoOpenPermissionRequest,
			phrase: "the spawn is ended"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, dbPath := apitest.SeedDecideFixture(t, "on")
			for _, step := range tc.steps {
				step(t, s, dbPath)
			}
			var ds api.DecideStore = s
			if tc.race {
				ds = decideSpawnEnds{Store: s, end: func() { end(t, s, dbPath) }, done: new(bool)}
			}

			_, err := api.Decide(ds, api.RelayView{Window: 24 * time.Hour}, api.DecideParams{ClaudeInstanceID: "id-d-1",
				RequestToken: storefix.TestRequestTokenA, Decision: "allow"})

			assertOneSentinel(t, err, tc.want)
			if tc.phrase != "" {
				adviceAssertPhrase(t, err, tc.phrase)
				adviceAssertPhrase(t, err, "nothing was recorded; do not answer it at the pane")
			}
			assertNoPaneAdvice(t, err)
			if tc.noRequestRow {
				return
			}
			if got := rowA(t, s).Decision; got != tc.decision {
				t.Errorf("request A decision = %q after decide; want %q (nothing recorded)", got, tc.decision)
			}
		})
	}
}

// TestAdviceFollow_E8_PauseTimeoutRetryPause: E8 Go doc "The caller's
// recourse is to retry the pause; the agent may still be running."; the
// retried pause must leave the row ended. The wait's deadline is real time,
// so a 0 s timeout makes the first wait time out at its first poll.
func TestAdviceFollow_E8_PauseTimeoutRetryPause(t *testing.T) {
	t.Parallel()
	adviceAssertGoDoc(t, "errors.go", "ErrPauseTimeout", "The caller's recourse is to retry the pause; the agent may still be running.")
	cases := []struct {
		name   string
		exitOn int      // the /exit the agent ends on (0: none typed by pause)
		late   bool     // the agent ends after the first wait timed out
		exits  []string // what the agent got submitted, both calls
		calls  int      // tmux calls the retry makes
	}{
		{name: "the agent ended after the wait timed out", late: true, exits: []string{exitText}},
		{name: "the agent ignored the first /exit", exitOn: 2, exits: []string{exitText, exitText},
			calls: len(pauseSendCalls)},
	}
	pause := func(e *killEnv, r killRow) (api.PauseResult, error) {
		return e.pauseWithin(context.Background(), 0, pauseParams(r))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			in := paneAgentExiting(t, e, r, false, tc.exitOn)

			if _, err := pause(e, r); !errors.Is(err, api.ErrPauseTimeout) {
				t.Fatalf("pause = %v; want ErrPauseTimeout", err)
			}
			if tc.late {
				if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", ""); !a.Applied {
					t.Fatalf("late SessionEnd not applied: %s", a.Reason)
				}
			}

			retry := runVerb(e, func() (api.PauseResult, error) { return pause(e, r) })

			if retry.err != nil {
				t.Fatalf("retried pause: %v; want the row ended (agent got %q)", retry.err, in.submitted)
			}
			if len(retry.calls) != tc.calls || !slices.Equal(in.submitted, tc.exits) {
				t.Errorf("retried pause made tmux calls %+v, agent got %q; want %d calls, %q", retry.calls,
					in.submitted, tc.calls, tc.exits)
			}
			pauAssertState(t, e, r.ID, store.StateEnded)
		})
	}
}

// TestAdviceFollow_E9_SendKeysPendingAllowPending: E9 a pending row's
// "state=pending" refusal, followed by the allow_pending text ("When true,
// also allows a pending row"); re-issued with it, the keys reach the launch's pane.
func TestAdviceFollow_E9_SendKeysPendingAllowPending(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "send-keys", "allow_pending", "When true, also allows a pending row")
	e := newKillEnv(t)
	r := e.seedPending(t, pendingFresh, pendingOurs)

	_, err := e.sendKeys(skaParams(r))
	adviceAssertAdvice(t, err, api.ErrSpawnNotInteractive, "state=pending")
	e.assertNoTmuxCall(t)

	p := skaParams(r)
	p.AllowPending = true
	if _, err := e.sendKeys(p); err != nil {
		t.Fatalf("re-issued with allow_pending: %v; want delivery", err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skaText)
}
