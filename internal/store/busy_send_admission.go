package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// BusySendAdmissionRequest is one send to a Covered Agent as AdmitBusySend
// admits it (SR-1.1 to SR-1.6). The caller has already refused today's and the
// busy-send options' malformed shapes; the store judges what needs its
// records.
type BusySendAdmissionRequest struct {
	// InstanceID is the target, the Message's original target.
	InstanceID string
	// Text is the validated original text: nonempty valid UTF-8, never
	// normalized. Pair arbitration compares these exact bytes.
	Text []byte
	// CallerUUID is the canonical Caller UUID, "" for none. With it,
	// DeliveryRequestID is the Message ID, the caller's or one the caller of
	// AdmitBusySend made (a canonical UUID), so an uncertain commit can still
	// name the pair.
	CallerUUID        string
	DeliveryRequestID string
	// SendMode and Confirmed are the Message's own options; nil takes the
	// published conversation's Agent Default.
	SendMode  *BusySendMode
	Confirmed *bool
}

// pair is the request's confirmed pair, and false when it names none.
func (r BusySendAdmissionRequest) pair() (BusySendPair, bool) {
	p := BusySendPair{CallerUUID: r.CallerUUID, DeliveryRequestID: r.DeliveryRequestID}
	return p, p.CallerUUID != "" && p.DeliveryRequestID != ""
}

// BusySendAdmissionOutcome is how an admission ended without refusal.
type BusySendAdmissionOutcome string

const (
	// BusySendAdmitted is a new Message committed Held.
	BusySendAdmitted BusySendAdmissionOutcome = "admitted"
	// BusySendReplayed is a repeat of an owned pair with the same target and
	// text: the owner's current Result, with nothing stored.
	BusySendReplayed BusySendAdmissionOutcome = "replayed"
)

// BusySendAdmission is a committed or replayed admission. It is returned only
// after the commit is verified, so it alone authorizes the wake.
type BusySendAdmission struct {
	Outcome BusySendAdmissionOutcome
	// Result is the Message's current Result as readBusySendMessageResult
	// projects it, the same as a Lookup of its pair at that durable state.
	Result BusySendResult
	// WakePath is the published current Launch's recorded wake endpoint, set
	// only for BusySendAdmitted and "" when the Launch records none. It is a
	// content-free hint and carries no authority.
	WakePath string
}

// BusySendRefusalReason is a cant_send refusal's reason (SR-1.2 to SR-1.6,
// SR-9.2).
type BusySendRefusalReason string

const (
	BusySendRefuseIDConflict       BusySendRefusalReason = "id_conflict"
	BusySendRefuseRetentionExpired BusySendRefusalReason = "retention_expired"
	BusySendRefuseAgentNotCovered  BusySendRefusalReason = "agent_not_covered"
	BusySendRefuseAgentCommand     BusySendRefusalReason = "agent_command"
	BusySendRefuseSteerUnsupported BusySendRefusalReason = "steer_unsupported"
	BusySendRefuseTextKind         BusySendRefusalReason = "text_kind"
	BusySendRefuseTextBytes        BusySendRefusalReason = "text_bytes"
	BusySendRefusePromptSize       BusySendRefusalReason = "prompt_size"
	BusySendRefuseHeldCount        BusySendRefusalReason = "held_count"
	BusySendRefuseHeldBytes        BusySendRefusalReason = "held_bytes"
)

// BusySendLimit is a refused value and its published limit (SR-1.6).
type BusySendLimit struct {
	Actual, Allowed int64
}

// BusySendRefusal is a refused admission: nothing was stored, no pair
// claimed and no sequence consumed. Owner is an id_conflict's owner Result.
type BusySendRefusal struct {
	Reason BusySendRefusalReason
	Limit  *BusySendLimit
	Owner  *BusySendResult
	Detail string
}

func (r *BusySendRefusal) Error() string { return "store: busy send refused: " + r.Detail }

// ErrBusySendMalformed marks a send whose options are malformed only once the
// published Agent Defaults resolve them: a confirmed send with no Caller UUID,
// or an unconfirmed one carrying a Caller UUID or Message ID (SR-1.3).
var ErrBusySendMalformed = errors.New("store: busy-send parameters malformed")

