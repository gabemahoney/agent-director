package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The force protocol (SR-2.2, SR-3.2, SR-4.1 to SR-4.3; M1's G3, M2 P-C4.4),
// route-neutral over the permanent core's force records: one explicit call's
// claim freezes every Held Message into a force hand-over (boundary force)
// owned by a running force; its prompts issue one at a time through the
// ordinary intent-plus-revalidation commit, under the force's own guard
// (CommitBusySendIssue with BusySendIssueRequest.Force); and the call's end,
// or the release of a call that never reached its end, fences the force by
// owner revision, returns its never-issued claims to Held and records its
// outcome by SR-4.3's rules (settleBusySendForceCall). A pending force needs
// no worker: admission evidence, a definitive negative or Launch End finishes
// it (settleBusySendForceEvidence, endBusySendLaunchForces). Nothing here
// runs from a read, sweep, timer, store open or retention.

// BusySendForceClaimRequest is one explicit force call's claim.
type BusySendForceClaimRequest struct {
	InstanceID string
	// Launch is the published current Launch the caller examined
	// (GetBusySendPublishedLaunch). The claim reads it again in its
	// transaction and refuses with ErrBusySendLaunchChanged, writing nothing,
	// when the published Launch is another.
	Launch BusySendPublishedLaunch
	// ForceID and OwnerCallID are the attempt's AD-made identities.
	ForceID, OwnerCallID string
	// ActiveBudget is the call's active issue and admission phase and
	// CleanupBudget its call-owned cleanup after it, both fixed from At and
	// never renewed: the hand-over's issue deadline is At+ActiveBudget, and
	// its job deadline and the call's recorded finite deadline are
	// At+ActiveBudget+CleanupBudget.
	ActiveBudget, CleanupBudget time.Duration
	// At is the call's entry; zero is now.
	At time.Time
}

// BusySendForceClaim is a committed force claim.
type BusySendForceClaim struct {
	// Force is the stored attempt as get-force reads it: running, with its
	// carried pairs, when it claimed; refused with its reason otherwise.
	Force BusySendForce
	// Ref is a running force's authority; zero for a refused attempt.
	Ref BusySendForceRef
	// Handover is the frozen hand-over a running force claimed; nil otherwise.
	Handover *BusySendHandover
	// WakePath is the published current Launch's wake endpoint ("" for none),
	// the content-free hint the call notifies (SR-3.1).
	WakePath string
	// Returned is the Messages the claim's recovery steps returned to Held
	// before it decided (an abandoned force's release, a dead job's expired
	// claims): a refused call notifies the route when it is not 0.
	Returned int
}

// ClaimBusySendForce stores one explicit force attempt under its own
// force_id (SR-4.1, SR-4.2, SR-2.2), in one transaction on a WAL
// synchronous=FULL connection bounded by ctx, bound to the published current
// Launch the caller examined. For a Launch busy send does not cover it is
// refused agent_not_covered (SR-9.2). For a covered one it refuses, in this
// order: agent_starting, launch_ended and route_unavailable (no recorded
// route or wake endpoint); then, after finishing a force whose call never
// reached its end (ReleaseAbandonedBusySendForce's rule),
// handover_in_progress for an open force; then, after releasing a dead
// ordinary job's expired never-issued claims, handover_in_progress for any
// earlier hand-over still owning order or transport, including a finished
// force's; then no_held_messages,
// earlier_message (an earlier Message still claimed or issued) and
// input_unresolved (AD's issued input awaiting evidence it was taken,
// SR-3.3). So a lifecycle or route refusal runs no recovery. A refusal
// itself stores only the attempt: it changes no custody beyond what those
// recovery steps returned to Held (BusySendForceClaim.Returned). Otherwise every
// then-Held Message, in accepted order, is frozen as the force's carried
// membership and claimed into a force hand-over with the guarded Launch's
// prompt partition, the captured destination and the original deadlines; the
// force runs, owned by OwnerCallID at revision 1, and the conversation names
// it as its latest force that began a hand-over (SR-5.6). The turn-end,
// Dialog and compaction readiness waits are not consulted (SR-4.2). Later
// admissions wait behind the hand-over and are never added. No Message shows
// forced until its prompt's issue commit. A missing row is ErrSpawnNotFound;
// an unknown COMMIT outcome is an error CommitUnconfirmed reports and grants
// nothing.
func (s *Store) ClaimBusySendForce(ctx context.Context, req BusySendForceClaimRequest) (BusySendForceClaim, error) {
	if req.ForceID == "" || req.OwnerCallID == "" || req.InstanceID == "" || req.Launch.LaunchID == "" {
		return BusySendForceClaim{}, fmt.Errorf("store: busy-send force claim: force_id, owner_call_id, instance_id and the examined Launch are required")
	}
	if req.ActiveBudget <= 0 || req.CleanupBudget < 0 {
		return BusySendForceClaim{}, fmt.Errorf("store: busy-send force claim needs a positive active phase and a cleanup of 0 or more")
	}
	var out BusySendForceClaim
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		out, err = claimBusySendForce(ctx, conn, s.StoreID(), req, busySendAt(req.At))
		return err
	})
	if err != nil {
		return BusySendForceClaim{}, err
	}
	return out, nil
}

