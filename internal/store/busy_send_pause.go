package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// The pause claim (SR-6.5; M2 RoutePauseClaim, producer obligations P-C4.1
// and P-C4.5 to P-C4.7). A conditional pause (only_if_waiting) claims the
// captured current covered Launch in one durable transaction that checks
// SR-6.5's four facts against the row's raw lifecycle state and the
// conversation's delivery records, or gives a standing claim's answer. The
// standing claim fences every later issue to that Launch (readBusySendIssueFacts'
// pauseFence, refused as BusySendGuardPause). Its owning call then persists
// the signal intent before its signal and records the call's outcome, or
// releases the claim on a refusal it proved before signalling; every owner
// write compares the claim's Launch, owner and revision and advances the
// revision, so a stale continuation changes nothing. Only an explicit pause
// releases a claim abandoned before any signal intent, and only Launch End on
// exit evidence or a new Launch's publication ends one
// (endBusySendPauseClaim): no worker, timer, read or sweep releases or ends a
// claim, and elapsed time never shows that no signal was sent. A plain pause
// never claims: it gets a standing claim's answers and sends its signal under
// the write lock, only while no claim stands (SignalPlainBusySendPauseWithin).

// The causes of a refused pause decision (SR-6.5). Missing rows, changed
// Launches and store failures are errors, never causes.
const (
	// BusySendPauseNotWaiting: the row's raw lifecycle state is not waiting
	// (ObservedState holds it). It wins over BusySendPauseTurnPending.
	BusySendPauseNotWaiting = "not_waiting"
	// BusySendPauseTurnPending: the row is waiting, but AD's issued input has
	// no evidence the agent took it (awaiting_turn_start), a prompt's emission
	// intent has no completed output, or a force is pending.
	BusySendPauseTurnPending = "turn_pending"
	// BusySendPauseInProgress: a claim stands whose owning call is still
	// within its deadline.
	BusySendPauseInProgress = "pause_in_progress"
	// BusySendPauseUncertain: a claim stands past its owner's deadline with a
	// signal intent and no recorded outcome; it and its fence stay.
	BusySendPauseUncertain = "pause_uncertain"
)

// BusySendPauseDecisionKind is how a pause decision answered.
type BusySendPauseDecisionKind string

const (
	// BusySendPauseDecisionClaimed: a new standing claim, owned by the
	// request's call (Claim, whose Ref is the owner's only authority).
	BusySendPauseDecisionClaimed BusySendPauseDecisionKind = "claimed"
	// BusySendPauseDecisionClear: no claim stands on the captured Launch (a
	// plain pause's Check only). It lets a plain pause go on to
	// SignalPlainBusySendPauseWithin, and never authorizes a signal itself.
	BusySendPauseDecisionClear BusySendPauseDecisionKind = "clear"
	// BusySendPauseDecisionRefused: Cause says why; nothing is claimed.
	BusySendPauseDecisionRefused BusySendPauseDecisionKind = "refused"
	// BusySendPauseDecisionRecorded: a claim stands whose owning call recorded
	// its outcome (Claim's CallOutcome and CallCause).
	BusySendPauseDecisionRecorded BusySendPauseDecisionKind = "recorded"
	// BusySendPauseDecisionEnded: no claim stands on the captured current
	// Launch, and its claim ended (Claim) or, for a conditional pause, its
	// End is recorded with no claim (Claim nil), since no claim is made on an
	// ended Launch. The pause behaves as SR-6.4's for an ended row; this is
	// the Launch's disposition, never a call outcome.
	BusySendPauseDecisionEnded BusySendPauseDecisionKind = "ended"
)

// BusySendPauseRequest is one pause call's captured target and budget.
type BusySendPauseRequest struct {
	// InstanceID, LaunchID and LaunchToken are the published current covered
	// Launch the call captured (GetBusySendPublishedLaunchWithin). Every
	// decision reads it again under the write lock: a missing row is
	// ErrSpawnNotFound, a row with no consistent binding ErrBusySendNoBinding,
	// and another Launch, an uncovered one, or a row no longer holding its
	// token ErrBusySendLaunchChanged, each with nothing written.
	InstanceID, LaunchID, LaunchToken string
	// OwnerCallID is the conditional call's AD-made identity, a new claim's
	// owner. Check and the plain signal use none.
	OwnerCallID string
	// Deadline is the call's original finite deadline (its pause budget's
	// end), never renewed: a new claim records it, and the plain signal is
	// admitted only before it.
	Deadline time.Time
	// Now is the caller's clock, read inside the transaction once it holds
	// the write lock (nil: the wall clock).
	Now func() time.Time
}

// busySendClockMillis reads now, nil being the wall clock, in milliseconds.
func busySendClockMillis(now func() time.Time) int64 {
	if now == nil {
		return storeNowMillis()
	}
	return now().UnixMilli()
}

func (r BusySendPauseRequest) target() error {
	if r.InstanceID == "" || r.LaunchID == "" || r.LaunchToken == "" {
		return fmt.Errorf("store: busy-send pause needs the captured instance, Launch and launch token")
	}
	return nil
}

