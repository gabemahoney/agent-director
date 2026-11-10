package api_test

// advice_follow_pane_test.go (b.fji E1-E9, F6; b.kdf F7): each refusal of send-keys,
// read-pane, pause and decide that says what to do next is followed
// literally, on the kill and pane-verb fixtures (fake tmux, injected clock).
// The agent's input box is modelled from the keys calls (paneInput,
// pane_input_fixture_test.go), so a retry that types or submits twice is seen.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
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
			got, err = e.readPaneClient(t, api.ReadPaneParams{InstanceID: r.ID})
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
// carries, what the agent's input box holds before the first call and
// whether send-keys types advPacedText in paced pieces (b.vr5).
type advPaneKeysFailure struct {
	name    string
	call    tmux.Call
	script  tmuxfix.Script
	reached bool
	advice  string
	typed   string
	paced   bool
}

// advPacedText is send-keys text that macOS paced Claude typing types in
// pieces of 512, 512 and 1 bytes (b.vr5).
var advPacedText = strings.Repeat("p", 1025)

// text is what f's send-keys types: advPacedText when paced, else skaText.
func (f advPaneKeysFailure) text() string {
	if f.paced {
		return advPacedText
	}
	return skaText
}

// advPaced is fs with send-keys typing advPacedText in paced pieces, whose
// Enter is the named Enter key (b.vr5).
func advPaced(fs []advPaneKeysFailure) []advPaneKeysFailure {
	var out []advPaneKeysFailure
	for _, f := range fs {
		f.name, f.paced = "paced: "+f.name, true
		out = append(out, f)
	}
	return out
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
		{name: "Enter timed out before reaching the pane", call: callEnterKey, script: once(tmux.FailTimeout),
			advice: timedOut},
		{name: "Enter failed, the follow-up lookup found the session", call: callEnterKey,
			script: once(tmux.FailNoServer), advice: failed},
	}
}

// advPaneEnterSubmitted is a timed-out Enter that submitted what was typed,
// its description carrying advice.
func advPaneEnterSubmitted(what, advice string) advPaneKeysFailure {
	return advPaneKeysFailure{name: "Enter timed out after submitting " + what, call: callEnterKey,
		script: tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, reached: true, advice: advice}
}

// advPaneKeysRefused seeds a live row watching its agent's input box, fails
// f's call once and sends f's text, which must be refused with f's advice.
// A paced f turns the macOS selection on until the test ends.
func advPaneKeysRefused(t *testing.T, f advPaneKeysFailure) (*killEnv, killRow, *paneInput) {
	t.Helper()
	if f.paced {
		t.Cleanup(api.SetClaudePacedTypingForTest(true))
	}
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	in := watchPaneInput(e, r.Spawn.Identity.PaneID, f.reached)
	e.rec.Script(r.Socket, f.script, f.call)
	_, err := e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: f.text()})
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

			read, err := e.readPaneClient(t, api.ReadPaneParams{InstanceID: r.ID})
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
// timeout, "the keys may have been delivered; " first) after the Enter call,
// or paced typing's named Enter (b.vr5), failed; send-keys with empty text
// must leave the text submitted once, also when the timed-out Enter had
// submitted it (Enter on an empty input submits nothing).
func TestAdviceFollow_E3_SendKeysEnterFailedEmptyTextSubmits(t *testing.T) {
	// Serial: its paced cells set the process-wide paced-typing selection with api.SetClaudePacedTypingForTest; its other cells run in parallel.
	adviceAssertManifest(t, "send-keys", "", advSendKeysEmptyTextManifest)
	failures := append(advPaneEnterFailures(advSendKeysEnterTimeout, advSendKeysNotSubmitted),
		advPaneEnterSubmitted("the text", advSendKeysEnterTimeout))
	for _, f := range append(failures, advPaced(failures)...) {
		t.Run(f.name, func(t *testing.T) {
			if !f.paced {
				t.Parallel()
			}
			e, r, in := advPaneKeysRefused(t, f)

			if _, err := e.sendKeys(api.SendKeysParams{InstanceID: r.ID}); err != nil {
				t.Fatalf("send-keys with empty text: %v; want Enter delivered", err)
			}
			in.assertSubmittedOnce(t, f.text())
		})
	}
}

// advSendKeysPacedReadPane is paced typing's advice once part of the text, or
// all of it unsubmitted, may be in the input (b.vr5).
const advSendKeysPacedReadPane = "read-pane before sending again, and do not resend the text blindly"