func claimBusySendForce(ctx context.Context, conn *sql.Conn, storeID string, req BusySendForceClaimRequest, now int64) (BusySendForceClaim, error) {
	f, err := readBusySendIssueFacts(ctx, conn, req.InstanceID)
	if err != nil {
		return BusySendForceClaim{}, err
	}
	l := req.Launch
	if f.launch != l.LaunchID || f.conversation != l.ConversationID || f.token != l.LaunchToken || f.provider != l.Provider {
		return BusySendForceClaim{}, ErrBusySendLaunchChanged
	}
	out := BusySendForceClaim{WakePath: f.wakePath}
	attempt := BusySendForce{ForceID: req.ForceID, InstanceID: req.InstanceID, ConversationID: f.conversation,
		LaunchID: f.launch, LaunchToken: f.token, Provider: f.provider, OwnerCallID: req.OwnerCallID, Revision: 1,
		CallDeadlineMillis: now, CallEndedAtMillis: now, Outcome: BusySendForceRefused, StartedAtMillis: now,
		EndedAtMillis: now, Retention: BusySendRetained}
	refuse := func(reason BusySendForceReason) (BusySendForceClaim, error) {
		attempt.Reason = reason
		if err := insertBusySendForceRow(ctx, conn, storeID, attempt); err != nil {
			return BusySendForceClaim{}, err
		}
		err := out.recorded(ctx, conn, storeID, attempt.ForceID)
		return out, err
	}
	if f.launchCoverage != BusySendCovered {
		return refuse(BusySendForceAgentNotCovered)
	}
	switch f.launchWait() {
	case BusySendWaitAgentStarting:
		return refuse(BusySendForceAgentStarting)
	case BusySendWaitLaunchEnded:
		return refuse(BusySendForceLaunchEnded)
	}
	if f.routeKind == "" || f.wakePath == "" {
		return refuse(BusySendForceRouteUnavailable)
	}
	abandoned, err := releaseAbandonedBusySendForce(ctx, conn, storeID, f.conversation, now)
	if err != nil {
		return BusySendForceClaim{}, err
	}
	out.Returned = abandoned.Returned
	var open bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_forces
	    WHERE conversation_id = ? AND outcome IN ('running', 'pending'))`, f.conversation).Scan(&open); err != nil {
		return BusySendForceClaim{}, err
	}
	if open {
		return refuse(BusySendForceHandoverInProgress)
	}
	if f, err = readBusySendIssueFacts(ctx, conn, req.InstanceID); err != nil {
		return BusySendForceClaim{}, err
	}
	if f.ownerHandover != "" {
		r, err := releaseBusySendClaims(ctx, conn, storeID, f.ownerHandover, 0, BusySendWaitReadinessUnknown, now)
		if err != nil && !errors.Is(err, errBusySendNotExpired) {
			return BusySendForceClaim{}, err
		}
		out.Returned += r.Released
		if !r.Closed {
			return refuse(BusySendForceHandoverInProgress)
		}
		if f, err = readBusySendIssueFacts(ctx, conn, req.InstanceID); err != nil {
			return BusySendForceClaim{}, err
		}
	}
	members, err := readBusySendEligiblePrefix(ctx, conn, f.conversation, BusySendBoundaryForce)
	if err != nil {
		return BusySendForceClaim{}, err
	}
	if len(members) == 0 {
		return refuse(BusySendForceNoHeldMessages)
	}
	var earlier bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_messages
	    WHERE conversation_id = ? AND place IN ('claimed', 'issued'))`, f.conversation).Scan(&earlier); err != nil {
		return BusySendForceClaim{}, err
	}
	if earlier {
		return refuse(BusySendForceEarlierMessage)
	}
	awaiting, err := readBusySendAwaiting(ctx, conn, f.conversation, f.launch)
	if err != nil {
		return BusySendForceClaim{}, err
	}
	if awaiting > 0 {
		return refuse(BusySendForceInputUnresolved)
	}
	// A force's prompt is a forced wake of the route's Wait Turn delivery,
	// so it takes that Launch's stored Wait Turn limits and measure. Across
	// resume, its envelope must still fit all earlier admitted whole Messages.
	caps, err := readBusySendLaunchCapabilities(ctx, conn, storeID, f.launch)
	if err != nil {
		return BusySendForceClaim{}, err
	}
	if caps == nil {
		return BusySendForceClaim{}, errBusySendCapabilities
	}
	limits, offered := caps.Mode(BusySendWaitTurn)
	if !offered {
		return refuse(BusySendForceRouteUnavailable)
	}
	texts := make([][]byte, len(members))
	for i, m := range members {
		texts[i] = m.text
	}
	parts, err := PartitionBusySendPrompts(texts, limits, nil)
	if err != nil {
		return BusySendForceClaim{}, err
	}
	active := now + req.ActiveBudget.Milliseconds()
	deadline := active + req.CleanupBudget.Milliseconds()
	h, err := insertBusySendHandover(ctx, conn, storeID, f, busySendHandoverSpec{InstanceID: req.InstanceID,
		Destination: f.destination(), Boundary: BusySendBoundaryForce, ForceID: req.ForceID,
		IssueDeadline: active, JobDeadline: deadline}, members, parts, now)
	if err != nil {
		return BusySendForceClaim{}, err
	}
	attempt.Outcome, attempt.Reason, attempt.HandoverID = BusySendForceRunning, "", h.HandoverID
	attempt.CallDeadlineMillis, attempt.CallEndedAtMillis, attempt.EndedAtMillis = deadline, 0, 0
	if err := insertBusySendForceRow(ctx, conn, storeID, attempt); err != nil {
		return BusySendForceClaim{}, err
	}
	for i, m := range members {
		if _, err := conn.ExecContext(ctx, `INSERT INTO busy_send_force_members (force_id, member_index, message_id, store_id)
		    VALUES (?, ?, ?, ?)`, req.ForceID, i, m.messageID, storeID); err != nil {
			return BusySendForceClaim{}, err
		}
	}
	if err := busySendChange(ctx, conn, `UPDATE busy_conversations SET handover_force_id = ?, revision = revision + 1
	    WHERE conversation_id = ? AND owner_handover_id = ?`, req.ForceID, f.conversation, h.HandoverID); err != nil {
		return BusySendForceClaim{}, err
	}
	out.Handover = h
	out.Ref = BusySendForceRef{ForceID: req.ForceID, OwnerCallID: req.OwnerCallID, Revision: attempt.Revision}
	err = out.recorded(ctx, conn, storeID, req.ForceID)
	return out, err
}

