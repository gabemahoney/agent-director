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
		r := BusySendResult{Pair: BusySendPair{CallerUUID: callerUUID, DeliveryRequestID: deliveryRequestID}}
		var retention, mode string
		var class, reason, waitReason, earlier, token, session, evidence, recordID, recordSHA sql.NullString
		var waitSince, queued, handedOver, delivered, final sql.NullInt64
		err := conn.QueryRowContext(ctx, `SELECT p.instance_id, m.retention, m.send_mode, m.result_class, m.result_final,
		        m.result_reason, m.forced, m.wait_reason, m.wait_since_ms, m.wait_earlier_message_id,
		        m.bound_launch_token, m.bound_session_id, m.evidence_kind, m.record_id, m.record_sha256,
		        m.accepted_at_ms, m.queued_at_ms, m.handed_over_at_ms, m.delivered_at_ms, m.final_at_ms
		    FROM busy_send_results p JOIN busy_send_messages m ON m.message_id = p.message_id
		    WHERE p.caller_uuid = ? AND p.delivery_request_id = ? AND p.store_id = ?`,
			callerUUID, deliveryRequestID, s.StoreID()).Scan(&r.InstanceID, &retention, &mode, &class, &r.Final,
			&reason, &r.Forced, &waitReason, &waitSince, &earlier, &token, &session, &evidence, &recordID, &recordSHA,
			&r.AcceptedAtMillis, &queued, &handedOver, &delivered, &final)
		if errors.Is(err, sql.ErrNoRows) {
			return readBusySendPairAbsent(ctx, conn, s.StoreID(), r.Pair, &out)
		}
		if err != nil {
			return err
		}
		if BusySendRetention(retention) == BusySendExpired {
			out = BusySendResultLookup{Lookup: BusySendLookupRetentionExpired}
			return nil
		}
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
			if earlier.Valid {
				if r.Wait.Earlier, err = readBusySendEarlierPair(ctx, conn, earlier.String); err != nil {
					return err
				}
			}
		}
		out = BusySendResultLookup{Lookup: BusySendLookupFound, Result: &r}
		return nil
	})
	if err != nil {
		return BusySendResultLookup{}, err
	}
	return out, nil
}

// readBusySendPairAbsent tells a pair never stored (unknown) from an
// ownership row whose Message is missing, which is a corrupt store, never an
// answer that the pair is free.
func readBusySendPairAbsent(ctx context.Context, conn *sql.Conn, storeID string, pair BusySendPair, out *BusySendResultLookup) error {
	var owned bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM busy_send_results
	    WHERE caller_uuid = ? AND delivery_request_id = ? AND store_id = ?)`,
		pair.CallerUUID, pair.DeliveryRequestID, storeID).Scan(&owned); err != nil {
		return err
	}
	if owned {
		return fmt.Errorf("store: busy-send pair has no Message")
	}
	*out = BusySendResultLookup{Lookup: BusySendLookupUnknown}
	return nil
}

// readBusySendEarlierPair names the earlier Message a Result waits behind by
// its confirmed pair; an unconfirmed earlier Message has none to show.
func readBusySendEarlierPair(ctx context.Context, conn *sql.Conn, messageID string) (*BusySendPair, error) {
	var p BusySendPair
	err := conn.QueryRowContext(ctx, `SELECT caller_uuid, delivery_request_id FROM busy_send_results WHERE message_id = ?`,
		messageID).Scan(&p.CallerUUID, &p.DeliveryRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
