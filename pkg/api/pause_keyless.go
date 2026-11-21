package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Keyless pause (SR-6.4): a row whose current Launch is recorded covered is
// paused by an identity-checked SIGTERM to its recorded agent process, never
// by keys: no line clear, no `/exit`, no relay guard, no input session and no
// pane call. Its Held Messages are neither waited for nor drained; they stay
// Held, in order with their resolved options, for a resume to carry. Pause
// succeeds only once it established resumable keyless shutdown within the
// existing pause budget: the agent's verified exit and the shared exit
// evidence for every owned process and captured destination of its Launch
// (judgeBusySendExit), then its guarded row transition to ended, retaining
// session_id. Anything less is ErrSpawnNotPausable with a structured cause.

// The NotPausableDetails causes of keyless pause (SR-6.4), beside
// agent_not_covered and the shared exit judgement's causes
// (owned_process_running, owned_process_unverifiable, destination_unfenced),
// which keyless pause returns when the agent exited but its Launch's
// exclusion was not established within the budget, and, owned_process_running,
// when a pause that sends no signal (its agent's exit already known, or its
// Launch ended with no claim) finds the recorded agent itself still running
// at the deadline.
const (
	// pauseCauseAgentUnverifiable: the recorded agent process cannot be
	// judged (no identity recorded, its start time unreadable, or a pid-only
	// identity whose pid is in use); nothing was signalled.
	pauseCauseAgentUnverifiable = "agent_process_unverifiable"
	// pauseCauseLaunchChanged: the row's published current Launch or its
	// recorded agent identity changed before the signal (a launch in
	// progress, or another one; nothing was signalled), or another Launch
	// replaced the captured one after the signal (signal_sent true), which
	// pause then neither judges nor changes.
	pauseCauseLaunchChanged = "launch_changed"
	// pauseCauseSignalUnsupported: this system has no identity-checked
	// signal; nothing was signalled.
	pauseCauseSignalUnsupported = "signal_unsupported"
	// pauseCauseSignalUncertain: the signal call failed, so whether the agent
	// got it is not known.
	pauseCauseSignalUncertain = "signal_uncertain"
	// pauseCauseExitUncertain: the signal was sent and the agent did not exit
	// within the pause budget. Uncertain exit, never success.
	pauseCauseExitUncertain = "exit_uncertain"
	// pauseCauseBudgetExhausted: the pause budget ran out at a store read or
	// write (the store's connection or its write lock was held) or just
	// before the signal, before keyless shutdown was established;
	// signal_sent says whether the SIGTERM was sent. Never success.
	pauseCauseBudgetExhausted = "budget_exhausted"
)

// pauseTerminate is the identity-checked SIGTERM keyless pause sends
// (probe.TerminateIdentity); tests replace it to observe or fail the signal.
var pauseTerminate = probe.TerminateIdentity

// keylessPauseStore is the store surface keyless pause needs besides
// PauseStore, every read and write within the caller's context and wait:
// the pause claim protocol (pauseClaimStore), the row read again before the
// signal, the published current Launch, the Launch End a verified agent exit
// records, the shared exit evidence, and the guarded row completion after
// that evidence is established. *store.Store satisfies it.
type keylessPauseStore interface {
	pauseClaimStore
	GetSpawnContext(ctx context.Context, instanceID string, maxWait time.Duration) (Spawn, error)
	GetBusySendPublishedLaunchWithin(ctx context.Context, instanceID string, maxWait time.Duration) (store.BusySendPublishedLaunch, error)
	EndBusySendLaunchWithin(ctx context.Context, instanceID, token string, maxWait time.Duration) (bool, error)
	ReadBusySendExitScopeOfWithin(ctx context.Context, instanceID, token string, maxWait time.Duration) (store.BusySendExitScope, bool, error)
	EstablishBusySendExitWithin(ctx context.Context, ev store.BusySendExitEvidence, maxWait time.Duration) (bool, error)
	MarkKeylessPauseEndedWithin(ctx context.Context, captured Spawn, launch store.BusySendPublishedLaunch, exit store.ProcessExit, maxWait time.Duration) (string, CondResult, error)
}

