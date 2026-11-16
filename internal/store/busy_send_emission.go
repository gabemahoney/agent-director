package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Durable emission (SR-2.4 to SR-2.6, SR-3.2, SR-5.1 to SR-5.3; M1's Intent,
// Revalidate, Record, ReleaseExpired, EndLaunch and Exit; M2 P-C4.3). A
// hand-over's job takes each prompt, in order, through one guarded write
// transaction on a WAL synchronous=FULL connection before any output:
// CommitBusySendIssue persists the prompt's emission intent with its captured
// destination, the row's Turn Start counter and the running-turn flag,
// compares every captured fact and records the issuer, all at once (M1's
// Intent(a) and Revalidate(a) with no step between them). A refused guard
// therefore leaves the prompt only claimed, so its job may release it, and no
// intent is ever stranded by a guard. Only after the verified commit may the
// route call its captured destination; from then on the prompt is never
// re-emitted and its Messages never return to Held (a provider's explicit
// not-submitted excepted): Launch End makes them Final sent_unconfirmed,
// whether or not anything was sent.
//
// Every job step compares the hand-over revision its caller holds and
// advances it; a release, a re-claim or a fence advances it too, so an
// obsolete job can neither persist intent nor issue. A failed or uncertain
// commit authorizes nothing. Evidence is then recorded by identity
// (RecordBusySendCompletion from the issuing job, RecordBusySendEvidence from
// any later callback): observed facts once each, custody and Results only on
// the live Launch, a Final Result never again.

// BusySendGuardFailure names the captured fact a job step found changed.
type BusySendGuardFailure string

const (
	// The hand-over closed, was released, re-claimed or fenced, or the caller's
	// revision is not its current one: the caller owns nothing.
	BusySendGuardOwner BusySendGuardFailure = "owner_changed"
	// The prompt is not at this step: another prompt is next, or it already
	// passed it.
	BusySendGuardPrompt BusySendGuardFailure = "prompt_state"
	// The job's original issue deadline has passed; it is never renewed.
	BusySendGuardDeadline BusySendGuardFailure = "deadline_passed"
	// The published current covered Launch is not the captured one, or it
	// ended or is starting.
	BusySendGuardLaunch BusySendGuardFailure = "launch_changed"
	// The provider session, route identity or connection generation changed.
	BusySendGuardDestination BusySendGuardFailure = "destination_changed"
	// A turn started since the claim (the Turn Start counter moved).
	BusySendGuardTurn BusySendGuardFailure = "turn_changed"
	// The permission epoch moved, or an authoritative permission hold stands.
	BusySendGuardPermission BusySendGuardFailure = "permission_changed"
	// A standing pause claim fences the Launch (SR-6.5).
	BusySendGuardPause BusySendGuardFailure = "pause_fence"
	// An earlier issuer of the Launch has an issued prompt with no recorded
	// outcome, so its identity cannot be replaced.
	BusySendGuardIssuer BusySendGuardFailure = "issuer_unresolved"
)

// BusySendGuardError is a job step refused by its guard: nothing was written.
// Wait is the cause to record for the waiting Messages, "" when the refusal
// says only that the caller's authority is gone.
type BusySendGuardError struct {
	Failure BusySendGuardFailure
	Wait    BusySendWaitReason
	Detail  string
}

func (e *BusySendGuardError) Error() string {
	return fmt.Sprintf("store: busy-send %s: %s", e.Failure, e.Detail)
}

func busySendGuard(failure BusySendGuardFailure, wait BusySendWaitReason, detail string) error {
	return &BusySendGuardError{Failure: failure, Wait: wait, Detail: detail}
}

// BusySendPromptRef names one prompt of a hand-over and the hand-over
// revision its job holds.
type BusySendPromptRef struct {
	HandoverID  string
	PromptIndex int
	Revision    int64
}

// busySendPromptRow is a prompt row as the evidence writers compare it.
type busySendPromptRow struct {
	index       int
	revision    int64
	emission    BusySendEmission
	transport   BusySendTransport
	orderKind   BusySendOrderKind
	admission   BusySendAdmissionKind
	evidence    BusySendEvidenceKind
	negative    BusySendNegativeKind
	fenced      bool
	memberCount int
}

func readBusySendPrompt(ctx context.Context, q busySendLaunchSQL, handoverID string, index int) (busySendPromptRow, error) {
	r := busySendPromptRow{index: index}
	var emission string
	var transport, order, admission, evidence, negative sql.NullString
	var fenced sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT revision, emission, transport, order_kind, admission_kind, evidence_kind,
	        negative_kind, fenced_at_ms, message_count
	    FROM busy_send_prompts WHERE handover_id = ? AND prompt_index = ?`, handoverID, index).Scan(
		&r.revision, &emission, &transport, &order, &admission, &evidence, &negative, &fenced, &r.memberCount)
	r.emission, r.transport, r.orderKind = BusySendEmission(emission), BusySendTransport(transport.String), BusySendOrderKind(order.String)
	r.admission, r.evidence = BusySendAdmissionKind(admission.String), BusySendEvidenceKind(evidence.String)
	r.negative, r.fenced = BusySendNegativeKind(negative.String), fenced.Valid
	return r, err
}

// busySendPromptMembersIn reports whether every Message of the prompt is in
// place and current in handoverID: the M1 v check, that a callback acts only
// on Messages still bound to its hand-over.
func busySendPromptMembersIn(ctx context.Context, q busySendLaunchSQL, handoverID string, index int, place BusySendPlace) (bool, error) {
	var stray int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM busy_send_handover_members hm
	    JOIN busy_send_messages m ON m.message_id = hm.message_id
	    WHERE hm.handover_id = ? AND hm.prompt_index = ?
	        AND (m.place IS NOT ? OR m.handover_id IS NOT hm.handover_id)`, handoverID, index, string(place)).Scan(&stray)
	return stray == 0, err
}

// busySendPromptMemberSQL selects the Messages of one prompt (two arguments:
// the hand-over and the prompt index).
const busySendPromptMemberSQL = `message_id IN (SELECT message_id FROM busy_send_handover_members
    WHERE handover_id = ? AND prompt_index = ?)`

