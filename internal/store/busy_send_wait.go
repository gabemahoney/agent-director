package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Result and wait transitions shared by the inbox's custody writers (SR-2.6,
// SR-3.3a, SR-5.1, SR-5.2). Custody and the public Result are separate facts:
// these helpers change only non-Final Results, never a Final one, and never
// move custody.

// busySendFinalReasonSQL is a finalized sent_unconfirmed Message's reason, by
// SR-3.3a's precedence: the sticky wait_turn_raced exposure first, then
// confirmation_off for an unconfirmed Message, else the one bound parameter
// (handed_over_not_proven at Claude's qualified boundary, launch_ended at
// Launch End).
const busySendFinalReasonSQL = `CASE WHEN race_exposed = 1 THEN 'wait_turn_raced'
    WHEN confirmed = 0 THEN 'confirmation_off' ELSE ? END`

// finalizeBusySendSent makes every non-Final Message that where selects Final
// sent_unconfirmed at now, with busySendFinalReasonSQL's reason and no wait.
// where is a fixed SQL fragment on busy_send_messages; args are its arguments.
func finalizeBusySendSent(ctx context.Context, q busySendLaunchSQL, confirmedReason BusySendResultReason, now int64, where string, args ...any) error {
	_, err := q.ExecContext(ctx, `UPDATE busy_send_messages SET result_class = 'sent_unconfirmed', result_final = 1,
	        final_at_ms = ?, result_reason = `+busySendFinalReasonSQL+`, wait_reason = NULL, wait_since_ms = NULL,
	        wait_earlier_message_id = NULL, revision = revision + 1
	    WHERE result_final = 0 AND `+where, append([]any{now, string(confirmedReason)}, args...)...)
	return err
}

// setBusySendWait records a non-Final Message's wait when its cause changes
// (SR-2.6): reason and earlier ("" for none) replace the stored ones with
// since = now; the same cause keeps its since. A non-Final Result's reason is
// its wait reason. A Final Result is never touched.
func setBusySendWait(ctx context.Context, q busySendLaunchSQL, messageID string, reason BusySendWaitReason, earlier string, now int64) error {
	_, err := q.ExecContext(ctx, `UPDATE busy_send_messages SET wait_reason = ?, result_reason = ?, wait_since_ms = ?,
	        wait_earlier_message_id = ?, revision = revision + 1
	    WHERE message_id = ? AND result_final = 0
	        AND (wait_reason IS NOT ? OR wait_earlier_message_id IS NOT ? OR result_reason IS NOT ?)`,
		string(reason), string(reason), now, nullableStringArg(earlier), messageID,
		string(reason), nullableStringArg(earlier), string(reason))
	return err
}

// setBusySendWaits is setBusySendWait for every Message where selects.
func setBusySendWaits(ctx context.Context, q busySendLaunchSQL, reason BusySendWaitReason, now int64, where string, args ...any) error {
	_, err := q.ExecContext(ctx, `UPDATE busy_send_messages SET wait_reason = ?, result_reason = ?, wait_since_ms = ?,
	        wait_earlier_message_id = NULL, revision = revision + 1
	    WHERE result_final = 0 AND (wait_reason IS NOT ? OR wait_earlier_message_id IS NOT NULL OR result_reason IS NOT ?)
	        AND `+where, append([]any{string(reason), string(reason), now, string(reason), string(reason)}, args...)...)
	return err
}

