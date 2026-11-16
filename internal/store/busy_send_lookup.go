package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ReadBusySendResult reads a confirmed Message's stored Result by its
// canonical pair (SR-5.4): callerUUID in lowercase hyphenated form and the
// Message ID as given. It reads durable facts only: no Active Check,
// provider, tmux or process probe, and no write. Pair ownership needs no
// agent row, so it reads the same after restarts, Launch End and deletion. A
// pair never stored is BusySendLookupUnknown and an expired Result
// BusySendLookupRetentionExpired, both with no Result; a read failure is an
// error, never either answer. Every read of a confirmed Message's Result by
// its pair must use it, so no reader turns a store failure into unknown,
// alters a Result or starts work.
func (s *Store) ReadBusySendResult(callerUUID, deliveryRequestID string) (BusySendResultLookup, error) {
	var out BusySendResultLookup
	err := s.readWithin(DefaultLockWait, "store: read busy-send result", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		owner, found, err := readBusySendPairOwner(ctx, conn, s.StoreID(), BusySendPair{CallerUUID: callerUUID, DeliveryRequestID: deliveryRequestID})
		switch {
		case err != nil:
			return err
		case !found:
			out = BusySendResultLookup{Lookup: BusySendLookupUnknown}
		case owner.expired:
			out = BusySendResultLookup{Lookup: BusySendLookupRetentionExpired}
		default:
			out = BusySendResultLookup{Lookup: BusySendLookupFound, Result: &owner.result}
		}
		return nil
	})
	if err != nil {
		return BusySendResultLookup{}, err
	}
	return out, nil
}

// busySendPairOwner is the Message that owns a confirmed pair: its original
// target and validated text, which pair arbitration compares, and its current
// Result. An expired owner keeps only the pair (SR-5.5): no text or Result.
type busySendPairOwner struct {
	messageID  string
	instanceID string
	text       []byte
	expired    bool
	result     BusySendResult
}

// readBusySendPairOwner reads pair's owner. found is false only when no
// ownership row exists; an ownership row whose Message is missing is a corrupt
// store, never a free pair (readBusySendPairAbsent).
func readBusySendPairOwner(ctx context.Context, q busySendLaunchSQL, storeID string, pair BusySendPair) (busySendPairOwner, bool, error) {
	var o busySendPairOwner
	var retention string
	err := q.QueryRowContext(ctx, `SELECT p.message_id, m.instance_id, m.text, m.retention
	    FROM busy_send_results p JOIN busy_send_messages m ON m.message_id = p.message_id
	    WHERE p.caller_uuid = ? AND p.delivery_request_id = ? AND p.store_id = ?`,
		pair.CallerUUID, pair.DeliveryRequestID, storeID).Scan(&o.messageID, &o.instanceID, &o.text, &retention)
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, readBusySendPairAbsent(ctx, q, storeID, pair)
	}
	if err != nil {
		return o, false, err
	}
	if BusySendRetention(retention) == BusySendExpired {
		o.expired, o.text = true, nil
		return o, true, nil
	}
	o.result, err = readBusySendMessageResult(ctx, q, storeID, o.messageID)
	return o, true, err
}