// checkBusySendJob is the guard every job step shares (SR-2.4): the
// hand-over is open, owns its conversation and is at ref's revision; ref's
// prompt is the next one; the issue deadline has not passed; and the
// published current covered Launch, its route identity and connection
// generation, the provider session, the Turn Start counter and the permission
// epoch are all still the captured ones, with no authoritative permission
// hold and no standing pause fence. A force's hand-over takes force's
// authority instead of none (checkBusySendForceJob) and skips only the
// readiness facts a force may skip (SR-4.2): the Turn Start counter, the
// permission hold and the permission epoch. Its refusals are
// *BusySendGuardError.
func checkBusySendJob(ctx context.Context, conn *sql.Conn, storeID string, ref BusySendPromptRef, force *BusySendForceRef, now int64) (busySendHandoverRow, busySendIssueFacts, error) {
	var f busySendIssueFacts
	h, err := readBusySendHandover(ctx, conn, storeID, ref.HandoverID)
	if errors.Is(err, sql.ErrNoRows) {
		return h, f, busySendGuard(BusySendGuardOwner, "", "no such hand-over")
	}
	if err != nil {
		return h, f, err
	}
	forced := h.ForceID != ""
	if forced != (force != nil) || forced && force.ForceID != h.ForceID {
		return h, f, busySendGuard(BusySendGuardOwner, "", "the step's force authority is not the hand-over's")
	}
	var owner sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT owner_handover_id FROM busy_conversations WHERE conversation_id = ?`,
		h.ConversationID).Scan(&owner); err != nil {
		return h, f, err
	}
	switch {
	case !h.Open || h.Revision != ref.Revision || owner.String != h.HandoverID:
		return h, f, busySendGuard(BusySendGuardOwner, "", "the hand-over no longer belongs to this job")
	case ref.PromptIndex != h.PromptCursor || ref.PromptIndex >= h.promptCount:
		return h, f, busySendGuard(BusySendGuardPrompt, "", fmt.Sprintf("prompt %d is not the next prompt", ref.PromptIndex))
	case now > h.IssueDeadline.UnixMilli():
		return h, f, busySendGuard(BusySendGuardDeadline, BusySendWaitReadinessUnknown, "the job's issue deadline has passed")
	}
	if forced {
		if err := checkBusySendForceJob(ctx, conn, storeID, *force, h, ref.PromptIndex, now); err != nil {
			return h, f, err
		}
	}
	f, err = readBusySendIssueFacts(ctx, conn, h.InstanceID)
	if errors.Is(err, ErrSpawnNotFound) {
		return h, f, busySendGuard(BusySendGuardLaunch, BusySendWaitLaunchEnded, "the agent row is gone")
	}
	if err != nil {
		return h, f, err
	}
	switch {
	case f.launchCoverage != BusySendCovered || f.launch != h.Destination.LaunchID || f.token != h.Destination.LaunchToken ||
		f.conversation != h.ConversationID:
		return h, f, busySendGuard(BusySendGuardLaunch, BusySendWaitLaunchEnded, "the captured Launch is no longer the published current Launch")
	case f.launchWait() != "":
		return h, f, busySendGuard(BusySendGuardLaunch, f.launchWait(), "the captured Launch is not running")
	case !f.destinationMatches(h.Destination):
		return h, f, busySendGuard(BusySendGuardDestination, BusySendWaitReadinessUnknown, "the captured session, route or connection changed")
	case !forced && f.turnStarts != h.ExpectedTurnStarts:
		return h, f, busySendGuard(BusySendGuardTurn, BusySendWaitTurnRunning, "a turn started since the claim")
	case !forced && f.permissionHold:
		return h, f, busySendGuard(BusySendGuardPermission, BusySendWaitDialog, "an open permission request holds the agent")
	case !forced && f.permissionEpoch != h.PermissionEpoch:
		return h, f, busySendGuard(BusySendGuardPermission, BusySendWaitReadinessUnknown, "the permission epoch changed since the claim")
	case f.pauseFence:
		return h, f, busySendGuard(BusySendGuardPause, BusySendWaitReadinessUnknown, "a standing pause claim fences the Launch")
	}
	return h, f, nil
}

// checkBusySendForceJob is a force prompt's own authority (SR-4.3, SR-3.4;
// M1's ForceFencedAtCallEnd): the hand-over's force still runs under the
// owner and revision force captured, its call's end is not recorded and its
// recorded call deadline has not passed; and a later prompt goes only once
// the prompt before it is admitted with no negative (its order is secured by
// the cursor check). After the call's end or release nothing of the force
// passes.
func checkBusySendForceJob(ctx context.Context, conn *sql.Conn, storeID string, force BusySendForceRef, h busySendHandoverRow, index int, now int64) error {
	fo, found, err := readBusySendForceRow(ctx, conn, storeID, force.ForceID)
	if err != nil {
		return err
	}
	if !found || fo.Outcome != BusySendForceRunning || fo.OwnerCallID != force.OwnerCallID || fo.Revision != force.Revision ||
		fo.CallEndedAtMillis != 0 || now > fo.CallDeadlineMillis {
		return busySendGuard(BusySendGuardOwner, "", "the force's call no longer owns its hand-over")
	}
	if index == 0 {
		return nil
	}
	prev, err := readBusySendPrompt(ctx, conn, h.HandoverID, index-1)
	if err != nil {
		return err
	}
	if !prev.effectivelyAdmitted() {
		return busySendGuard(BusySendGuardPrompt, "", "the force's earlier prompt has no recorded admission")
	}
	return nil
}

// busySendTurnRunning reports whether a row's raw state is inside a running
// turn: working, or a Dialog or question the turn raised. A force's prompt
// records it as its intent's running-turn fact (b.7si t1.7si.nc SR-1.4).
func busySendTurnRunning(state string) bool {
	switch state {
	case StateWorking, StateCheckPermission, StateAskUser, StateNeedsIntelligence:
		return true
	}
	return false
}

// advanceBusySendHandover compares and advances a hand-over's revision, once
// per transaction that changes it, with set's extra assignments ("" for
// none).
func advanceBusySendHandover(ctx context.Context, q busySendLaunchSQL, h busySendHandoverRow, set string, args ...any) (int64, error) {
	if set != "" {
		set = ", " + set
	}
	err := busySendChange(ctx, q, `UPDATE busy_send_handovers SET revision = revision + 1`+set+`
	    WHERE handover_id = ? AND revision = ?`, append(args, h.HandoverID, h.Revision)...)
	return h.Revision + 1, err
}

// BusySendIssuer is the process that will issue a prompt (SR-3.6, SR-6.2):
// its role among the Launch's owned processes and its identity (PID, process
// start time and PID namespace, as busy_send_launch_processes records them).
// Lifecycle exit evidence covers it, so a hook or delivery job suspended
// between its issue commit and its output is never forgotten (M1 R6).
type BusySendIssuer struct {
	Role         BusySendProcessRole
	PID          int
	StartTime    string
	PIDNamespace string
}

// BusySendIssueRequest asks to commit ref's issue, recording Issuer, when set,
// as the Launch's process in its role. Force is the running force's authority
// for a force hand-over's prompt (ReadBusySendForcePrompt), nil for an
// ordinary one. Now is the caller's clock, the one its hand-over's deadlines
// were fixed on (nil: the wall clock); the commit reads it inside its
// transaction, never before the wait for the write lock.
type BusySendIssueRequest struct {
	Ref    BusySendPromptRef
	Force  *BusySendForceRef
	Issuer *BusySendIssuer
	Now    func() time.Time
}

// clock reads the request's clock: Now, or the zero time (busySendAt's
// wall clock) when it is nil.
func (r BusySendIssueRequest) clock() time.Time {
	if r.Now == nil {
		return time.Time{}
	}
	return r.Now()
}

// BusySendIssue is a committed issue: the route may now call the captured
// destination, once.
type BusySendIssue struct {
	// Revision is the hand-over revision the job holds from here on.
	Revision int64
	// TurnStarts, RunningTurn and PermissionEpoch are this issue's own intent
	// facts, read in its transaction: the row's Turn Start counter, whether
	// the prompt goes into a running turn and the permission epoch (b.7si
	// t1.7si.nc SR-1.3, SR-1.4; E5 decisions 2 and 9). The hand-over stores
	// only its first issue's (intent_turn_starts, intent_running_turn and,
	// for a force, permission_epoch); a later prompt's are returned here and
	// not stored. An ordinary hand-over's later prompts pass only with the
	// same turn and epoch, so theirs equal the stored ones; a force's may
	// differ, since its guard skips those readiness facts.
	TurnStarts      int64
	RunningTurn     bool
	PermissionEpoch int64
	IssuedAt        time.Time
}

// CommitBusySendIssue persists ref's emission intent and captured
// destination and passes its issue revalidation in one transaction on a WAL
// synchronous=FULL connection bounded by ctx, before any possibly irreversible
// route call (SR-2.4; M2 P-C4.3). It is M1's Intent(a) followed at once by
// Revalidate(a), with nothing between them: checkBusySendJob's guard runs
// once, and the prompt must still be only claimed, every Message bound to
// this hand-over. Its time is req's clock, read inside the transaction once
// it holds the write lock, and persisted as the issue's time, so a wait for
// the connection or the lock that outlasts the hand-over's issue deadline
// refuses (deadline_passed) rather than issuing late; the caller bounds ctx by
// that deadline too. In that one
// transaction: the issuer is registered (an
// issuer replaces the Launch's earlier process of its role only when no other
// issued prompt of the Launch is without a recorded order or negative
// outcome, BusySendGuardIssuer otherwise; the agent role is never an
// issuer's); the prompt's emission becomes issued, with its intent and issue
// times; the hand-over's first issue records spawns.turn_starts and whether it
// went into a running turn (false for a turn-end hand-over, the row's actual
// running turn for a force's, read in this transaction), and a force's first
// issue its current permission epoch, while every issue returns its own
// (BusySendIssue); and the Messages
// become issued, take the hand-over's sticky race exposure, keep their
// reservations, show forced for a force's hand-over (its start, SR-4.2) and
// become non-Final sent_unconfirmed bound to the captured Launch and session,
// since output may now reach the provider (SR-5.2). A force's prompt passes
// across turn and permission changes since its claim or earlier prompt; every
// other guard, its destination included, stands (checkBusySendJob). The
// prompt row with emission issued and no admission or negative evidence is
// the durable owned-input obligation the awaiting count and the turn-end
// claim's AD-owned-work gate read (SR-3.3, SR-5.6; readBusySendAwaiting); it
// exists before any output can. Only a verified commit authorizes the one route call.
// A refusal (*BusySendGuardError) writes nothing, so the prompt stays claimed
// and its job may release it; a COMMIT whose outcome is unknown is an error
// CommitUnconfirmed reports and authorizes nothing, and Launch End resolves
// an intent that did land.
func (s *Store) CommitBusySendIssue(ctx context.Context, req BusySendIssueRequest) (BusySendIssue, error) {
	if req.Issuer != nil && (req.Issuer.Role == "" || req.Issuer.Role == BusySendProcessAgent || req.Issuer.PID <= 0 || req.Issuer.StartTime == "") {
		return BusySendIssue{}, fmt.Errorf("store: busy-send issuer needs a non-agent role, a PID and a start time")
	}
	var out BusySendIssue
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		now := busySendAt(req.clock())
		h, f, err := checkBusySendJob(ctx, conn, s.StoreID(), req.Ref, req.Force, now)
		if err != nil {
			return err
		}
		p, err := readBusySendPrompt(ctx, conn, h.HandoverID, req.Ref.PromptIndex)
		if err != nil {
			return err
		}
		claimed, err := busySendPromptMembersIn(ctx, conn, h.HandoverID, p.index, BusySendClaimed)
		if err != nil {
			return err
		}
		if p.emission != BusySendEmissionNone || p.fenced || !claimed {
			return busySendGuard(BusySendGuardPrompt, "", "the prompt is no longer only claimed")
		}
		if req.Issuer != nil {
			if err := registerBusySendIssuer(ctx, conn, s.StoreID(), h, *req.Issuer, now); err != nil {
				return err
			}
		}
		if err := busySendChange(ctx, conn, `UPDATE busy_send_prompts SET emission = 'issued', intent_at_ms = ?, issued_at_ms = ?,
		        revision = revision + 1
		    WHERE handover_id = ? AND prompt_index = ? AND revision = ? AND emission = 'none'`,
			now, now, h.HandoverID, p.index, p.revision); err != nil {
			return err
		}
		forced := h.ForceID != ""
		running := h.Boundary != BusySendBoundaryTurnEnd
		if forced {
			running = busySendTurnRunning(f.state)
		}
		// A force hand-over's stored permission epoch becomes its first
		// prompt's intent fact, as its turn facts do; an ordinary one's is the
		// claim's, which its every issue must still match.
		if out.Revision, err = advanceBusySendHandover(ctx, conn, h, `intent_at_ms = COALESCE(intent_at_ms, ?),
		        intent_turn_starts = COALESCE(intent_turn_starts, ?), intent_running_turn = COALESCE(intent_running_turn, ?),
		        permission_epoch = CASE WHEN forced = 1 AND intent_at_ms IS NULL THEN ? ELSE permission_epoch END`,
			now, f.turnStarts, boolInt(running), f.permissionEpoch); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE busy_send_messages SET place = 'issued', race_exposed = MAX(race_exposed, ?),
		        forced = MAX(forced, ?), result_class = CASE WHEN result_final = 0 THEN 'sent_unconfirmed' ELSE result_class END,
		        bound_launch_id = ?, bound_launch_token = ?, bound_session_id = ?, revision = revision + 1
		    WHERE handover_id = ? AND place = 'claimed' AND `+busySendPromptMemberSQL,
			boolInt(h.RaceExposed), boolInt(forced), h.Destination.LaunchID, nullableStringArg(h.Destination.LaunchToken),
			nullableStringArg(h.Destination.SessionID), h.HandoverID, h.HandoverID, p.index); err != nil {
			return err
		}
		out.TurnStarts, out.RunningTurn, out.PermissionEpoch = f.turnStarts, running, f.permissionEpoch
		out.IssuedAt = time.UnixMilli(now).UTC()
		return nil
	})
	if err != nil {
		return BusySendIssue{}, err
	}
	return out, nil
}