// BusySendPauseRef is an owning call's retained authority over its claim:
// the claim, its instance, Launch and owner, and the revision the owner last
// wrote or was granted. Every owner write compares all of it. It is never a
// request to fetch fresh authority: a snapshot of another revision never
// authorizes adopting it.
type BusySendPauseRef struct {
	ClaimID, InstanceID, LaunchID, OwnerCallID string
	Revision                                   int64
}

// BusySendPauseClaim is a durable claim snapshot. A zero time is NULL: no
// signal intent, no recorded outcome, or a claim still standing.
type BusySendPauseClaim struct {
	Ref            BusySendPauseRef
	ClaimedAt      time.Time
	Deadline       time.Time
	SignalKind     BusySendPauseSignal
	SignalIntentAt time.Time
	// CallOutcome is "" when none is recorded; ClosedBy is "" while standing.
	CallOutcome   BusySendPauseCallOutcome
	CallCause     string
	CallOutcomeAt time.Time
	State         BusySendPauseClaimState
	ClosedBy      BusySendPauseClosure
	ClosedAt      time.Time
}

// BusySendPauseDecision is a pause decision's answer, returned only after its
// transaction committed. Only Claimed grants authority (Claim.Ref). Claim is
// the claim the answer is about: the new one, the standing one answering, the
// Launch's ended one, or, for Clear and a fresh refusal, the abandoned claim
// this transaction released (nil when none).
type BusySendPauseDecision struct {
	Kind  BusySendPauseDecisionKind
	Cause string
	// ObservedState is the raw state, for BusySendPauseNotWaiting only.
	ObservedState string
	Claim         *BusySendPauseClaim
}

// BusySendPauseWriteRequest is an owner write that carries no value: the
// signal intent.
type BusySendPauseWriteRequest struct {
	Ref BusySendPauseRef
	Now func() time.Time
}

// BusySendPauseOutcomeRequest records the owning call's outcome after its
// signal intent: paused, exit_uncertain or failed, with its structured cause
// ("" for none). VerifiedExitAt is when the call verified its owned process's
// exit, required for paused and within the claim's deadline.
type BusySendPauseOutcomeRequest struct {
	Ref            BusySendPauseRef
	Now            func() time.Time
	Outcome        BusySendPauseCallOutcome
	Cause          string
	VerifiedExitAt time.Time
}

// BusySendPauseRefusalRequest records the owning call's refusal before its
// signal, with its structured cause ("" for none). Precondition: the owning
// live call has positive proof that no signal was sent, such as the
// identity-checked signal's explicit unverifiable or unsupported return after
// the intent. A deadline alone never satisfies it.
type BusySendPauseRefusalRequest struct {
	Ref   BusySendPauseRef
	Now   func() time.Time
	Cause string
}

// BusySendPauseWriteResult is an owner write's result, returned only after
// its transaction committed. Applied false is a no-op: the claim's snapshot
// when it is retained, never authority. NextRef is set only by a successful
// signal intent, for its continuing owner; an outcome or release finishes
// the owner's authority.
type BusySendPauseWriteResult struct {
	Applied bool
	Claim   *BusySendPauseClaim
	NextRef *BusySendPauseRef
}

// BusySendPlainPauseSignalResult is the plain signal's result. Invoked says
// the signal callback was called, Completed that it returned, and
// SignalError is what it returned. These facts survive any outer error: once
// Invoked, the outer error never means that nothing was sent, and Completed
// false means the signal's result is unknown. Decision is a standing or ended
// claim's answer, which suppressed the callback. Recheck is set, with the
// claim's snapshot in Decision (its Kind empty, no answer), for a standing
// claim abandoned before its signal intent: only a separately committed
// CheckBusySendPauseWithin may release it before the signal is tried again.
type BusySendPlainPauseSignalResult struct {
	Invoked     bool
	Completed   bool
	SignalError error
	Decision    *BusySendPauseDecision
	Recheck     bool
}

const busySendPauseClaimColumns = `claim_id, instance_id, launch_id, owner_call_id, revision, claimed_at_ms, deadline_ms,
    signal_kind, signal_intent_at_ms, call_outcome, call_cause, call_outcome_at_ms, state, closed_by, closed_at_ms`

func scanBusySendPauseClaim(row *sql.Row) (BusySendPauseClaim, error) {
	var c BusySendPauseClaim
	var claimed, deadline int64
	var kind, state string
	var intent, outcomeAt, closedAt sql.NullInt64
	var outcome, cause, closedBy sql.NullString
	if err := row.Scan(&c.Ref.ClaimID, &c.Ref.InstanceID, &c.Ref.LaunchID, &c.Ref.OwnerCallID, &c.Ref.Revision, &claimed, &deadline,
		&kind, &intent, &outcome, &cause, &outcomeAt, &state, &closedBy, &closedAt); err != nil {
		return c, err
	}
	c.ClaimedAt, c.Deadline = time.UnixMilli(claimed).UTC(), time.UnixMilli(deadline).UTC()
	c.SignalKind, c.SignalIntentAt = BusySendPauseSignal(kind), millisTime(intent)
	c.CallOutcome, c.CallCause, c.CallOutcomeAt = BusySendPauseCallOutcome(outcome.String), cause.String, millisTime(outcomeAt)
	c.State, c.ClosedBy, c.ClosedAt = BusySendPauseClaimState(state), BusySendPauseClosure(closedBy.String), millisTime(closedAt)
	return c, nil
}