// busySendReplay ends a replay's transaction without writing.
type busySendReplay struct{ admission BusySendAdmission }

func (*busySendReplay) Error() string { return "store: busy-send replay" }

// AdmitBusySend admits one Message to a Covered Agent in one transaction on a
// connection with WAL synchronous=FULL (SR-1.1), at the store's lock wait and
// the store's call context. In order it: arbitrates the confirmed pair
// store-wide (a repeat with the same target and exact text bytes returns the
// owner's current Result, after Launch End, deletion or a full inbox
// included; another target or text is id_conflict with the owner's Result);
// reads the published conversation and current Launch (no row is
// ErrSpawnNotFound; an uncovered current Launch is agent_not_covered);
// resolves the Message's options over the Agent Defaults
// (ErrBusySendMalformed); refuses an Agent Command, an unoffered Send Mode,
// another text kind and the text, prompt and held limits; then allocates the
// next accepted sequence, reserves the held count and bytes, and stores the
// Message Held with its immutable options, original text and digest, its
// pair and its initial wait, comparing and advancing the conversation's
// revision.
//
// A refusal is a *BusySendRefusal and writes nothing. A COMMIT whose outcome
// is unknown returns an error CommitUnconfirmed reports: never a rollback,
// a free pair or delivery authority; a retry with the same pair learns the
// outcome. The store does no provider, process or file work in the
// transaction; the caller touches WakePath only after a BusySendAdmitted
// return.
func (s *Store) AdmitBusySend(req BusySendAdmissionRequest) (BusySendAdmission, error) {
	if len(req.Text) == 0 || !utf8.Valid(req.Text) {
		return BusySendAdmission{}, fmt.Errorf("%w: text must be nonempty UTF-8", ErrBusySendMalformed)
	}
	var out BusySendAdmission
	ctx, cancel := s.operationContext(DefaultLockWait)
	defer cancel()
	err := s.inDurableWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		out, err = admitBusySend(ctx, conn, s.StoreID(), req, storeNowMillis())
		return err
	})
	var replay *busySendReplay
	if errors.As(err, &replay) {
		return replay.admission, nil
	}
	if err != nil {
		return BusySendAdmission{}, err
	}
	return out, nil
}