// registerBusySendIssuer records issuer as the Launch's process of its role,
// in the issue's transaction. The same identity is kept as it is.
// Another identity replaces an earlier one only when every other issued
// prompt of the Launch has its order or a negative outcome recorded: the
// earlier issuer can then emit nothing still unresolved. An issuer that may
// still emit is never overwritten. This is the helper slot's serialization
// (Main Agent decision 5): an earlier helper's hand-over closed with its
// outcome recorded permits replacement, which is not exit evidence. A
// replacement on the earlier helper's verified exit alone is never needed on
// a live Launch, whose unresolved prompt still owns the conversation and so
// refuses every claim, and is not offered.
func registerBusySendIssuer(ctx context.Context, conn *sql.Conn, storeID string, h busySendHandoverRow, issuer BusySendIssuer, now int64) error {
	var unresolved bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_prompts p
	    JOIN busy_send_handovers o ON o.handover_id = p.handover_id
	    WHERE o.launch_id = ? AND p.emission IN ('intent', 'issued') AND p.order_kind IS NULL AND p.negative_kind IS NULL)`,
		h.Destination.LaunchID).Scan(&unresolved); err != nil {
		return err
	}
	var pid int
	var start string
	var namespace sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT pid, start_time, pid_namespace FROM busy_send_launch_processes
	    WHERE launch_id = ? AND role = ?`, h.Destination.LaunchID, string(issuer.Role)).Scan(&pid, &start, &namespace)
	switch {
	case err == nil && pid == issuer.PID && start == issuer.StartTime && namespace.String == issuer.PIDNamespace:
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	case err == nil && unresolved:
		return busySendGuard(BusySendGuardIssuer, BusySendWaitHandoverInProgress, "an earlier issuer's prompt has no recorded outcome")
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO busy_send_launch_processes (launch_id, role, store_id, pid, start_time, pid_namespace, recorded_at_ms)
	    VALUES (?, ?, ?, ?, ?, ?, ?)
	    ON CONFLICT(launch_id, role) DO UPDATE SET pid = excluded.pid, start_time = excluded.start_time,
	        pid_namespace = excluded.pid_namespace, endpoint = NULL, recorded_at_ms = excluded.recorded_at_ms`,
		h.Destination.LaunchID, string(issuer.Role), storeID, issuer.PID, issuer.StartTime,
		nullableStringArg(issuer.PIDNamespace), now)
	return err
}

// BusySendOutput is what a route observed of its own output for an issued
// prompt (SR-2.4, SR-3.2). complete: the whole payload was written; a
// preparation, a serialization, a hook's start or a wake-file touch is never
// complete. not_submitted: proof that nothing reached the provider, the
// route's qualified explicit not-submitted (M1 R1); missing proof, an error
// or elapsed time is never it. partial: some but not all bytes were written.
// uncertain: the route cannot tell (a failed or short write, a lost reply,
// process loss).
type BusySendOutput string

const (
	BusySendOutputComplete     BusySendOutput = "complete"
	BusySendOutputNotSubmitted BusySendOutput = "not_submitted"
	BusySendOutputPartial      BusySendOutput = "partial"
	BusySendOutputUncertain    BusySendOutput = "uncertain"
)

// transport is the prompt transport fact an output records.
func (o BusySendOutput) transport() (BusySendTransport, error) {
	switch o {
	case BusySendOutputComplete:
		return BusySendTransportWritten, nil
	case BusySendOutputNotSubmitted:
		return BusySendTransportUnwritten, nil
	case BusySendOutputPartial:
		return BusySendTransportPartial, nil
	case BusySendOutputUncertain:
		return BusySendTransportUncertain, nil
	}
	return "", fmt.Errorf("store: unknown busy-send output %q", o)
}

// BusySendCompletion is the issuing job's report of its route call for the
// prompt ref names. The report is the prompt's own evidence, authorized by
// that prompt's issue identity (its hand-over, its index and its emission
// issued), not by ref.Revision: a release or expiry of the hand-over's
// never-issued remainder meanwhile does not void it (M1: Expire touches only
// unissued claims; RecAcc and RecNs apply to the issued prompt). ref.Revision
// decides only whether the job keeps authority to go on with the hand-over
// (BusySendCompletionResult.Revision). Order is qualified order evidence observed with complete output (Claude's
// complete keyless emission is emission_complete, M1's non-admitting acc),
// "" for none; UnprovenBoundary says that complete output ends a confirmed
// Message at the route's qualified handed_over_not_proven boundary (SR-3.5).
type BusySendCompletion struct {
	Ref              BusySendPromptRef
	Output           BusySendOutput
	Order            BusySendOrderKind
	UnprovenBoundary bool
	At               time.Time
}

// BusySendCompletionResult is what a completion or evidence write changed.
type BusySendCompletionResult struct {
	// Revision is the hand-over revision the completing job holds from here
	// on, to go on with its hand-over: set only when the job's own revision
	// was still the hand-over's when its completion was recorded, 0 when
	// another step (an expiry of the remainder, a fence) took the hand-over's
	// authority meanwhile, and always 0 from evidence, which grants none.
	Revision int64
	// Recorded is whether any fact or custody changed; false for a repeat.
	Recorded bool
	// OrderSecured is whether the prompt's order is secured, by this write or
	// an earlier one, so the job may take the next prompt.
	OrderSecured bool
	// Returned is the Messages an explicit not-submitted returned to Held.
	Returned int
	// Closed is whether the hand-over no longer owns order and transport.
	Closed bool
}

// RecordBusySendCompletion records the issuing job's observed output for ref
// (SR-2.4, SR-2.5; M2 P-C4.3), in one transaction on a WAL synchronous=FULL
// connection bounded by ctx. The prompt must be issued. Its transport fact is
// recorded once: a repeat of the same output changes nothing, and a later
// different report keeps the first. A fact recorded after a Message of the
// prompt is Final is stamped after that Final time, so it never enters that
// Final Result's projection (readBusySendMessageResult): a Final Result, its
// compatibility outcome included, never changes. On the live Launch, with the
// Messages still issued in this hand-over, complete output
// with qualified Order secures the prompt's order (M1's RecAcc): the Messages
// leave custody, their reservations end and their Results follow the
// acceptance rules; the cursor moves to the next prompt and the hand-over
// closes after its last. not_submitted returns the prompt's and every later
// never-issued prompt's Messages to Held (M1's RecNs); for a force's prompt it
// no longer counts as issued, and a force that issued nothing else is refused
// not_submitted (settleBusySendForceEvidence). Partial, uncertain and
// unqualified complete output keep custody and the order barrier: nothing is
// re-emitted, and only Launch End and exit evidence resolve them.
func (s *Store) RecordBusySendCompletion(ctx context.Context, c BusySendCompletion) (BusySendCompletionResult, error) {
	transport, err := c.Output.transport()
	if err != nil {
		return BusySendCompletionResult{}, err
	}
	if c.Order != "" && c.Output != BusySendOutputComplete {
		return BusySendCompletionResult{}, fmt.Errorf("store: busy-send order evidence needs complete output")
	}
	var out BusySendCompletionResult
	err = s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		now := busySendAt(c.At)
		h, err := readBusySendHandover(ctx, conn, s.StoreID(), c.Ref.HandoverID)
		if err != nil {
			return fmt.Errorf("store: busy-send completion: %w", err)
		}
		p, err := readBusySendPrompt(ctx, conn, h.HandoverID, c.Ref.PromptIndex)
		if err != nil {
			return fmt.Errorf("store: busy-send completion: %w", err)
		}
		if p.emission != BusySendEmissionIssued {
			return fmt.Errorf("store: busy-send completion for a prompt that was never issued")
		}
		if p.transport == "" {
			// A Final Message of the prompt already fixed its projection: a
			// later fact is stamped after that Final time, never before.
			var finalAt sql.NullInt64
			if err := conn.QueryRowContext(ctx, `SELECT MAX(final_at_ms) FROM busy_send_messages
			    WHERE result_final = 1 AND `+busySendPromptMemberSQL, h.HandoverID, p.index).Scan(&finalAt); err != nil {
				return err
			}
			at := now
			if finalAt.Valid && finalAt.Int64 >= at {
				at = finalAt.Int64 + 1
			}
			if err := busySendChange(ctx, conn, `UPDATE busy_send_prompts SET transport = ?, transport_at_ms = ?, revision = revision + 1
			    WHERE handover_id = ? AND prompt_index = ? AND revision = ? AND transport IS NULL`,
				string(transport), at, h.HandoverID, p.index, p.revision); err != nil {
				return err
			}
			out.Recorded, p.transport = true, transport
			p.revision++
		}
		held := h.Revision == c.Ref.Revision
		live, err := busySendLive(ctx, conn, h, p.index, BusySendIssued)
		if err != nil {
			return err
		}
		if live && p.transport == transport {
			switch {
			case c.Output == BusySendOutputComplete && c.Order != "" && p.orderKind == "":
				err = applyBusySendOrder(ctx, conn, &h, p, c.Order, c.UnprovenBoundary, now)
				out.Recorded = true
			case c.Output == BusySendOutputNotSubmitted && p.negative == "":
				if out.Returned, err = returnBusySendNotSubmitted(ctx, conn, &h, p, now); err == nil {
					err = settleBusySendForceEvidence(ctx, conn, s.StoreID(), h.HandoverID, now)
				}
				out.Recorded = true
			}
			if err != nil {
				return err
			}
		}
		if err := readBusySendCompletionState(ctx, conn, h.HandoverID, p.index, &out); err != nil || held {
			return err
		}
		out.Revision = 0
		return nil
	})
	if err != nil {
		return BusySendCompletionResult{}, err
	}
	return out, nil
}

// busySendLive is M1's LiveCb with its v check: the hand-over's captured
// Launch is still the published current covered Launch and running, and every
// Message of the prompt is still in place in this hand-over.
func busySendLive(ctx context.Context, conn *sql.Conn, h busySendHandoverRow, index int, place BusySendPlace) (bool, error) {
	f, err := readBusySendIssueFacts(ctx, conn, h.InstanceID)
	if errors.Is(err, ErrSpawnNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if f.launch != h.Destination.LaunchID || f.conversation != h.ConversationID || f.launchWait() != "" || !h.Open {
		return false, nil
	}
	return busySendPromptMembersIn(ctx, conn, h.HandoverID, index, place)
}

func readBusySendCompletionState(ctx context.Context, conn *sql.Conn, handoverID string, index int, out *BusySendCompletionResult) error {
	var state string
	var order sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT h.revision, h.state, p.order_kind FROM busy_send_handovers h
	    JOIN busy_send_prompts p ON p.handover_id = h.handover_id AND p.prompt_index = ?
	    WHERE h.handover_id = ?`, index, handoverID).Scan(&out.Revision, &state, &order)
	out.Closed, out.OrderSecured = BusySendHandoverState(state) == BusySendHandoverClosed, order.Valid
	return err
}