// readBusySendPauseClaim reads claimID's snapshot; found is false for none.
func readBusySendPauseClaim(ctx context.Context, q busySendLaunchSQL, storeID, claimID string) (BusySendPauseClaim, bool, error) {
	c, err := scanBusySendPauseClaim(q.QueryRowContext(ctx, `SELECT `+busySendPauseClaimColumns+`
	    FROM busy_send_pause_claims WHERE claim_id = ? AND store_id = ?`, claimID, storeID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// readBusySendPauseClaimOn reads the claim that answers for launchID: its
// standing claim, else its ended one. A Launch has at most one of each (the
// partial unique index; no claim is made on an ended Launch), and its
// released claims never answer.
func readBusySendPauseClaimOn(ctx context.Context, q busySendLaunchSQL, storeID, instanceID, launchID string) (BusySendPauseClaim, bool, error) {
	c, err := scanBusySendPauseClaim(q.QueryRowContext(ctx, `SELECT `+busySendPauseClaimColumns+`
	    FROM busy_send_pause_claims WHERE instance_id = ? AND launch_id = ? AND store_id = ? AND state IN ('standing', 'ended')
	    ORDER BY state = 'standing' DESC LIMIT 1`, instanceID, launchID, storeID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

// readBusySendPauseTarget reads req's captured target under the caller's
// write lock: the published current Launch (readExaminedBusySendLaunch, its
// raw state included), which must be the captured covered Launch with its
// token, and the row must still hold that token, so a staged candidate's
// row or a later Launch is ErrBusySendLaunchChanged, never a decision.
func readBusySendPauseTarget(ctx context.Context, q busySendLaunchSQL, req BusySendPauseRequest) (busySendPublishedRow, error) {
	p, err := readExaminedBusySendLaunch(ctx, q, req.InstanceID)
	if err != nil {
		return p, err
	}
	var rowToken sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT launch_token FROM spawns WHERE instance_id = ?`, req.InstanceID).Scan(&rowToken); err != nil {
		return p, err
	}
	if p.launchCoverage != BusySendCovered || p.launch != req.LaunchID || p.token == "" || p.token != req.LaunchToken ||
		rowToken.String != p.token {
		return p, fmt.Errorf("%w: %s's published current covered Launch is not the captured %s", ErrBusySendLaunchChanged,
			req.InstanceID, req.LaunchID)
	}
	return p, nil
}

// busySendPauseRepeat is the pure answer of a standing claim c at now, for
// either option value (M2's RepeatDecision): its recorded outcome; else
// pause_in_progress while its owner's deadline has not passed, intent or
// not; else pause_uncertain with a signal intent. Otherwise abandoned is
// true: its owner passed its deadline with neither an intent nor an outcome,
// so it was abandoned before any signal, and only an explicit pause's
// committed release (releaseAbandonedBusySendPause) may act on that.
func busySendPauseRepeat(c BusySendPauseClaim, now int64) (answer BusySendPauseDecision, abandoned bool) {
	switch {
	case c.CallOutcome != "":
		return BusySendPauseDecision{Kind: BusySendPauseDecisionRecorded, Claim: &c}, false
	case now <= c.Deadline.UnixMilli():
		return BusySendPauseDecision{Kind: BusySendPauseDecisionRefused, Cause: BusySendPauseInProgress, Claim: &c}, false
	case !c.SignalIntentAt.IsZero():
		return BusySendPauseDecision{Kind: BusySendPauseDecisionRefused, Cause: BusySendPauseUncertain, Claim: &c}, false
	}
	return BusySendPauseDecision{}, true
}

// releaseAbandonedBusySendPause releases the abandoned standing claim c
// (abandoned_before_signal) at now in the caller's transaction, comparing
// every captured key and the facts that made it abandoned, and keeping its
// history. Its revision advances, so its owner's ref is stale for good.
func releaseAbandonedBusySendPause(ctx context.Context, q busySendLaunchSQL, storeID string, c BusySendPauseClaim, now int64) (BusySendPauseClaim, error) {
	if err := busySendChange(ctx, q, `UPDATE busy_send_pause_claims SET state = 'released', closed_by = 'abandoned_before_signal',
	        closed_at_ms = ?, revision = revision + 1
	    WHERE claim_id = ? AND store_id = ? AND launch_id = ? AND owner_call_id = ? AND revision = ? AND state = 'standing'
	        AND signal_intent_at_ms IS NULL AND call_outcome IS NULL AND deadline_ms < ?`,
		now, c.Ref.ClaimID, storeID, c.Ref.LaunchID, c.Ref.OwnerCallID, c.Ref.Revision, now); err != nil {
		return c, err
	}
	released, _, err := readBusySendPauseClaim(ctx, q, storeID, c.Ref.ClaimID)
	return released, err
}

// readBusySendPauseRefusal is SR-6.5's fresh check of p, the captured current
// Launch, in the claim's transaction (P-C4.1): BusySendPauseNotWaiting when
// the row's raw lifecycle state, never its settle-time display, is not
// waiting; BusySendPauseTurnPending when the Launch has an awaiting obligation
// (readBusySendAwaiting), a prompt of any of its hand-overs, closed ones
// included, has an emission intent without completed output (transport
// written), or the conversation has a pending force (E5's pending: an issued
// force prompt's admission still unknown); "" when every check passes. The
// output check is literal: neither admission or negative evidence, nor
// partial or uncertain output, stands in for completed output.
func readBusySendPauseRefusal(ctx context.Context, q busySendLaunchSQL, p busySendPublishedRow) (string, error) {
	if p.state != StateWaiting {
		return BusySendPauseNotWaiting, nil
	}
	awaiting, err := readBusySendAwaiting(ctx, q, p.conversation, p.launch)
	if err != nil {
		return "", err
	}
	var unwritten, forcePending bool
	if err := q.QueryRowContext(ctx, `SELECT
	        EXISTS(SELECT 1 FROM busy_send_prompts pr JOIN busy_send_handovers h ON h.handover_id = pr.handover_id
	            WHERE h.conversation_id = ? AND h.launch_id = ? AND pr.emission IN ('intent', 'issued') AND pr.transport IS NOT 'written'),
	        EXISTS(SELECT 1 FROM busy_send_forces WHERE conversation_id = ? AND outcome = 'pending')`,
		p.conversation, p.launch, p.conversation).Scan(&unwritten, &forcePending); err != nil {
		return "", err
	}
	if awaiting > 0 || unwritten || forcePending {
		return BusySendPauseTurnPending, nil
	}
	return "", nil
}

// errBusySendPauseSpent is a pause operation refused because the call's
// original deadline has passed: its budget is spent, nothing was written.
func errBusySendPauseSpent(before string) error {
	return fmt.Errorf("%w: the pause call's deadline passed before %s: %w", ErrStoreBusy, before, context.DeadlineExceeded)
}

// decideBusySendPause is the decision transaction Claim and Check share, in
// the caller's write transaction. It validates the captured target, then
// answers from the Launch's own claim: an ended claim answers ended; a
// standing one answers by busySendPauseRepeat, or, when abandoned, is released
// here. A plain pause (conditional false) then answers clear. A conditional
// one makes its fresh attempt: never on an ended Launch (ended); refused,
// writing nothing more, when a check fails; otherwise a new standing claim.
func decideBusySendPause(ctx context.Context, q busySendLaunchSQL, storeID string, req BusySendPauseRequest, conditional bool) (BusySendPauseDecision, error) {
	now := busySendClockMillis(req.Now)
	p, err := readBusySendPauseTarget(ctx, q, req)
	if err != nil {
		return BusySendPauseDecision{}, err
	}
	c, found, err := readBusySendPauseClaimOn(ctx, q, storeID, req.InstanceID, p.launch)
	if err != nil {
		return BusySendPauseDecision{}, err
	}
	var released *BusySendPauseClaim
	switch {
	case found && c.State == BusySendPauseClaimEnded:
		return BusySendPauseDecision{Kind: BusySendPauseDecisionEnded, Claim: &c}, nil
	case found:
		answer, abandoned := busySendPauseRepeat(c, now)
		if !abandoned {
			return answer, nil
		}
		r, err := releaseAbandonedBusySendPause(ctx, q, storeID, c, now)
		if err != nil {
			return BusySendPauseDecision{}, err
		}
		released = &r
	}
	if !conditional {
		return BusySendPauseDecision{Kind: BusySendPauseDecisionClear, Claim: released}, nil
	}
	if p.launchEnded {
		return BusySendPauseDecision{Kind: BusySendPauseDecisionEnded}, nil
	}
	cause, err := readBusySendPauseRefusal(ctx, q, p)
	if err != nil {
		return BusySendPauseDecision{}, err
	}
	if cause != "" {
		out := BusySendPauseDecision{Kind: BusySendPauseDecisionRefused, Cause: cause, Claim: released}
		if cause == BusySendPauseNotWaiting {
			out.ObservedState = p.state
		}
		return out, nil
	}
	deadline := req.Deadline.UnixMilli()
	if now >= deadline {
		return BusySendPauseDecision{}, errBusySendPauseSpent("its claim")
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return BusySendPauseDecision{}, err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO busy_send_pause_claims
	        (claim_id, store_id, instance_id, launch_id, owner_call_id, revision, claimed_at_ms, deadline_ms, signal_kind, state)
	    VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, 'standing')`,
		id.String(), storeID, req.InstanceID, p.launch, req.OwnerCallID, now, deadline, string(BusySendSignalSIGTERM)); err != nil {
		return BusySendPauseDecision{}, err
	}
	claimed, _, err := readBusySendPauseClaim(ctx, q, storeID, id.String())
	if err != nil {
		return BusySendPauseDecision{}, err
	}
	return BusySendPauseDecision{Kind: BusySendPauseDecisionClaimed, Claim: &claimed}, nil
}

// ClaimBusySendPauseWithin is a conditional pause's claim (SR-6.5; M2's
// PauseClaimBy, PauseRefuse and PauseRepeat), one durable transaction on a
// WAL synchronous=FULL connection within ctx, all its waits bounded by
// maxWait, the call's remaining budget (durableWriteWithin). It validates the
// captured target (BusySendPauseRequest), then, on that Launch:
//   - a standing claim answers, whatever the option: its recorded outcome
//     (recorded), pause_in_progress within its owner's deadline,
//     pause_uncertain past it with a signal intent, or, abandoned before any
//     intent, it is released (abandoned_before_signal) and this call makes a
//     fresh attempt in the same transaction;
//   - an ended claim, or an ended Launch, answers ended, and no claim is
//     ever made on an ended Launch;
//   - a fresh attempt refuses not_waiting (with the raw state) or
//     turn_pending (readBusySendPauseRefusal), writing nothing but an
//     abandonment release this transaction made; or records a standing claim
//     owned by req.OwnerCallID with req.Deadline, signal kind sigterm and
//     revision 1 (claimed).
//
// Concurrent callers make at most one standing claim. Held Messages, their
// order, options and reservations, and every row, custody, wait and counter
// are unchanged. The clock is read once the write lock is held. Results are
// returned only after the commit is verified: an unconfirmed commit is an
// error CommitUnconfirmed reports and grants nothing.
func (s *Store) ClaimBusySendPauseWithin(ctx context.Context, req BusySendPauseRequest, maxWait time.Duration) (BusySendPauseDecision, error) {
	if err := req.target(); err != nil {
		return BusySendPauseDecision{}, err
	}
	if req.OwnerCallID == "" || req.Deadline.IsZero() {
		return BusySendPauseDecision{}, fmt.Errorf("store: busy-send pause claim needs its owning call and the call's deadline")
	}
	var out BusySendPauseDecision
	err := s.durableWriteWithin(ctx, maxWait, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		out, err = decideBusySendPause(ctx, conn, s.StoreID(), req, true)
		return err
	})
	if err != nil {
		return BusySendPauseDecision{}, fmt.Errorf("store: claim busy-send pause: %w", err)
	}
	return out, nil
}

// CheckBusySendPauseWithin is a plain pause's claim answer (Main Agent
// decisions 1 and 4), in the same kind of transaction as
// ClaimBusySendPauseWithin: a standing claim on the captured Launch answers
// as it does there, an ended one answers ended, and a claim abandoned before
// any intent is released (abandoned_before_signal) and clear is returned.
// With no claim it is clear, with no eligibility check. It never claims,
// takes no owner and renews no deadline. Clear only lets the plain pause go on
// to SignalPlainBusySendPauseWithin.
func (s *Store) CheckBusySendPauseWithin(ctx context.Context, req BusySendPauseRequest, maxWait time.Duration) (BusySendPauseDecision, error) {
	if err := req.target(); err != nil {
		return BusySendPauseDecision{}, err
	}
	var out BusySendPauseDecision
	err := s.durableWriteWithin(ctx, maxWait, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		out, err = decideBusySendPause(ctx, conn, s.StoreID(), req, false)
		return err
	})
	if err != nil {
		return BusySendPauseDecision{}, fmt.Errorf("store: check busy-send pause: %w", err)
	}
	return out, nil
}

// busySendSignalFacts are the plain signal's invocation facts, shared between
// the bounded operation that may invoke the callback and its caller, which
// may stop waiting for that operation first.
type busySendSignalFacts struct {
	mu        sync.Mutex
	invoked   bool
	completed bool
	err       error
}

// SignalPlainBusySendPauseWithin is a covered plain pause's signal (Main Agent
// decision 4; M2's PauseSignalBy(FALSE), whose FenceOpen holds at the signal
// step). In one BEGIN IMMEDIATE transaction within ctx, its waits and work
// bounded by maxWait, the call's remaining budget, it validates the captured
// target, and reads the Launch's claim:
//   - a standing claim's answer (busySendPauseRepeat) or an ended claim's
//     (ended) is returned in Decision, and signal is not called;
//   - a standing claim abandoned before any intent gives Recheck, with no
//     call: it is released only by a separately committed
//     CheckBusySendPauseWithin, after which this is called again;
//   - with no claim, it checks ctx and req.Deadline once more and calls
//     signal once, while still holding the write lock, so no claim can be
//     made between the check and the signal.
//
// It writes nothing: no claim, intent, outcome, revision, row, custody or
// wait; the transaction only holds the lock and is rolled back. signal must
// make one identity-checked signal and return, without using the store,
// waiting for exit or retrying. Its facts (Invoked, Completed, SignalError)
// are returned with any outer error: the callback is admitted under the
// bounded operation's guard, so a caller that stopped waiting before it was
// admitted got a busy error and it is never called later, while one that
// stopped waiting after it gets an error CommitUnconfirmed reports, never
// ErrStoreBusy, with Invoked true. The signal is never retried.
func (s *Store) SignalPlainBusySendPauseWithin(ctx context.Context, req BusySendPauseRequest, signal func() error, maxWait time.Duration) (BusySendPlainPauseSignalResult, error) {
	if err := req.target(); err != nil {
		return BusySendPlainPauseSignalResult{}, err
	}
	if signal == nil || req.Deadline.IsZero() {
		return BusySendPlainPauseSignalResult{}, fmt.Errorf("store: plain busy-send pause signal needs its signal and the call's deadline")
	}
	facts := &busySendSignalFacts{}
	var decision *BusySendPauseDecision
	var recheck bool
	opCtx, cancel := s.WithContext(ctx).operationContext(maxWait)
	defer cancel()
	err := s.withConnContext(opCtx, maxWait, func(ctx context.Context, conn *sql.Conn, busyMs int, cleanup *connectionCleanup) error {
		if err := beginWithin(ctx, conn, busyMs); err != nil {
			if isBusy(err) || ctx.Err() != nil {
				return fmt.Errorf("%w: the write lock was not free within %d ms: %w", ErrStoreBusy, busyMs, err)
			}
			return fmt.Errorf("store: begin: %w", err)
		}
		cleanup.rollback = true
		d, again, admitted, err := s.plainBusySendPauseDecision(ctx, conn, req)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("%w: before the signal: %w", ErrStoreBusy, err)
			}
			return err
		}
		if !admitted {
			decision, recheck = d, again
			return nil
		}
		if err := admitBusySendSignal(ctx, cleanup.operation, facts); err != nil {
			return err
		}
		signalErr := signal()
		facts.mu.Lock()
		facts.completed, facts.err = true, signalErr
		facts.mu.Unlock()
		return nil
	})
	facts.mu.Lock()
	out := BusySendPlainPauseSignalResult{Invoked: facts.invoked, Completed: facts.completed, SignalError: facts.err}
	facts.mu.Unlock()
	if err != nil {
		return out, fmt.Errorf("store: plain busy-send pause signal: %w", err)
	}
	out.Decision, out.Recheck = decision, recheck
	return out, nil
}

// plainBusySendPauseDecision is the plain signal's read under its write
// lock: a claim answer (Decision), a Recheck for an abandoned claim, or
// admitted, when no claim stands and the call's deadline has not passed.
func (s *Store) plainBusySendPauseDecision(ctx context.Context, conn *sql.Conn, req BusySendPauseRequest) (*BusySendPauseDecision, bool, bool, error) {
	p, err := readBusySendPauseTarget(ctx, conn, req)
	if err != nil {
		return nil, false, false, err
	}
	c, found, err := readBusySendPauseClaimOn(ctx, conn, s.StoreID(), req.InstanceID, p.launch)
	if err != nil {
		return nil, false, false, err
	}
	switch {
	case found && c.State == BusySendPauseClaimEnded:
		return &BusySendPauseDecision{Kind: BusySendPauseDecisionEnded, Claim: &c}, false, false, nil
	case found:
		answer, abandoned := busySendPauseRepeat(c, busySendClockMillis(req.Now))
		if abandoned {
			return &BusySendPauseDecision{Claim: &c}, true, false, nil
		}
		return &answer, false, false, nil
	}
	if busySendClockMillis(req.Now) >= req.Deadline.UnixMilli() {
		return nil, false, false, errBusySendPauseSpent("the signal")
	}
	return nil, false, true, nil
}

// admitBusySendSignal admits the plain signal's one callback: never once its
// bounded operation's caller has stopped waiting (op.abandoned) or its context
// ended. Admission is recorded in facts under the operation's own lock, so a
// caller that stops waiting afterwards finds Invoked set, and its abandonment
// is reported as unconfirmed (commitStarted), never as a busy refusal before
// any signal (runConnectionOperation).
func admitBusySendSignal(ctx context.Context, op *connectionOperation, facts *busySendSignalFacts) error {
	if op != nil {
		op.mu.Lock()
		defer op.mu.Unlock()
		if op.abandoned {
			return operationBusy(context.DeadlineExceeded)
		}
	}
	if err := ctx.Err(); err != nil {
		return operationBusy(err)
	}
	if op != nil {
		op.commitStarted = true
	}
	facts.mu.Lock()
	facts.invoked = true
	facts.mu.Unlock()
	return nil
}

// writeBusySendPauseOwner is one owner write of ref's claim at now in the
// caller's transaction: when ref is the claim's current authority (every key
// and its revision), the claim stands with no recorded outcome and ready
// holds, it applies set, comparing the same facts, and advances the revision
// once. Otherwise nothing is written: Applied false, with the claim's
// snapshot when it is retained. A ready error rolls the transaction back.
func writeBusySendPauseOwner(ctx context.Context, q busySendLaunchSQL, storeID string, ref BusySendPauseRef,
	ready func(BusySendPauseClaim) (bool, error), set string, args ...any) (BusySendPauseWriteResult, error) {
	c, found, err := readBusySendPauseClaim(ctx, q, storeID, ref.ClaimID)
	if err != nil || !found {
		return BusySendPauseWriteResult{}, err
	}
	if c.Ref != ref || c.State != BusySendPauseClaimStanding || c.CallOutcome != "" {
		return BusySendPauseWriteResult{Claim: &c}, nil
	}
	if ok, err := ready(c); err != nil || !ok {
		return BusySendPauseWriteResult{Claim: &c}, err
	}
	if err := busySendChange(ctx, q, `UPDATE busy_send_pause_claims SET `+set+`, revision = revision + 1
	    WHERE claim_id = ? AND store_id = ? AND instance_id = ? AND launch_id = ? AND owner_call_id = ? AND revision = ?
	        AND state = 'standing' AND call_outcome IS NULL`,
		append(args, ref.ClaimID, storeID, ref.InstanceID, ref.LaunchID, ref.OwnerCallID, ref.Revision)...); err != nil {
		return BusySendPauseWriteResult{}, err
	}
	if c, _, err = readBusySendPauseClaim(ctx, q, storeID, ref.ClaimID); err != nil {
		return BusySendPauseWriteResult{}, err
	}
	return BusySendPauseWriteResult{Applied: true, Claim: &c}, nil
}

// ownerBusySendPauseWrite runs one owner write in a durable transaction
// within ctx, its waits bounded by maxWait, and returns its result only after
// the commit is verified.
func (s *Store) ownerBusySendPauseWrite(ctx context.Context, maxWait time.Duration, what string,
	write func(ctx context.Context, conn *sql.Conn) (BusySendPauseWriteResult, error)) (BusySendPauseWriteResult, error) {
	var out BusySendPauseWriteResult
	err := s.durableWriteWithin(ctx, maxWait, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		out, err = write(ctx, conn)
		return err
	})
	if err != nil {
		return BusySendPauseWriteResult{}, fmt.Errorf("store: %s: %w", what, err)
	}
	return out, nil
}