func admitBusySend(ctx context.Context, conn *sql.Conn, storeID string, req BusySendAdmissionRequest, now int64) (BusySendAdmission, error) {
	if pair, ok := req.pair(); ok {
		owner, found, err := readBusySendPairOwner(ctx, conn, storeID, pair)
		switch {
		case err != nil:
			return BusySendAdmission{}, err
		case found && owner.expired:
			return BusySendAdmission{}, &BusySendRefusal{Reason: BusySendRefuseRetentionExpired,
				Detail: "the pair's Result is past retention; its ownership is kept and the pair is never free"}
		case found && owner.instanceID == req.InstanceID && bytes.Equal(owner.text, req.Text):
			return BusySendAdmission{}, &busySendReplay{BusySendAdmission{Outcome: BusySendReplayed, Result: owner.result}}
		case found:
			return BusySendAdmission{}, &BusySendRefusal{Reason: BusySendRefuseIDConflict, Owner: &owner.result,
				Detail: "the pair already names a Message with another target or text"}
		}
	}
	p, err := readExaminedBusySendLaunch(ctx, conn, req.InstanceID)
	if err != nil {
		return BusySendAdmission{}, err
	}
	if p.launchCoverage != BusySendCovered {
		return BusySendAdmission{}, &BusySendRefusal{Reason: BusySendRefuseAgentNotCovered,
			Detail: "busy send does not cover the target's current Launch"}
	}
	mode, confirmed := p.defaults.SendMode, p.defaults.Confirmed
	if req.SendMode != nil {
		mode = *req.SendMode
	}
	if req.Confirmed != nil {
		confirmed = *req.Confirmed
	}
	switch {
	case confirmed && req.CallerUUID == "":
		return BusySendAdmission{}, fmt.Errorf("%w: confirmed delivery (from the agent's defaults) needs caller_uuid", ErrBusySendMalformed)
	case !confirmed && (req.CallerUUID != "" || req.DeliveryRequestID != ""):
		return BusySendAdmission{}, fmt.Errorf("%w: an unconfirmed send (from the agent's defaults) carries no caller_uuid or delivery_request_id", ErrBusySendMalformed)
	}
	caps, err := readBusySendLaunchCapabilities(ctx, conn, storeID, p.launch)
	if err != nil {
		return BusySendAdmission{}, err
	}
	if caps == nil {
		return BusySendAdmission{}, errBusySendCapabilities
	}
	if err := busySendEligible(p, *caps, mode, req.Text); err != nil {
		return BusySendAdmission{}, err
	}
	seq := busySendAddBytes(p.acceptedSeq, 1)
	if seq < 0 {
		return BusySendAdmission{}, fmt.Errorf("store: busy-send accepted sequence is unrepresentable")
	}
	wait, err := initialBusySendWait(ctx, conn, p)
	if err != nil {
		return BusySendAdmission{}, err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return BusySendAdmission{}, err
	}
	size, digest := int64(len(req.Text)), sha256.Sum256(req.Text)
	if _, err := conn.ExecContext(ctx, `INSERT INTO busy_send_messages
	    (message_id, store_id, conversation_id, instance_id, accepted_seq, accepted_at_ms, send_mode, confirmed,
	     text, text_bytes, text_sha256, place, handover_id, reserved, result_class, result_final, result_reason,
	     wait_reason, wait_since_ms, wait_earlier_message_id)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'held', NULL, 1, 'held', 0, ?, ?, ?, ?)`,
		id.String(), storeID, p.conversation, req.InstanceID, seq, now, string(mode), boolInt(confirmed),
		req.Text, size, hex.EncodeToString(digest[:]), string(wait.reason), string(wait.reason), now,
		nullableStringArg(wait.earlier)); err != nil {
		return BusySendAdmission{}, err
	}
	if confirmed {
		if _, err := conn.ExecContext(ctx, `INSERT INTO busy_send_results
		    (caller_uuid, delivery_request_id, store_id, message_id, instance_id) VALUES (?, ?, ?, ?, ?)`,
			req.CallerUUID, req.DeliveryRequestID, storeID, id.String(), req.InstanceID); err != nil {
			return BusySendAdmission{}, err
		}
	}
	if err := busySendChange(ctx, conn, `UPDATE busy_conversations SET accepted_seq = ?,
	        reserved_count = reserved_count + 1, reserved_bytes = reserved_bytes + ?, revision = revision + 1
	    WHERE conversation_id = ? AND revision = ? AND accepted_seq = ? AND publication = 'published'`,
		seq, size, p.conversation, p.conversationRevision, p.acceptedSeq); err != nil {
		return BusySendAdmission{}, err
	}
	result, err := readBusySendMessageResult(ctx, conn, storeID, id.String())
	if err != nil {
		return BusySendAdmission{}, err
	}
	return BusySendAdmission{Outcome: BusySendAdmitted, Result: result, WakePath: p.wakePath}, nil
}