// TestAdviceFollow_E20_SendKeysPacedPieceFailedReadPane: E20 (b.vr5)
// "read-pane before sending again, and do not resend the text blindly" after
// a later paced piece failed, the first timed out, or the relay guard held
// input or could not read the row's permission requests before the next piece
// or the Enter: partial_input_unconfirmed
// counting the pieces attempted, with no further piece, Enter or follow-up
// lookup and never "the same send-keys" or "send-keys with empty text";
// read-pane then shows what was typed, unsubmitted.
func TestAdviceFollow_E20_SendKeysPacedPieceFailedReadPane(t *testing.T) {
	// Serial: it sets the process-wide paced-typing selection with api.SetClaudePacedTypingForTest.
	t.Cleanup(api.SetClaudePacedTypingForTest(true))
	for _, tc := range []struct {
		name     string
		failure  tmux.Failure
		piece    int  // the piece that fails, of advPacedText's 512, 512 and 1 bytes; 0: none
		held     int  // with none failing, the pieces typed before the relay guard holds input: 1, the second piece; 3, the Enter
		readFail bool // with held, the guard's permission-request read fails instead of finding a held request
	}{
		{"first piece timed out", tmux.FailTimeout, 1, 0, false},
		{"second piece timed out", tmux.FailTimeout, 2, 0, false},
		{"second piece failed", tmux.FailNoServer, 2, 0, false},
		{"relay guard held the second piece", 0, 0, 1, false},
		{"relay guard held the Enter", 0, 0, 3, false},
		{"permission read failed before the second piece", 0, 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, other := e.seedKeysDisagreeCase(t, keysDisagreeCase{spec: killRowSpec{RelayOn: tc.held > 0},
				setup: []func(*testing.T, *killEnv, *killRow){ktrRestart}})
			in := watchPaneInput(e, r.Spawn.Identity.PaneID, true)
			attempted, typed, pieces := len(advPacedText), len(advPacedText), 3
			switch {
			case tc.held > 0:
				// A held permission request arrives, or the permission-request
				// read starts failing, in the pause after the held-th piece,
				// before the reread for the next piece (100 ms) or for the Enter
				// (the 500 ms after the last piece's 100 ms).
				attempted, pieces = min(512*tc.held, len(advPacedText)), tc.held
				typed = attempted
				pause := 100 * time.Millisecond
				if tc.held == 3 {
					pause = 500 * time.Millisecond
				}
				texts, sleep := 0, e.sleep
				e.rec.AfterCall(tmux.CallSendText, func(tmuxfix.SocketCall, error) { texts++ })
				e.sleep = func(d time.Duration) {
					if texts == tc.held && d == pause && tc.readFail {
						e.store.failPermissionRequests(nil)
					} else if texts == tc.held && d == pause {
						held := apitest.PermissionTimes(e.clock.Now(), time.Time{}, time.Time{}, time.Time{}, time.Time{}, "")
						if _, err := apitest.SeedPermissionRequest(e.dbPath, r.ID, "Bash", held); err != nil {
							t.Error(err)
						}
					}
					sleep(d)
				}
			case tc.piece == 2:
				e.rec.Script(r.Socket, tmuxfix.Script{Times: 1}, tmux.CallSendText) // the first piece goes through
			}
			if tc.piece > 0 {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tc.failure, Times: 1}, tmux.CallSendText)
				attempted, typed, pieces = 512*tc.piece, 512*tc.piece, tc.piece // a timed-out piece reached the pane
				if tc.failure != tmux.FailTimeout {
					typed -= 512
				}
			}
			before := e.columns(t, r.ID)

			run := runVerb(e, func() (api.SendKeysResult, error) {
				return e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: advPacedText})
			})

			d := assertDeliveryError(t, run.err, "partial_input_unconfirmed", attempted, false)
			if !d.KeysSent || d.Phase != "text_attempted" || d.RowState != store.StateWaiting {
				t.Errorf("details = %+v; want keys_sent, phase text_attempted, row_state waiting", d)
			}
			adviceAssertAdvice(t, run.err, api.ErrSubmissionUnconfirmed, advSendKeysPacedReadPane)
			for _, resend := range []string{"the same send-keys", "send-keys with empty text"} {
				if strings.Contains(errText(run.err), resend) {
					t.Errorf("description %q advises %q", errText(run.err), resend)
				}
			}
			// The target's lookup and listing, input admission's before the
			// first piece and the identity recheck's before each later one; a
			// relay-guard stop comes before that recheck.
			want := []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallLookup, tmux.CallListPanes}
			for i := range pieces {
				if i > 0 {
					want = append(want, tmux.CallLookup, tmux.CallListPanes)
				}
				want = append(want, tmux.CallSendText)
			}
			if got := paKinds(run.calls); !slices.Equal(got, want) {
				t.Errorf("tmux calls = %v; want %v: nothing after the failed piece, no Enter", got, want)
			}
			e.assertRowUnchanged(t, r.ID, before)
			assertKeysDisagrees(t, e, sktDisagrees(t, r.ID), r, "send-keys", other, []disagreeWant{
				{reason: "server_restarted", server: "restarted", verdict: "ours", action: "text_sent", ours: true}})

			read, err := e.readPaneClient(t, api.ReadPaneParams{InstanceID: r.ID})
			if err != nil || read.Pane != advPacedText[:typed] || len(in.submitted) != 0 {
				t.Errorf("read-pane = %d bytes, %v, with %d submissions; want the %d bytes typed, unsubmitted",
					len(read.Pane), err, len(in.submitted), typed)
			}
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

