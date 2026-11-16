package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
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
	// Force is the conversation's running or pending force, else its latest
	// force that began a hand-over (busy_conversations.handover_force_id); nil
	// when neither exists, so a force refused before any hand-over never
	// shows. A running one's call has not returned (outcome running).
	Force *BusySendForce
	// AwaitingTurnStart is SR-5.6's awaiting_turn_start: the current
	// Launch's hand-overs whose issued input is still AD-owned work with no
	// evidence it was taken (SR-3.3; readBusySendAwaiting), whatever its
	// output's state, and zero once the Launch has ended. The issue commit
	// records each obligation before any output can exist, and only recorded
	// evidence clears it; no read repairs it.
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
		var err error
		out, err = readBusySendObservationOn(ctx, conn, s.StoreID(), instanceID)
		return err
	})
	if errors.Is(err, ErrSpawnNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("store: read busy-send delivery: %w", err)
	}
	return out, nil
}

// readBusySendObservationOn is ReadBusySendObservation's read in the
// caller's read transaction on conn.
func readBusySendObservationOn(ctx context.Context, conn *sql.Conn, storeID, instanceID string) (*BusySendObservation, error) {
	p, err := readExaminedBusySendLaunch(ctx, conn, instanceID)
	if err != nil || p.launchCoverage != BusySendCovered {
		return nil, err
	}
	o := BusySendObservation{Provider: p.provider, Defaults: p.defaults}
	if o.HeldCount, o.HeldBytes, o.FirstWait, err = readBusySendHeld(ctx, conn, p.conversation); err != nil {
		return nil, err
	}
	if o.HandoverCount, err = readBusySendHandoverCount(ctx, conn, p.conversation); err != nil {
		return nil, err
	}
	if o.Force, err = readBusySendForceSummary(ctx, conn, storeID, p.conversation); err != nil {
		return nil, err
	}
	if o.AwaitingTurnStart, err = readBusySendAwaiting(ctx, conn, p.conversation, p.launch); err != nil {
		return nil, err
	}
	return &o, nil
}

// BusySendDeliveries holds the delivery facts a row read took for its covered
// rows in the row read's own SQLite read transaction (ListFilters.Delivery,
// RecoveryRowsWithDelivery): the reader begins it before its row SELECT and
// holds it through these reads, on its one zero-wait acquisition of the store
// connection. A reading verb that has the row thus needs no second
// acquisition to show them, so covering an agent makes its reads no less
// available, and the facts are of the very row state read (SR-5.6, SR-9.1).
// A full collection (NewBusySendDeliveries, for get, get-many and status)
// takes each covered row's whole delivery observation; a defaults collection
// (NewBusySendDefaults, for list) takes only the Agent Defaults, in one
// batched query. Each entry is the latest read of its instance, with the row
// snapshot and launch token it was read with, and a row's own read error (no
// consistent binding) rather than failing the row read. Safe for concurrent
// use.
type BusySendDeliveries struct {
	mu           sync.Mutex
	defaultsOnly bool
	byID         map[string]busySendDelivery
}

type busySendDelivery struct {
	snapshot    RowSnapshot
	token       string
	observation *BusySendObservation
	defaults    *BusySendAgentDefaults
	err         error
}

// NewBusySendDeliveries returns an empty full collection.
func NewBusySendDeliveries() *BusySendDeliveries {
	return &BusySendDeliveries{byID: map[string]busySendDelivery{}}
}

// NewBusySendDefaults returns an empty defaults collection: Agent Defaults
// only, as list shows.
func NewBusySendDefaults() *BusySendDeliveries {
	return &BusySendDeliveries{defaultsOnly: true, byID: map[string]busySendDelivery{}}
}