// refreshBusySendHeldWaits records the conversation's Held Messages' waits
// after custody changed (SR-2.6): while a hand-over owns the conversation
// every one waits handover_in_progress, as admission records it; otherwise
// the first waits first and each later one earlier_message, behind the Held
// Message before it. Unchanged causes keep their since.
func refreshBusySendHeldWaits(ctx context.Context, q busySendLaunchSQL, conversation string, first BusySendWaitReason, now int64) error {
	var owner sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT owner_handover_id FROM busy_conversations WHERE conversation_id = ?`,
		conversation).Scan(&owner); err != nil {
		return err
	}
	held := `conversation_id = ? AND place = 'held'`
	if owner.Valid {
		return setBusySendWaits(ctx, q, BusySendWaitHandoverInProgress, now, held, conversation)
	}
	var firstID string
	var firstSeq int64
	err := q.QueryRowContext(ctx, `SELECT message_id, accepted_seq FROM busy_send_messages WHERE `+held+`
	    ORDER BY accepted_seq LIMIT 1`, conversation).Scan(&firstID, &firstSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := setBusySendWait(ctx, q, firstID, first, "", now); err != nil {
		return err
	}
	const before = `(SELECT p.message_id FROM busy_send_messages p WHERE p.conversation_id = busy_send_messages.conversation_id
	    AND p.place = 'held' AND p.accepted_seq < busy_send_messages.accepted_seq ORDER BY p.accepted_seq DESC LIMIT 1)`
	_, err = q.ExecContext(ctx, `UPDATE busy_send_messages SET wait_reason = 'earlier_message', result_reason = 'earlier_message',
	        wait_since_ms = ?, wait_earlier_message_id = `+before+`, revision = revision + 1
	    WHERE `+held+` AND result_final = 0 AND accepted_seq > ?
	        AND (wait_reason IS NOT 'earlier_message' OR wait_earlier_message_id IS NOT `+before+`)`, now, conversation, firstSeq)
	return err
}

// freeBusySendReservations ends the held reservation of every reserved
// Message where selects, in conversation's counts (SR-1.6): only custody that
// can no longer return to Held is freed.
func freeBusySendReservations(ctx context.Context, q busySendLaunchSQL, conversation string, where string, args ...any) error {
	var count, size int64
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(text_bytes), 0) FROM busy_send_messages
	    WHERE reserved = 1 AND conversation_id = ? AND `+where, append([]any{conversation}, args...)...).Scan(&count, &size); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if _, err := q.ExecContext(ctx, `UPDATE busy_send_messages SET reserved = 0, revision = revision + 1
	    WHERE reserved = 1 AND conversation_id = ? AND `+where, append([]any{conversation}, args...)...); err != nil {
		return err
	}
	return busySendChange(ctx, q, `UPDATE busy_conversations SET reserved_count = reserved_count - ?,
	        reserved_bytes = reserved_bytes - ?, revision = revision + 1
	    WHERE conversation_id = ? AND reserved_count >= ? AND reserved_bytes >= ?`, count, size, conversation, count, size)
}

// BusySendWaitRequest records why an eligible event issued nothing (SR-2.6,
// SR-3.2): the event's Launch and the cause the route or a store guard gave.
type BusySendWaitRequest struct {
	InstanceID string
	LaunchID   string
	Reason     BusySendWaitReason
	At         time.Time
}

// RecordBusySendWait records a no-issue cause on the first Held Message of
// the published conversation, when req.LaunchID is still its current covered
// Launch: handover_in_progress while a hand-over owns the conversation, else
// req.Reason. A repeated cause keeps its since; later Held Messages keep
// waiting behind the Message before them. Waiting has no age deadline, sets no
// needs_intelligence, writes no lifecycle state and claims nothing. It returns
// whether a wait changed; a stale Launch changes nothing.
func (s *Store) RecordBusySendWait(ctx context.Context, req BusySendWaitRequest) (bool, error) {
	if req.Reason == "" {
		return false, fmt.Errorf("store: busy-send wait needs a reason")
	}
	var changed bool
	err := s.inWriteTxContext(ctx, DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		p, err := readExaminedBusySendLaunch(ctx, conn, req.InstanceID)
		if err != nil || p.launch != req.LaunchID || p.launchCoverage != BusySendCovered {
			return err
		}
		var first string
		err = conn.QueryRowContext(ctx, `SELECT message_id FROM busy_send_messages
		    WHERE conversation_id = ? AND place = 'held' AND result_final = 0 ORDER BY accepted_seq LIMIT 1`, p.conversation).Scan(&first)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		reason := req.Reason
		if p.ownerHandover != "" {
			reason = BusySendWaitHandoverInProgress
		}
		r, err := conn.ExecContext(ctx, `UPDATE busy_send_messages SET wait_reason = ?, result_reason = ?, wait_since_ms = ?,
		        wait_earlier_message_id = NULL, revision = revision + 1
		    WHERE message_id = ? AND result_final = 0 AND (wait_reason IS NOT ? OR wait_earlier_message_id IS NOT NULL)`,
			string(reason), string(reason), busySendAt(req.At), first, string(reason))
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		changed = n > 0
		return err
	})
	return changed, err
}