// The relay guard's advice (b.2b8, b.ah6, b.ceq; b.146 rule 7): send-keys'
// refusal says only to answer the open request it names with decide, or to
// retry send-keys later when the request it names is decided (E6);
// ErrRelayFallenBack, from decide and from a plain send-keys, says to answer
// the request at the pane with send-keys and its request_token, or to close it
// with record-pane-answer (E7, b.pzy, b.146 decision 10 A); decide's refusal
// when the Spawn is not shown sitting on that request alone says not to answer
// it at the pane (F6, b.t6e). A request the relay hook denied at its deadline
// is ErrAlreadyDecided naming that deny.
const (
	advRelayFallenBack = "answer it at the pane with send-keys and its request_token, " +
		"or, if it was answered outside agent-director, close it with record-pane-answer"
	advDecideNotShown     = "so its permission dialog cannot be shown to be on screen; do not answer it at the pane"
	advDecideHookTimedOut = `already decided as "deny" (decision_reason "timeout")`
)

// advPaneAnswer follows E7's first branch literally on r's request token at
// now (relayGuardWindow): read-pane of the agent's pane, then send-keys with
// the token, as allow, the key "1" and read-pane's pane_sha256. It returns
// that send-keys' run.
func advPaneAnswer(t *testing.T, e *killEnv, r killRow, token string, now time.Time) verbRun[struct{}] {
	t.Helper()
	e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "the agent's pane\n")
	res, err := e.readPaneClient(t, api.ReadPaneParams{InstanceID: r.ID})
	if err != nil {
		t.Fatalf("read-pane: %v", err)
	}
	return runVerb(e, func() (struct{}, error) {
		_, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{InstanceID: r.ID, RequestToken: token,
			As: "allow", Key: "1", ExpectPaneSHA256: res.PaneSHA256})
		return struct{}{}, err
	})
}

// advAssertPaneAnswered fails unless run sent the one key "1" to r's agent
// pane, no Enter, and request token reads pane_answer sent, decision allow.
func advAssertPaneAnswered(t *testing.T, e *killEnv, r killRow, token string, run verbRun[struct{}]) {
	t.Helper()
	if run.err != nil {
		t.Fatalf("pane answer (send-keys with request_token %s): %v; want it accepted", token, run.err)
	}
	assertOneKey(t, run.calls, r.Spawn.Identity.PaneID, "1", paneAnswerBeforeKey...)
	if pr, err := e.st.GetPermissionRequest(r.ID, token); err != nil || pr.PaneAnswer != store.PaneAnswerSent || pr.Decision != "allow" {
		t.Errorf("request %s = %+v, %v; want pane_answer sent, decision allow", token, pr, err)
	}
}

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
// (b.z6g), or is ErrRelayFallenBack, whose advice (E7) followed at once answers the request at the pane with one key
// (b.ah6; b.146 rule 8).
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
				_, err := e.sendKeysAt(relayGuardWindow, *now, api.SendKeysParams{InstanceID: r.ID, Text: "1"})
				return err
			}
			advRelayAdvice(t, send(), api.ErrSendKeysWhileRelayed, advSendKeysAnswerWithDecide(storefix.TestRequestTokenA))
			e.assertNoTmuxCall(t)

			// The advice, literally: decide the request it names; decide's wait moves the clock.
			_, err := api.DecideWithSleep(e.st, api.RelayView{Window: relayGuardWindow, Now: func() time.Time { return *now }}, func(d time.Duration) { advance(now.Add(d)) },
				api.DecideParams{InstanceID: r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})

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
				advRelayAdvice(t, err, api.ErrRelayFallenBack, advRelayFallenBack)
				advAssertPaneAnswered(t, e, r, storefix.TestRequestTokenA, advPaneAnswer(t, e, r, storefix.TestRequestTokenA, *now))
			}
		})
	}
}

// TestAdviceFollow_E6_SendKeysWhileRelayedDecidedRequestRetryLater: E6 "the relayed permission verdict on request
// <token> is recorded and its relay hook may still be delivering it; retry send-keys later" naming the one request,
// recorded before schema v7 and allowed in its window (b.ceq). send-keys retried is refused alike until that relay
// hook is presumed settled (b.z6g), whether the agent moved on (the hook delivered) or not (it died), since rule 7
// reads the requests in every live state (b.146). Then the recorded verdict is no proof its dialog is gone (b.146
// step 2c): retried, it is ErrDialogMaybeOpen until the agent's next Stop proves the request gone; then it delivers.
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
				api.DecideParams{InstanceID: r.ID, RequestToken: tok, Decision: "allow"}); err != nil {
				t.Fatalf("decide in the window: %v; want the allow recorded", err)
			}
			send := func() error {
				_, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{InstanceID: r.ID, Text: "1"})
				return err
			}

			first := send()
			advRelayAdvice(t, first, api.ErrSendKeysWhileRelayed, advSendKeysRetryLater(tok))
			e.assertNoTmuxCall(t)

			if tc.delivered {
				// The agent runs the tool; no other request is open, so its move to
				// working takes the row out of check_permission.
				if err := seedAgentState(e.st, e.dbPath, r.ID, store.StateWorking); err != nil {
					t.Fatalf("agent's move to working: %v", err)
				}
			}
			// The advice, literally: send-keys again later. Up to its relay hook's
			// settling the same refusal comes back, whatever the row's state.
			now = createdAt.Add(relayGuardSettled - time.Nanosecond)
			if again := send(); errText(again) != errText(first) {
				t.Errorf("send-keys retried before the relay hook settles: %v; want the same refusal %v", again, first)
			}
			e.assertNoTmuxCall(t)

			now = now.Add(time.Nanosecond) // the request's relay hook is presumed settled
			row, err := e.st.GetSpawn(r.ID)
			if err != nil {
				t.Fatalf("GetSpawn: %v", err)
			}
			assertDialogMaybeOpen(t, send(), tok, row.State)
			e.assertNoTmuxCall(t)
			proveGone(t, e, r.ID, turnEnd()) // the agent's next Stop
			if err := send(); err != nil {
				t.Fatalf("send-keys retried later: %v; want delivery", err)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
		})
	}
}