// keylessBudget is keyless pause's one call budget (SR-6.4): the call's own
// context bounded by the pause deadline. Every store read and write runs in
// ctx with what is left of it as its wait (store connection and write lock
// alike), so no phase starts a fresh wait of its own.
type keylessBudget struct {
	ctx      context.Context
	deadline time.Time
}

// pauseNow is the clock keyless pause's budget and polls read; tests replace
// it to cross a deadline without waiting.
var pauseNow = time.Now

// newKeylessBudget starts the budget: timeoutSeconds from now, within r.ctx.
func (r *pauseRun) newKeylessBudget() (keylessBudget, context.CancelFunc) {
	parent := r.ctx
	if parent == nil {
		parent = context.Background()
	}
	deadline := pauseNow().Add(time.Duration(r.timeoutSeconds) * time.Second)
	ctx, cancel := context.WithDeadline(parent, deadline)
	return keylessBudget{ctx: ctx, deadline: deadline}, cancel
}

// wait is what is left of the budget for one store call: a positive bound,
// so the call shares the deadline (a store call past it fails at once).
func (b keylessBudget) wait() time.Duration { return max(b.deadline.Sub(pauseNow()), time.Millisecond) }

// spent reports whether the budget's deadline has passed.
func (b keylessBudget) spent() bool { return !pauseNow().Before(b.deadline) }

// notPausable is keyless pause's refusal: ErrSpawnNotPausable with cause and
// whether the signal was sent (nil when that is not known).
func notPausable(instanceID, description, cause string, signalSent *bool) error {
	return &DetailedError{
		Err:     fmt.Errorf("%w: pause %s: %s", ErrSpawnNotPausable, instanceID, description),
		Details: NotPausableDetails{Cause: cause, SignalSent: signalSent},
	}
}

// agentUnverifiable is the refusal when the recorded agent process cannot be
// judged (not recorded, or unreadable) and nothing was signalled.
func agentUnverifiable(instanceID string) error {
	notSent := false
	return notPausable(instanceID, "its recorded agent process cannot be checked, so a keyless shutdown cannot be verified; nothing was signalled",
		pauseCauseAgentUnverifiable, &notSent)
}

// budgetExhausted is the refusal when the budget ran out at a store call or
// before the signal, keeping the signal facts.
func budgetExhausted(instanceID string, sent bool) error {
	return budgetExhaustedAfter(instanceID, &sent)
}

// budgetExhaustedAfter is budgetExhausted with the signal fact sent, nil when
// it is not known (a plain signal begun under the store's lock whose result
// was not returned, or failed with an unknown result): signal_sent is then
// omitted, never false.
func budgetExhaustedAfter(instanceID string, sent *bool) error {
	switch {
	case sent == nil:
		return notPausable(instanceID, "the pause budget ran out while the signal was being sent, so whether the agent got it is not known; inspect with get before more input or another pause",
			pauseCauseBudgetExhausted, nil)
	case !*sent:
		return notPausable(instanceID, "the pause budget ran out before keyless shutdown was established (the store did not answer in time); nothing was signalled; retry pause",
			pauseCauseBudgetExhausted, sent)
	}
	return notPausable(instanceID, "the signal was sent but the pause budget ran out before keyless shutdown was established (the store did not answer in time); inspect with get before more input, and retry pause",
		pauseCauseBudgetExhausted, sent)
}

// storeFailed maps a keyless store call's error: the caller's own
// cancellation is its bare error, as every verb returns it; the budget
// running out (the call's deadline, or a wait cut short: ErrStoreBusy) is
// budget_exhausted with the signal facts; anything else is the store's.
func (r *pauseRun) storeFailed(b keylessBudget, instanceID string, sent bool, err error) error {
	return r.storeFailedAfter(b, instanceID, &sent, err)
}

// storeFailedAfter is storeFailed with the signal fact sent, nil when it is
// not known (budgetExhaustedAfter).
func (r *pauseRun) storeFailedAfter(b keylessBudget, instanceID string, sent *bool, err error) error {
	if cerr := r.ctxErr(); cerr != nil {
		return cerr
	}
	if b.cut(err) {
		return budgetExhaustedAfter(instanceID, sent)
	}
	return err
}