// applyBusySendOrder secures p's order with kind (M1's first order update):
// its issued Messages leave custody with their reservations, record when they
// were handed over and take the acceptance Result. An unconfirmed Message
// becomes Final sent_unconfirmed (confirmation_off, or the sticky
// wait_turn_raced); a confirmed one does too, as handed_over_not_proven, at
// the route's qualified unproven boundary, and otherwise stays non-Final
// sent_unconfirmed awaiting proof. The cursor moves past p, and the hand-over
// closes once nothing of it is in custody. A Final Result is never changed.
func applyBusySendOrder(ctx context.Context, conn *sql.Conn, h *busySendHandoverRow, p busySendPromptRow, kind BusySendOrderKind, unproven bool, now int64) error {
	if err := busySendChange(ctx, conn, `UPDATE busy_send_prompts SET order_kind = ?, order_at_ms = ?, revision = revision + 1
	    WHERE handover_id = ? AND prompt_index = ? AND revision = ? AND order_kind IS NULL`,
		string(kind), now, h.HandoverID, p.index, p.revision); err != nil {
		return err
	}
	members := `handover_id = ? AND place = 'issued' AND ` + busySendPromptMemberSQL
	args := []any{h.HandoverID, h.HandoverID, p.index}
	if err := freeBusySendReservations(ctx, conn, h.ConversationID, members, args...); err != nil {
		return err
	}
	if unproven {
		if err := finalizeBusySendSent(ctx, conn, BusySendReasonHandedOverNotProven, now, members, args...); err != nil {
			return err
		}
	} else {
		if err := finalizeBusySendSent(ctx, conn, BusySendReasonHandedOverNotProven, now, `confirmed = 0 AND `+members, args...); err != nil {
			return err
		}
		if err := setBusySendWaits(ctx, conn, BusySendWaitAwaitingProof, now, members, args...); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE busy_send_messages SET place = 'closed', handover_id = NULL,
	        handed_over_at_ms = COALESCE(handed_over_at_ms, ?), revision = revision + 1 WHERE `+members,
		append([]any{now}, args...)...); err != nil {
		return err
	}
	cursor := h.PromptCursor
	if cursor == p.index {
		cursor++
	}
	var err error
	if h.Revision, err = advanceBusySendHandover(ctx, conn, *h, `prompt_cursor = ?`, cursor); err != nil {
		return err
	}
	h.PromptCursor = cursor
	if err := closeBusySendHandoverIfDone(ctx, conn, h, now); err != nil {
		return err
	}
	return refreshBusySendHeldWaits(ctx, conn, h.ConversationID, BusySendWaitReadinessUnknown, now)
}

// returnBusySendNotSubmitted applies a provider's explicit not-submitted for
// p (M1's RecNs; SR-2.4): p's Messages and those of every later prompt still
// only claimed return to Held in accepted order, current membership cleared
// and history kept, reservations kept; a Message already Final keeps its
// Result and leaves custody instead. p records the negative; the later
// prompts are released as never issued. It returns how many Messages are
// Held again.
func returnBusySendNotSubmitted(ctx context.Context, conn *sql.Conn, h *busySendHandoverRow, p busySendPromptRow, now int64) (int, error) {
	if err := busySendChange(ctx, conn, `UPDATE busy_send_prompts SET negative_kind = 'not_submitted', negative_at_ms = ?, revision = revision + 1
	    WHERE handover_id = ? AND prompt_index = ? AND revision = ? AND negative_kind IS NULL`,
		now, h.HandoverID, p.index, p.revision); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE busy_send_prompts SET emission = 'released', released_at_ms = ?, revision = revision + 1
	    WHERE handover_id = ? AND prompt_index > ? AND emission = 'none'`, now, h.HandoverID, p.index); err != nil {
		return 0, err
	}
	members := `handover_id = ? AND message_id IN (SELECT message_id FROM busy_send_handover_members
	    WHERE handover_id = ? AND (prompt_index = ? OR prompt_index IN (SELECT prompt_index FROM busy_send_prompts
	        WHERE handover_id = ? AND prompt_index > ? AND emission = 'released')))`
	args := []any{h.HandoverID, h.HandoverID, p.index, h.HandoverID, p.index}
	if _, err := conn.ExecContext(ctx, `UPDATE busy_send_messages SET place = 'closed', handover_id = NULL, revision = revision + 1
	    WHERE result_final = 1 AND `+members, args...); err != nil {
		return 0, err
	}
	n, err := returnBusySendHeld(ctx, conn, members, args...)
	if err != nil {
		return 0, err
	}
	if h.Revision, err = advanceBusySendHandover(ctx, conn, *h, ""); err != nil {
		return 0, err
	}
	if err := closeBusySendHandoverIfDone(ctx, conn, h, now); err != nil {
		return 0, err
	}
	return n, refreshBusySendHeldWaits(ctx, conn, h.ConversationID, BusySendWaitReadinessUnknown, now)
}