// TestAdviceFollow_E7_RelayFallenBackAnswerAtPaneOrRecord: E7 "answer it at the pane with send-keys and its
// request_token, or, if it was answered outside agent-director, close it with record-pane-answer" (b.146 rules 8, 13;
// decision 10 A), from decide and from a plain send-keys on a request whose relay hook is gone, followed both ways: a
// pane answer sends one key and closes the request; a record after a person answered at tmux closes it too. Either
// way plain send-keys is no longer ErrRelayFallenBack but ErrDialogMaybeOpen until the main agent's Stop proves the
// request gone (b.146 step 2c), then types again. ErrRelayFallenBack's Go doc says it arrives within seconds, from
// either verb, and no description says the dialog is on screen.
func TestAdviceFollow_E7_RelayFallenBackAnswerAtPaneOrRecord(t *testing.T) {
	t.Parallel()
	adviceAssertGoDoc(t, "decide.go", "ErrRelayFallenBack", "so the refusal arrives within seconds of the hook's end")
	adviceAssertGoDoc(t, "decide.go", "ErrRelayFallenBack", "SendKeys without a request token (b.146 rule 7)")
	adviceAssertGoDoc(t, "decide.go", "ErrRelayFallenBack", "The request is closed by a pane answer through SendKeys with "+
		"its request token, or, when it was answered outside agent-director, by RecordPaneAnswer")
	tok := storefix.TestRequestTokenA
	for _, branch := range []string{"pane answer", "record-pane-answer"} {
		for _, from := range []string{"decide", "send-keys"} {
			t.Run(branch+" after "+from, func(t *testing.T) {
				t.Parallel()
				p := newPAEnv(t)
				var err error
				if from == "decide" {
					_, err = api.Decide(p.st, p.view(), api.DecideParams{InstanceID: p.r.ID, RequestToken: tok, Decision: "deny"})
				} else {
					err = p.sendKeys(p.plain("hi"))
				}
				advRelayAdvice(t, err, api.ErrRelayFallenBack, advRelayFallenBack)
				if strings.Contains(err.Error(), "on screen") {
					t.Errorf("description %q says the dialog is on screen", err)
				}
				p.assertNoTmuxCall(t)

				if branch == "pane answer" {
					run := p.sendKeysRun(p.answer(tok, "deny", "Escape"))
					if run.err != nil {
						t.Fatalf("pane answer: %v", run.err)
					}
					assertOneKey(t, run.calls, p.r.Spawn.Identity.PaneID, "Escape", paneAnswerBeforeKey...)
				} else {
					p.setPane("answered by a person at tmux\n")
					p.now = p.now.Add(2 * time.Second) // the refusal recorded hook_gone_at
					if _, err := p.recordPaneAnswer(p.outside(tok, "unknown")); err != nil {
						t.Fatalf("record-pane-answer: %v", err)
					}
				}
				if pr := p.request(t, tok); pr.AwaitsAnswer() {
					t.Errorf("request A = %+v; want it closed", pr)
				}
				assertDialogMaybeOpen(t, p.sendKeys(p.plain("next")), tok, store.StateCheckPermission)
				p.prove(t, turnEnd())
				if err := p.sendKeys(p.plain("next")); err != nil {
					t.Errorf("plain send-keys once the main agent's Stop proved it gone: %v; want it typed", err)
				}
			})
		}
	}
}

// TestAdviceFollow_E7_DecideFallenBackOtherRequestHoldsGuard: E7's pane answer while another request B's relay hook
// may still answer is ErrSendKeysWhileRelayed naming B (b.146 rule 7, row 3), whose advice (E6, "answer it with
// decide") followed lets B's hook ack its verdict and exit; the pane answer on A then lands.
func TestAdviceFollow_E7_DecideFallenBackOtherRequestHoldsGuard(t *testing.T) {
	t.Parallel()
	tokA, tokB := storefix.TestRequestTokenA, storefix.TestRequestTokenB
	p := newPAEnv(t)
	hookB := api.ProcessIdentity{PID: 54322, Starttime: relayEnvHook.Starttime, PIDNamespace: relayNS}
	p.pc.Set(hookB.PID, procfix.Alive(hookB.Starttime))
	p.seedRequest(t, tokB, "toolu_01B", hookB) // a subagent's request, its hook polling
	_, err := api.Decide(p.st, p.view(), api.DecideParams{InstanceID: p.r.ID, RequestToken: tokA, Decision: "allow"})
	advRelayAdvice(t, err, api.ErrRelayFallenBack, advRelayFallenBack)

	refused := p.sendKeysRun(p.answer(tokA, "allow", "1"))
	advRelayAdvice(t, refused.err, api.ErrSendKeysWhileRelayed, advSendKeysAnswerWithDecide(tokB))
	if len(refused.calls) != 0 {
		t.Fatalf("calls = %v; want none", paKinds(refused.calls))
	}
	// Establish B's verdict with a finite setup budget; advance the fixture's ack clock.
	if _, err := api.DecideWithSleep(p.st, p.view(), func(d time.Duration) { p.now = p.now.Add(d) }, api.DecideParams{InstanceID: p.r.ID, RequestToken: tokB, Decision: "allow",
		MaxWaitMs: ptr(int64(10000))}); err != nil {
		t.Fatalf("decide %s, which the refusal names: %v; want the verdict recorded", tokB, err)
	}
	if _, _, ok, err := p.st.AckRelayDecision(p.r.ID, tokB, p.now, store.DefaultLockWait, nil); err != nil || !ok {
		t.Fatalf("B's hook acks = %v, %v", ok, err)
	}
	p.pc.Set(hookB.PID, procfix.Gone())

	run := p.sendKeysRun(p.answer(tokA, "allow", "1"))
	if run.err != nil {
		t.Fatalf("pane answer on A once B's hook delivered: %v; want it accepted", run.err)
	}
	assertOneKey(t, run.calls, p.r.Spawn.Identity.PaneID, "1", paneAnswerBeforeKey...)
}

