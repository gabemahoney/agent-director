package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// The inbox's claim (SR-2.1 to SR-2.3; M1's Claim): one transaction freezes the
// eligible contiguous prefix of a conversation's Held Messages at an
// accepted-sequence cutoff into a new hand-over, with its prompt partition, its
// captured destination and epochs and its finite job's original deadlines. The
// hand-over is the conversation's one owner of order and transport until it
// closes. Nothing here makes a provider, process or file call; the frozen
// prompts reach emission code only after the commit is verified.

// BusySendDestination is the destination a route authenticated for an event
// and a hand-over captures (SR-2.1, SR-2.4): the Launch, its provider session,
// route identity and connection generation, and the provider turn id where the
// route has one. Dispatch goes only to it; revalidation compares it with the
// current facts.
type BusySendDestination struct {
	LaunchID, LaunchToken string
	Provider              string
	SessionID             string
	RouteKind             BusySendRouteKind
	RouteID               string
	ConnectionGeneration  int64
	TurnID                string
}

// BusySendClaimRequest is an eligible event's claim. Destination and
// RaceExposed come from the route's eligibility; the claim compares the
// destination with the published current Launch and its recorded route, so an
// event cannot claim for any other Launch, session or connection.
type BusySendClaimRequest struct {
	InstanceID  string
	Destination BusySendDestination
	// Boundary is the event's boundary: turn_end claims every Held Message,
	// tool_boundary the contiguous Steer Turn prefix. A force is E5's.
	Boundary BusySendBoundary
	// RaceExposed is SR-3.3a's boundary-race exposure the route reports.
	RaceExposed bool
	// Measure is the route's complete-payload measure (nil: the mode's own
	// measure from the capability table, BusySendModeLimits.Measure). The
	// limits are the capability table's.
	Measure BusySendPromptMeasure
	// IssueBudget and JobBudget are the finite job's original budgets from
	// At: no intent or revalidation passes after the issue deadline, and the
	// job ends by the job deadline. Neither is ever renewed.
	IssueBudget, JobBudget time.Duration
	// At is the claim's time; zero is now.
	At time.Time
}

// BusySendClaimOutcome is how a claim ended. Only claimed grants anything.
type BusySendClaimOutcome string

const (
	// BusySendClaimMade froze a new hand-over: Handover is set.
	BusySendClaimMade BusySendClaimOutcome = "claimed"
	// BusySendClaimEmpty found no eligible Held Message.
	BusySendClaimEmpty BusySendClaimOutcome = "empty"
	// BusySendClaimOwned found an earlier unresolved hand-over owning the
	// conversation's order and transport (Wait handover_in_progress).
	BusySendClaimOwned BusySendClaimOutcome = "owned"
	// BusySendClaimIneligible found an authoritative fact that prohibits an
	// attempt (Wait names it) or a destination other than the current one.
	BusySendClaimIneligible BusySendClaimOutcome = "ineligible"
)

// BusySendClaim is a committed claim's result, returned only after the commit
// is verified. A non-claimed outcome changed no custody; it may have released
// an expired, never-issued claim of a dead job (ReleasedExpired).
type BusySendClaim struct {
	Outcome         BusySendClaimOutcome
	Wait            BusySendWaitReason
	Detail          string
	ReleasedExpired int
	Handover        *BusySendHandover
}

// BusySendHandover is a frozen hand-over as emission code uses it: its
// identity and revision, its captured destination and epochs, its original
// deadlines and its immutable prompt partition. Revision is the hand-over's
// entity revision, which every job step compares and advances; a release,
// re-claim or fence advances it too, so a job holding an older one has no
// authority left.
type BusySendHandover struct {
	HandoverID     string
	InstanceID     string
	ConversationID string
	HandoverSeq    int64
	Revision       int64
	OwnerRevision  int64
	CutoffSeq      int64
	Boundary       BusySendBoundary
	// ForceID is the force whose hand-over this is (boundary force), "" for
	// an ordinary hand-over. Only that force's running call may issue it.
	ForceID            string
	RaceExposed        bool
	Destination        BusySendDestination
	ExpectedTurnStarts int64
	PermissionEpoch    int64
	Open               bool
	PromptCursor       int
	ClaimedAt          time.Time
	IssueDeadline      time.Time
	JobDeadline        time.Time
	Prompts            []BusySendPrompt
}

