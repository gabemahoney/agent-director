package api_test

// advice_follow_pane_test.go (b.fji E1-E9): each refusal of send-keys,
// read-pane, pause and decide that says what to do next is followed
// literally, on the kill and pane-verb fixtures (fake tmux, injected clock).
// The agent's input box is modelled from the keys calls (advPaneInput), so a
// retry that types or submits twice is seen.

import (
	"context"
	"errors"
	"slices"
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

// advPaneInput is the agent pane's input box as the keys calls left it: a
// text call that reached the pane types its text, an Enter that reached it
// submits the box. A failed call reached the pane only when it timed out and
// timeoutsReach is set (the description's "may have been delivered").
type advPaneInput struct {
	timeoutsReach bool
	box           string
	submitted     []string
	onSubmit      func(line string)
}

// advPaneWatch models the input box of pane on e's Recorder.
func advPaneWatch(e *killEnv, pane string, timeoutsReach bool) *advPaneInput {
	in := &advPaneInput{timeoutsReach: timeoutsReach}
	e.rec.AfterCall(tmux.CallSendText, func(c tmuxfix.SocketCall, err error) {
		if c.Target == pane && in.reached(err) {
			in.box += c.Text
		}
	}).AfterCall(tmux.CallSendEnter, func(c tmuxfix.SocketCall, err error) {
		if c.Target == pane && in.reached(err) {
			line := in.box
			in.submitted, in.box = append(in.submitted, line), ""
			if in.onSubmit != nil {
				in.onSubmit(line)
			}
		}
	})
	return in
}

// reached reports whether a keys call that returned err reached the pane.
func (in *advPaneInput) reached(err error) bool {
	var ce *tmux.CallError
	return err == nil || (in.timeoutsReach && errors.As(err, &ce) && ce.Failure == tmux.FailTimeout)
}

// assertSubmittedOnce fails unless the agent got exactly text, submitted once, and nothing is left typed.
func (in *advPaneInput) assertSubmittedOnce(t *testing.T, text string) {
	t.Helper()
	if !slices.Equal(in.submitted, []string{text}) || in.box != "" {
		t.Errorf("agent got submissions %q with %q left typed; want %q submitted once", in.submitted, in.box, text)
	}
}

// advPaneAgent is advPaneWatch on r's agent pane for an agent that ends its
// row (SessionEnd) on its exitOn-th "/exit" submission (0: never).
func advPaneAgent(t *testing.T, e *killEnv, r killRow, timeoutsReach bool, exitOn int) *advPaneInput {
	t.Helper()
	in := advPaneWatch(e, r.Spawn.Identity.PaneID, timeoutsReach)
	exits := 0
	in.onSubmit = func(line string) {
		if line != exitText {
			return
		}
		if exits++; exits == exitOn {
			if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", r.Spawn.ClaudeSessionID); !a.Applied {
				t.Errorf("SessionEnd after /exit not applied: %s", a.Reason)
			}
		}
	}
	return in
}

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
	in := advPaneWatch(e, r.Spawn.Identity.PaneID, false)
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
	in := advPaneAgent(t, e, r, false, 1)
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
// carries, and why a literal retry is known not to work ("" when it works).
type advPaneKeysFailure struct {
	name    string
	call    tmux.Call
	script  tmuxfix.Script
	reached bool
	advice  string
	broken  string
}

// The keys-failure advice sentences (SR-1.4, SR-7.3).
const (
	advPaneKeysMayHaveRetry  = "the keys may have been delivered; retry later"
	advPaneEnterTimeoutRetry = "the keys may have been delivered; the text may be typed but not submitted; retry later"
	advPaneNotSubmittedRetry = "the text may be typed but not submitted; retry later"
)

// advPaneEnterFailures are a failed Enter after the text went through.
func advPaneEnterFailures(broken string) []advPaneKeysFailure {
	once := func(f tmux.Failure) tmuxfix.Script { return tmuxfix.Script{Failure: f, Times: 1} }
	return []advPaneKeysFailure{
		{name: "Enter timed out before reaching the pane", call: tmux.CallSendEnter, script: once(tmux.FailTimeout),
			advice: advPaneEnterTimeoutRetry, broken: broken},
		{name: "Enter failed, the follow-up lookup found the session", call: tmux.CallSendEnter,
			script: once(tmux.FailNoServer), advice: advPaneNotSubmittedRetry, broken: broken},
	}
}