// recorded sets out.Force to the attempt as stored, read in the writer's own
// transaction, so what a call returns is what get-force reads.
func (out *BusySendForceClaim) recorded(ctx context.Context, conn *sql.Conn, storeID, forceID string) error {
	f, err := readBusySendRecordedForce(ctx, conn, storeID, forceID)
	out.Force = f
	return err
}

// readBusySendRecordedForce reads a force a writer just stored or settled,
// with its carried pairs and Results; one past retention has no result to
// return and is an error.
func readBusySendRecordedForce(ctx context.Context, conn *sql.Conn, storeID, forceID string) (BusySendForce, error) {
	lookup, err := readBusySendForceByID(ctx, conn, storeID, forceID)
	if err != nil {
		return BusySendForce{}, err
	}
	if lookup.ForceResult == nil {
		return BusySendForce{}, fmt.Errorf("store: busy-send force %s is %s", forceID, lookup.Lookup)
	}
	return *lookup.ForceResult, nil
}

// destination is the current Launch's destination as the facts captured it.
func (f busySendIssueFacts) destination() BusySendDestination {
	return BusySendDestination{LaunchID: f.launch, LaunchToken: f.token, Provider: f.provider, SessionID: f.sessionID,
		RouteKind: BusySendRouteKind(f.routeKind), RouteID: f.routeID, ConnectionGeneration: f.connectionGeneration}
}

// BusySendForceDeletedRequest is a send's force whose confirmed pair's
// owner Message belongs to a deleted record (SR-4.1).
type BusySendForceDeletedRequest struct {
	ForceID, OwnerCallID string
	Pair                 BusySendPair
	At                   time.Time
}

// RefuseBusySendForceForDeleted stores, in one write transaction, a refused
// force attempt with reason record_deleted for a repeat of a confirmed pair
// whose owner Message's conversation was retired by its record's deletion
// (SR-4.1): bound to that retained ownership (the Message's original target
// and conversation, with no Launch), never to an agent created later under
// the same instance id. It reports false, writing nothing, when the pair has
// no owner or its conversation was not retired by deletion; the caller then
// forces the live agent. It changes no custody or Result and starts no work.
func (s *Store) RefuseBusySendForceForDeleted(ctx context.Context, req BusySendForceDeletedRequest) (BusySendForce, bool, error) {
	if req.ForceID == "" || req.OwnerCallID == "" {
		return BusySendForce{}, false, fmt.Errorf("store: busy-send force refusal needs a force_id and an owner_call_id")
	}
	var out BusySendForce
	var deleted bool
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		var instance, conversation, provider string
		var retired sql.NullString
		err := conn.QueryRowContext(ctx, `SELECT m.instance_id, m.conversation_id, c.provider, c.retired_reason
		    FROM busy_send_results r JOIN busy_send_messages m ON m.message_id = r.message_id
		    JOIN busy_conversations c ON c.conversation_id = m.conversation_id
		    WHERE r.caller_uuid = ? AND r.delivery_request_id = ? AND r.store_id = ?`,
			req.Pair.CallerUUID, req.Pair.DeliveryRequestID, s.StoreID()).Scan(&instance, &conversation, &provider, &retired)
		if errors.Is(err, sql.ErrNoRows) || err == nil && BusySendRetiredReason(retired.String) != BusySendRecordDeleted {
			return nil
		}
		if err != nil {
			return err
		}
		now := busySendAt(req.At)
		if err := insertBusySendForceRow(ctx, conn, s.StoreID(), BusySendForce{ForceID: req.ForceID, InstanceID: instance,
			ConversationID: conversation, Provider: provider, OwnerCallID: req.OwnerCallID, Revision: 1,
			CallDeadlineMillis: now, CallEndedAtMillis: now, Outcome: BusySendForceRefused,
			Reason: BusySendForceRecordDeleted, StartedAtMillis: now, EndedAtMillis: now}); err != nil {
			return err
		}
		deleted = true
		out, err = readBusySendRecordedForce(ctx, conn, s.StoreID(), req.ForceID)
		return err
	})
	if err != nil {
		return BusySendForce{}, false, err
	}
	return out, deleted, nil
}