// cut reports whether err is a store call the budget cut short: its deadline
// passed, or the call's wait ran out (ErrStoreBusy).
func (b keylessBudget) cut(err error) bool {
	return errors.Is(err, store.ErrStoreBusy) || b.ctx.Err() != nil || b.spent()
}

// pauseRowReader is the row read pause starts with
// (store.GetSpawnConnBounded). *store.Store satisfies it; a store without it
// is read as before, through GetSpawn.
type pauseRowReader interface {
	GetSpawnConnBounded(ctx context.Context, instanceID string) (row Spawn, queried bool, err error)
}

// readRow is pause's first row read, its wait for the store's connection
// inside the call's budget b (its deadline, and r.ctx): covered keyless pause
// and busy uncovered Codex signal pause carry what remains, never a fresh one. Once it
// holds the connection the read is GetSpawn's, unbounded by b: the store's
// busy timeout for a lock, and GetSpawn's row or error, returned as they are.
// Coverage is not known until the row is read, so a connection wait the
// budget or the caller's cancellation cuts short, no SQL run, falls back to
// the read pause made before keyless pause (GetSpawn, which waits for the
// connection as every other store call does and does not observe r.ctx). An
// uncovered waiting row keeps its legacy first read, with one busy timeout,
// and its own full wait after its keys; a row using signal shutdown read late
// is refused with nothing signalled or written (the budget spent,
// or the bare cancellation). A store without this read, or a call with no
// budget (timeoutSeconds 0, which no configured pause passes), reads through
// GetSpawn as before.
func (r *pauseRun) readRow(b keylessBudget, instanceID string) (Spawn, error) {
	reader, ok := r.s.(pauseRowReader)
	if !ok || r.timeoutSeconds <= 0 {
		return r.s.GetSpawn(instanceID)
	}
	row, queried, err := reader.GetSpawnConnBounded(b.ctx, instanceID)
	if !queried {
		return r.s.GetSpawn(instanceID)
	}
	return row, err
}