// returnBusySendHeld returns every Message where selects to Held: no current
// membership (its busy_send_handover_members rows stay as its earlier
// attempts), its reservation kept, its Result held and no longer forced, since
// its next hand-over decides that (SR-2.1, SR-4.2). Its caller records the
// waits (refreshBusySendHeldWaits) once the hand-over's state is settled.
func returnBusySendHeld(ctx context.Context, conn *sql.Conn, where string, args ...any) (int, error) {
	r, err := conn.ExecContext(ctx, `UPDATE busy_send_messages SET place = 'held', handover_id = NULL, result_class = 'held', forced = 0,
	        bound_launch_id = NULL, bound_launch_token = NULL, bound_session_id = NULL, revision = revision + 1
	    WHERE result_final = 0 AND place IN ('claimed', 'issued') AND `+where, args...)
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	return int(n), err
}

// closeBusySendHandoverIfDone closes h once none of its Messages is still in
// custody (claimed or issued), and frees its conversation's ownership, so a
// later eligible event may claim again. It does not advance h's revision;
// its caller's transaction already did.
func closeBusySendHandoverIfDone(ctx context.Context, conn *sql.Conn, h *busySendHandoverRow, now int64) error {
	var custody bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_messages WHERE handover_id = ?)`,
		h.HandoverID).Scan(&custody); err != nil || custody {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE busy_send_handovers SET state = 'closed', closed_at_ms = ?
	    WHERE handover_id = ? AND state = 'open'`, now, h.HandoverID); err != nil {
		return err
	}
	h.Open = false
	_, err := conn.ExecContext(ctx, `UPDATE busy_conversations SET owner_handover_id = NULL, revision = revision + 1
	    WHERE conversation_id = ? AND owner_handover_id = ?`, h.ConversationID, h.HandoverID)
	return err
}