// BusySendForcePrompt is a running force's next prompt as a route's forced
// wake reads it (ReadBusySendForcePrompt).
type BusySendForcePrompt struct {
	// Force is the running force's authority, which the prompt's issue commit
	// compares; zero when the agent has no running force.
	Force BusySendForceRef
	// Handover is the force's frozen hand-over, each prompt's body rendered
	// from its Messages' retained text; nil when it has closed.
	Handover *BusySendHandover
	// Next is the prompt the force may issue now, -1 for none
	// (busySendForceNext).
	Next int
}

// ReadBusySendForcePrompt reads instanceID's running force and the frozen
// prompt it may issue now (SR-3.4, SR-4.3), in one read transaction: the
// hand-over's cursor prompt while it is still only claimed and, after the
// first, once the prompt before it has secured order and its admission is
// recorded with no negative. For a Claude wake, order requires its sighting
// plus written output, in either order; a parent Stop supplies neither fact.
// It creates no force, chooses no new group,
// releases nothing and writes nothing: the issue commit judges every captured
// fact again. No row is ErrSpawnNotFound.
func (s *Store) ReadBusySendForcePrompt(ctx context.Context, instanceID string) (BusySendForcePrompt, error) {
	out := BusySendForcePrompt{Next: -1}
	err := s.WithContext(ctx).readWithin(DefaultLockWait, "store: read busy-send force prompt", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		out = BusySendForcePrompt{Next: -1}
		p, err := readExaminedBusySendLaunch(ctx, conn, instanceID)
		if err != nil {
			return err
		}
		var id string
		err = conn.QueryRowContext(ctx, `SELECT force_id FROM busy_send_forces WHERE conversation_id = ? AND outcome = 'running'`,
			p.conversation).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		f, _, err := readBusySendForceRow(ctx, conn, s.StoreID(), id)
		if err != nil {
			return err
		}
		out.Force = BusySendForceRef{ForceID: f.ForceID, OwnerCallID: f.OwnerCallID, Revision: f.Revision}
		h, err := readBusySendHandover(ctx, conn, s.StoreID(), f.HandoverID)
		if err != nil || !h.Open {
			return err
		}
		frozen, err := h.frozen(ctx, conn)
		if err != nil {
			return err
		}
		out.Handover = &frozen
		out.Next, err = busySendForceNext(ctx, conn, h)
		return err
	})
	if err != nil {
		return BusySendForcePrompt{}, err
	}
	return out, nil
}

// busySendForceNext is the prompt of the open force hand-over h that may
// issue now, -1 for none: its cursor prompt while still only claimed, and,
// after the first, only once the prompt before it is admitted with no
// negative and has secured order. Neither admission alone nor completed
// output alone is enough (SR-3.4).
func busySendForceNext(ctx context.Context, q busySendLaunchSQL, h busySendHandoverRow) (int, error) {
	k := h.PromptCursor
	if !h.Open || k >= h.promptCount {
		return -1, nil
	}
	p, err := readBusySendPrompt(ctx, q, h.HandoverID, k)
	if err != nil || p.emission != BusySendEmissionNone || p.fenced {
		return -1, err
	}
	if k > 0 {
		prev, err := readBusySendPrompt(ctx, q, h.HandoverID, k-1)
		if err != nil || !prev.effectivelyAdmitted() || prev.orderKind == "" {
			return -1, err
		}
	}
	return k, nil
}

// effectivelyAdmitted reports a recorded admission with no definitive
// negative: a negative revokes it while the force is open (M1 RecDn).
func (p busySendPromptRow) effectivelyAdmitted() bool {
	return p.admission != "" && p.negative == ""
}

// BusySendForceProgress is what a force's call reads while it waits: only its
// own force's durable facts (ReadBusySendForceProgress).
type BusySendForceProgress struct {
	// Outcome is the force's stored outcome; anything but running means the
	// call's end will find it finished.
	Outcome BusySendForceOutcome
	// Next is the prompt the force may issue now, -1 for none
	// (busySendForceNext): the call notifies the route once for each.
	Next int
	// Decided is whether, while it runs, the call-end rules can already give
	// a terminal outcome: nothing is left only claimed and every issued
	// prompt is admitted, or a definitive negative is recorded. The call may
	// then end early.
	Decided bool
}