// keyless is pause of a row whose current Launch is recorded covered, row as
// pause first read it (readRow); conditional is only_if_waiting. A finished
// row (ended or missing) that no claim can answer for is the no-op success it
// was before the pause claim, with no claim check: at once through a store
// without keyless pause or with no launch token, and, after the published
// read, with no published binding or a published Launch that is not its own
// covered one. Through a store without keyless pause a live row is refused with
// nothing sent. Otherwise the caller's cancellation is its bare error and a
// spent budget is budget_exhausted, nothing signalled or written: the first
// read can return after both when another holder kept the store's connection
// past them (readRow's fallback), or a lock held its read for up to the
// store's busy timeout. Otherwise, within the rest of the same budget b
// (keylessBudget: timeoutSeconds from the start of the call, within r.ctx),
// which bounds every store read and write as well as the polls:
//
//  1. The captured Launch: the published current Launch must be covered and
//     the row's own (its launch token), else launch_changed.
//  2. Its pause claim (SR-6.5), before any finished-row shortcut: a
//     conditional pause of a live row claims it (ClaimBusySendPauseWithin);
//     any other pause checks it (CheckBusySendPauseWithin), which releases a
//     claim abandoned before its signal intent. A refusal, a standing claim's
//     answer or a recorded failure's or uncertain exit's replay is the call's
//     answer (claimAnswer), nothing signalled. With no claim standing a
//     finished row is the no-op success, and a live one goes on, conditional
//     as the claim's owner. A Launch whose agent's exit is already known (its
//     claim ended on exit evidence, or stands with paused recorded) or, for a
//     claim attempt, whose End is recorded with no claim, is no answer
//     (claimEnded): a finished row is the no-op success, and a live one goes
//     on as a plain pause that signals nothing, to finish the exclusion and
//     row end a partial pause left.
//  3. Its recorded agent process (agentProcess) judged gone needs no signal;
//     alive is signalled (signal), except after claimEnded; anything else is
//     agent_process_unverifiable.
//  4. The agent's exit is polled every pausePollInterval; still running at
//     the deadline is exit_uncertain, the signal sent, or, with no signal
//     sent (claimEnded), owned_process_running, an agent that can no
//     longer be judged then being agent_process_unverifiable at once. A
//     claim's owner records paused once the exit is verified after its
//     signal intent.
//  5. Its verified exit records the Launch End (store.EndBusySendLaunchWithin,
//     for the captured token only), which ends a claim on the Launch, then
//     the shared exit judgement of the captured Launch
//     (busySendExitForLaunch) is retried until the deadline:
//     store.EstablishBusySendExitWithin applies the exit evidence, then
//     MarkKeylessPauseEndedWithin records ended, retaining session_id. Both
//     writes recheck the captured Launch and share the budget, checked before
//     and after each write. A process still running when the budget ends,
//     also when the budget ends during a later judgement's read,
//     refuses owned_process_running, and any unverifiable process or route
//     refuses with the judgement's cause; a Launch that replaced the captured
//     one is launch_changed, with nothing judged or recorded for it.
//
// A claim's owner then records the call's disposition of any error
// (pauseOwner.settle) while its budget lasts. A store call the budget cuts
// short is budget_exhausted, with the signal facts; the caller's cancellation
// is its bare context error. It never types, reads the pane or waits for
// Messages, never extends the budget and never signals any process but the
// captured agent.
func (r *pauseRun) keyless(b keylessBudget, row Spawn, conditional bool) error {
	finished := row.State == store.StateEnded || row.State == store.StateMissing
	st, ok := r.s.(keylessPauseStore)
	// A claim can answer only for the published Launch whose token the row
	// holds, so a finished row with no launch token has none to read.
	if finished && (!ok || row.Identity.Token == "") {
		return nil
	}
	if err := r.ctxErr(); err != nil {
		return err
	}
	if b.spent() {
		return budgetExhausted(row.InstanceID, false)
	}
	if !ok || (r.procs == nil && !finished) {
		return fmt.Errorf("pause %s: this store cannot pause a covered agent keylessly; nothing was sent", row.InstanceID)
	}
	published, err := st.GetBusySendPublishedLaunchWithin(b.ctx, row.InstanceID, b.wait())
	if finished && errors.Is(err, store.ErrBusySendNoBinding) {
		return nil
	}
	if err != nil {
		return r.storeFailed(b, row.InstanceID, false, err)
	}
	if published.Coverage != store.BusySendCovered || published.LaunchToken == "" || published.LaunchToken != row.Identity.Token {
		if finished {
			// The published Launch is not the row's own covered one, so no
			// claim answers for this row.
			return nil
		}
		notSent := false
		return notPausable(row.InstanceID, "the agent's current Launch is starting or changed; nothing was signalled; retry pause once get shows it waiting",
			pauseCauseLaunchChanged, &notSent)
	}
	own, ended, answered, err := r.decideClaim(b, st, row, published, conditional && !finished)
	if answered || finished {
		return err
	}
	err = r.keylessShutdown(b, st, row, published, own, ended)
	own.settle(r, b, err)
	return err
}

// decideClaim is keyless pause's claim decision on the captured Launch:
// claim makes a conditional pause's claim attempt, and otherwise the claim is
// checked. answered says the decision is the call's answer (err, nil for
// success); otherwise no claim stands but, when claimed, the call's own
// (own), and ended says nothing may signal the Launch (claimEnded).
func (r *pauseRun) decideClaim(b keylessBudget, st keylessPauseStore, row Spawn, published store.BusySendPublishedLaunch, claim bool) (own *pauseOwner, ended, answered bool, err error) {
	req := pauseClaimRequest(b, row.InstanceID, published, "")
	decide := st.CheckBusySendPauseWithin
	if claim {
		owner, uerr := uuid.NewRandom()
		if uerr != nil {
			return nil, false, true, fmt.Errorf("pause %s: make the pause call's identity: %w; nothing was signalled", row.InstanceID, uerr)
		}
		req.OwnerCallID, decide = owner.String(), st.ClaimBusySendPauseWithin
	}
	d, err := decide(b.ctx, req, b.wait())
	if err != nil {
		return nil, false, true, r.claimFailed(b, row.InstanceID, err)
	}
	switch {
	case d.Kind == store.BusySendPauseDecisionClaimed && d.Claim != nil:
		return &pauseOwner{claims: st, ref: d.Claim.Ref}, false, false, nil
	case d.Kind == store.BusySendPauseDecisionClear:
		return nil, false, false, nil
	case claimEnded(d):
		return nil, true, false, nil
	}
	return nil, false, true, claimAnswer(row.InstanceID, d)
}