// readBusySendPairAbsent tells a pair never stored (nil) from an ownership row
// whose Message is missing, which is a corrupt store, never an answer that
// the pair is free.
func readBusySendPairAbsent(ctx context.Context, q busySendLaunchSQL, storeID string, pair BusySendPair) error {
	var owned bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_results
	    WHERE caller_uuid = ? AND delivery_request_id = ? AND store_id = ?)`,
		pair.CallerUUID, pair.DeliveryRequestID, storeID).Scan(&owned); err != nil {
		return err
	}
	if owned {
		return fmt.Errorf("store: busy-send pair has no Message")
	}
	return nil
}

// busySendResultSelect is the one projection of a stored Result
// (scanBusySendResult): a retained Message m with its confirmed pair p (NULL
// for an unconfirmed one), the pair e of the earlier Message it waits behind,
// if any, and the transport fact of the prompt of its latest hand-over
// attempt, with that fact's time. A reader appends its own WHERE on m.
const busySendResultSelect = `SELECT p.caller_uuid, p.delivery_request_id, m.instance_id, m.send_mode,
        m.result_class, m.result_final, m.result_reason, m.forced, m.wait_reason, m.wait_since_ms,
        e.caller_uuid, e.delivery_request_id, m.bound_launch_token, m.bound_session_id, m.evidence_kind, m.record_id,
        m.record_sha256, m.accepted_at_ms, m.queued_at_ms, m.handed_over_at_ms, m.delivered_at_ms, m.final_at_ms,
        (SELECT lp.transport FROM busy_send_handover_members lhm
            JOIN busy_send_handovers lh ON lh.handover_id = lhm.handover_id
            JOIN busy_send_prompts lp ON lp.handover_id = lhm.handover_id AND lp.prompt_index = lhm.prompt_index
            WHERE lhm.message_id = m.message_id ORDER BY lh.handover_seq DESC LIMIT 1),
        (SELECT lp.transport_at_ms FROM busy_send_handover_members lhm
            JOIN busy_send_handovers lh ON lh.handover_id = lhm.handover_id
            JOIN busy_send_prompts lp ON lp.handover_id = lhm.handover_id AND lp.prompt_index = lhm.prompt_index
            WHERE lhm.message_id = m.message_id ORDER BY lh.handover_seq DESC LIMIT 1)
    FROM busy_send_messages m LEFT JOIN busy_send_results p ON p.message_id = m.message_id
    LEFT JOIN busy_send_results e ON e.message_id = m.wait_earlier_message_id`

// busySendScanner is a *sql.Row or *sql.Rows positioned on a row.
type busySendScanner interface {
	Scan(dest ...any) error
}

// scanBusySendResult scans one busySendResultSelect row into its Result.
func scanBusySendResult(row busySendScanner) (BusySendResult, error) {
	var r BusySendResult
	var mode string
	var caller, request, class, reason, waitReason, earlierCaller, earlierRequest, token, session, evidence, recordID,
		recordSHA, transport sql.NullString
	var waitSince, queued, handedOver, delivered, final, transportAt sql.NullInt64
	if err := row.Scan(&caller, &request, &r.InstanceID, &mode, &class, &r.Final, &reason, &r.Forced, &waitReason, &waitSince,
		&earlierCaller, &earlierRequest, &token, &session, &evidence, &recordID, &recordSHA,
		&r.AcceptedAtMillis, &queued, &handedOver, &delivered, &final, &transport, &transportAt); err != nil {
		return r, err
	}
	r.Pair = BusySendPair{CallerUUID: caller.String, DeliveryRequestID: request.String}
	r.SendMode, r.Class, r.Reason = BusySendMode(mode), BusySendResultClass(class.String), reason.String
	r.SessionID, r.RecordID, r.RecordSHA256 = session.String, recordID.String, recordSHA.String
	r.QueuedAtMillis, r.HandedOverAtMillis = queued.Int64, handedOver.Int64
	r.DeliveredAtMillis, r.FinalAtMillis = delivered.Int64, final.Int64
	if token.Valid {
		r.LaunchToken = &token.String
	}
	if evidence.Valid {
		kind := BusySendEvidenceKind(evidence.String)
		r.EvidenceKind = &kind
	}
	if waitReason.Valid {
		r.Wait = &BusySendWait{Reason: BusySendWaitReason(waitReason.String), SinceMillis: waitSince.Int64}
		if earlierCaller.Valid {
			// An unconfirmed earlier Message has no pair to show.
			r.Wait.Earlier = &BusySendPair{CallerUUID: earlierCaller.String, DeliveryRequestID: earlierRequest.String}
		}
	}
	// A Final Result projects only a fact recorded by its Final time; a later
	// fact is stamped after it (RecordBusySendCompletion), so the Final Result,
	// its compatibility outcome included, never changes (SR-5.1).
	r.PartialTransport = BusySendTransport(transport.String) == BusySendTransportPartial &&
		(!r.Final || transportAt.Int64 <= r.FinalAtMillis)
	return r, nil
}

// readBusySendMessageResult reads the current Result of the retained Message
// messageID, with its confirmed pair when it has one (an unconfirmed Message
// has none). It is the one projection of a stored Result
// (busySendResultSelect): a send's response, a conflict's owner Result,
// get-delivery and a force's carried Results read it alike, so all agree at
// the same durable state. PartialTransport comes from the Message's latest
// hand-over attempt, so the compatibility outcome need not guess it.
func readBusySendMessageResult(ctx context.Context, q busySendLaunchSQL, storeID, messageID string) (BusySendResult, error) {
	r, err := scanBusySendResult(q.QueryRowContext(ctx, busySendResultSelect+`
	    WHERE m.message_id = ? AND m.store_id = ? AND m.retention = 'retained'`, messageID, storeID))
	if err != nil {
		return r, fmt.Errorf("store: read busy-send Message Result: %w", err)
	}
	return r, nil
}

// readBusySendEarlierPair names the earlier Message a Result waits behind by
// its confirmed pair; an unconfirmed earlier Message has none to show.
func readBusySendEarlierPair(ctx context.Context, q busySendLaunchSQL, messageID string) (*BusySendPair, error) {
	var p BusySendPair
	err := q.QueryRowContext(ctx, `SELECT caller_uuid, delivery_request_id FROM busy_send_results WHERE message_id = ?`,
		messageID).Scan(&p.CallerUUID, &p.DeliveryRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
