package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// The pause claim's call side (SR-6.5; M2 RoutePauseClaim, P-C4.6 and P-C4.7).
// A covered pause reads and writes its Launch's claim only through
// internal/store's pause claim protocol: a conditional pause (only_if_waiting)
// of a live row claims the captured current Launch, or gets its refusal or the
// standing claim's answer; any other covered pause gets the standing claim's
// answer, after the store released a claim abandoned before its signal intent,
// and a plain one signals only under the store's write lock while no claim
// stands (SignalPlainBusySendPauseWithin), so a plain pause honours a standing
// claim too. The claim's owner persists its signal intent right before its
// signal and records the call's outcome, or releases the claim on a refusal it
// proved before any signal, every write within the original pause budget and
// compared by the store against the owner's retained ref.

// pauseClaimStore is the pause claim protocol's store surface, every call
// within the caller's context and what is left of its budget. *store.Store
// satisfies it; keylessPauseStore embeds it, so a store without it pauses no
// covered agent and never falls back to keys.
type pauseClaimStore interface {
	ClaimBusySendPauseWithin(ctx context.Context, req store.BusySendPauseRequest, maxWait time.Duration) (store.BusySendPauseDecision, error)
	CheckBusySendPauseWithin(ctx context.Context, req store.BusySendPauseRequest, maxWait time.Duration) (store.BusySendPauseDecision, error)
	SignalPlainBusySendPauseWithin(ctx context.Context, req store.BusySendPauseRequest, signal func() error, maxWait time.Duration) (store.BusySendPlainPauseSignalResult, error)
	PersistBusySendPauseSignalWithin(ctx context.Context, req store.BusySendPauseWriteRequest, maxWait time.Duration) (store.BusySendPauseWriteResult, error)
	RecordBusySendPauseOutcomeWithin(ctx context.Context, req store.BusySendPauseOutcomeRequest, maxWait time.Duration) (store.BusySendPauseWriteResult, error)
	ReleaseBusySendPauseBeforeSignalWithin(ctx context.Context, req store.BusySendPauseRefusalRequest, maxWait time.Duration) (store.BusySendPauseWriteResult, error)
}

var _ keylessPauseStore = (*store.Store)(nil)

// The pause claim's own causes (SR-6.5): the two refusals of a conditional
// pause's fresh check, which change nothing, and the two answers of a claim
// standing on the Launch, for either option value.
const (
	pauseCauseNotWaiting  = store.BusySendPauseNotWaiting
	pauseCauseTurnPending = store.BusySendPauseTurnPending
	pauseCauseInProgress  = store.BusySendPauseInProgress
	pauseCauseUncertain   = store.BusySendPauseUncertain
)

// claimStanding is how every answer of a claim that stands describes its
// fence and how it ends.
const claimStanding = "its claim on this Launch and that claim's fence on input stay until the Launch ends with evidence that its agent exited, or a resume or reuse publishes a new Launch (kill establishes that evidence)"

// pauseClaimRequest is the claim protocol's request for the captured Launch:
// the call's original deadline, never renewed, and keyless pause's clock.
// owner is the conditional call's identity, "" for any other.
func pauseClaimRequest(b keylessBudget, instanceID string, launch store.BusySendPublishedLaunch, owner string) store.BusySendPauseRequest {
	return store.BusySendPauseRequest{InstanceID: instanceID, LaunchID: launch.LaunchID, LaunchToken: launch.LaunchToken,
		OwnerCallID: owner, Deadline: b.deadline, Now: pauseNow}
}

// claimEnded reports whether decision d leaves the Launch to a shutdown with
// no claim and no signal instead of answering: its claim ended on exit
// evidence, a claim attempt found its End recorded with no claim, or its claim
// stands with paused recorded, the agent's exit already verified. A finished
// row is then the no-op success, which is paused's replay; a live one has its
// exclusion and row end finished (keylessShutdown, ended).
func claimEnded(d store.BusySendPauseDecision) bool {
	return d.Kind == store.BusySendPauseDecisionEnded ||
		d.Kind == store.BusySendPauseDecisionRecorded && d.Claim != nil && d.Claim.CallOutcome == store.BusySendPausePaused
}