// ReadBusySendForceProgress reads forceID's progress in one read transaction,
// from durable facts only. It infers nothing, secures nothing, issues nothing
// and finishes nothing. An unknown force is an error.
func (s *Store) ReadBusySendForceProgress(ctx context.Context, forceID string) (BusySendForceProgress, error) {
	var out BusySendForceProgress
	err := s.WithContext(ctx).readWithin(DefaultLockWait, "store: read busy-send force progress", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		out = BusySendForceProgress{Next: -1}
		f, found, err := readBusySendForceRow(ctx, conn, s.StoreID(), forceID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("store: no busy-send force %s", forceID)
		}
		out.Outcome = f.Outcome
		if f.Outcome != BusySendForceRunning || f.HandoverID == "" {
			return nil
		}
		h, err := readBusySendHandover(ctx, conn, s.StoreID(), f.HandoverID)
		if err != nil {
			return err
		}
		if out.Next, err = busySendForceNext(ctx, conn, h); err != nil {
			return err
		}
		p, err := readBusySendForcePromptFacts(ctx, conn, f.HandoverID)
		if err != nil {
			return err
		}
		var unissued bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_prompts WHERE handover_id = ? AND emission = 'none')`,
			f.HandoverID).Scan(&unissued); err != nil {
			return err
		}
		out.Decided = p.negated || (!unissued && p.issued > 0 && p.unadmitted == 0)
		return nil
	})
	if err != nil {
		return BusySendForceProgress{}, err
	}
	return out, nil
}

// BusySendForceEnd is a force call's recorded end (EndBusySendForceCall).
type BusySendForceEnd struct {
	// Force is the force's recorded result, read in the end's transaction:
	// what this end recorded, or, when it lost, what was recorded before.
	Force BusySendForce
	// Ended is whether this call end recorded the force's outcome; false when
	// a release, Launch End or a not-submitted had already finished it.
	Ended bool
	// HeldMembers is how many of the force's carried Messages are Held at
	// the end, returned by this end or before it: the call notifies the
	// route when it is not 0 (SR-4.3).
	HeldMembers int
	// WakePath is the published current Launch's wake endpoint when it is
	// still the force's Launch, "" otherwise.
	WakePath string
}

// EndBusySendForceCall records ref's call end (SR-4.3, SR-3.2) in one
// transaction on a WAL synchronous=FULL connection bounded by ctx. When the
// force still runs under ref's owner and revision, it fences the force's job
// by owner revision, returns its never-issued claims to Held in accepted
// order, records the call's end and the outcome SR-4.3's call-end rules
// give, and applies their member rules (settleBusySendForceCall); it records
// the end even when the force claims nothing. Otherwise the end lost (a
// release, Launch End or a not-submitted finished the force, or another
// owner holds it): it changes nothing and returns ref's force's recorded
// result, even when a later force has started, never touching that one. An
// unknown force is an error.
func (s *Store) EndBusySendForceCall(ctx context.Context, ref BusySendForceRef, at time.Time) (BusySendForceEnd, error) {
	var out BusySendForceEnd
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		out = BusySendForceEnd{}
		f, found, err := readBusySendForceRow(ctx, conn, s.StoreID(), ref.ForceID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("store: no busy-send force %s", ref.ForceID)
		}
		if f.Outcome == BusySendForceRunning && f.OwnerCallID == ref.OwnerCallID && f.Revision == ref.Revision &&
			f.CallEndedAtMillis == 0 {
			if _, err := settleBusySendForceCall(ctx, conn, s.StoreID(), f, true, busySendAt(at)); err != nil {
				return err
			}
			out.Ended = true
		}
		if out.Force, err = readBusySendRecordedForce(ctx, conn, s.StoreID(), f.ForceID); err != nil {
			return err
		}
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM busy_send_force_members fm
		    JOIN busy_send_messages m ON m.message_id = fm.message_id WHERE fm.force_id = ? AND m.place = 'held'`,
			f.ForceID).Scan(&out.HeldMembers); err != nil {
			return err
		}
		p, err := readExaminedBusySendLaunch(ctx, conn, f.InstanceID)
		if err == nil && p.launch == f.LaunchID {
			out.WakePath = p.wakePath
		}
		if errors.Is(err, ErrSpawnNotFound) || errors.Is(err, ErrBusySendNoBinding) {
			err = nil
		}
		return err
	})
	if err != nil {
		return BusySendForceEnd{}, err
	}
	return out, nil
}

// BusySendForceRelease is what an abandoned force's release did.
type BusySendForceRelease struct {
	// Released is whether a force whose call never reached its end was
	// finished; false changed nothing.
	Released bool
	ForceID  string
	// Returned is the never-issued Messages it returned to Held.
	Returned int
	// WakePath is the published current Launch's wake endpoint ("" for
	// none), which the releasing command notifies when Returned is not 0.
	WakePath string
}