// keylessShutdown is keyless pause's steps 3 to 5 (keyless) for the captured
// Launch, own the call's claim (nil for none). ended says nothing may signal
// the Launch (claimEnded): its agent is then never signalled, and an agent
// that still runs is waited for and refused as an owned process still
// running.
func (r *pauseRun) keylessShutdown(b keylessBudget, st keylessPauseStore, row Spawn, published store.BusySendPublishedLaunch, own *pauseOwner, ended bool) error {
	sent := false
	agent := agentProcess(row, row.Identity).Identity
	switch tmux.JudgeProcess(r.procs, agent) {
	case tmux.ProcGone:
	case tmux.ProcAlive:
		if ended {
			break
		}
		var answered bool
		var err error
		if sent, answered, err = r.signal(b, st, row, published, agent, own); err != nil || answered {
			return err
		}
	default:
		return agentUnverifiable(row.InstanceID)
	}
	if err := r.awaitAgentExit(row.InstanceID, agent, b.deadline, sent); err != nil {
		return err
	}
	own.verified(r, b, pauseNow())
	if _, err := st.EndBusySendLaunchWithin(b.ctx, row.InstanceID, published.LaunchToken, b.wait()); err != nil {
		return r.storeFailed(b, row.InstanceID, sent, err)
	}
	return r.awaitLaunchExit(b, st, row, published, sent)
}

// signal revalidates the captured Launch and sends the identity-checked
// SIGTERM, checking the budget right before it: a claim's owner (own) after
// persisting its signal intent, any other pause under the store's write lock
// while no claim stands (signalPlain). It reports whether the signal was
// sent; a target already gone is (false, nil), as no signal is needed.
// answered says a claim found standing answered the call instead (err, nil
// for success), nothing signalled.
func (r *pauseRun) signal(b keylessBudget, st keylessPauseStore, row Spawn, published store.BusySendPublishedLaunch, agent tmux.ProcIdentity, own *pauseOwner) (sent, answered bool, err error) {
	notSent := false
	again, err := st.GetSpawnContext(b.ctx, row.InstanceID, b.wait())
	if err != nil {
		return false, false, r.storeFailed(b, row.InstanceID, notSent, err)
	}
	now, err := st.GetBusySendPublishedLaunchWithin(b.ctx, row.InstanceID, b.wait())
	if err != nil {
		return false, false, r.storeFailed(b, row.InstanceID, notSent, err)
	}
	if launchOf(again) != launchOf(row) || agentProcess(again, again.Identity).Identity != agent || now.LaunchID != published.LaunchID {
		return false, false, launchChangedBeforeSignal(row.InstanceID)
	}
	if err := r.ctxErr(); err != nil {
		return false, false, err
	}
	if b.spent() {
		return false, false, budgetExhausted(row.InstanceID, notSent)
	}
	terminate := func() error { return pauseTerminate(agent.PID, agent.Starttime) }
	if own == nil {
		return r.signalPlain(b, st, pauseClaimRequest(b, row.InstanceID, published, ""), terminate)
	}
	// The owner's intent is persisted right before the signal, which checks
	// the identity once more at its own boundary, keeping Darwin's gap between
	// that check and the kill as narrow as before.
	applied, err := own.persistIntent(b)
	if err != nil {
		return false, false, r.storeFailed(b, row.InstanceID, notSent, err)
	}
	if !applied {
		// The store compares the claim's deadline in whole milliseconds.
		if b.spent() || pauseNow().UnixMilli() >= b.deadline.UnixMilli() {
			return false, false, budgetExhausted(row.InstanceID, notSent)
		}
		return false, false, notPausable(row.InstanceID, "the agent's Launch changed or ended before the signal; nothing was signalled; retry pause",
			pauseCauseLaunchChanged, &notSent)
	}
	if err := r.ctxErr(); err != nil {
		return false, false, err
	}
	if b.spent() {
		return false, false, budgetExhausted(row.InstanceID, notSent)
	}
	sent, err = signalOutcome(row.InstanceID, terminate())
	return sent, false, err
}

