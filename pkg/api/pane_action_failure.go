package api

import (
	"errors"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the pane verbs' shared mapping of a failed action call to
// the verb error (SR-2.5, SR-7.3, SR-1.4, SR-13.2): read-pane's capture, and
// send-keys' and pause's text and Enter sends and pause's line clear. It has
// no verb-specific branch: each verb passes its action call, its gone
// sentinel and the sentences that say what it did not do, and the verbs that
// type keys select the "keys may have reached the pane" mode
// (paneActionFailure.Keys), which says what they may have done, with the
// verb's next step after it (paneActionFailure.Next).

// paneActionFailure holds what a pane verb gives the action-failure mapping.
type paneActionFailure struct {
	// Call is the action call that failed: tmux.CallCapture (read-pane),
	// tmux.CallSendText for the text and Enter calls (send-keys, pause; the
	// error names which) or tmux.CallSendKey (pause's line clear).
	Call tmux.Call
	// Gone is the verb's gone sentinel: tmux.ErrTmuxCaptureFailed
	// (read-pane) or tmux.ErrTmuxSendKeys (send-keys, pause).
	Gone error
	// Pane holds the gone error's inputs (paneGoneError): the instance id,
	// the row's recorded name and what was not done.
	Pane paneRefusal
	// Refusal holds the ErrTmuxUnresponsive and ErrTmuxNotAvailable
	// descriptions' inputs: the instance id, the context, and the verb's
	// Consequence sentence ("" for "nothing was done", as read-pane gives).
	// In Keys mode the mapping sets the consequence and the retry sentence
	// itself (keysReached); otherwise Retry stays "" ("retry later"). Socket
	// and Call are set by the mapping: the socket from the follow-up's
	// launch, the call from Call or tmux.CallLookup.
	Refusal cantTellRefusal
	// Keys selects the "keys may have reached the pane" mode, for the verbs
	// whose action types keys (send-keys' text and Enter, pause's line clear,
	// /exit and Enter): a failure may leave keys in the pane, so the
	// descriptions say what may have happened instead of that nothing was
	// done (keysReached). read-pane leaves it false.
	Keys bool
	// Next, in Keys mode, is the verb's next step after a failure that may
	// have left its text in the pane, in place of "retry later" (b.9o4):
	// send-keys passes sendKeysNext; pause passes the zero value and keeps
	// "retry later", since its retry clears the one-line `/exit` its failed
	// attempt left before typing.
	Next keysNext
}

// keysNext is a keys verb's next step after a keys failure that may have
// left its text in the pane, given in its ErrTmuxUnresponsive descriptions
// in place of "retry later" (b.9o4). A field left "" keeps "retry later".
type keysNext struct {
	// TextTimeout follows a timed-out text call, which may or may not have
	// typed the text.
	TextTimeout string
	// NotSubmitted follows an Enter call that failed or timed out after the
	// text call went through: the text may be typed but not submitted.
	NotSubmitted string
}

// sendKeysNext is send-keys' next step after a keys failure (b.9o4): a
// literal retry would type the text again after an unsubmitted copy, so
// after a timed-out text call the caller reads the pane and, if the text is
// typed, sends empty text (Enter only); after a failed Enter it sends empty
// text. Enter on an empty Claude Code input submits nothing, so the
// Enter-only send is safe also when a timed-out Enter had submitted the
// text.
var sendKeysNext = keysNext{
	TextTimeout:  "read-pane; if the text is typed, send-keys with empty text, otherwise the same send-keys",
	NotSubmitted: "send-keys with empty text submits it",
}

// The "keys may have reached the pane" mode's sentences (SR-1.4 rows "keys
// action timed out" and "Enter failed after the text went through", SR-7.3).
const (
	// keysMayHaveBeenDelivered replaces "nothing was done" when a text or
	// Enter call timed out.
	keysMayHaveBeenDelivered = "the keys may have been delivered"
	// textNotSubmitted is said, in whatever class results, when the text
	// call succeeded and the Enter call then failed or timed out; it replaces
	// the gone error's "nothing was sent" and the other classes' "nothing
	// was done".
	textNotSubmitted paneNothing = "the text may be typed but not submitted"
)

// enterFailedAfterText reports that a keys action's failure is its Enter
// call's, so its text call went through (tmux.SendKeysPane makes the Enter
// call only after the text call succeeded).
func enterFailedAfterText(ce *tmux.CallError) bool {
	return ce != nil && ce.Call == tmux.CallSendEnter
}

// keysReached applies the "keys may have reached the pane" mode to a keys
// action's failure ce (SR-1.4, SR-7.3), returning the gone error's inputs
// and the other classes' refusal with their sentences set; next is the
// verb's next step (b.9o4), which only an ErrTmuxUnresponsive description
// gives:
//
//   - a timed-out line clear, text or Enter call: the consequence is "the
//     keys may have been delivered" (never "nothing was done"), followed, for
//     the Enter call, by "the text may be typed but not submitted"; the retry
//     sentence is next.TextTimeout after the text call, next.NotSubmitted
//     after the Enter call;
//   - any other failure of the Enter call: the gone error and every other
//     class say "the text may be typed but not submitted", and neither
//     "nothing was sent" nor "nothing was done"; the retry sentence is
//     next.NotSubmitted;
//   - any other failure of the line clear or the text call, or an error that
//     is not a *tmux.CallError (counted as the failed call's unrecognised
//     reply): pane and r unchanged, since no key was typed.
//
// A retry sentence left "" is "retry later".
func keysReached(ce *tmux.CallError, pane paneRefusal, r cantTellRefusal, next keysNext) (paneRefusal, cantTellRefusal) {
	enter := enterFailedAfterText(ce)
	switch {
	case ce != nil && ce.Failure == tmux.FailTimeout:
		r.Consequence = keysMayHaveBeenDelivered
		switch {
		case enter:
			r.Consequence += "; " + string(textNotSubmitted)
			r.Retry = next.NotSubmitted
		case ce.Call == tmux.CallSendText:
			r.Retry = next.TextTimeout
		}
	case enter:
		pane.Nothing = textNotSubmitted
		r.Consequence = string(textNotSubmitted)
		r.Retry = next.NotSubmitted
	}
	return pane, r
}

// paneFollowUp is the follow-up lookup of a failed action, for the verbs
// that record it (send-keys' trail, emitProvenanceDisagree).
type paneFollowUp struct {
	// Ran reports that the follow-up lookup was made; false after a timeout.
	Ran bool
	// Result is the follow-up's Result; zero when Ran is false.
	Result tmux.Result
}

// Token returns the follow-up's outcome token (SR-3.4), or tmux.TokenNotRun
// when no follow-up ran.
func (f paneFollowUp) Token() string {
	if !f.Ran {
		return tmux.TokenNotRun
	}
	return f.Result.Token()
}

// paneActionFailureError maps a failed action call of read-pane, send-keys
// or pause to the verb error (SR-2.5, SR-7.3, SR-1.4), making at most one
// follow-up lookup (SR-13.2). The action's reply text never classifies: an
// action call recognises no reply (SR-2.5), and an error that is not a
// *tmux.CallError counts as an unrecognised reply of f.Call (Appendix F.3).
//
//   - A timeout (*tmux.CallError with tmux.FailTimeout): ErrTmuxUnresponsive
//     naming the call and its effective timeout in seconds, the verb's
//     consequence and the retry sentence ("retry later" unless keysReached
//     set the verb's next step); no follow-up call.
//   - In Keys mode, keysReached first sets the sentences every class below
//     gives: "the keys may have been delivered" after a timeout, and "the
//     text may be typed but not submitted" once the text call went through,
//     with the verb's next step (f.Next), which every ErrTmuxUnresponsive
//     below ends with in place of "retry later" once its text may be in the
//     pane.
//   - Any other failure: exactly one follow-up, tmux.Lookup on launch (the
//     row's launch identity with this store's id and the socket already
//     resolved for the action) through t and pc, mapped by the cases below.
//   - Follow-up Gone or Leftover: f.Gone through paneGoneError, "the row's
//     session is not there", with the failed action as its detail.
//   - Follow-up Ours, unreadable or provenance_conflict: ErrTmuxUnresponsive
//     naming the failed action (with the reply's first line, trimmed to 200
//     bytes, when it gave one), what the follow-up found, the verb's
//     consequence and the retry sentence. SR-7.3's Can't tell rule excepts
//     only a different server, so conflicting labels here are not the first
//     lookup's ErrTmuxSessionConflict.
//   - Follow-up different server or tmux unavailable: ErrTmuxNotAvailable
//     through cantTellError's builders (different server, missing binary,
//     socket permission), with the failed action before the verb's
//     consequence.
//
// It returns the follow-up (Ran false after a timeout) and the verb error,
// which matches exactly one catalogued sentinel under errors.Is (SR-1.5). No
// description carries a label's value, another row's id, "dead" or "gone",
// or a session-ending command.
func paneActionFailureError(actionErr error, f paneActionFailure, t TmuxLookup, pc ProcChecker, launch tmux.Launch) (paneFollowUp, error) {
	var ce *tmux.CallError
	if !errors.As(actionErr, &ce) {
		ce = nil
	}
	pane, r := f.Pane, f.Refusal
	if f.Keys {
		pane, r = keysReached(ce, pane, r, f.Next)
	}
	r.Socket = launch.Socket
	r.Call = f.Call
	if ce != nil && ce.Failure == tmux.FailTimeout {
		return paneFollowUp{}, unreadableError(ce, r)
	}
	res := tmux.Lookup(t, pc, launch, "")
	fu := paneFollowUp{Ran: true, Result: res}
	action := actionFailureText(ce, f.Call)
	switch res.Verdict {
	case tmux.Gone, tmux.Leftover:
		return fu, paneGoneError(f.Gone, pane, action)
	case tmux.Ours:
		r.Consequence = "the follow-up lookup found this launch's session; " + r.consequence()
		return fu, unreadableError(ce, r)
	}
	switch res.CantTell {
	case tmux.CantTellDifferentServer, tmux.CantTellUnavailable:
		lr := r
		lr.Call = tmux.CallLookup
		lr.Consequence = action + "; " + r.consequence()
		return fu, cantTellError(res, lr)
	case tmux.CantTellProvenanceConflict:
		r.Consequence = "the follow-up lookup found conflicting labels; " + r.consequence()
		return fu, unreadableError(ce, r)
	}
	found := "the follow-up lookup gave a reply agent-director does not recognise"
	if res.Cause != nil {
		found = "the follow-up could not tell: " + res.Cause.Error()
	}
	r.Consequence = found + "; " + r.consequence()
	return fu, unreadableError(ce, r)
}

// actionFailureText names a failed action call: the *tmux.CallError's text
// (the call and its failure, with the reply's first line when it gave one),
// or, for an error that is not a *tmux.CallError, an unrecognised reply of
// call with no reply text.
func actionFailureText(ce *tmux.CallError, call tmux.Call) string {
	if ce != nil {
		return ce.Error()
	}
	return "tmux " + string(call) + " failed: unrecognized reply"
}