// TestAdviceFollow_E7_DecideFallenBackAnswerAtPane: E7 "answer it at the pane
// with send-keys and its request_token", for a request recorded before schema
// v7 (judged by time). A caller that branches on decide's error name and
// answers at the pane at once (read-pane, then one key with the request's
// token), at the instant decide returned, is not refused by the relay guard
// (b.ah6) and never types into Claude's prompt after a live relay hook denied
// the request at its poll deadline (b.pzy). Decide's earliest refusal, window
// - 1 s (SR-4.2, SR-4.4), is TestDecideDeliverabilityBoundary's.
func TestAdviceFollow_E7_DecideFallenBackAnswerAtPane(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "decide", "", "first wins (else ErrAlreadyDecided)",
		"Hook gone: immediate ErrRelayFallenBack.")
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
					api.DecideParams{InstanceID: r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})
				answered := false
				var run verbRun[struct{}]
				if errors.Is(err, api.ErrRelayFallenBack) {
					// At once: the clock stays at the instant decide returned.
					run = advPaneAnswer(t, e, r, storefix.TestRequestTokenA, *now)
					answered = true
				}

				if !hookAlive {
					advRelayAdvice(t, err, api.ErrRelayFallenBack, advRelayFallenBack)
					advAssertPaneAnswered(t, e, r, storefix.TestRequestTokenA, run)
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
				api.DecideParams{InstanceID: r.ID, RequestToken: tokB, Decision: "allow"}); err != nil {
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
				api.DecideParams{InstanceID: r.ID, RequestToken: tokA, Decision: "allow"})
			if errors.Is(err, api.ErrRelayFallenBack) { // E7's advice, as a caller branching on the name follows it
				_, _ = e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{InstanceID: r.ID, Text: "1"})
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
// A finished Spawn refuses decide without changing its verdict, including legacy rows closed before delivery (b.w21).
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
	const closedBeforeDelivery = "its spawn ended, or a shared check marked it missing, before the request's relay hook delivered a verdict"
	cases := []struct {
		name         string
		steps        []decideStep // in order, before decide
		race         bool         // the Spawn ends after decide's read instead
		want         error
		phrase       string // carried by the refusal with F7 ("": neither checked)
		decision     string // request A's decision afterwards ("": open)
		noRequestRow bool
	}{
		{name: "ended, request denied by the SessionEnd", steps: []decideStep{openA, end}, want: store.ErrNoOpenPermissionRequest,
			phrase: closedBeforeDelivery, decision: "deny"},
		{name: "ended, decided before the SessionEnd, not acked", steps: []decideStep{relayAllowA, end}, want: store.ErrNoOpenPermissionRequest,
			phrase: closedBeforeDelivery, decision: "allow"},
		{name: "ended, request decided before schema v7", steps: []decideStep{openA, allowA, end}, want: store.ErrNoOpenPermissionRequest,
			phrase: closedBeforeDelivery, decision: "allow"},
		{name: "ended, request recorded after the SessionEnd", steps: []decideStep{end, openA}, want: store.ErrNoOpenPermissionRequest,
			phrase: "the spawn is ended"},
		{name: "ended, no such request", steps: []decideStep{end}, want: store.ErrNoOpenPermissionRequest, noRequestRow: true},
		{name: "missing, request denied by the mark", steps: []decideStep{openA, markMissing}, want: store.ErrAlreadyDecided, decision: "deny"},
		{name: "missing, decided before the mark, not acked", steps: []decideStep{relayAllowA, markMissing}, want: store.ErrNoOpenPermissionRequest,
			phrase: closedBeforeDelivery, decision: "allow"},
		{name: "missing, request recorded after the mark", steps: []decideStep{markMissing, openA}, want: store.ErrNoOpenPermissionRequest,
			phrase: "the spawn is missing"},
		{name: "ended after decide read the spawn", steps: []decideStep{openA}, race: true, want: store.ErrNoOpenPermissionRequest,
			phrase: closedBeforeDelivery, decision: "deny"},
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

			_, err := api.Decide(ds, api.RelayView{Window: 24 * time.Hour}, api.DecideParams{InstanceID: "id-d-1",
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

// E8 follows the inspection advice before a caller chooses guarded input.
func TestAdviceFollow_E8_PauseTimeoutInspectBeforeInput(t *testing.T) {
	t.Parallel()
	adviceAssertGoDoc(t, "errors.go", "ErrPauseTimeout", "require session inspection and a caller's decision before more input")
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	in := watchPaneInput(e, r.Spawn.Identity.PaneID, false)
	const prompt = "Background work is running\n1. Exit and stop tasks / 2. Stay\n"

	_, err := e.pauseWithin(context.Background(), 0, pauseParams(r))

	assertPauseInspectionDetails(t, err, store.StateWaiting)
	in.assertSubmittedOnce(t, exitText)
	e.assertExitDelivered(t, r.Socket, r.Spawn.Identity.PaneID)
	e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, prompt)
	row, err := e.st.GetSpawn(r.ID)
	if err != nil || row.State != store.StateWaiting {
		t.Fatalf("get after timeout = %+v, %v; want waiting", row, err)
	}
	pane, err := e.readPane(api.ReadPaneParams{InstanceID: r.ID})
	if err != nil || pane.Pane != prompt {
		t.Fatalf("read-pane = %+v, %v; want the prompt for caller inspection", pane, err)
	}
	// The test caller chooses the answer; pause never interprets the capture.
	in.onSubmit = func(line string) {
		if line == "1" {
			if ok, why := e.endAsAgent(t, r); !ok {
				t.Errorf("SessionEnd: %s", why)
			}
		}
	}
	if _, err := e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "1", ExpectPaneSHA256: pane.PaneSHA256}); err != nil {
		t.Fatalf("guarded caller answer: %v", err)
	}
	if !slices.Equal(in.submitted, []string{exitText, "1"}) {
		t.Errorf("submitted = %q; want /exit, then caller's answer once", in.submitted)
	}
	pauAssertState(t, e, r.ID, store.StateEnded)
}