// TestAdviceFollow_E2_SendKeysTimeoutRetryLater: E2 "the keys may have been
// delivered; retry later" after the text call timed out; the same send-keys
// re-issued must leave the agent with the text submitted once.
func TestAdviceFollow_E2_SendKeysTimeoutRetryLater(t *testing.T) {
	timeout := tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}
	advPaneKeysRetry(t, "E2", []advPaneKeysFailure{
		{name: "the timed-out text call did not reach the pane", call: tmux.CallSendText, script: timeout,
			advice: advPaneKeysMayHaveRetry},
		{name: "the timed-out text call typed the text", call: tmux.CallSendText, script: timeout, reached: true,
			advice: advPaneKeysMayHaveRetry,
			broken: "the retry types the whole text again after the unsubmitted copy and submits both as one message"},
	})
}

// TestAdviceFollow_E3_SendKeysEnterFailedRetryLater: E3 "the text may be
// typed but not submitted; retry later" after the Enter call failed; the
// same send-keys re-issued must leave the agent with the text submitted once.
func TestAdviceFollow_E3_SendKeysEnterFailedRetryLater(t *testing.T) {
	failures := append(advPaneEnterFailures("the retry types the whole text again after the typed, unsubmitted "+
		"copy and submits both as one message; the description offers no Enter-only retry"),
		advPaneKeysFailure{name: "Enter timed out after submitting the text", call: tmux.CallSendEnter,
			script: tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, reached: true, advice: advPaneEnterTimeoutRetry,
			broken: "the text was submitted, and the retry submits it a second time"})
	advPaneKeysRetry(t, "E3", failures)
}

// advPaneKeysRetry runs each send-keys keys failure: refused with its advice,
// then re-issued, after which the agent must have the text submitted once.
func advPaneKeysRetry(t *testing.T, id string, failures []advPaneKeysFailure) {
	t.Helper()
	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			in := advPaneWatch(e, r.Spawn.Identity.PaneID, f.reached)
			e.rec.Script(r.Socket, f.script, f.call)

			_, err := e.sendKeys(skaParams(r))
			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, f.advice)
			if f.broken != "" {
				knownBrokenAdvice(t, id, f.broken)
			}

			if _, err := e.sendKeys(skaParams(r)); err != nil {
				t.Fatalf("re-issued send-keys: %v; want delivery", err)
			}
			in.assertSubmittedOnce(t, skaText)
		})
	}
}

// TestAdviceFollow_E5_PauseKeysFailedRetryLater: E5 "the keys may have been
// delivered; retry later" (and pause's Enter failures, "the text may be typed
// but not submitted; retry later"); pause re-issued must end the row: a no-op
// on a finished row, else /exit submitted once.
func TestAdviceFollow_E5_PauseKeysFailedRetryLater(t *testing.T) {
	timeout := tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}
	retypes := "the retry types /exit again after the typed, unsubmitted /exit and submits /exit/exit, " +
		"which is not /exit; the agent keeps running and the retried pause times out"
	failures := append([]advPaneKeysFailure{
		{name: "the /exit text call timed out before reaching the pane", call: tmux.CallSendText, script: timeout,
			advice: advPaneKeysMayHaveRetry},
		{name: "the /exit text call timed out after typing /exit", call: tmux.CallSendText, script: timeout,
			reached: true, advice: advPaneKeysMayHaveRetry, broken: retypes},
		{name: "Enter timed out after submitting /exit", call: tmux.CallSendEnter, script: timeout, reached: true,
			advice: advPaneEnterTimeoutRetry},
	}, advPaneEnterFailures(retypes)...)
	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			fastPausePolls(t)
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			in := advPaneAgent(t, e, r, f.reached, 1)
			e.rec.Script(r.Socket, f.script, f.call)

			_, err := e.pause(pauseParams(r))
			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, f.advice)
			if f.broken != "" {
				knownBrokenAdvice(t, "E5", f.broken)
			}

			if _, err := e.pause(pauseParams(r)); err != nil {
				t.Fatalf("re-issued pause: %v; want the row ended (agent got %q, %q left typed)", err, in.submitted, in.box)
			}
			in.assertSubmittedOnce(t, exitText)
			pauAssertState(t, e, r.ID, store.StateEnded)
		})
	}
}