// claimAnswer is a pause decision's public answer when it is neither claimed,
// clear nor claimEnded, which its callers act on: a fresh refusal or a
// standing claim's answer is ErrSpawnNotPausable with its cause; a recorded
// failure or uncertain exit is replayed (recordedAnswer). It sends nothing and
// is the same for either option value.
func claimAnswer(instanceID string, d store.BusySendPauseDecision) error {
	switch d.Kind {
	case store.BusySendPauseDecisionRefused:
		return claimRefusal(instanceID, d)
	case store.BusySendPauseDecisionRecorded:
		if d.Claim != nil {
			return recordedAnswer(instanceID, *d.Claim)
		}
	}
	return fmt.Errorf("pause %s: the pause claim answered %q, which pause cannot act on; nothing was signalled", instanceID, d.Kind)
}

// claimRefusal is a refused decision's ErrSpawnNotPausable: a conditional
// pause's not_waiting (with the raw state it observed) or turn_pending, which
// claimed, signalled and changed nothing, or a standing claim's
// pause_in_progress or pause_uncertain, which sent nothing.
func claimRefusal(instanceID string, d store.BusySendPauseDecision) error {
	details := NotPausableDetails{Cause: d.Cause}
	var description string
	switch d.Cause {
	case pauseCauseNotWaiting:
		details.ObservedState = d.ObservedState
		description = fmt.Sprintf("the agent is %s, not waiting; nothing was signalled or changed; retry pause once it is waiting, or pause without only_if_waiting", d.ObservedState)
	case pauseCauseTurnPending:
		description = "the agent has a turn pending (input it has not started, a prompt whose output is not complete, or a pending force); nothing was signalled or changed; retry pause after that turn"
	case pauseCauseInProgress:
		description = "another pause of this Launch is in progress; nothing was signalled; retry pause"
	case pauseCauseUncertain:
		description = "an earlier pause of this Launch persisted its signal intent and recorded no outcome, so whether it signalled is not known; nothing was signalled; " + claimStanding + "; inspect with get before more input"
	default:
		return fmt.Errorf("pause %s: the pause claim refused with cause %q, which pause does not know; nothing was signalled", instanceID, d.Cause)
	}
	return &DetailedError{
		Err:     fmt.Errorf("%w: pause %s: %s", ErrSpawnNotPausable, instanceID, description),
		Details: details,
	}
}

// recordedAnswer replays the outcome an earlier pause recorded on the claim c
// standing on the captured current Launch, sending nothing: exit_uncertain is
// that cause with signal_sent true, since it is recorded only once the signal
// was sent; failed is its recorded cause, or pause_uncertain with none,
// signal_sent omitted, since a signal intent proves no signal. paused is not
// replayed here (claimEnded). The earlier call returned its own error.
func recordedAnswer(instanceID string, c store.BusySendPauseClaim) error {
	switch {
	case c.CallOutcome == store.BusySendPauseExitUncertain:
		sent := true
		return notPausable(instanceID, "an earlier pause of this Launch sent its signal and the agent did not exit within that pause's budget; nothing was signalled now; "+claimStanding+"; inspect with get before more input",
			pauseCauseExitUncertain, &sent)
	case c.CallOutcome == store.BusySendPauseFailed && c.CallCause != "":
		return notPausable(instanceID, fmt.Sprintf("an earlier pause of this Launch failed (%s) after its signal intent; nothing was signalled now; %s; inspect with get before more input", c.CallCause, claimStanding),
			c.CallCause, nil)
	case c.CallOutcome == store.BusySendPauseFailed:
		return notPausable(instanceID, "an earlier pause of this Launch failed after its signal intent with no recorded cause, so whether it signalled is not known; nothing was signalled now; "+claimStanding+"; inspect with get before more input",
			pauseCauseUncertain, nil)
	}
	return fmt.Errorf("pause %s: the pause claim recorded outcome %q, which pause cannot replay; nothing was signalled", instanceID, c.CallOutcome)
}

// claimFailed maps a pause claim store call's error: a Launch the store
// found changed (another or an uncovered Launch, or a row holding a
// candidate's token) is launch_changed, nothing signalled; the rest is
// storeFailed's, nothing signalled.
func (r *pauseRun) claimFailed(b keylessBudget, instanceID string, err error) error {
	if errors.Is(err, store.ErrBusySendLaunchChanged) && r.ctxErr() == nil {
		return launchChangedBeforeSignal(instanceID)
	}
	return r.storeFailed(b, instanceID, false, err)
}

// launchChangedBeforeSignal is launch_changed with nothing signalled.
func launchChangedBeforeSignal(instanceID string) error {
	notSent := false
	return notPausable(instanceID, "the agent's Launch changed before the signal; nothing was signalled; retry pause",
		pauseCauseLaunchChanged, &notSent)
}