// TestAdviceFollow_E9_SendKeysPendingAllowPending: E9 a pending row's
// "state=pending" refusal, followed by the allow_pending text ("When true,
// also allows a pending row"); re-issued with it, the call passes the pending-row
// eligibility guard, then refuses ordinary text because SessionStart is still
// missing, without typing (b.qcf).
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
	_, err = e.sendKeys(p)
	e.assertSessionStartMissing(t, err)
}

// E15 (b.qcr): "put the prompt in a file and send a short instruction to read it".
// Safe Unicode/newline boundaries retain literal delivery.
func TestAdviceFollow_E15_CodexFallbackLiteralSafety(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, text, reason string }{
		{"bare dollar", "read $", "skill picker"},
		{"named dollar", "read $skill", "skill picker"},
		{"trailing space", "read $skill   ", "skill picker"},
		{"unicode spaces", "read\u00a0$skill\u2003", "skill picker"},
		{"shell spelling", "$HOME", "skill picker"},
		{"embedded terminator", "hello\x1b[201~tail", "control character"},
		{"NUL", "hello\x00tail", "control character"},
		// b.deq: allowing literal TAB must retain final-dollar-token refusal.
		{"final dollar then tab", "read $\t", "skill picker"},
		{"C1 control", "hello\u0085tail", "control character"},
		{"invalid UTF8", "hello\xff", "valid UTF-8"},
		{"earlier dollar", "read $skill then continue", ""},
		{"embedded dollar", "cost$", ""},
		{"newline ends completion", "read $\n", ""},
		{"newline then spaces", "read $skill\n  ", ""},
		{"unicode line separator", "read $skill\u2028 ", ""},
		{"unicode paragraph separator", "read $skill\u2029 ", ""},
		{"CR LF literals", "-界é\r\n#{pane_id};", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
			before := e.columns(t, r.ID)
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: tc.text})
			text := strings.ReplaceAll(tc.text, "\r", "")
			if tc.reason != "" {
				d := assertDeliveryError(t, err, "not_started", 0, false)
				want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: store.StateWaiting, Reason: d.Reason}
				if d != want || !strings.Contains(d.Reason, tc.reason) {
					t.Fatalf("refusal=%+v; want %+v with reason %q", d, want, tc.reason)
				}
				e.assertNoTmuxCall(t)
				e.assertRowUnchanged(t, r.ID, before)
				adviceAssertAdvice(t, err, api.ErrSubmissionUnconfirmed, "put the prompt in a file and send a short instruction to read it")
				// Follow the advice only after proven pre-input refusal.
				file := filepath.Join(t.TempDir(), "prompt.txt")
				if err := os.WriteFile(file, []byte(tc.text), 0o600); err != nil {
					t.Fatal(err)
				}
				text = "Read " + file + " and follow its instructions."
				err = e.send(api.SendKeysParams{InstanceID: r.ID, Text: text})
			}
			if err != nil {
				t.Fatal(err)
			}
			assertInputWrites(t, e.killEnv, r, text)
			if calls := e.rec.SocketCallsOf(tmux.CallSendText); len(calls) != 1 || !calls[0].Paste {
				t.Fatalf("not one paste: %+v", calls)
			}
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