// busySendEligible is new-send eligibility after pair arbitration, in order:
// an Agent Command, the Send Mode the provider offers, its text kind, and the
// Message, prompt and held limits (SR-1.5, SR-1.6). The limits are the
// published current Launch's immutable snapshot, read after pair arbitration
// and malformed-option validation in the same durable transaction.
func busySendEligible(p busySendPublishedRow, caps BusySendLaunchCapabilities, mode BusySendMode, text []byte) error {
	if isAgentCommand(text) {
		return &BusySendRefusal{Reason: BusySendRefuseAgentCommand,
			Detail: "the text's first non-whitespace character is / or !, an Agent Command"}
	}
	limits, offered := caps.Mode(mode)
	if !offered {
		return &BusySendRefusal{Reason: BusySendRefuseSteerUnsupported,
			Detail: fmt.Sprintf("send_mode %s is not offered for this agent", mode)}
	}
	if !limits.Accepts(text) {
		return &BusySendRefusal{Reason: BusySendRefuseTextKind,
			Detail: "the text holds a C0 control character other than TAB and LF, such as NUL or CR"}
	}
	size := int64(len(text))
	refuse := func(reason BusySendRefusalReason, actual, allowed int64, what string) error {
		return &BusySendRefusal{Reason: reason, Limit: &BusySendLimit{Actual: actual, Allowed: allowed},
			Detail: fmt.Sprintf("%s %d exceeds the published %d", what, actual, allowed)}
	}
	if size > limits.MessageBytes {
		return refuse(BusySendRefuseTextBytes, size, limits.MessageBytes, "text bytes")
	}
	prompt := limits.SinglePromptBytes(text)
	if prompt < 0 {
		return fmt.Errorf("%w: prompt size is unrepresentable", errBusySendCapabilities)
	}
	if prompt > limits.PromptBytes {
		return refuse(BusySendRefusePromptSize, prompt, limits.PromptBytes, "prompt bytes")
	}
	count := busySendAddBytes(p.reservedCount, 1)
	if count < 0 {
		return fmt.Errorf("store: busy-send held count is unrepresentable")
	}
	if count > caps.HeldCount {
		return refuse(BusySendRefuseHeldCount, count, caps.HeldCount, "held Messages")
	}
	heldBytes := busySendAddBytes(p.reservedBytes, size)
	if heldBytes < 0 {
		return fmt.Errorf("store: busy-send held bytes are unrepresentable")
	}
	if heldBytes > caps.HeldBytes {
		return refuse(BusySendRefuseHeldBytes, heldBytes, caps.HeldBytes, "held bytes")
	}
	return nil
}

// isAgentCommand reports whether text's first non-whitespace character is
// / or !, an Agent Command (SR-1.5).
func isAgentCommand(text []byte) bool {
	for len(text) > 0 {
		r, n := utf8.DecodeRune(text)
		if !unicode.IsSpace(r) {
			return r == '/' || r == '!'
		}
		text = text[n:]
	}
	return false
}

// busySendInitialWait is a new Message's first wait: its reason and, behind
// an earlier Message still in custody, that Message's id.
type busySendInitialWait struct {
	reason  BusySendWaitReason
	earlier string
}

// initialBusySendWait derives a new Message's wait from durable facts only,
// never from the pane or process liveness (SR-2.6): a pending row (spawn,
// reuse or resume in progress, whose staging may already have recorded the
// published Launch's End) is agent_starting, and otherwise the published
// current Launch ended, or the row finished, is launch_ended; a Launch with no
// recorded route or wake endpoint is route_unavailable; an open hand-over is
// handover_in_progress; an earlier Message still in custody earlier_message;
// then the row's raw state: a Dialog (check_permission, ask_user), a running
// turn (working), and otherwise readiness_unknown, since admission itself
// establishes no readiness. Later custody writers record each change.
func initialBusySendWait(ctx context.Context, conn *sql.Conn, p busySendPublishedRow) (busySendInitialWait, error) {
	switch {
	case p.state == StatePending:
		return busySendInitialWait{reason: BusySendWaitAgentStarting}, nil
	case p.launchEnded || p.state == StateEnded || p.state == StateMissing:
		return busySendInitialWait{reason: BusySendWaitLaunchEnded}, nil
	case p.routeKind == "" || p.wakePath == "":
		return busySendInitialWait{reason: BusySendWaitRouteUnavailable}, nil
	case p.ownerHandover != "":
		return busySendInitialWait{reason: BusySendWaitHandoverInProgress}, nil
	}
	var earlier string
	err := conn.QueryRowContext(ctx, `SELECT message_id FROM busy_send_messages
	    WHERE conversation_id = ? AND place IN ('held', 'claimed', 'issued')
	    ORDER BY accepted_seq DESC LIMIT 1`, p.conversation).Scan(&earlier)
	switch {
	case err == nil:
		return busySendInitialWait{reason: BusySendWaitEarlierMessage, earlier: earlier}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return busySendInitialWait{}, err
	}
	switch p.state {
	case StateCheckPermission, StateAskUser:
		return busySendInitialWait{reason: BusySendWaitDialog}, nil
	case StateWorking:
		return busySendInitialWait{reason: BusySendWaitTurnRunning}, nil
	}
	return busySendInitialWait{reason: BusySendWaitReadinessUnknown}, nil
}