// BusySendPrompt is one prompt of a hand-over's partition: its Messages in
// accepted order, the body rendered from their retained text
// (RenderBusySendPromptBody) and its complete measured size, with its emission
// and transport so far.
type BusySendPrompt struct {
	Index         int
	MessageIDs    []string
	Body          []byte
	RenderedBytes int64
	Emission      BusySendEmission
	Transport     BusySendTransport
	OrderKind     BusySendOrderKind
}

// busySendAt is a request time in milliseconds, now for the zero time.
func busySendAt(at time.Time) int64 {
	if at.IsZero() {
		return storeNowMillis()
	}
	return at.UnixMilli()
}

// busySendIssueFacts are the current facts a claim captures and every later
// job step compares (SR-2.4): the published binding with its current Launch,
// that Launch's recorded route identity and connection generation, and the
// row's provider session, Turn Start counter, permission epoch, authoritative
// permission hold and standing pause fence.
type busySendIssueFacts struct {
	busySendPublishedRow
	routeID              string
	connectionGeneration int64
	sessionID            string
	turnStarts           int64
	permissionEpoch      int64
	permissionHold       bool
	pauseFence           bool
}

func readBusySendIssueFacts(ctx context.Context, q busySendLaunchSQL, instanceID string) (busySendIssueFacts, error) {
	var f busySendIssueFacts
	var err error
	if f.busySendPublishedRow, err = readExaminedBusySendLaunch(ctx, q, instanceID); err != nil {
		return f, err
	}
	var route, session sql.NullString
	err = q.QueryRowContext(ctx, `SELECT l.route_id, l.connection_generation, s.session_id, s.turn_starts, s.permission_epoch,
	        EXISTS(SELECT 1 FROM permission_requests pr WHERE pr.instance_id = s.instance_id AND `+holdsRowSQL+`),
	        EXISTS(SELECT 1 FROM busy_send_pause_claims c WHERE c.launch_id = l.launch_id AND c.state = 'standing')
	    FROM spawns s JOIN busy_send_launches l ON l.launch_id = ?
	    WHERE s.instance_id = ?`, f.launch, instanceID).Scan(&route, &f.connectionGeneration, &session,
		&f.turnStarts, &f.permissionEpoch, &f.permissionHold, &f.pauseFence)
	if err != nil {
		return f, fmt.Errorf("store: read busy-send issue facts: %w", err)
	}
	f.routeID, f.sessionID = route.String, session.String
	return f, nil
}

// launchWait is the wait a starting, ended or missing current Launch gives,
// or "" for a running one. A pending row is starting even when its staging
// already recorded the published Launch's End (initialBusySendWait's order).
func (f busySendIssueFacts) launchWait() BusySendWaitReason {
	switch {
	case f.state == StatePending:
		return BusySendWaitAgentStarting
	case f.launchEnded || f.state == StateEnded || f.state == StateMissing:
		return BusySendWaitLaunchEnded
	}
	return ""
}

// destinationMatches reports whether d is the current Launch's destination.
func (f busySendIssueFacts) destinationMatches(d BusySendDestination) bool {
	return d.LaunchID == f.launch && d.LaunchToken == f.token && d.Provider == f.provider &&
		string(d.RouteKind) == f.routeKind && d.RouteID == f.routeID &&
		d.ConnectionGeneration == f.connectionGeneration && d.SessionID == f.sessionID
}

// ClaimBusySendHandover claims for an eligible event (SR-2.2) in one
// transaction on a WAL synchronous=FULL connection, bounded by ctx. In order
// it: reads the published current Launch and refuses an uncovered one, one
// that is not req's destination (ineligible, no Wait), an ended or starting
// one and one with no recorded route (ineligible with the Wait); finishes a
// force whose call never reached its end (ReleaseAbandonedBusySendForce's
// rule, as the agent's eligible event); refuses an authoritative permission
// hold (ineligible, dialog); releases a dead job's expired never-issued
// claims (ReleaseBusySendClaims' expiry rule, never a force's); finds an
// earlier hand-over still owning order and transport (owned); at turn end,
// refuses while the
// current Launch has an awaiting obligation, earlier issued input with no
// evidence it was taken (ineligible, readiness_unknown; SR-3.3,
// readBusySendAwaiting); then freezes the eligible
// contiguous prefix of Held Messages at its last accepted sequence, partitions
// it (PartitionBusySendPrompts) under the conversation provider's mode limits
// and req.Measure, and records the hand-over, its prompts and ordered
// membership with the captured destination, the row's Turn Start counter and
// permission epoch and the original deadlines; the Messages become claimed,
// keep their reservations and wait handover_in_progress, and the conversation
// names the hand-over as its owner. Later admissions take later sequences and
// wait behind it. A COMMIT whose outcome is unknown is an error CommitUnconfirmed
// reports and grants nothing.
func (s *Store) ClaimBusySendHandover(ctx context.Context, req BusySendClaimRequest) (BusySendClaim, error) {
	if req.IssueBudget <= 0 || req.JobBudget < req.IssueBudget {
		return BusySendClaim{}, fmt.Errorf("store: busy-send claim needs a positive issue budget within the job budget")
	}
	var out BusySendClaim
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		out, err = claimBusySendHandover(ctx, conn, s.StoreID(), req, busySendAt(req.At))
		return err
	})
	if err != nil {
		return BusySendClaim{}, err
	}
	return out, nil
}