// E15 (b.vr5): "put the prompt in a file and send a short instruction to read
// it" after macOS paced Claude text over 32 KiB is refused before any input,
// typing nothing and recording no input attempt; followed, the instruction is
// delivered. Text of 32 KiB is typed. This unsupported launch records no
// attempt for paced text either way; TestSendKeysMacClaudePacedRecordAndAttemptFacts
// pins the refusal before the record on a supported one.
func TestAdviceFollow_E15_PacedClaudeTextOver32KiB(t *testing.T) {
	// Serial: it sets the process-wide paced-typing selection with api.SetClaudePacedTypingForTest.
	t.Cleanup(api.SetClaudePacedTypingForTest(true))
	for _, n := range []int{32 << 10, 32<<10 + 1} {
		t.Run(fmt.Sprintf("%d bytes", n), func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
			pane := r.Spawn.Identity.PaneID
			text := strings.Repeat("l", n)
			before := e.columns(t, r.ID)

			_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: text})

			if n == 32<<10 {
				typed := ""
				for _, c := range e.rec.SocketCallsOf(tmux.CallSendText) {
					typed += c.Text
				}
				if err != nil || typed != text || len(ordinaryEnters(e.killEnv)) != 1 {
					t.Fatalf("send = %v, typed %d bytes, %d Enters; want the %d bytes and one Enter", err, len(typed), len(ordinaryEnters(e.killEnv)), n)
				}
				return
			}
			d := assertDeliveryError(t, err, "not_started", 0, false)
			if d.KeysSent || d.Phase != "pre_input" {
				t.Errorf("details = %+v; want nothing sent, phase pre_input", d)
			}
			adviceAssertAdvice(t, err, api.ErrSubmissionUnconfirmed, "put the prompt in a file and send a short instruction to read it")
			e.assertNothingSent(t)
			e.assertNoCalls(t, tmux.CallSendKey)
			if e.b.current != nil || len(e.b.history) != 0 {
				t.Errorf("input attempts = %+v, %+v; want no durable input record", e.b.current, e.b.history)
			}
			e.assertRowUnchanged(t, r.ID, before)

			file := filepath.Join(t.TempDir(), "prompt.txt")
			if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			short := "Read " + file + " and follow its instructions."
			if _, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: short}); err != nil {
				t.Fatalf("send-keys of the short instruction: %v", err)
			}
			assertTextSent(t, e.rec, r.Socket, pane, short)
			assertEnterSent(t, e.rec, r.Socket, pane)
		})
	}
}

// E16 (b.qcr): "use a paste-capable adapter" after a pre-input refusal.
func TestAdviceFollow_E16_CodexFallbackRequiresPasteCapability(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
	before := e.columns(t, r.ID)
	p := api.SendKeysParams{InstanceID: r.ID, Text: skpText}
	// Interface embedding deliberately hides Recorder's optional capability.
	legacy := struct{ api.SendKeysTmux }{e.rec}
	_, err := api.SendKeys(e.s, legacy, e.env, p)
	d := assertDeliveryError(t, err, "not_started", 0, false)
	if d.KeysSent || d.Phase != "pre_input" {
		t.Fatalf("capability refusal=%+v", d)
	}
	adviceAssertAdvice(t, err, api.ErrSubmissionUnconfirmed, "use a paste-capable adapter")
	e.assertNoTmuxCall(t)
	e.assertRowUnchanged(t, r.ID, before)
	if err := e.send(p); err != nil {
		t.Fatal(err)
	}
	assertInputWrites(t, e.killEnv, r, skpText)
	if !e.rec.SocketCallsOf(tmux.CallSendText)[0].Paste {
		t.Fatal("replacement adapter did not paste")
	}
}

// E17 (b.qcr): "inspect get or status and escalate if submission remains unknown".
// Never inherit manual advice to send another Enter.
func TestAdviceFollow_E17_CodexFallbackTransportUncertain(t *testing.T) {
	t.Parallel()
	for _, call := range []tmux.Call{tmux.CallSendText, tmux.CallSendEnter} {
		for _, failure := range []tmux.Failure{tmux.FailTimeout, tmux.FailNoServer} {
			t.Run(fmt.Sprintf("%s/%v", call, failure), func(t *testing.T) {
				t.Parallel()
				e := newSubmissionEnv(t)
				r, other := e.seedKeysDisagreeCase(t, keysDisagreeCase{
					spec:  codexSpec(store.StatePending, agentAlive, false),
					setup: []func(*testing.T, *killEnv, *killRow){ktrRestart},
				})
				before := e.columns(t, r.ID)
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: failure}, call)
				err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
				outcome, phase, enters := "partial_input_unconfirmed", "text_attempted", 0
				if call == tmux.CallSendEnter {
					outcome, phase, enters = "submission_unconfirmed", "enter_attempted", 1
				}
				d := assertDeliveryError(t, err, outcome, len(skpText), enters == 1)
				if !d.KeysSent || d.Phase != phase || d.RequestID != "" {
					t.Fatalf("paste uncertainty=%+v", d)
				}
				adviceAssertAdvice(t, err, api.ErrSubmissionUnconfirmed, "do not automatically replay text or Enter")
				adviceAssertAdvice(t, err, api.ErrSubmissionUnconfirmed, "inspect get or status and escalate if submission remains unknown")
				row, getErr := api.Get(e.st, e.env.Relay, r.ID)
				if getErr != nil || row.State != store.StatePending {
					t.Fatalf("inspect Get=%+v, %v", row, getErr)
				}
				if texts := e.rec.SocketCallsOf(tmux.CallSendText); len(texts) != 1 || !texts[0].Paste {
					t.Fatalf("text attempts=%+v", texts)
				}
				if got := len(e.rec.SocketCallsOf(tmux.CallSendEnter)); got != enters {
					t.Fatalf("Enter attempts=%d; want %d", got, enters)
				}
				e.assertNoCalls(t, tmux.CallSendKey, tmux.CallCapture)
				e.assertRowUnchanged(t, r.ID, before)
				assertKeysDisagrees(t, e.killEnv, sktDisagrees(t, r.ID), r, "send-keys", other, []disagreeWant{
					{reason: "server_restarted", server: "restarted", verdict: "ours", action: "text_sent", ours: true},
				})
				if e.waits != 0 || e.s.reads != 0 {
					t.Fatalf("polled submission after failed transport: %d/%d", e.waits, e.s.reads)
				}
			})
		}
	}
}

