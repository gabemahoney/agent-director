package api

import (
	"errors"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds the pane verbs' shared mapping of a failed action call to
// the verb error (SR-2.5, SR-7.3, SR-1.4, SR-13.2): read-pane's capture, and
// send-keys' and pause's text and Enter sends. It has no verb-specific
// branch: each verb passes its action call, its gone sentinel and the
// sentences that say what it did not do or may have done.

// paneActionFailure holds what a pane verb gives the action-failure mapping.
type paneActionFailure struct {
	// Call is the action call that failed: tmux.CallCapture (read-pane),
	// tmux.CallSendText or tmux.CallSendEnter (send-keys, pause).
	Call tmux.Call
	// Gone is the verb's gone sentinel: tmux.ErrTmuxCaptureFailed
	// (read-pane) or tmux.ErrTmuxSendKeys (send-keys, pause).
	Gone error
	// Pane holds the gone error's inputs (paneGoneError): the instance id,
	// the row's recorded name and what was not done.
	Pane paneRefusal
	// Refusal holds the ErrTmuxUnresponsive and ErrTmuxNotAvailable
	// descriptions' inputs: the instance id, the context, and the verb's
	// Consequence sentence ("" for "nothing was done", as read-pane gives,
	// or that the keys may have been delivered, or that the text may be
	// typed but not submitted).
	// Retry stays "" ("retry later"). Socket and Call are set by the mapping:
	// the socket from the follow-up's launch, the call from Call or
	// tmux.CallLookup.
	Refusal cantTellRefusal
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
//     consequence and "retry later"; no follow-up call.
//   - Any other failure: exactly one follow-up, tmux.Lookup on launch (the
//     row's launch identity with this store's id and the socket already
//     resolved for the action) through t and pc, mapped by the cases below.
//   - Follow-up Gone or Leftover: f.Gone through paneGoneError, "the row's
//     session is not there", with the failed action as its detail.
//   - Follow-up Ours, unreadable or provenance_conflict: ErrTmuxUnresponsive
//     naming the failed action (with the reply's first line, trimmed to 200
//     bytes, when it gave one), what the follow-up found, the verb's
//     consequence and "retry later". SR-7.3's Can't tell rule excepts only a
//     different server, so conflicting labels here are not the first
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
	r := f.Refusal
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
		return fu, paneGoneError(f.Gone, f.Pane, action)
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