// BusySendReceipt is a qualified exact receipt's identity and digest
// (SR-5.1), never raw evidence.
type BusySendReceipt struct {
	Kind         BusySendEvidenceKind
	RecordID     string
	RecordSHA256 string
}

// BusySendPromptEvidence is a route callback's evidence for one issued
// prompt (M1's Record; R7): the prompt and the destination it names (Launch
// and connection generation), with any of the route's qualified ordered
// acceptance (Order, M1's acc, with UnprovenBoundary as for a completion),
// admission event (Admission, M1's ad) and exact receipt (Receipt, M1's rc).
type BusySendPromptEvidence struct {
	HandoverID           string
	PromptIndex          int
	LaunchID             string
	ConnectionGeneration int64
	Order                BusySendOrderKind
	UnprovenBoundary     bool
	Admission            BusySendAdmissionKind
	Receipt              *BusySendReceipt
	At                   time.Time
}

// RecordBusySendEvidence records a route callback's evidence by its prompt's
// identity, in one transaction on a WAL synchronous=FULL connection bounded by
// ctx. Evidence naming another destination, or a prompt never issued, changes
// nothing. Each fact is recorded once, so a repeated acceptance, admission or
// receipt changes nothing the second time (M1 "repeats are inert"). An
// admission changes no custody or Result (it never releases an order
// barrier); it is recorded by the prompt's identity, whatever its Messages'
// custody or Results, and the last missing admission of a pending force makes
// it handed_over (settleBusySendForceEvidence). On the live Launch, with the
// Messages still bound to this
// hand-over: the first acceptance secures order as a completion's does; a
// receipt secures order if needed and makes each non-Final confirmed Message
// Final delivered_confirmed (exact_receipt) and each unconfirmed one Final
// sent_unconfirmed. Late evidence for an ended or replaced Launch records its
// fact only; no Final Result is reopened.
func (s *Store) RecordBusySendEvidence(ctx context.Context, ev BusySendPromptEvidence) (BusySendCompletionResult, error) {
	var out BusySendCompletionResult
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		out, err = recordBusySendEvidence(ctx, conn, s.StoreID(), ev, busySendAt(ev.At))
		return err
	})
	if err != nil {
		return BusySendCompletionResult{}, err
	}
	out.Revision = 0
	return out, nil
}

