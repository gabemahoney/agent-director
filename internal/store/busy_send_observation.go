package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// BusySendObservation is a Covered Agent's delivery facts as get and status
// show them (SR-5.6), read in one transaction with the published conversation
// and current Launch they belong to. It is observational: reading it issues
// no input, wakes no route, changes no Result and repairs nothing.
type BusySendObservation struct {
	// Provider is the published conversation's provider, whose row of the
	// capability table get publishes as delivery_capabilities.
	Provider string
	// Defaults are the published conversation's Agent Defaults.
	Defaults BusySendAgentDefaults
	// HeldCount and HeldBytes are the Messages actually Held and their text
	// bytes. Reserved capacity (busy_conversations.reserved_*) is larger
	// while custody that can return to Held is out of the inbox.
	HeldCount, HeldBytes int64
	// FirstWait is the first Held Message's wait; nil with none Held.
	FirstWait *BusySendWait
	// HandoverCount is the non-Final hand-overs: distinct hand-overs with at
	// least one member, out of Held, whose Result is not Final
	// (readBusySendHandoverCount). It counts hand-overs, not Messages.
	HandoverCount int64
	// StartupWait is waiting startup work (SR-6.1). Always nil here: Claude's
	// startup prompt is part of its Launch and never waits in the inbox.
	StartupWait *BusySendWait
	// Force is the conversation's pending force, else its latest force that
	// began a hand-over (busy_conversations.handover_force_id); nil when
	// neither exists, so a force refused before any hand-over never shows.
	Force *BusySendForce
	// AwaitingTurnStart is busySendAwaitingTurnStart's count.
	AwaitingTurnStart int64
}

// ReadBusySendObservation reads instanceID's delivery facts (SR-5.6), or nil
// when its published current Launch is not covered: an uncovered row shows
// none. Its waits are bounded by maxWait (readWithin; a wait cut short is
// ErrStoreBusy). No row is ErrSpawnNotFound; a missing or inconsistent
// binding, or a read failure, is an error, never a nil observation.
func (s *Store) ReadBusySendObservation(instanceID string, maxWait time.Duration) (*BusySendObservation, error) {
	var out *BusySendObservation
	err := s.readWithin(maxWait, "", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		p, err := readExaminedBusySendLaunch(ctx, conn, instanceID)
		if err != nil || p.launchCoverage != BusySendCovered {
			return err
		}
		o := BusySendObservation{Provider: p.provider, Defaults: p.defaults}
		if o.HeldCount, o.HeldBytes, o.FirstWait, err = readBusySendHeld(ctx, conn, p.conversation); err != nil {
			return err
		}
		if o.HandoverCount, err = readBusySendHandoverCount(ctx, conn, p.conversation); err != nil {
			return err
		}
		if o.Force, err = readBusySendForceSummary(ctx, conn, s.StoreID(), p.conversation); err != nil {
			return err
		}
		o.AwaitingTurnStart = busySendAwaitingTurnStart(p)
		out = &o
		return nil
	})
	if errors.Is(err, ErrSpawnNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("store: read busy-send delivery: %w", err)
	}
	return out, nil
}