// ReleaseAbandonedBusySendForce finishes instanceID's force whose call never
// reached its end (SR-3.2, SR-4.3), for the agent's eligible event (the
// coordinator runs it first, before its route's wait decision) or an explicit
// send-keys for that agent, in one transaction on a WAL
// synchronous=FULL connection bounded by ctx: only a running force whose
// recorded call deadline has strictly passed by at (zero: now) with no
// recorded call end. It fences the force by owner revision, returns only its
// proven-never-issued Messages to Held in accepted order, and finishes it by
// the call-end rules (settleBusySendForceCall), without recording a call end;
// issued Messages keep their custody and are never re-issued or re-fenced.
// A release before the deadline has passed, after the call end was recorded,
// or of a force no longer running changes nothing. Reads, Lookup, sweep,
// timers, store open and retention never call it. No row is
// ErrSpawnNotFound.
func (s *Store) ReleaseAbandonedBusySendForce(ctx context.Context, instanceID string, at time.Time) (BusySendForceRelease, error) {
	var out BusySendForceRelease
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		p, err := readExaminedBusySendLaunch(ctx, conn, instanceID)
		if err != nil {
			return err
		}
		if out, err = releaseAbandonedBusySendForce(ctx, conn, s.StoreID(), p.conversation, busySendAt(at)); err != nil {
			return err
		}
		out.WakePath = p.wakePath
		return nil
	})
	if err != nil {
		return BusySendForceRelease{}, err
	}
	return out, nil
}

// releaseAbandonedBusySendForce is ReleaseAbandonedBusySendForce's work for
// conversation's force inside its caller's transaction, at now: the one
// release path, which every eligible event's claim and every explicit force
// or send for the agent runs first.
func releaseAbandonedBusySendForce(ctx context.Context, conn *sql.Conn, storeID, conversation string, now int64) (BusySendForceRelease, error) {
	var id string
	err := conn.QueryRowContext(ctx, `SELECT force_id FROM busy_send_forces WHERE conversation_id = ? AND outcome = 'running'
	    AND call_ended_at_ms IS NULL AND call_deadline_ms < ?`, conversation, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return BusySendForceRelease{}, nil
	}
	if err != nil {
		return BusySendForceRelease{}, err
	}
	f, _, err := readBusySendForceRow(ctx, conn, storeID, id)
	if err != nil {
		return BusySendForceRelease{}, err
	}
	returned, err := settleBusySendForceCall(ctx, conn, storeID, f, false, now)
	if err != nil {
		return BusySendForceRelease{}, err
	}
	return BusySendForceRelease{Released: true, ForceID: id, Returned: returned}, nil
}

// busySendForcePromptFacts are a force's prompt facts SR-4.3's rules read,
// from its hand-over's prompts and never from Message Results or custody:
// whether any prompt's emission intent was persisted (a not-submitted one
// included); issued, the prompts with intent that count as issued (an
// explicit not-submitted one does not); unadmitted, those of them with no
// admission recorded or with a definitive negative; and whether such a
// negative is recorded.
type busySendForcePromptFacts struct {
	intended           bool
	issued, unadmitted int
	negated            bool
}

func readBusySendForcePromptFacts(ctx context.Context, q busySendLaunchSQL, handoverID string) (busySendForcePromptFacts, error) {
	var p busySendForcePromptFacts
	if handoverID == "" {
		return p, nil
	}
	var intended, negated int
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(emission IN ('intent', 'issued')), 0),
	        COALESCE(SUM(emission IN ('intent', 'issued') AND negative_kind IS NOT 'not_submitted'), 0),
	        COALESCE(SUM(emission IN ('intent', 'issued') AND negative_kind IS NOT 'not_submitted'
	            AND (admission_kind IS NULL OR negative_kind IS NOT NULL)), 0),
	        COALESCE(SUM(emission IN ('intent', 'issued') AND negative_kind IS 'no_admission'), 0)
	    FROM busy_send_prompts WHERE handover_id = ?`, handoverID).Scan(&intended, &p.issued, &p.unadmitted, &negated)
	p.intended, p.negated = intended > 0, negated > 0
	return p, err
}

// busySendForceOutcome is SR-4.3's outcome from a force's prompt facts, at
// its call's end or release, at Launch End (launchEnded), or when evidence
// reaches a pending force: nothing intended is refused not_issued and only
// not-submitted prompts refused not_submitted, whatever else holds; every
// issued prompt admitted is handed_over; an unadmitted one is failed
// launch_ended after Launch End, failed admission_unknown under a definitive
// negative or on a route that can never admit it (canAdmit false), and
// pending otherwise.
func busySendForceOutcome(p busySendForcePromptFacts, launchEnded, canAdmit bool) (BusySendForceOutcome, BusySendForceReason) {
	switch {
	case !p.intended:
		return BusySendForceRefused, BusySendForceNotIssued
	case p.issued == 0:
		return BusySendForceRefused, BusySendForceNotSubmitted
	case p.unadmitted == 0:
		return BusySendForceHandedOver, BusySendForceAdmitted
	case launchEnded:
		return BusySendForceFailed, BusySendForceLaunchEnded
	case p.negated || !canAdmit:
		return BusySendForceFailed, BusySendForceAdmissionUnknown
	}
	return BusySendForcePending, BusySendForceAwaitingAdmission
}

// settleBusySendForceCall applies SR-4.3's call-end rules to the running force
// f, for its call's end (callEnded: the end is recorded) or the release of a
// call that never reached it, at now: it fences the force's job, returns its
// never-issued claims to Held (fenceBusySendForce), chooses the outcome from
// its prompt facts and applies the member rules, then records the waits of
// the conversation's Held Messages. It returns the Messages it returned.
func settleBusySendForceCall(ctx context.Context, conn *sql.Conn, storeID string, f BusySendForce, callEnded bool, now int64) (int, error) {
	returned, err := fenceBusySendForce(ctx, conn, storeID, f, now)
	if err != nil {
		return 0, err
	}
	p, err := readBusySendForcePromptFacts(ctx, conn, f.HandoverID)
	if err != nil {
		return 0, err
	}
	var ended bool
	if err := conn.QueryRowContext(ctx, `SELECT ended_at_ms IS NOT NULL FROM busy_send_launches WHERE launch_id = ?`,
		f.LaunchID).Scan(&ended); err != nil {
		return 0, err
	}
	outcome, reason := busySendForceOutcome(p, ended, BusySendCapabilitiesFor(f.Provider).ForceAdmission != "")
	if err := finishBusySendForce(ctx, conn, f, outcome, reason, callEnded, now); err != nil {
		return 0, err
	}
	return returned, refreshBusySendHeldWaits(ctx, conn, f.ConversationID, BusySendWaitReadinessUnknown, now)
}

// fenceBusySendForce fences force f's hand-over job (SR-4.3): every prompt
// still with no emission intent is released, its claimed Messages return to
// Held in accepted order with current membership cleared and reservations
// kept, and the hand-over's revision advances, so no job holding it can
// persist intent; the hand-over closes once nothing of it is in custody.
// Issued Messages keep their custody and order barrier. It returns how many
// Messages are Held again.
func fenceBusySendForce(ctx context.Context, conn *sql.Conn, storeID string, f BusySendForce, now int64) (int, error) {
	if f.HandoverID == "" {
		return 0, nil
	}
	h, err := readBusySendHandover(ctx, conn, storeID, f.HandoverID)
	if err != nil || !h.Open {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE busy_send_prompts SET emission = 'released', released_at_ms = ?, revision = revision + 1
	    WHERE handover_id = ? AND emission = 'none'`, now, h.HandoverID); err != nil {
		return 0, err
	}
	returned, err := returnBusySendHeld(ctx, conn, `handover_id = ? AND place = 'claimed'`, h.HandoverID)
	if err != nil {
		return 0, err
	}
	if h.Revision, err = advanceBusySendHandover(ctx, conn, h, ""); err != nil {
		return 0, err
	}
	return returned, closeBusySendHandoverIfDone(ctx, conn, &h, now)
}