// PersistBusySendPauseSignalWithin persists the owner's signal intent before
// its signal (SR-6.5; M2's PausePersistIntentBy) in one durable transaction:
// only while ref is current, no intent or outcome is recorded, the call's
// deadline has not passed and the claim's Launch is still the published
// current covered Launch, not ended, held by the row's token. It neither
// checks waiting again nor signals. Applied returns NextRef, the owner's
// authority to signal once; anything else is Applied false and no signal.
func (s *Store) PersistBusySendPauseSignalWithin(ctx context.Context, req BusySendPauseWriteRequest, maxWait time.Duration) (BusySendPauseWriteResult, error) {
	return s.ownerBusySendPauseWrite(ctx, maxWait, "persist busy-send pause signal intent", func(ctx context.Context, conn *sql.Conn) (BusySendPauseWriteResult, error) {
		now := busySendClockMillis(req.Now)
		ready := func(c BusySendPauseClaim) (bool, error) {
			if !c.SignalIntentAt.IsZero() || now >= c.Deadline.UnixMilli() {
				return false, nil
			}
			return busySendPauseLaunchCurrent(ctx, conn, c.Ref.InstanceID, c.Ref.LaunchID)
		}
		out, err := writeBusySendPauseOwner(ctx, conn, s.StoreID(), req.Ref, ready, `signal_intent_at_ms = ?`, now)
		if err == nil && out.Applied {
			next := out.Claim.Ref
			out.NextRef = &next
		}
		return out, err
	})
}

