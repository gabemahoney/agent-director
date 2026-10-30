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
//   - sendKeysActionKeysSent: input was attempted with no recorded send error.
//   - sendKeysActionTextSent: structured details report possible input, including
//     paste failures; otherwise text was sent before Enter or its admission
//     failed, or the legacy transport reported a timeout.
//   - sendKeysActionNothingSent: no text call was made (after a failed line
//     clear of pause's too: C-u types no text), or the text call failed
//     other than by timing out.
const (
	sendKeysActionKeysSent    = "keys_sent"
	sendKeysActionTextSent    = "text_sent"
	sendKeysActionNothingSent = "nothing_sent"
)

// sendKeysAction retains successful text even when the separate Enter or
// intervening admission failed. Error call labels alone cannot establish it.
func sendKeysAction(f keysFacts) string {
	if !f.Sent {
		return sendKeysActionNothingSent
	}
	if f.SendErr == nil {
		return sendKeysActionKeysSent
	}
	if details, ok := ErrDetails(f.SendErr).(SubmissionUnconfirmedDetails); ok && details.KeysSent {
		return sendKeysActionTextSent
	}
	var ce *tmux.CallError
	if f.TextSent || errors.As(f.SendErr, &ce) && (ce.Failure == tmux.FailTimeout || enterFailedAfterText(ce)) {
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
