package api_test

// advice_follow_pane_test.go (b.fji E1-E9): each refusal of send-keys,
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

// The relay guard's advice (b.2b8): send-keys' refusal states when the guard
// releases (E6); decide's fallen-back refusal points at send-keys from then,
// at most 2 s after it (E7).
const (
	advSendKeysGuardReleases = "guard releases 1 s after every request's delivery window elapses"
	advDecideFallenBack      = "fell back — too late; answer at the pane with send-keys once its relay guard releases, " +
		"1 s after every request's delivery window elapses (for this request, at most 2 s after this refusal)"
)

// advRelayGuardReleased is when the advice says the guard releases for r's
// one request: 1 s after its window elapses.
func advRelayGuardReleased(t *testing.T, e *killEnv, r killRow) (createdAt, released time.Time) {
	t.Helper()
	pr, err := e.st.GetPermissionRequest(r.ID, storefix.TestRequestTokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	return pr.CreatedAt, pr.CreatedAt.Add(relayGuardWindow + time.Second)
}

// TestAdviceFollow_E6_SendKeysRelayGuardReleases: E6 "guard releases 1 s
// after every request's delivery window elapses"; send-keys is refused
// unchanged until then, and re-issued at that instant or later it delivers.
func TestAdviceFollow_E6_SendKeysRelayGuardReleases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		after time.Duration // past the stated release
	}{
		{name: "retried as the guard releases"},
		{name: "retried an hour after the guard released", after: time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := seedRelayRow(t, e, storefix.TestRequestTokenA)
			createdAt, released := advRelayGuardReleased(t, e, r)
			send := func(now time.Time) error {
				_, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
				return err
			}
			first := send(createdAt.Add(relayGuardWindow / 2))
			adviceAssertAdvice(t, first, api.ErrSendKeysWhileRelayed, advSendKeysGuardReleases)
			for _, at := range []struct {
				name string
				now  time.Time
			}{
				{"inside the window", createdAt.Add(relayGuardWindow - time.Minute)},
				{"as the window elapses", createdAt.Add(relayGuardWindow)},
				{"just before the stated release", released.Add(-time.Nanosecond)},
			} {
				if again := send(at.now); errText(again) != errText(first) {
					t.Errorf("retried %s: %v; want the same refusal %v", at.name, again, first)
				}
			}
			e.assertNoTmuxCall(t)

			if err := send(released.Add(tc.after)); err != nil {
				t.Fatalf("re-issued %v after the stated release (1 s after the window elapsed): %v; want delivery", tc.after, err)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
		})
	}
}

// TestAdviceFollow_E7_DecideFallenBackAnswerAtPane: E7 "fell back — too late;
// answer at the pane with send-keys once its relay guard releases, 1 s after
// every request's delivery window elapses"; send-keys of the answer at that
// instant (at once if it has passed) is delivered. Decide's earliest refusal,
// window - 1 s (SR-4.2, SR-4.4), is TestDecideDeliverabilityBoundary's.
func TestAdviceFollow_E7_DecideFallenBackAnswerAtPane(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "decide", "", "ErrRelayFallenBack (answer at the pane)")
	cases := []struct {
		name string
		age  time.Duration // the request's age when decide is refused
	}{
		{name: "request an hour past its window", age: 2 * relayGuardWindow},
		{name: "request at the end of its window", age: relayGuardWindow},
		{name: "request as decide starts refusing it", age: relayGuardWindow - api.RelayKillSafetyMargin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := seedRelayRow(t, e, storefix.TestRequestTokenA)
			createdAt, released := advRelayGuardReleased(t, e, r)
			refused := createdAt.Add(tc.age)
			_, err := api.Decide(e.st, relayGuardWindow, refused, api.DecideParams{ClaudeInstanceID: r.ID,
				RequestToken: storefix.TestRequestTokenA, Decision: "allow"})
			adviceAssertAdvice(t, err, api.ErrRelayFallenBack, advDecideFallenBack)
			answer := func(now time.Time) error {
				_, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
				return err
			}

			at := released
			if refused.Before(released) {
				// Not the advice: answered early, while the row is still
				// check_permission, the guard holds and states its release (E6).
				adviceAssertAdvice(t, answer(refused), api.ErrSendKeysWhileRelayed, advSendKeysGuardReleases)
				e.assertNoTmuxCall(t)
			} else {
				at = refused
			}
			if err := answer(at); err != nil {
				t.Fatalf("answer at the pane (send-keys) once its relay guard released: %v; want it accepted", err)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
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