// signalPlain sends a plain pause's signal: through the store's write lock,
// while no claim stands on req's Launch, so a claim made meanwhile is never
// signalled a second time, calling terminate at most once and never retrying
// it. A standing claim found there answers the call (claimAnswer), nothing
// signalled; one whose agent's exit is already known (claimEnded) sends
// nothing and lets the call go on as for an agent already gone; a claim
// abandoned before its signal intent is released by a separately committed
// check, in the same budget, before the signal is tried again. Once the
// signal was tried, its own fact (plainSignalFact: sent, not sent, or not
// known) is kept apart from the store's outer error, which comes first: the
// caller's cancellation is its bare error, a budget the call ran out of is
// budget_exhausted with that fact (signal_sent omitted when not known, never
// false), and any other outer error is returned as the store gave it. Only
// with no outer error does the signal's own refusal answer.
func (r *pauseRun) signalPlain(b keylessBudget, st keylessPauseStore, req store.BusySendPauseRequest, terminate func() error) (sent, answered bool, err error) {
	id := req.InstanceID
	for {
		res, err := st.SignalPlainBusySendPauseWithin(b.ctx, req, terminate, b.wait())
		switch {
		case res.Invoked:
			fact, refusal := plainSignalFact(id, res)
			if err != nil {
				return false, false, r.storeFailedAfter(b, id, fact, err)
			}
			if refusal != nil {
				return false, false, refusal
			}
			return *fact, false, nil
		case err != nil:
			return false, false, r.claimFailed(b, id, err)
		case res.Recheck:
			d, err := st.CheckBusySendPauseWithin(b.ctx, req, b.wait())
			if err != nil {
				return false, false, r.claimFailed(b, id, err)
			}
			switch {
			case d.Kind == store.BusySendPauseDecisionClear:
			case claimEnded(d):
				return false, false, nil
			default:
				return false, true, claimAnswer(id, d)
			}
			if err := r.ctxErr(); err != nil {
				return false, false, err
			}
			if b.spent() {
				return false, false, budgetExhausted(id, false)
			}
		case res.Decision != nil && claimEnded(*res.Decision):
			return false, false, nil
		case res.Decision != nil:
			return false, true, claimAnswer(id, *res.Decision)
		default:
			return false, false, fmt.Errorf("pause %s: the store neither signalled nor answered; nothing was signalled", id)
		}
	}
}

// plainSignalFact is a plain signal's own fact, apart from the store's outer
// error: whether it was sent, nil when that is not known (the callback was
// begun and its result not returned, or it failed with an unknown result),
// and the refusal its result alone would give. A nil refusal comes with a
// known fact.
func plainSignalFact(instanceID string, res store.BusySendPlainPauseSignalResult) (*bool, error) {
	if !res.Completed {
		return nil, notPausable(instanceID, "the signal was begun but its result was not returned, so whether the agent got it is not known; inspect with get before retrying",
			pauseCauseSignalUncertain, nil)
	}
	sent, refusal := signalOutcome(instanceID, res.SignalError)
	if d, ok := ErrDetails(refusal).(NotPausableDetails); ok && d.SignalSent == nil {
		return nil, refusal
	}
	return &sent, refusal
}