// finishBusySendForce records outcome and reason for the open force f, with
// SR-4.3's member rules for a failed one: every carried Message of an issued
// prompt (an explicit not-submitted one excepted) whose Result is not Final
// becomes Final sent_unconfirmed for reason, keeping its custody, and its
// held reservation ends; a Final Result is kept and a Message never intended
// stays Held. A pending force keeps no end time; any other ends now, with its
// call's end recorded when callEnded. The force's revision advances, so a
// call or release holding the old one changes nothing.
func finishBusySendForce(ctx context.Context, conn *sql.Conn, f BusySendForce, outcome BusySendForceOutcome,
	reason BusySendForceReason, callEnded bool, now int64) error {
	if outcome == BusySendForceFailed {
		issued := `message_id IN (SELECT hm.message_id FROM busy_send_handover_members hm
		    JOIN busy_send_prompts p ON p.handover_id = hm.handover_id AND p.prompt_index = hm.prompt_index
		    WHERE hm.handover_id = ? AND p.emission IN ('intent', 'issued') AND p.negative_kind IS NOT 'not_submitted')`
		if err := freeBusySendReservations(ctx, conn, f.ConversationID, `place = 'issued' AND `+issued, f.HandoverID); err != nil {
			return err
		}
		resultReason := BusySendReasonAdmissionUnknown
		if reason == BusySendForceLaunchEnded {
			resultReason = BusySendReasonLaunchEnded
		}
		if err := finalizeBusySendSent(ctx, conn, resultReason, now, issued, f.HandoverID); err != nil {
			return err
		}
	}
	var ended, callEnd any
	if outcome != BusySendForcePending {
		ended = now
	}
	if callEnded {
		callEnd = now
	}
	return busySendChange(ctx, conn, `UPDATE busy_send_forces SET outcome = ?, reason = ?, ended_at_ms = ?,
	        call_ended_at_ms = COALESCE(call_ended_at_ms, ?), revision = revision + 1
	    WHERE force_id = ? AND revision = ? AND outcome IN ('running', 'pending')`,
		string(outcome), string(reason), ended, callEnd, f.ForceID, f.Revision)
}