// busySendPauseLaunchCurrent reports whether launchID is instanceID's
// published current covered Launch, its End not recorded and its token the
// row's. A missing row or binding is false.
func busySendPauseLaunchCurrent(ctx context.Context, q busySendLaunchSQL, instanceID, launchID string) (bool, error) {
	p, err := readExaminedBusySendLaunch(ctx, q, instanceID)
	if errors.Is(err, ErrSpawnNotFound) || errors.Is(err, ErrBusySendNoBinding) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var rowToken sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT launch_token FROM spawns WHERE instance_id = ?`, instanceID).Scan(&rowToken); err != nil {
		return false, err
	}
	return p.launch == launchID && p.launchCoverage == BusySendCovered && !p.launchEnded && p.token != "" &&
		rowToken.String == p.token, nil
}

// RecordBusySendPauseOutcomeWithin records the owning call's outcome after
// its signal intent (paused, exit_uncertain or failed, with its cause) in one
// durable transaction, while ref is current and no outcome is recorded; the
// fence stays. paused needs VerifiedExitAt within the claim's deadline; the
// store probes no process. A claim a lifecycle transition ended, or any
// other stale ref, is Applied false with its snapshot: lost authority, never
// to be retried with a fresher ref. It returns no NextRef.
func (s *Store) RecordBusySendPauseOutcomeWithin(ctx context.Context, req BusySendPauseOutcomeRequest, maxWait time.Duration) (BusySendPauseWriteResult, error) {
	switch req.Outcome {
	case BusySendPausePaused:
		if req.VerifiedExitAt.IsZero() {
			return BusySendPauseWriteResult{}, fmt.Errorf("store: busy-send pause outcome paused needs the verified exit time")
		}
	case BusySendPauseExitUncertain, BusySendPauseFailed:
	default:
		return BusySendPauseWriteResult{}, fmt.Errorf("store: busy-send pause outcome %q is not recorded after a signal intent", req.Outcome)
	}
	return s.ownerBusySendPauseWrite(ctx, maxWait, "record busy-send pause outcome", func(ctx context.Context, conn *sql.Conn) (BusySendPauseWriteResult, error) {
		now := busySendClockMillis(req.Now)
		ready := func(c BusySendPauseClaim) (bool, error) {
			if c.SignalIntentAt.IsZero() {
				return false, nil
			}
			if req.Outcome == BusySendPausePaused && req.VerifiedExitAt.UnixMilli() > c.Deadline.UnixMilli() {
				return false, fmt.Errorf("store: busy-send pause outcome paused needs its exit verified within the claim's deadline")
			}
			return true, nil
		}
		return writeBusySendPauseOwner(ctx, conn, s.StoreID(), req.Ref, ready,
			`call_outcome = ?, call_cause = ?, call_outcome_at_ms = ?`, string(req.Outcome), nullableStringArg(req.Cause), now)
	})
}

// ReleaseBusySendPauseBeforeSignalWithin records the owning call's refusal
// before its signal (M2's PausePreSignalRefuseBy) in one durable
// transaction, while ref is current and no outcome is recorded: outcome
// refused with its cause, and the claim released (refused_before_signal) in
// the same write, keeping any signal intent. Precondition: the owner has
// positive proof no signal was sent (BusySendPauseRefusalRequest); a
// deadline or an unknown signal result never permits it. It returns no
// NextRef.
func (s *Store) ReleaseBusySendPauseBeforeSignalWithin(ctx context.Context, req BusySendPauseRefusalRequest, maxWait time.Duration) (BusySendPauseWriteResult, error) {
	return s.ownerBusySendPauseWrite(ctx, maxWait, "release busy-send pause before its signal", func(ctx context.Context, conn *sql.Conn) (BusySendPauseWriteResult, error) {
		now := busySendClockMillis(req.Now)
		ready := func(BusySendPauseClaim) (bool, error) { return true, nil }
		return writeBusySendPauseOwner(ctx, conn, s.StoreID(), req.Ref, ready,
			`call_outcome = 'refused', call_cause = ?, call_outcome_at_ms = ?, state = 'released',
	        closed_by = 'refused_before_signal', closed_at_ms = ?`, nullableStringArg(req.Cause), now, now)
	})
}

// endBusySendPauseClaim ends launchID's standing pause claim, if it has one,
// by closure at now, in the caller's lifecycle transaction, and only once that
// Launch's End is recorded (SR-6.5; M2's EndStep, EndClaimOnEvidence,
// ResumeStep and ReusePublishStep). Its callers are exactly the transitions
// SR-6.5 names: positive evidence that the Launch's recorded agent process
// exited (launch_end), and a new Launch's publication (launch_published). It
// captures the claim's owner and revision under the caller's write lock and
// compares them, advancing the revision, so every owner continuation's ref
// is stale for good. The signal intent and any recorded call outcome are
// kept; a released or ended claim is unchanged, so the first closure wins.
// An error rolls the whole transition back.
func endBusySendPauseClaim(ctx context.Context, q busySendLaunchSQL, launchID string, closure BusySendPauseClosure, now int64) error {
	var claimID, owner string
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT c.claim_id, c.owner_call_id, c.revision FROM busy_send_pause_claims c
	    JOIN busy_send_launches l ON l.launch_id = c.launch_id AND l.ended_at_ms IS NOT NULL
	    WHERE c.launch_id = ? AND c.state = 'standing'`, launchID).Scan(&claimID, &owner, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: end busy-send pause claim: %w", err)
	}
	return busySendChange(ctx, q, `UPDATE busy_send_pause_claims SET state = 'ended', closed_by = ?, closed_at_ms = ?,
	        revision = revision + 1
	    WHERE claim_id = ? AND launch_id = ? AND owner_call_id = ? AND revision = ? AND state = 'standing'`,
		string(closure), now, claimID, launchID, owner, revision)
}

// endBusySendRowPauseClaim ends, as launch_end, the standing pause claim of
// instanceID's row's own current Launch (busySendRowLaunch), whether its End
// was recorded earlier or in the caller's transaction. Only a caller holding
// positive evidence that this Launch's recorded agent process exited calls
// it; a logical End never does.
func endBusySendRowPauseClaim(ctx context.Context, q busySendLaunchSQL, storeID, instanceID string, now int64) error {
	launch, _, err := busySendRowLaunch(ctx, q, storeID, instanceID)
	if err != nil || launch == "" {
		return err
	}
	return endBusySendPauseClaim(ctx, q, launch, BusySendPauseLaunchEnd, now)
}