// readBusySendHeld counts conversation's Held Messages and their bytes and
// reads the first one's wait.
func readBusySendHeld(ctx context.Context, conn *sql.Conn, conversation string) (int64, int64, *BusySendWait, error) {
	var count, size int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(text_bytes), 0) FROM busy_send_messages
	    WHERE conversation_id = ? AND place = 'held'`, conversation).Scan(&count, &size); err != nil {
		return 0, 0, nil, err
	}
	var reason, earlier sql.NullString
	var since sql.NullInt64
	err := conn.QueryRowContext(ctx, `SELECT wait_reason, wait_since_ms, wait_earlier_message_id FROM busy_send_messages
	    WHERE conversation_id = ? AND place = 'held' ORDER BY accepted_seq LIMIT 1`, conversation).Scan(&reason, &since, &earlier)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !reason.Valid {
		return count, size, nil, nil
	}
	if err != nil {
		return 0, 0, nil, err
	}
	wait := &BusySendWait{Reason: BusySendWaitReason(reason.String), SinceMillis: since.Int64}
	if earlier.Valid {
		if wait.Earlier, err = readBusySendEarlierPair(ctx, conn, earlier.String); err != nil {
			return 0, 0, nil, err
		}
	}
	return count, size, wait, nil
}

// readBusySendHandoverCount counts conversation's non-Final hand-overs: the
// distinct hand-overs that are the latest attempt of at least one Message
// that left Held and whose Result is not Final. It reads the durable
// membership (busy_send_handover_members), so a hand-over still counts after
// its Messages' custody closed, and a Message returned to Held and handed over
// again counts only its latest hand-over. A hand-over of three such Messages
// counts once.
func readBusySendHandoverCount(ctx context.Context, conn *sql.Conn, conversation string) (int64, error) {
	var count int64
	err := conn.QueryRowContext(ctx, `SELECT COUNT(DISTINCT hm.handover_id)
	    FROM busy_send_messages m
	    JOIN busy_send_handover_members hm ON hm.message_id = m.message_id
	    JOIN busy_send_handovers h ON h.handover_id = hm.handover_id
	    WHERE m.conversation_id = ? AND m.place <> 'held' AND m.result_final = 0
	        AND h.handover_seq = (SELECT MAX(h2.handover_seq) FROM busy_send_handover_members hm2
	            JOIN busy_send_handovers h2 ON h2.handover_id = hm2.handover_id WHERE hm2.message_id = m.message_id)`,
		conversation).Scan(&count)
	return count, err
}

// readBusySendForceSummary is the force get and status show: the
// conversation's running or pending force, else the latest force that began a
// hand-over, never a later refused attempt.
func readBusySendForceSummary(ctx context.Context, conn *sql.Conn, storeID, conversation string) (*BusySendForce, error) {
	var forceID sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT COALESCE(
	        (SELECT force_id FROM busy_send_forces WHERE conversation_id = ? AND outcome IN ('running', 'pending')),
	        (SELECT handover_force_id FROM busy_conversations WHERE conversation_id = ?))`,
		conversation, conversation).Scan(&forceID)
	if err != nil || !forceID.Valid {
		return nil, err
	}
	lookup, err := readBusySendForceByID(ctx, conn, storeID, forceID.String)
	return lookup.ForceResult, err
}

// busySendAwaitingTurnStart is SR-5.6's awaiting_turn_start for the published
// current Launch: its completed hand-overs whose input is still AD-owned work
// with no evidence it was taken (SR-3.3). This is the one place the count is
// derived. Until the Claude route records those hand-overs it is zero; the
// route extends this from facts it writes in the same transaction as each
// emission intent and completion, and no read repairs them.
func busySendAwaitingTurnStart(busySendPublishedRow) int64 { return 0 }

// ReadBusySendDefaults returns the published Agent Defaults of every listed
// instance whose current Launch is covered, keyed by instance id; an
// uncovered or unknown instance has no entry. One read serves a whole list;
// its waits are bounded by maxWait, as ReadBusySendObservation's are.
func (s *Store) ReadBusySendDefaults(instanceIDs []string, maxWait time.Duration) (map[string]BusySendAgentDefaults, error) {
	wanted := make(map[string]bool, len(instanceIDs))
	for _, id := range instanceIDs {
		wanted[id] = true
	}
	out := make(map[string]BusySendAgentDefaults)
	if len(wanted) == 0 {
		return out, nil
	}
	err := s.readWithin(maxWait, "store: read busy-send defaults", func(ctx context.Context, conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `SELECT s.instance_id, c.default_send_mode, c.default_confirmed, c.confirm_from_transcripts
		    FROM spawns s JOIN busy_conversations c ON c.conversation_id = s.busy_conversation_id
		        AND c.instance_id = s.instance_id AND c.publication = 'published'
		    WHERE s.busy_send = 'covered'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, mode string
			var d BusySendAgentDefaults
			if err := rows.Scan(&id, &mode, &d.Confirmed, &d.ConfirmFromTranscripts); err != nil {
				return err
			}
			if wanted[id] {
				d.SendMode = BusySendMode(mode)
				out[id] = d
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