// signalOutcome reads the identity-checked signal's return err (probe's): nil
// is sent; a target already gone is (false, nil), as no signal is needed; an
// unverifiable target or an unsupported system is a refusal with nothing
// sent; any other failure is signal_uncertain, whether it was sent not known.
func signalOutcome(instanceID string, err error) (bool, error) {
	notSent := false
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, probe.ErrSignalTargetGone):
		return false, nil
	case errors.Is(err, probe.ErrSignalTargetUnverifiable):
		return false, notPausable(instanceID, "its recorded agent process cannot be checked at the signal, so a keyless shutdown cannot be verified; nothing was signalled",
			pauseCauseAgentUnverifiable, &notSent)
	case errors.Is(err, probe.ErrProbeUnsupported):
		return false, notPausable(instanceID, "this system cannot send an identity-checked signal; nothing was signalled",
			pauseCauseSignalUnsupported, &notSent)
	default:
		return false, notPausable(instanceID, fmt.Sprintf("the signal failed (%v), so whether the agent got it is not known; inspect with get before retrying", err),
			pauseCauseSignalUncertain, nil)
	}
}

// awaitAgentExit polls the captured agent process until it is gone, the
// deadline passes or r.ctx ends. With a signal sent, the deadline is
// exit_uncertain. With none sent (keylessShutdown's ended, which never
// signals), an agent that can no longer be
// judged is agent_process_unverifiable at once, as the first judgement
// refuses it, and one still running at the deadline is owned_process_running,
// nothing signalled either way.
func (r *pauseRun) awaitAgentExit(instanceID string, agent tmux.ProcIdentity, deadline time.Time, sent bool) error {
	for {
		if err := r.ctxErr(); err != nil {
			return err
		}
		switch tmux.JudgeProcess(r.procs, agent) {
		case tmux.ProcGone:
			return nil
		case tmux.ProcAlive:
		default:
			if !sent {
				return agentUnverifiable(instanceID)
			}
		}
		if !pauseNow().Before(deadline) {
			if !sent {
				return notPausable(instanceID, "its agent's exit is already recorded or its Launch has ended, so this pause sends it no signal, but its recorded agent process still runs, so its keyless shutdown is not established; nothing was signalled; inspect with get, and retry pause once that process has exited, or kill",
					exitCauseProcessRunning, &sent)
			}
			return notPausable(instanceID, "the signal was sent and the agent did not exit within the pause budget; its exit is uncertain, not done; inspect with get before more input",
				pauseCauseExitUncertain, &sent)
		}
		pauseSleep(min(pausePollInterval, deadline.Sub(pauseNow())))
	}
}

// awaitLaunchExit retries the shared exit judgement for the agent's Launch,
// the captured one in row and launch, now that the agent is gone,
// until it is established and applied or the deadline passes. A process
// still running is waited for, and is the refusal when the budget ends with
// it running; an unverifiable process or route refuses at once. A Launch
// that replaced the captured one (a resume or reuse after the signal) is
// never judged or changed: launch_changed, the signal sent. When the budget
// ends, the last completed judgement decides: a process still running then
// (also when the budget ends during a later judgement's read) is
// owned_process_running, so the refusal does not depend on where in a poll
// the deadline falls.
func (r *pauseRun) awaitLaunchExit(b keylessBudget, st keylessPauseStore, row Spawn, launch store.BusySendPublishedLaunch, sent bool) error {
	instanceID, token := row.InstanceID, launch.LaunchToken
	var running *busySendExitRefusal
	changed := false
	stillRunning := func(ref *busySendExitRefusal) error {
		return notPausable(instanceID, "the agent exited but "+ref.describe()+", so its keyless shutdown is not established; inspect with get, and retry pause "+ref.clears(),
			ref.cause, &sent)
	}
	for {
		if err := r.ctxErr(); err != nil {
			return err
		}
		if b.spent() {
			switch {
			case running != nil:
				return stillRunning(running)
			case changed:
				return notPausable(instanceID, "the agent exited but its Launch's owned processes kept changing, so its keyless shutdown is not established; retry pause",
					exitCauseProcessUnverifiable, &sent)
			}
			return budgetExhausted(instanceID, sent)
		}
		ev, refusal, err := busySendExitForLaunch(b.ctx, st, r.procs, instanceID, token, b.wait())
		if err != nil {
			if running != nil && r.ctxErr() == nil && b.cut(err) {
				return stillRunning(running)
			}
			return r.storeFailed(b, instanceID, sent, err)
		}
		running = nil
		switch {
		case refusal != nil && refusal.cause == exitCauseLaunchChanged:
			return notPausable(instanceID, "the agent exited but its Launch was replaced before its keyless shutdown was established; nothing more was recorded; inspect with get",
				pauseCauseLaunchChanged, &sent)
		case refusal != nil && refusal.cause == exitCauseProcessRunning:
			running = refusal
		case refusal != nil:
			// The agent is gone and the row may be finished already, where kill
			// is a no-op and sweep ends no process, so the advice names the
			// condition that clears the refusal (decision 16).
			return notPausable(instanceID, "the agent exited but "+refusal.describe()+", so its keyless shutdown is not established; inspect with get, and retry pause "+refusal.clears(),
				refusal.cause, &sent)
		case ev == nil:
			return r.endKeylessRow(b, st, row, launch, sent, false)
		default:
			// The write that establishes it is bounded by the same budget,
			// checked before it and, at the success boundary, after it.
			if err := r.ctxErr(); err != nil {
				return err
			}
			if b.spent() {
				return budgetExhausted(instanceID, sent)
			}
			applied, err := st.EstablishBusySendExitWithin(b.ctx, *ev, b.wait())
			if err != nil {
				return r.storeFailed(b, instanceID, sent, err)
			}
			if applied {
				return r.endKeylessRow(b, st, row, launch, sent, true)
			}
			changed = true
		}
		pauseSleep(min(pausePollInterval, max(b.deadline.Sub(pauseNow()), 0)))
	}
}