// pauseOwner is a conditional pause's authority over the claim it made: ref,
// as the store granted it or an applied signal intent advanced it, never
// refreshed from a snapshot. intent says that signal intent is confirmed.
// done is set once the owner recorded its outcome or released the claim,
// which ends its authority. A nil *pauseOwner (a plain pause, or a covered
// pause that made no claim) writes nothing.
type pauseOwner struct {
	claims pauseClaimStore
	ref    store.BusySendPauseRef
	intent bool
	done   bool
}

// admits reports whether the owner may still write: it has authority, and
// the call is neither cancelled nor out of budget, since no owner write
// starts after either (no detached or after-deadline write).
func (o *pauseOwner) admits(r *pauseRun, b keylessBudget) bool {
	return o != nil && !o.done && r.ctxErr() == nil && !b.spent()
}

// persistIntent persists the owner's signal intent right before its signal
// and advances ref on success. Only an applied write authorizes the one
// signal; an error, or a write that did not apply (a stale ref, a claim a
// lifecycle transition ended, or a Launch no longer current), sends none.
func (o *pauseOwner) persistIntent(b keylessBudget) (bool, error) {
	res, err := o.claims.PersistBusySendPauseSignalWithin(b.ctx, store.BusySendPauseWriteRequest{Ref: o.ref, Now: pauseNow}, b.wait())
	if err != nil || !res.Applied || res.NextRef == nil {
		return false, err
	}
	o.ref, o.intent = *res.NextRef, true
	return true, nil
}

// verified records paused once the owner's agent process is verified gone
// within the budget, after its signal intent (P-C4.7): before the Launch End
// it then records, which ends the claim (launch_end) and would leave a later
// outcome write unapplied. With no intent (the agent was gone before any
// signal) nothing is recorded: the Launch End ends the claim with no outcome.
func (o *pauseOwner) verified(r *pauseRun, b keylessBudget, at time.Time) {
	if !o.admits(r, b) || !o.intent {
		return
	}
	o.record(b, store.BusySendPauseOutcomeRequest{Ref: o.ref, Now: pauseNow, Outcome: store.BusySendPausePaused, VerifiedExitAt: at})
}

// settle records the owner's disposition of the call's error err, within the
// budget: before its signal intent, every error is a refusal proven before
// any signal, so the claim is released with err's cause; after it, the
// signal's own proof that nothing was sent (agent_process_unverifiable or
// signal_unsupported, which no later step returns) releases it too, keeping
// the intent; exit_uncertain is recorded as such, and any other error as
// failed with its cause ("" for an error with none). A success, a
// cancellation, a spent budget and an owner already done write nothing. The
// call returns its own err whatever the write's result: a write that does not
// apply or commit leaves the claim as it stands, for a later pause to answer.
func (o *pauseOwner) settle(r *pauseRun, b keylessBudget, err error) {
	if err == nil || !o.admits(r, b) {
		return
	}
	var cause string
	if d, ok := ErrDetails(err).(NotPausableDetails); ok {
		cause = d.Cause
	}
	switch {
	case !o.intent, cause == pauseCauseAgentUnverifiable, cause == pauseCauseSignalUnsupported:
		o.done = true
		// The call's own error stands; the claim's state is what a later pause reads.
		_, _ = o.claims.ReleaseBusySendPauseBeforeSignalWithin(b.ctx, store.BusySendPauseRefusalRequest{Ref: o.ref, Now: pauseNow, Cause: cause}, b.wait())
	case cause == pauseCauseExitUncertain:
		o.record(b, store.BusySendPauseOutcomeRequest{Ref: o.ref, Now: pauseNow, Outcome: store.BusySendPauseExitUncertain, Cause: cause})
	default:
		o.record(b, store.BusySendPauseOutcomeRequest{Ref: o.ref, Now: pauseNow, Outcome: store.BusySendPauseFailed, Cause: cause})
	}
}

// record writes the owner's one outcome. Its result is not the call's: the
// call keeps its own, and a write that does not apply or commit leaves the
// claim as it stands, for a later pause to answer.
func (o *pauseOwner) record(b keylessBudget, req store.BusySendPauseOutcomeRequest) {
	o.done = true
	_, _ = o.claims.RecordBusySendPauseOutcomeWithin(b.ctx, req, b.wait())
}
