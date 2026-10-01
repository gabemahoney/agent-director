package api

import (
	"context"
	"errors"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// The ad.provenance.disagree action values the keys verbs write (SR-14):
// what the call typed into the agent's pane. send-keys and pause share them
// (keysRun.emitDisagree); pause's text is `/exit`.
//
//   - sendKeysActionKeysSent: the text call and the Enter call both went
//     through.
//   - sendKeysActionTextSent: the text may have been typed with no submit
//     known to have gone through: the text call timed out, or it went through
//     and the Enter call then failed or timed out.
//   - sendKeysActionNothingSent: no keys call was made, or the text call
//     failed other than by timing out.
const (
	sendKeysActionKeysSent    = "keys_sent"
	sendKeysActionTextSent    = "text_sent"
	sendKeysActionNothingSent = "nothing_sent"
)

// sendKeysAction is the call's action value from whether SendKeysPane was
// called (sent) and its error (sendErr).
func sendKeysAction(sent bool, sendErr error) string {
	if !sent {
		return sendKeysActionNothingSent
	}
	if sendErr == nil {
		return sendKeysActionKeysSent
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
		"claude_instance_id": params.ClaudeInstanceID,
		"allow_pending":      params.AllowPending,
		"row_state":          f.RowState,
		"guard_evaluation":   f.Guard,
		"outcome":            outcome,
		"caller_process":     f.Caller.process,
		"caller_pid":         f.Caller.pid,
		"caller_hostname":    f.Caller.hostname,
		"caller_user":        f.Caller.user,
		"source":             "ad_send_keys",
	})
}