// E19 (b.qcf): "nothing was typed, so the same send-keys can be retried later".
// The capability refusal instead says "use an adapter with that capability".
func TestAdviceFollow_E19_ClaudeFallbackTerminalRetry(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cooked", "read failed", "no capability"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{State: store.StatePending})
			before := e.columns(t, r.ID)
			e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
			var adapter api.SendKeysTmux = e.rec
			advice := "nothing was typed, so the same send-keys can be retried later"
			reason := "the agent's terminal did not take raw input within 12 seconds; " + advice
			var readError error
			e.rec.AfterCall(tmux.CallPaneTerminal, func(_ tmuxfix.SocketCall, err error) { readError = err })
			switch mode {
			case "cooked":
				e.rec.SetTerminalRaw(r.Socket, r.Spawn.Identity.PaneID, false)
			case "read failed":
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallPaneTerminal)
			case "no capability":
				adapter = struct{ api.SendKeysTmux }{e.rec}
				advice = "use an adapter with that capability, or send once the row is no longer pending"
				reason = "the tmux adapter cannot read the agent's terminal mode, which pending Claude fallback input waits on; nothing was typed, so the same send-keys can be retried later; " + advice
			}
			waits := 0
			sleep := e.env.Sleep
			e.env.Sleep = func(d time.Duration) {
				waits++
				if d <= 0 || d > 100*time.Millisecond || waits > 120 {
					t.Fatalf("readiness wait %d=%v; want bounded 100ms polls", waits, d)
				}
				sleep(d)
			}
			params := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true}
			start := e.clock.Now()
			_, err := api.SendKeys(e.s, adapter, e.env, params)
			if mode == "read failed" {
				if readError == nil {
					t.Fatal("scripted terminal read did not fail")
				}
				reason = "the agent's terminal mode could not be read: " + readError.Error() + "; " + advice
			}
			assertOneName(t, err, "ErrSubmissionUnconfirmed")
			want := api.SubmissionUnconfirmedDetails{Outcome: "not_started", Phase: "pre_input", RowState: store.StatePending, Reason: reason}
			if got := api.ErrDetails(err); got != want {
				t.Fatalf("refusal=%+v; want %+v", got, want)
			}
			adviceAssertPhrase(t, err, advice)
			e.assertNothingSent(t)
			e.assertNoCalls(t, tmux.CallSendKey)
			e.assertRowUnchanged(t, r.ID, before)
			if e.b.current != nil || len(e.b.history) != 0 || e.s.sent {
				t.Fatal("not-started refusal journalled or sent input")
			}
			reads := len(e.rec.SocketCallsOf(tmux.CallPaneTerminal))
			switch mode {
			case "cooked":
				elapsed := e.clock.Now().Sub(start)
				if reads != 120 || waits != 120 || elapsed != 12*time.Second+2*time.Nanosecond {
					t.Fatalf("terminal reads/waits/elapsed=%d/%d/%v; want 120/120/12s plus initial lookup/listing", reads, waits, elapsed)
				}
			case "read failed":
				if reads != 1 || waits != 0 {
					t.Fatalf("terminal reads/waits=%d/%d; want 1/0", reads, waits)
				}
			case "no capability":
				e.assertNoTmuxCall(t)
				if waits != 0 {
					t.Fatalf("waits=%d; want none", waits)
				}
			}
			// Follow the exact retry advice: clear the condition, keep the request unchanged.
			e.clock.Advance(time.Second)
			e.rec.Reset()
			e.rec.SetTerminalRaw(r.Socket, r.Spawn.Identity.PaneID, true)
			e.sessionStartsWhenPolled(t, r, nil)
			e.env.Sleep = sleep // Each retry has a fresh readiness budget.
			e.turnStartsAfterEnter(t, r)
			result, err := e.send(params)
			if err != nil || result != (api.SendKeysResult{}) {
				t.Fatalf("same send after condition clears: %+v, %v", result, err)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText, 2)
		})
	}
}