func claimBusySendHandover(ctx context.Context, conn *sql.Conn, storeID string, req BusySendClaimRequest, now int64) (BusySendClaim, error) {
	f, err := readBusySendIssueFacts(ctx, conn, req.InstanceID)
	if err != nil {
		return BusySendClaim{}, err
	}
	ineligible := func(wait BusySendWaitReason, detail string) (BusySendClaim, error) {
		return BusySendClaim{Outcome: BusySendClaimIneligible, Wait: wait, Detail: detail}, nil
	}
	switch {
	case f.launchCoverage != BusySendCovered:
		return ineligible("", "the current Launch is not covered")
	case req.Destination.LaunchID != f.launch || req.Destination.LaunchToken != f.token:
		return ineligible("", "the event's Launch is not the published current Launch")
	case f.launchWait() != "":
		return ineligible(f.launchWait(), "the current Launch is not running")
	case f.routeKind == "":
		return ineligible(BusySendWaitRouteUnavailable, "the current Launch has no recorded route")
	case !f.destinationMatches(req.Destination):
		return ineligible("", "the event's session, route or connection is not the current one")
	}
	// Once the event is the current running Launch's, it finishes a force
	// whose call never reached its end (SR-3.2, SR-4.3) before its other
	// refusals, so neither the force's claims nor its outcome wait on a dead
	// caller (the coordinator has usually done so already, before its route's
	// wait decision); generic expiry never releases a force's claims.
	abandoned, err := releaseAbandonedBusySendForce(ctx, conn, storeID, f.conversation, now)
	if err != nil {
		return BusySendClaim{}, err
	}
	released := abandoned.Returned
	if abandoned.Released {
		if f, err = readBusySendIssueFacts(ctx, conn, req.InstanceID); err != nil {
			return BusySendClaim{}, err
		}
	}
	if f.permissionHold {
		return BusySendClaim{Outcome: BusySendClaimIneligible, Wait: BusySendWaitDialog, ReleasedExpired: released,
			Detail: "an open permission request holds the agent"}, nil
	}
	if f.ownerHandover != "" {
		r, err := releaseBusySendClaims(ctx, conn, storeID, f.ownerHandover, 0, BusySendWaitReadinessUnknown, now)
		if err != nil && !errors.Is(err, errBusySendNotExpired) {
			return BusySendClaim{}, err
		}
		released += r.Released
		if !r.Closed {
			return BusySendClaim{Outcome: BusySendClaimOwned, Wait: BusySendWaitHandoverInProgress, ReleasedExpired: released,
				Detail: "an earlier hand-over still owns the conversation's order and transport"}, nil
		}
		if f, err = readBusySendIssueFacts(ctx, conn, req.InstanceID); err != nil {
			return BusySendClaim{}, err
		}
	}
	members, err := readBusySendEligiblePrefix(ctx, conn, f.conversation, req.Boundary)
	if err != nil {
		return BusySendClaim{}, err
	}
	if len(members) == 0 {
		return BusySendClaim{Outcome: BusySendClaimEmpty, ReleasedExpired: released}, nil
	}
	if req.Boundary == BusySendBoundaryTurnEnd {
		// SR-3.3's AD-owned-work rule: turn-end evidence is not idleness while
		// AD's own earlier input may still be pending at the provider.
		awaiting, err := readBusySendAwaiting(ctx, conn, f.conversation, f.launch)
		if err != nil {
			return BusySendClaim{}, err
		}
		if awaiting > 0 {
			return BusySendClaim{Outcome: BusySendClaimIneligible, Wait: BusySendWaitReadinessUnknown, ReleasedExpired: released,
				Detail: "earlier input AD issued has no evidence the agent took it"}, nil
		}
	}
	mode := BusySendWaitTurn
	if req.Boundary == BusySendBoundaryToolBoundary {
		mode = BusySendSteerTurn
	}
	limits, offered := BusySendCapabilitiesFor(f.provider).Mode(mode)
	if !offered {
		return ineligible(BusySendWaitReadinessUnknown, fmt.Sprintf("send_mode %s is not offered for this agent", mode))
	}
	texts := make([][]byte, len(members))
	for i, m := range members {
		texts[i] = m.text
	}
	parts, err := PartitionBusySendPrompts(texts, limits, req.Measure)
	if err != nil {
		return BusySendClaim{}, err
	}
	h, err := insertBusySendHandover(ctx, conn, storeID, f, busySendHandoverSpec{InstanceID: req.InstanceID,
		Destination: req.Destination, Boundary: req.Boundary, RaceExposed: req.RaceExposed,
		IssueDeadline: now + req.IssueBudget.Milliseconds(), JobDeadline: now + req.JobBudget.Milliseconds()}, members, parts, now)
	if err != nil {
		return BusySendClaim{}, err
	}
	return BusySendClaim{Outcome: BusySendClaimMade, ReleasedExpired: released, Handover: h}, nil
}

