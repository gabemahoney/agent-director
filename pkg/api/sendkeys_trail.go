package api

import (
	"context"
	"errors"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// The ad.provenance.disagree action values send-keys writes (SR-14): what
// the call typed into the agent's pane.
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

// emitDisagree writes the call's ad.provenance.disagree records, fail-open
// (SR-7.4, SR-14): one per distinct reason collected during the call, none in
// the normal case. The reasons are collected as kill collects them: the first
// lookup's, a failed pane listing's and the follow-up lookup's Disagree
// reasons, name_changed when the Ours session carries another name, and
// adopted only when the adoption write applied. It runs on the path
// Client.SendKeys and SendKeys share, so each entry point writes each reason
// at most once per call. No record carries a label's value, a launch token,
// the typed text, a session-environment value or another row's id (SR-15).
func (r *sendKeysRun) emitDisagree(instanceID string) {
	f := r.facts
	var reasons []string
	var current string
	verdict := tmux.TokenNotRun
	if f.LookupRan {
		verdict = f.Lookup.Token()
		reasons = append(reasons, f.Lookup.Disagree...)
		if nameChanged(f.Lookup, r.row.TmuxSessionName) {
			reasons = append(reasons, tmux.ReasonNameChanged)
			current = f.Lookup.Session.Name
		}
	}
	reasons = append(reasons, f.Listing.Disagree...)
	if f.Adopted {
		reasons = append(reasons, tmux.ReasonAdopted)
	}
	if f.FollowUp.Ran {
		reasons = append(reasons, f.FollowUp.Result.Disagree...)
	}
	emitProvenanceDisagree(provenanceDisagree{
		Verb:               "send-keys",
		Source:             "ad_send_keys",
		InstanceID:         instanceID,
		Socket:             f.Socket,
		SessionName:        r.row.TmuxSessionName,
		SessionID:          f.Session.ID,
		CurrentSessionName: current,
		Server:             f.Lookup.Server,
		Verdict:            verdict,
		Action:             sendKeysAction(f.Sent, f.SendErr),
		Caller:             f.Caller,
	}, reasons...)
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
