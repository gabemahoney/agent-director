package api

import (
	"context"
	"errors"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// The ad.provenance.disagree action values the keys verbs write (SR-14):
// recorded input attempts and transport outcomes, not submission proof.
// send-keys and pause share them (keysRun.emitDisagree); pause's text is `/exit`.
//
//   - sendKeysActionKeysSent: input was attempted with no recorded send error
//     (text/paste and any requested Enter, or a key).
//   - sendKeysActionTextSent: structured SubmissionUnconfirmedDetails reports
//     KeysSent true, including fallback paste failures without a timeout;
//     otherwise the legacy transport mapping applies: a text/key timeout,
//     or successful text followed by a failed or timed-out Enter.
//   - sendKeysActionNothingSent: no input attempt (including after pause's
//     failed line clear: C-u types no text), or any remaining send error.
//     The legacy non-timeout text/key failure rule does not override
//     structured possible-input details.
const (
	sendKeysActionKeysSent    = "keys_sent"
	sendKeysActionTextSent    = "text_sent"
	sendKeysActionNothingSent = "nothing_sent"
)

// sendKeysAction is the call's action value from whether text, paste or a key
// was attempted (sent; a failed line clear before it leaves sent false) and
// the recorded send error (sendErr), including structured paste uncertainty.
func sendKeysAction(sent bool, sendErr error) string {
	if !sent {
		return sendKeysActionNothingSent
	}
	if sendErr == nil {
		return sendKeysActionKeysSent
	}
	if details, ok := ErrDetails(sendErr).(SubmissionUnconfirmedDetails); ok && details.KeysSent {
		return sendKeysActionTextSent
	}
	var ce *tmux.CallError
	if errors.As(sendErr, &ce) && (ce.Failure == tmux.FailTimeout || enterFailedAfterText(ce)) {
		return sendKeysActionTextSent
	}
	return sendKeysActionNothingSent
}

// emitSendKeysCalled writes Client.SendKeys' one ad.send_keys.called event,
// fail-open (SR-7.4, SR-14): row_state is the stored state of the row read
// ("" when none was read), outcome the name errnames.Classify gives err
// (errorName; "ok" on success), guard_evaluation the relay guard's
// evaluation, so the relay-recovery send is distinguishable from ordinary
// sends and from guard refusals (SR-5.2). A trail-write failure is discarded
// and never changes the send's result.
func emitSendKeysCalled(params SendKeysParams, f sendKeysFacts, err error) {
	outcome := "ok"
	if err != nil {
		outcome = errorName(err)
	}
	_ = trail.Emit(context.Background(), "ad.send_keys.called", map[string]any{
		"instance_id":      params.InstanceID,
		"allow_pending":    params.AllowPending,
		"row_state":        f.RowState,
		"guard_evaluation": f.Guard,
		"outcome":          outcome,
		"caller_process":   f.Caller.process,
		"caller_pid":       f.Caller.pid,
		"caller_hostname":  f.Caller.hostname,
		"caller_user":      f.Caller.user,
		"source":           "ad_send_keys",
	})
}