// entry is the delivery facts taken with row's read, if any.
func (d *BusySendDeliveries) entry(row Spawn) (busySendDelivery, bool) {
	if d == nil {
		return busySendDelivery{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.byID[row.InstanceID]
	if !ok || e.snapshot != row.Snapshot || e.token != row.Identity.Token {
		return busySendDelivery{}, false
	}
	return e, true
}

// Lookup returns a full collection's delivery facts taken with row's read:
// found is false when none was taken for that instance at row's snapshot and
// launch token (or the collection holds defaults only), so the caller reads
// them itself. A nil observation found means its current Launch was not
// covered at that read; err is that read's own error.
func (d *BusySendDeliveries) Lookup(row Spawn) (observation *BusySendObservation, found bool, err error) {
	e, ok := d.entry(row)
	if !ok || d.defaultsOnly {
		return nil, false, nil
	}
	return e.observation, true, e.err
}

// LookupDefaults returns the Agent Defaults taken with row's read: found is
// false when none was taken for that instance at row's snapshot and launch
// token; ok is false, found, when that read showed none (its current Launch
// not covered, or no consistent published binding).
func (d *BusySendDeliveries) LookupDefaults(row Spawn) (defaults BusySendAgentDefaults, ok, found bool) {
	e, found := d.entry(row)
	switch {
	case !found:
		return BusySendAgentDefaults{}, false, false
	case e.observation != nil:
		return e.observation.Defaults, true, true
	case e.defaults != nil:
		return *e.defaults, true, true
	}
	return BusySendAgentDefaults{}, false, true
}

// beginRead begins the row read's read transaction when d is set: the row
// SELECT and read then see one snapshot. The returned end rolls it back.
func (d *BusySendDeliveries) beginRead(ctx context.Context, conn *sql.Conn) (end func(), err error) {
	if d == nil {
		return func() {}, nil
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return nil, err
	}
	return func() { rollbackConn(ctx, conn) }, nil
}

// read takes the delivery facts of rows' covered rows on conn, inside the
// read transaction beginRead began before the rows were selected. A failure
// that is the connection's or the transaction's (busy, cancelled) fails the
// whole read, as a row read's own failure does; any other is recorded for
// its row.
func (d *BusySendDeliveries) read(ctx context.Context, conn *sql.Conn, storeID string, rows []Spawn) error {
	if d == nil {
		return nil
	}
	var covered []Spawn
	for _, row := range rows {
		if row.BusySend == BusySendCovered {
			covered = append(covered, row)
		}
	}
	if len(covered) == 0 {
		return nil
	}
	got := make(map[string]busySendDelivery, len(covered))
	if d.defaultsOnly {
		defaults, err := readBusySendDefaultsOn(ctx, conn, covered)
		if err != nil {
			return err
		}
		for _, row := range covered {
			e := busySendDelivery{snapshot: row.Snapshot, token: row.Identity.Token}
			if v, ok := defaults[row.InstanceID]; ok {
				e.defaults = &v
			}
			got[row.InstanceID] = e
		}
	} else {
		for _, row := range covered {
			o, err := readBusySendObservationOn(ctx, conn, storeID, row.InstanceID)
			if err != nil && (isBusy(err) || ctx.Err() != nil) {
				return err
			}
			if err != nil {
				err = fmt.Errorf("store: read busy-send delivery: %w", err)
			}
			got[row.InstanceID] = busySendDelivery{snapshot: row.Snapshot, token: row.Identity.Token, observation: o, err: err}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	maps.Copy(d.byID, got)
	return nil
}

// readBusySendDefaultsOn reads rows' published Agent Defaults in one query on
// conn, keyed by instance id; a row whose current Launch is not covered or
// that has no consistent published binding has no entry.
func readBusySendDefaultsOn(ctx context.Context, conn *sql.Conn, rows []Spawn) (map[string]BusySendAgentDefaults, error) {
	args := make([]any, 0, len(rows))
	for _, row := range rows {
		args = append(args, row.InstanceID)
	}
	q, err := conn.QueryContext(ctx, `SELECT s.instance_id, c.default_send_mode, c.default_confirmed, c.confirm_from_transcripts
	    FROM spawns s JOIN busy_conversations c ON c.conversation_id = s.busy_conversation_id
	        AND c.instance_id = s.instance_id AND c.publication = 'published'
	    WHERE s.busy_send = 'covered' AND s.instance_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	out := make(map[string]BusySendAgentDefaults, len(rows))
	for q.Next() {
		var id, mode string
		var d BusySendAgentDefaults
		if err := q.Scan(&id, &mode, &d.Confirmed, &d.ConfirmFromTranscripts); err != nil {
			return nil, err
		}
		d.SendMode = BusySendMode(mode)
		out[id] = d
	}
	return out, q.Err()
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