// recordBusySendEvidence is RecordBusySendEvidence's work inside its caller's
// transaction (RecordBusySendSighting's too), at now.
func recordBusySendEvidence(ctx context.Context, conn *sql.Conn, storeID string, ev BusySendPromptEvidence, now int64) (BusySendCompletionResult, error) {
	var out BusySendCompletionResult
	h, err := readBusySendHandover(ctx, conn, storeID, ev.HandoverID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	p, err := readBusySendPrompt(ctx, conn, h.HandoverID, ev.PromptIndex)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if ev.LaunchID != h.Destination.LaunchID || ev.ConnectionGeneration != h.Destination.ConnectionGeneration ||
		p.emission != BusySendEmissionIssued {
		err := readBusySendCompletionState(ctx, conn, h.HandoverID, p.index, &out)
		return out, err
	}
	admission := ev.Admission
	if ev.Receipt != nil && admission == "" {
		admission = BusySendAdmissionExactReceipt
	}
	if admission != "" && p.admission == "" {
		if err := busySendChange(ctx, conn, `UPDATE busy_send_prompts SET admission_kind = ?, admitted_at_ms = ?, revision = revision + 1
		    WHERE handover_id = ? AND prompt_index = ? AND revision = ? AND admission_kind IS NULL`,
			string(admission), now, h.HandoverID, p.index, p.revision); err != nil {
			return out, err
		}
		out.Recorded, p.admission = true, admission
		p.revision++
	}
	if ev.Receipt != nil && p.evidence == "" {
		r := ev.Receipt
		if err := busySendChange(ctx, conn, `UPDATE busy_send_prompts SET evidence_kind = ?, record_id = ?, record_sha256 = ?,
		        receipt_at_ms = ?, revision = revision + 1
		    WHERE handover_id = ? AND prompt_index = ? AND revision = ? AND evidence_kind IS NULL`,
			string(r.Kind), nullableStringArg(r.RecordID), nullableStringArg(r.RecordSHA256), now,
			h.HandoverID, p.index, p.revision); err != nil {
			return out, err
		}
		out.Recorded = true
		p.revision++
		if err := applyBusySendReceipt(ctx, conn, &h, p, *r, now); err != nil {
			return out, err
		}
	} else if ev.Order != "" && p.orderKind == "" {
		live, err := busySendLive(ctx, conn, h, p.index, BusySendIssued)
		if err != nil {
			return out, err
		}
		if live {
			if err := applyBusySendOrder(ctx, conn, &h, p, ev.Order, ev.UnprovenBoundary, now); err != nil {
				return out, err
			}
			out.Recorded = true
		}
	}
	if out.Recorded {
		if err := settleBusySendForceEvidence(ctx, conn, storeID, h.HandoverID, now); err != nil {
			return out, err
		}
	}
	err = readBusySendCompletionState(ctx, conn, h.HandoverID, p.index, &out)
	return out, err
}

// applyBusySendReceipt applies an exact receipt for p on the live Launch (M1's
// RecRc): an issued prompt's order is secured first, then every non-Final
// Message of p still bound to it, issued here or closed by this hand-over,
// becomes Final: delivered_confirmed with the receipt's identity when
// confirmed, sent_unconfirmed when not.
func applyBusySendReceipt(ctx context.Context, conn *sql.Conn, h *busySendHandoverRow, p busySendPromptRow, r BusySendReceipt, now int64) error {
	f, err := readBusySendIssueFacts(ctx, conn, h.InstanceID)
	if errors.Is(err, ErrSpawnNotFound) {
		return nil
	}
	if err != nil || f.launch != h.Destination.LaunchID || f.launchWait() != "" {
		return err
	}
	if p.orderKind == "" {
		issued, err := busySendPromptMembersIn(ctx, conn, h.HandoverID, p.index, BusySendIssued)
		if err != nil {
			return err
		}
		if issued && h.Open {
			if err := applyBusySendOrder(ctx, conn, h, p, BusySendOrderExactReceipt, false, now); err != nil {
				return err
			}
		}
	}
	bound := `bound_launch_id = ? AND ` + busySendPromptMemberSQL + ` AND (handover_id = ? OR (place = 'closed'
	    AND (SELECT h2.handover_id FROM busy_send_handover_members hm2 JOIN busy_send_handovers h2 ON h2.handover_id = hm2.handover_id
	         WHERE hm2.message_id = busy_send_messages.message_id ORDER BY h2.handover_seq DESC LIMIT 1) = ?))`
	args := []any{h.Destination.LaunchID, h.HandoverID, p.index, h.HandoverID, h.HandoverID}
	if _, err := conn.ExecContext(ctx, `UPDATE busy_send_messages SET result_class = 'delivered_confirmed', result_final = 1,
	        result_reason = 'exact_receipt', final_at_ms = ?, delivered_at_ms = ?, evidence_kind = ?, record_id = ?,
	        record_sha256 = ?, wait_reason = NULL, wait_since_ms = NULL, wait_earlier_message_id = NULL, revision = revision + 1
	    WHERE result_final = 0 AND confirmed = 1 AND `+bound,
		append([]any{now, now, string(r.Kind), nullableStringArg(r.RecordID), nullableStringArg(r.RecordSHA256)}, args...)...); err != nil {
		return err
	}
	return finalizeBusySendSent(ctx, conn, BusySendReasonConfirmationOff, now, `confirmed = 0 AND `+bound, args...)
}

// errBusySendNotExpired refuses an expiry release before the issue deadline.
var errBusySendNotExpired = errors.New("store: busy-send hand-over's issue deadline has not passed")

// BusySendReleaseRequest releases a hand-over's never-issued claims. With
// Revision set, the releaser is the job that holds it; with Revision 0, the
// hand-over's original issue deadline must have passed by At (zero: now), the
// abandoned-job release M1's ReleaseExpired is. Wait is the returned
// Messages' wait (readiness_unknown when "").
type BusySendReleaseRequest struct {
	HandoverID string
	Revision   int64
	Wait       BusySendWaitReason
	At         time.Time
}

// BusySendRelease is a committed release.
type BusySendRelease struct {
	Released int
	Closed   bool
	Revision int64
}

// ReleaseBusySendClaims returns a hand-over's never-issued claims to Held
// (SR-2.4), in one transaction on a WAL synchronous=FULL connection bounded by
// ctx: every prompt still with no emission intent is released, its Messages
// return to Held in accepted order with current membership cleared, history
// kept and reservations kept. With no such prompt it changes nothing, the
// hand-over's revision included. The positive never-issued evidence is that an
// intent commit compares the hand-over revision, which this advances, and the
// issue deadline: no intent of these prompts can commit afterwards. A prompt
// with persisted intent is never released here, whatever its job's fate. The
// hand-over closes once nothing of it is in custody. An owner release with a
// stale revision is a *BusySendGuardError; an expiry release before the
// deadline, or of a force's hand-over at any time, changes nothing and is an
// error.
func (s *Store) ReleaseBusySendClaims(ctx context.Context, req BusySendReleaseRequest) (BusySendRelease, error) {
	var out BusySendRelease
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		wait := req.Wait
		if wait == "" {
			wait = BusySendWaitReadinessUnknown
		}
		var err error
		out, err = releaseBusySendClaims(ctx, conn, s.StoreID(), req.HandoverID, req.Revision, wait, busySendAt(req.At))
		return err
	})
	if err != nil {
		return BusySendRelease{}, err
	}
	return out, nil
}

func releaseBusySendClaims(ctx context.Context, conn *sql.Conn, storeID, handoverID string, revision int64, wait BusySendWaitReason, now int64) (BusySendRelease, error) {
	h, err := readBusySendHandover(ctx, conn, storeID, handoverID)
	if err != nil {
		return BusySendRelease{}, err
	}
	out := BusySendRelease{Closed: !h.Open, Revision: h.Revision}
	switch {
	case revision != 0 && (revision != h.Revision || !h.Open):
		return out, busySendGuard(BusySendGuardOwner, "", "the hand-over no longer belongs to this job")
	case !h.Open:
		return out, nil
	case revision == 0 && (h.ForceID != "" || now <= h.IssueDeadline.UnixMilli()):
		// A force's claims belong to its call: only the force's own
		// settlement (EndBusySendForceCall, ReleaseAbandonedBusySendForce,
		// Launch End) returns them, never generic expiry.
		return out, errBusySendNotExpired
	}
	r, err := conn.ExecContext(ctx, `UPDATE busy_send_prompts SET emission = 'released', released_at_ms = ?, revision = revision + 1
	    WHERE handover_id = ? AND emission = 'none'`, now, h.HandoverID)
	if err != nil {
		return out, err
	}
	if n, err := r.RowsAffected(); err != nil || n == 0 {
		// Nothing never-issued was left: a no-op changes no revision, so an
		// issued prompt's job keeps its authority.
		return out, err
	}
	members := `handover_id = ? AND place = 'claimed' AND message_id IN (SELECT hm.message_id FROM busy_send_handover_members hm
	    JOIN busy_send_prompts p ON p.handover_id = hm.handover_id AND p.prompt_index = hm.prompt_index
	    WHERE hm.handover_id = ? AND p.emission = 'released')`
	if out.Released, err = returnBusySendHeld(ctx, conn, members, h.HandoverID, h.HandoverID); err != nil {
		return out, err
	}
	if h.Revision, err = advanceBusySendHandover(ctx, conn, h, ""); err != nil {
		return out, err
	}
	if err := closeBusySendHandoverIfDone(ctx, conn, &h, now); err != nil {
		return out, err
	}
	out.Closed, out.Revision = !h.Open, h.Revision
	return out, refreshBusySendHeldWaits(ctx, conn, h.ConversationID, wait, now)
}