// settleBusySendForceEvidence applies SR-4.3's evidence rules to the force
// that owns handoverID, if any, in the transaction that recorded a prompt's
// admission, definitive negative or explicit not-submitted, at now. A pending
// force finishes as busySendForceOutcome gives once its last unadmitted
// prompt is admitted (handed_over), a negative is recorded (failed
// admission_unknown) or its only issued prompts were not submitted (refused
// not_submitted). A running force is decided at its call's end, except that a
// not-submitted for every prompt it issued refuses it at once, fencing it. A
// finished force never changes.
func settleBusySendForceEvidence(ctx context.Context, conn *sql.Conn, storeID, handoverID string, now int64) error {
	var forceID sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT force_id FROM busy_send_handovers WHERE handover_id = ?`,
		handoverID).Scan(&forceID); err != nil || !forceID.Valid {
		return err
	}
	f, _, err := readBusySendForceRow(ctx, conn, storeID, forceID.String)
	if err != nil {
		return err
	}
	p, err := readBusySendForcePromptFacts(ctx, conn, f.HandoverID)
	if err != nil {
		return err
	}
	switch f.Outcome {
	case BusySendForcePending:
		outcome, reason := busySendForceOutcome(p, false, true)
		if outcome == BusySendForcePending {
			return nil
		}
		return finishBusySendForce(ctx, conn, f, outcome, reason, false, now)
	case BusySendForceRunning:
		if !p.intended || p.issued > 0 {
			return nil
		}
		if _, err := fenceBusySendForce(ctx, conn, storeID, f, now); err != nil {
			return err
		}
		if err := finishBusySendForce(ctx, conn, f, BusySendForceRefused, BusySendForceNotSubmitted, false, now); err != nil {
			return err
		}
		return refreshBusySendHeldWaits(ctx, conn, f.ConversationID, BusySendWaitReadinessUnknown, now)
	}
	return nil
}

// endBusySendLaunchForces finishes launchID's open forces at its Launch End
// (SR-4.3, SR-6.2; M1's EndLaunch), after the Launch End custody disposition
// returned their claims and fenced their issued prompts: a running force by
// the call-end rules with its Launch ended (refused not_issued with nothing
// intended, handed_over with every issued prompt admitted, failed
// launch_ended otherwise) and a pending one failed launch_ended, with the
// member rules. No call end is recorded: the owning call's own end, if it
// comes, loses and returns this recorded result. Their Results never carry.
func endBusySendLaunchForces(ctx context.Context, conn *sql.Conn, storeID, launchID string, now int64) error {
	rows, err := conn.QueryContext(ctx, `SELECT force_id FROM busy_send_forces WHERE launch_id = ? AND outcome IN ('running', 'pending')`,
		launchID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		f, _, err := readBusySendForceRow(ctx, conn, storeID, id)
		if err != nil {
			return err
		}
		if _, err := fenceBusySendForce(ctx, conn, storeID, f, now); err != nil {
			return err
		}
		p, err := readBusySendForcePromptFacts(ctx, conn, f.HandoverID)
		if err != nil {
			return err
		}
		outcome, reason := busySendForceOutcome(p, true, true)
		if err := finishBusySendForce(ctx, conn, f, outcome, reason, false, now); err != nil {
			return err
		}
	}
	return nil
}

// BusySendPromptNegative is a route's definitive negative for one issued
// prompt (SR-4.3): an event after which no admission evidence can arrive for
// it and which is not a not-submitted (no_admission), naming the prompt and
// the destination it was issued to.
type BusySendPromptNegative struct {
	HandoverID           string
	PromptIndex          int
	LaunchID             string
	ConnectionGeneration int64
	At                   time.Time
}

// RecordBusySendNegative records a qualified no_admission negative for an
// issued prompt by its identity (SR-4.3; M1's RecDn), in one transaction on a
// WAL synchronous=FULL connection bounded by ctx. A negative naming another
// destination, or a prompt never issued, changes nothing; a repeat is inert.
// It changes no custody, order or Result by itself and ends the prompt's
// awaiting obligation (SR-3.3). For a force's prompt it revokes any admission
// while the force is open: a running force keeps the fact for its call's end,
// which then fails it admission_unknown; a pending force fails at once with
// the member rules (settleBusySendForceEvidence); a finished force never
// changes. A route records one only where the capability table names such an
// event; it is never inferred from elapsed time or missing evidence.
func (s *Store) RecordBusySendNegative(ctx context.Context, ev BusySendPromptNegative) (BusySendCompletionResult, error) {
	var out BusySendCompletionResult
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		out = BusySendCompletionResult{}
		h, err := readBusySendHandover(ctx, conn, s.StoreID(), ev.HandoverID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := readBusySendPrompt(ctx, conn, h.HandoverID, ev.PromptIndex)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if ev.LaunchID == h.Destination.LaunchID && ev.ConnectionGeneration == h.Destination.ConnectionGeneration &&
			p.emission == BusySendEmissionIssued && p.negative == "" {
			now := busySendAt(ev.At)
			if err := busySendChange(ctx, conn, `UPDATE busy_send_prompts SET negative_kind = 'no_admission', negative_at_ms = ?,
			        revision = revision + 1
			    WHERE handover_id = ? AND prompt_index = ? AND revision = ? AND negative_kind IS NULL`,
				now, h.HandoverID, p.index, p.revision); err != nil {
				return err
			}
			out.Recorded = true
			if err := settleBusySendForceEvidence(ctx, conn, s.StoreID(), h.HandoverID, now); err != nil {
				return err
			}
		}
		return readBusySendCompletionState(ctx, conn, h.HandoverID, p.index, &out)
	})
	if err != nil {
		return BusySendCompletionResult{}, err
	}
	out.Revision = 0
	return out, nil
}