// endKeylessRow completes the verified pause in the same budget. The store
// rechecks the captured Launch under its write lock, so a resume after Exit
// committed is still launch_changed, never an end of the replacement.
func (r *pauseRun) endKeylessRow(b keylessBudget, st keylessPauseStore, row Spawn, launch store.BusySendPublishedLaunch, sent, recorded bool) error {
	if err := r.established(b, row.InstanceID, sent, recorded); err != nil {
		return err
	}
	exit := provenExit(agentProcess(row, row.Identity).Identity, clockNow(r.now))
	prior, res, err := st.MarkKeylessPauseEndedWithin(b.ctx, row, launch, exit, b.wait())
	if errors.Is(err, store.ErrBusySendLaunchChanged) || (err == nil && res == store.CondAbsent) {
		return notPausable(row.InstanceID, "the agent exited but its Launch was replaced before its keyless shutdown was established; nothing more was recorded; inspect with get",
			pauseCauseLaunchChanged, &sent)
	}
	if err != nil {
		return r.storeFailed(b, row.InstanceID, sent, err)
	}
	if res == store.CondApplied {
		emitMarkTick(recoveryTickTrail(recoveryTrail(b.ctx), "ad_recovery"), row.InstanceID, prior, store.StateEnded, reasonProcAbsent, nil)
	}
	return r.established(b, row.InstanceID, sent, recorded)
}

// established is keyless pause's success boundary, after its last store
// call: the caller's cancellation (its context error, wrapped) or the budget
// running out (budget_exhausted, the signal facts kept) since that call is
// reported instead of success. recorded says the call committed the exit
// evidence, which a refusal here never rolls back: the refusal says it stays
// recorded.
func (r *pauseRun) established(b keylessBudget, instanceID string, sent, recorded bool) error {
	kept := ""
	if recorded {
		kept = "; its exit evidence was recorded and stays recorded"
	}
	if err := r.ctxErr(); err != nil {
		return fmt.Errorf("pause %s: the call ended before pause could report its keyless shutdown%s: %w", instanceID, kept, err)
	}
	if b.spent() {
		return notPausable(instanceID, "the pause budget ran out before pause could report its keyless shutdown"+kept+"; inspect with get",
			pauseCauseBudgetExhausted, &sent)
	}
	return nil
}

// ctxErr is r.ctx's error, nil for no context.
func (r *pauseRun) ctxErr() error {
	if r.ctx == nil {
		return nil
	}
	return r.ctx.Err()
}