// applyBusySendLaunchEnd is the inbox's custody disposition at Launch End
// (SR-6.2; M1's EndLaunch, G4, sf-answer-2), for a lifecycle writer to
// compose into the transaction that records the Launch End of launchID. Per
// prompt of each of the Launch's open hand-overs: a prompt with no persisted
// intent is released and its claimed Messages return to Held in accepted
// order, to carry to resume; a prompt with persisted intent, issued or not,
// is fenced: its Messages stay in custody, so the order barrier stands, and
// each non-Final one becomes Final sent_unconfirmed (wait_turn_raced,
// confirmation_off or launch_ended) and is never carried. Messages this
// Launch handed over still awaiting proof become Final the same way, and Held
// Messages wait launch_ended. The Launch's open forces then finish
// (endBusySendLaunchForces): a pending one failed launch_ended. Every
// hand-over's revision advances, so no old job persists intent or passes
// revalidation. Only exit evidence
// (applyBusySendLaunchExit) lifts a fence; a store fence, the absence of a
// passed revalidation, an ended or missing row or elapsed time never does.
func applyBusySendLaunchEnd(ctx context.Context, conn *sql.Conn, storeID, launchID string, now int64) error {
	var conversation string
	if err := conn.QueryRowContext(ctx, `SELECT conversation_id FROM busy_send_launches WHERE launch_id = ? AND store_id = ?`,
		launchID, storeID).Scan(&conversation); err != nil {
		return fmt.Errorf("store: busy-send Launch End: %w", err)
	}
	ids, err := readBusySendOpenHandovers(ctx, conn, launchID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		h, err := readBusySendHandover(ctx, conn, storeID, id)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE busy_send_prompts SET emission = 'released', released_at_ms = ?, revision = revision + 1
		    WHERE handover_id = ? AND emission = 'none'`, now, id); err != nil {
			return err
		}
		if _, err := returnBusySendHeld(ctx, conn, `handover_id = ? AND place = 'claimed'`, id); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE busy_send_prompts SET fenced_at_ms = ?, revision = revision + 1
		    WHERE handover_id = ? AND emission IN ('intent', 'issued') AND fenced_at_ms IS NULL`, now, id); err != nil {
			return err
		}
		issued := `handover_id = ? AND place = 'issued'`
		if err := freeBusySendReservations(ctx, conn, conversation, issued, id); err != nil {
			return err
		}
		if err := finalizeBusySendSent(ctx, conn, BusySendReasonLaunchEnded, now, issued, id); err != nil {
			return err
		}
		if h.Revision, err = advanceBusySendHandover(ctx, conn, h, ""); err != nil {
			return err
		}
		if err := closeBusySendHandoverIfDone(ctx, conn, &h, now); err != nil {
			return err
		}
	}
	if err := finalizeBusySendSent(ctx, conn, BusySendReasonLaunchEnded, now, `place = 'closed' AND bound_launch_id = ?`, launchID); err != nil {
		return err
	}
	if err := endBusySendLaunchForces(ctx, conn, storeID, launchID, now); err != nil {
		return err
	}
	return setBusySendWaits(ctx, conn, BusySendWaitLaunchEnded, now, `conversation_id = ? AND place = 'held'`, conversation)
}

// applyBusySendLaunchExit lifts the fences of launchID's intent-bearing
// prompts (M1's Exit), for the shared exit-evidence operation to compose into
// its transaction only under positive evidence: identity-checked exit of every
// process the Launch owns that can emit (busy_send_launch_processes, the
// agent and every recorded issuer) together with its captured destination's
// fencing (R6, R8). The Launch's End must already be recorded. Its fenced
// Messages leave custody and its hand-overs close, so its conversation may be
// claimed again after a resume; their Final Results are unchanged.
func applyBusySendLaunchExit(ctx context.Context, conn *sql.Conn, storeID, launchID string, now int64) error {
	var ended sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT ended_at_ms FROM busy_send_launches WHERE launch_id = ? AND store_id = ?`,
		launchID, storeID).Scan(&ended); err != nil {
		return fmt.Errorf("store: busy-send Launch exit: %w", err)
	}
	if !ended.Valid {
		return fmt.Errorf("store: busy-send Launch exit before its Launch End")
	}
	ids, err := readBusySendOpenHandovers(ctx, conn, launchID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		h, err := readBusySendHandover(ctx, conn, storeID, id)
		if err != nil {
			return err
		}
		var claimed bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_messages WHERE handover_id = ? AND place = 'claimed')`,
			id).Scan(&claimed); err != nil {
			return err
		}
		if claimed {
			return fmt.Errorf("store: busy-send Launch exit before its Launch End custody")
		}
		if err := finalizeBusySendSent(ctx, conn, BusySendReasonLaunchEnded, now, `handover_id = ? AND place = 'issued'`, id); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE busy_send_messages SET place = 'closed', handover_id = NULL, revision = revision + 1
		    WHERE handover_id = ? AND place = 'issued'`, id); err != nil {
			return err
		}
		if h.Revision, err = advanceBusySendHandover(ctx, conn, h, ""); err != nil {
			return err
		}
		if err := closeBusySendHandoverIfDone(ctx, conn, &h, now); err != nil {
			return err
		}
	}
	return nil
}

func readBusySendOpenHandovers(ctx context.Context, conn *sql.Conn, launchID string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT handover_id FROM busy_send_handovers WHERE launch_id = ? AND state = 'open'
	    ORDER BY handover_seq`, launchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// BusySendLaunchProcess is one process a Launch owns
// (busy_send_launch_processes): the agent or an issuer, with its identity.
type BusySendLaunchProcess struct {
	Role         BusySendProcessRole
	PID          int
	StartTime    string
	PIDNamespace string
	Endpoint     string
}

// busySendLaunchFacts is what exit evidence must cover for one Launch (SR-6.2;
// M1 R6, R8): every process it owns (busy_send_launch_processes, in role
// order), the distinct route kinds of its hand-overs still open, whose
// captured destinations the evidence must fence, and whether any prompt of
// any of its hand-overs ever persisted emission intent (whether AD ever
// issued input for it). readBusySendLaunchFacts is the one production reading
// of them: the instance-level exit scope (ReadBusySendExitScope) is built from
// it.
type busySendLaunchFacts struct {
	processes []BusySendLaunchProcess
	routes    []BusySendRouteKind
	issued    bool
}

// readBusySendLaunchFacts reads launchID's exit facts in the caller's read
// transaction on conn, from durable records only: no process probe, provider
// call or write.
func readBusySendLaunchFacts(ctx context.Context, conn *sql.Conn, launchID string) (busySendLaunchFacts, error) {
	var out busySendLaunchFacts
	rows, err := conn.QueryContext(ctx, `SELECT role, pid, start_time, pid_namespace, endpoint FROM busy_send_launch_processes
	    WHERE launch_id = ? ORDER BY role`, launchID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p BusySendLaunchProcess
		var role string
		var namespace, endpoint sql.NullString
		if err := rows.Scan(&role, &p.PID, &p.StartTime, &namespace, &endpoint); err != nil {
			rows.Close()
			return out, err
		}
		p.Role, p.PIDNamespace, p.Endpoint = BusySendProcessRole(role), namespace.String, endpoint.String
		out.processes = append(out.processes, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	routes, err := conn.QueryContext(ctx, `SELECT DISTINCT route_kind FROM busy_send_handovers
	    WHERE launch_id = ? AND state = 'open' ORDER BY route_kind`, launchID)
	if err != nil {
		return out, err
	}
	for routes.Next() {
		var kind string
		if err := routes.Scan(&kind); err != nil {
			routes.Close()
			return out, err
		}
		out.routes = append(out.routes, BusySendRouteKind(kind))
	}
	if err := routes.Err(); err != nil {
		routes.Close()
		return out, err
	}
	if err := routes.Close(); err != nil {
		return out, err
	}
	err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_prompts p
	    JOIN busy_send_handovers h ON h.handover_id = p.handover_id
	    WHERE h.launch_id = ? AND p.intent_at_ms IS NOT NULL)`, launchID).Scan(&out.issued)
	return out, err
}