// busySendClaimMember is a Held Message a claim freezes.
type busySendClaimMember struct {
	messageID string
	seq       int64
	revision  int64
	text      []byte
}

// readBusySendEligiblePrefix reads the conversation's Held Messages in
// accepted order and returns the eligible contiguous prefix: all of them at
// turn end, the Steer Turn ones before the first Wait Turn Message at a tool
// boundary (SR-2.2). A force carries every Held Message (ClaimBusySendForce).
func readBusySendEligiblePrefix(ctx context.Context, conn *sql.Conn, conversation string, boundary BusySendBoundary) ([]busySendClaimMember, error) {
	if boundary != BusySendBoundaryTurnEnd && boundary != BusySendBoundaryToolBoundary && boundary != BusySendBoundaryForce {
		return nil, fmt.Errorf("store: busy-send claim at boundary %q is not supported", boundary)
	}
	rows, err := conn.QueryContext(ctx, `SELECT message_id, accepted_seq, revision, send_mode, text FROM busy_send_messages
	    WHERE conversation_id = ? AND place = 'held' ORDER BY accepted_seq`, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []busySendClaimMember
	for rows.Next() {
		var m busySendClaimMember
		var mode string
		if err := rows.Scan(&m.messageID, &m.seq, &m.revision, &mode, &m.text); err != nil {
			return nil, err
		}
		if boundary == BusySendBoundaryToolBoundary && BusySendMode(mode) != BusySendSteerTurn {
			break
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// busySendHandoverSpec is what a claim decides for its new hand-over beyond
// the current facts: its target, captured destination, boundary and race
// exposure, the force it belongs to ("" for an ordinary claim) and its
// original issue and job deadlines in milliseconds.
type busySendHandoverSpec struct {
	InstanceID                 string
	Destination                BusySendDestination
	Boundary                   BusySendBoundary
	RaceExposed                bool
	ForceID                    string
	IssueDeadline, JobDeadline int64
}

// insertBusySendHandover records a claim's hand-over, prompts and membership
// and moves its Messages to claimed, in the claim's transaction.
func insertBusySendHandover(ctx context.Context, conn *sql.Conn, storeID string, f busySendIssueFacts, spec busySendHandoverSpec,
	members []busySendClaimMember, parts []BusySendPromptPart, now int64) (*BusySendHandover, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	var seq int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(handover_seq), 0) + 1 FROM busy_send_handovers WHERE conversation_id = ?`,
		f.conversation).Scan(&seq); err != nil {
		return nil, err
	}
	d := spec.Destination
	h := &BusySendHandover{HandoverID: id.String(), InstanceID: spec.InstanceID, ConversationID: f.conversation,
		HandoverSeq: seq, Revision: 1, OwnerRevision: f.conversationRevision + 1, CutoffSeq: members[len(members)-1].seq,
		Boundary: spec.Boundary, ForceID: spec.ForceID, RaceExposed: spec.RaceExposed, Destination: d,
		ExpectedTurnStarts: f.turnStarts, PermissionEpoch: f.permissionEpoch, Open: true, ClaimedAt: time.UnixMilli(now).UTC(),
		IssueDeadline: time.UnixMilli(spec.IssueDeadline).UTC(), JobDeadline: time.UnixMilli(spec.JobDeadline).UTC()}
	if _, err := conn.ExecContext(ctx, `INSERT INTO busy_send_handovers
	    (handover_id, store_id, conversation_id, handover_seq, instance_id, launch_id, launch_token, provider, session_id,
	     route_kind, route_id, connection_generation, owner_revision, revision, cutoff_seq, boundary, race_exposed, forced,
	     force_id, expected_turn_starts, expected_turn_id, permission_epoch, prompt_count, prompt_cursor, state,
	     claimed_at_ms, issue_deadline_ms, job_deadline_ms)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'open', ?, ?, ?)`,
		h.HandoverID, storeID, f.conversation, seq, spec.InstanceID, f.launch, nullableStringArg(f.token), f.provider,
		nullableStringArg(f.sessionID), f.routeKind, nullableStringArg(f.routeID), f.connectionGeneration, h.OwnerRevision,
		h.CutoffSeq, string(spec.Boundary), boolInt(spec.RaceExposed), boolInt(spec.ForceID != ""),
		nullableStringArg(spec.ForceID), f.turnStarts, nullableStringArg(d.TurnID),
		f.permissionEpoch, len(parts), now, spec.IssueDeadline, spec.JobDeadline); err != nil {
		return nil, err
	}
	for i, part := range parts {
		p := BusySendPrompt{Index: i, RenderedBytes: part.RenderedBytes, Emission: BusySendEmissionNone}
		texts := make([][]byte, 0, part.Count)
		for j := part.First; j < part.First+part.Count; j++ {
			p.MessageIDs = append(p.MessageIDs, members[j].messageID)
			texts = append(texts, members[j].text)
		}
		p.Body = RenderBusySendPromptBody(texts)
		if _, err := conn.ExecContext(ctx, `INSERT INTO busy_send_prompts (handover_id, prompt_index, store_id, message_count, rendered_bytes)
		    VALUES (?, ?, ?, ?, ?)`, h.HandoverID, i, storeID, part.Count, part.RenderedBytes); err != nil {
			return nil, err
		}
		h.Prompts = append(h.Prompts, p)
	}
	member := 0
	for i, part := range parts {
		for j := part.First; j < part.First+part.Count; j++ {
			m := members[j]
			if _, err := conn.ExecContext(ctx, `INSERT INTO busy_send_handover_members (handover_id, member_index, message_id, prompt_index, store_id)
			    VALUES (?, ?, ?, ?, ?)`, h.HandoverID, member, m.messageID, i, storeID); err != nil {
				return nil, err
			}
			if err := busySendChange(ctx, conn, `UPDATE busy_send_messages SET place = 'claimed', handover_id = ?, revision = revision + 1
			    WHERE message_id = ? AND place = 'held' AND handover_id IS NULL AND revision = ?`, h.HandoverID, m.messageID, m.revision); err != nil {
				return nil, err
			}
			member++
		}
	}
	if err := busySendChange(ctx, conn, `UPDATE busy_conversations SET owner_handover_id = ?, revision = revision + 1
	    WHERE conversation_id = ? AND revision = ? AND owner_handover_id IS NULL AND publication = 'published'`,
		h.HandoverID, f.conversation, f.conversationRevision); err != nil {
		return nil, err
	}
	if err := setBusySendWaits(ctx, conn, BusySendWaitHandoverInProgress, now, `handover_id = ?`, h.HandoverID); err != nil {
		return nil, err
	}
	return h, refreshBusySendHeldWaits(ctx, conn, f.conversation, BusySendWaitHandoverInProgress, now)
}

// ReadBusySendHandover reads one hand-over as it is durably stored, with each
// prompt's body rendered again from its Messages' retained text, so a later
// event or a reopened store sees exactly the frozen partition. It writes
// nothing and starts no work. An unknown id is sql.ErrNoRows.
func (s *Store) ReadBusySendHandover(ctx context.Context, handoverID string) (BusySendHandover, error) {
	var out BusySendHandover
	err := s.WithContext(ctx).readWithin(DefaultLockWait, "store: read busy-send hand-over", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		h, err := readBusySendHandover(ctx, conn, s.StoreID(), handoverID)
		if err != nil {
			return err
		}
		out, err = h.frozen(ctx, conn)
		return err
	})
	return out, err
}

// busySendHandoverRow is a hand-over row as the emission writers compare it.
type busySendHandoverRow struct {
	BusySendHandover
	promptCount int
}

func readBusySendHandover(ctx context.Context, q busySendLaunchSQL, storeID, handoverID string) (busySendHandoverRow, error) {
	var h busySendHandoverRow
	var token, session, routeID, turnID, forceID sql.NullString
	var state, boundary, routeKind string
	var claimed, issue, job int64
	err := q.QueryRowContext(ctx, `SELECT handover_id, instance_id, conversation_id, handover_seq, revision, owner_revision,
	        cutoff_seq, boundary, force_id, race_exposed, launch_id, launch_token, provider, session_id, route_kind, route_id,
	        connection_generation, expected_turn_id, expected_turn_starts, permission_epoch, state, prompt_count,
	        prompt_cursor, claimed_at_ms, issue_deadline_ms, job_deadline_ms
	    FROM busy_send_handovers WHERE handover_id = ? AND store_id = ?`, handoverID, storeID).Scan(
		&h.HandoverID, &h.InstanceID, &h.ConversationID, &h.HandoverSeq, &h.Revision, &h.OwnerRevision,
		&h.CutoffSeq, &boundary, &forceID, &h.RaceExposed, &h.Destination.LaunchID, &token, &h.Destination.Provider, &session,
		&routeKind, &routeID, &h.Destination.ConnectionGeneration, &turnID, &h.ExpectedTurnStarts, &h.PermissionEpoch,
		&state, &h.promptCount, &h.PromptCursor, &claimed, &issue, &job)
	if err != nil {
		return h, err
	}
	h.Boundary, h.ForceID, h.Open = BusySendBoundary(boundary), forceID.String, BusySendHandoverState(state) == BusySendHandoverOpen
	h.Destination.LaunchToken, h.Destination.SessionID = token.String, session.String
	h.Destination.RouteKind, h.Destination.RouteID, h.Destination.TurnID = BusySendRouteKind(routeKind), routeID.String, turnID.String
	h.ClaimedAt, h.IssueDeadline, h.JobDeadline = time.UnixMilli(claimed).UTC(), time.UnixMilli(issue).UTC(), time.UnixMilli(job).UTC()
	return h, nil
}

// frozen adds the hand-over's prompts, each body rendered from its members'
// retained text in membership order.
func (h busySendHandoverRow) frozen(ctx context.Context, q busySendQuerySQL) (BusySendHandover, error) {
	out := h.BusySendHandover
	rows, err := q.QueryContext(ctx, `SELECT p.prompt_index, p.rendered_bytes, p.emission, p.transport, p.order_kind, hm.message_id, m.text
	    FROM busy_send_prompts p
	    JOIN busy_send_handover_members hm ON hm.handover_id = p.handover_id AND hm.prompt_index = p.prompt_index
	    JOIN busy_send_messages m ON m.message_id = hm.message_id
	    WHERE p.handover_id = ? ORDER BY hm.member_index`, h.HandoverID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var texts [][][]byte
	for rows.Next() {
		var index int
		var size int64
		var emission string
		var transport, order sql.NullString
		var messageID string
		var text []byte
		if err := rows.Scan(&index, &size, &emission, &transport, &order, &messageID, &text); err != nil {
			return out, err
		}
		if index == len(out.Prompts) {
			out.Prompts = append(out.Prompts, BusySendPrompt{Index: index, RenderedBytes: size, Emission: BusySendEmission(emission),
				Transport: BusySendTransport(transport.String), OrderKind: BusySendOrderKind(order.String)})
			texts = append(texts, nil)
		}
		if index != len(out.Prompts)-1 {
			return out, fmt.Errorf("store: busy-send hand-over %s membership out of prompt order", h.HandoverID)
		}
		out.Prompts[index].MessageIDs = append(out.Prompts[index].MessageIDs, messageID)
		texts[index] = append(texts[index], text)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Prompts) != h.promptCount {
		return out, fmt.Errorf("store: busy-send hand-over %s has %d of %d prompts", h.HandoverID, len(out.Prompts), h.promptCount)
	}
	for i := range out.Prompts {
		out.Prompts[i].Body = RenderBusySendPromptBody(texts[i])
	}
	return out, nil
}

// busySendQuerySQL is the multi-row read surface of a Conn or Tx.
type busySendQuerySQL interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