// TestAdviceFollow_E6_SendKeysRelayGuardReleases: E6 "guard releases once
// every request's delivery window elapses"; send-keys is refused unchanged
// inside the window, and re-issued once it has elapsed it must deliver.
func TestAdviceFollow_E6_SendKeysRelayGuardReleases(t *testing.T) {
	const advice = "guard releases once every request's delivery window elapses"
	cases := []struct {
		name   string
		after  time.Duration // past the request's created_at + window
		broken string
	}{
		{name: "retried as the window elapses",
			broken: "the guard holds until api.RelayKillSafetyMargin (1 s) past the window, so this retry is refused again"},
		{name: "retried an hour after the window elapsed", after: time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := seedRelayRow(t, e, storefix.TestRequestTokenA)
			pr, err := e.st.GetPermissionRequest(r.ID, storefix.TestRequestTokenA)
			if err != nil {
				t.Fatalf("GetPermissionRequest: %v", err)
			}
			send := func(now time.Time) error {
				_, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
				return err
			}
			first := send(pr.CreatedAt.Add(relayGuardWindow / 2))
			adviceAssertAdvice(t, first, api.ErrSendKeysWhileRelayed, advice)
			if again := send(pr.CreatedAt.Add(relayGuardWindow - time.Minute)); errText(again) != errText(first) {
				t.Errorf("retried inside the window: %v; want the same refusal %v", again, first)
			}
			e.assertNoTmuxCall(t)
			if tc.broken != "" {
				knownBrokenAdvice(t, "E6", tc.broken)
			}

			if err := send(pr.CreatedAt.Add(relayGuardWindow + tc.after)); err != nil {
				t.Fatalf("re-issued once the window elapsed: %v; want delivery", err)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
		})
	}
}

// TestAdviceFollow_E7_DecideFallenBackAnswerAtPane: E7 "fell back — too late,
// answer at the pane"; send-keys of the answer at that moment must be
// accepted. Its delivery into the agent's pane is
// TestRelayFallenBackIncidentRegression's (relay_fallenback_test.go).
func TestAdviceFollow_E7_DecideFallenBackAnswerAtPane(t *testing.T) {
	const advice = "fell back — too late, answer at the pane"
	cases := []struct {
		name   string
		age    time.Duration // the request's age when decide is refused
		broken string
	}{
		{name: "request an hour past its window", age: 2 * relayGuardWindow},
		{name: "request at the end of its window", age: relayGuardWindow,
			broken: "decide refuses from window - 1 s but the send-keys guard holds until window + 1 s, so the " +
				"answer at the pane is refused with ErrSendKeysWhileRelayed for up to 2 s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := seedRelayRow(t, e, storefix.TestRequestTokenA)
			pr, err := e.st.GetPermissionRequest(r.ID, storefix.TestRequestTokenA)
			if err != nil {
				t.Fatalf("GetPermissionRequest: %v", err)
			}
			now := pr.CreatedAt.Add(tc.age)
			_, err = api.Decide(e.st, relayGuardWindow, now, api.DecideParams{ClaudeInstanceID: r.ID,
				RequestToken: storefix.TestRequestTokenA, Decision: "allow"})
			adviceAssertAdvice(t, err, api.ErrRelayFallenBack, advice)
			if tc.broken != "" {
				knownBrokenAdvice(t, "E7", tc.broken)
			}

			if _, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"}); err != nil {
				t.Fatalf("answer at the pane (send-keys): %v; want it accepted", err)
			}
		})
	}
}

// TestAdviceFollow_E8_PauseTimeoutRetryPause: E8 Go doc "The caller's
// recourse is to retry the pause; the agent may still be running."; the
// retried pause must leave the row ended. The wait's deadline is real time,
// so a 0 s timeout makes the first wait time out at its first poll.
func TestAdviceFollow_E8_PauseTimeoutRetryPause(t *testing.T) {
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
			calls: len(paneSendCalls)},
	}
	pause := func(e *killEnv, r killRow) (api.PauseResult, error) {
		return e.pauseWithin(context.Background(), 0, pauseParams(r))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			in := advPaneAgent(t, e, r, false, tc.exitOn)

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
