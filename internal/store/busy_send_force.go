package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrBusySendLaunchChanged means the instance's published current Launch is
// no longer the one its caller examined: a resume or reuse published another
// Launch between the caller's read and its write. Nothing was written; the
// caller decides again from a fresh read.
var ErrBusySendLaunchChanged = errors.New("store: busy-send Launch changed since it was examined")

// BusySendPublishedLaunch is an instance's published current Launch as a
// call about the current Launch examines it (SR-9.1): its conversation, its
// own Launch id, launch token ("" for NULL) and provider, and its own
// recorded coverage, all read together. Coverage is the Launch's
// (busy_send_launches.busy_send, the same fact as spawns.busy_send), never
// its conversation's: a covered conversation can have an uncovered current
// Launch, and the conversation's coverage serves only new-Launch decisions
// (ResumeLaunchCoverage). A resume or reuse candidate staged meanwhile is
// never it, although spawns.launch_token may already name the candidate.
type BusySendPublishedLaunch struct {
	ConversationID string
	LaunchID       string
	LaunchToken    string
	Provider       string
	Coverage       BusySendCoverage
}

// GetBusySendPublishedLaunch reads the published current Launch in one read
// transaction. No row is ErrSpawnNotFound; a missing or inconsistent binding,
// or a read failure, is an error, never an uncovered answer. It writes nothing
// and runs no Active Check. Every call that records a fact bound to the
// current Launch must use it to read that Launch's identity and coverage
// together, then write through a guarded insert that rereads it, as
// InsertBusySendForce does; never take spawns.launch_token or
// Spawn.Identity.Token as the published Launch's token, since either may
// already name a staged candidate.
func (s *Store) GetBusySendPublishedLaunch(instanceID string) (BusySendPublishedLaunch, error) {
	return s.GetBusySendPublishedLaunchWithin(context.Background(), instanceID, DefaultLockWait)
}

// GetBusySendPublishedLaunchWithin is GetBusySendPublishedLaunch within ctx,
// its waits bounded by maxWait (readWithin; a wait cut short is
// ErrStoreBusy): keyless pause passes its one call budget. Its result is read
// only once the read completed: a read the budget cuts short returns its
// error alone, while its abandoned operation may still be running.
func (s *Store) GetBusySendPublishedLaunchWithin(ctx context.Context, instanceID string, maxWait time.Duration) (BusySendPublishedLaunch, error) {
	var out BusySendPublishedLaunch
	err := s.WithContext(ctx).readWithin(maxWait, "", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		p, err := readExaminedBusySendLaunch(ctx, conn, instanceID)
		if err != nil {
			return err
		}
		out = BusySendPublishedLaunch{ConversationID: p.conversation, LaunchID: p.launch, LaunchToken: p.token,
			Provider: p.provider, Coverage: p.launchCoverage}
		return nil
	})
	if err != nil {
		return BusySendPublishedLaunch{}, err
	}
	return out, nil
}

// readExaminedBusySendLaunch is the published current Launch of an existing
// row (readBusySendPublished): ErrSpawnNotFound for no row, and an error that
// names the instance and holds ErrBusySendNoBinding for a row with no
// consistent published binding.
func readExaminedBusySendLaunch(ctx context.Context, conn busySendLaunchSQL, instanceID string) (busySendPublishedRow, error) {
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spawns WHERE instance_id = ?)`, instanceID).Scan(&exists); err != nil {
		return busySendPublishedRow{}, err
	}
	if !exists {
		return busySendPublishedRow{}, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	p, err := readBusySendPublished(ctx, conn, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, busySendNoBindingError{instanceID}
	}
	return p, err
}

// ErrBusySendNoBinding is a row that exists with no consistent published
// busy-send binding (history from before the core): a fact of the row, never
// a store failure, which callers keep apart from errors reading it.
var ErrBusySendNoBinding = errors.New("store: no consistent published busy-send Launch binding")

// busySendNoBindingError names the instance and is ErrBusySendNoBinding.
type busySendNoBindingError struct{ instanceID string }

func (e busySendNoBindingError) Error() string {
	return fmt.Sprintf("store: %s has no consistent published busy-send Launch binding", e.instanceID)
}

func (e busySendNoBindingError) Is(target error) bool { return target == ErrBusySendNoBinding }

// InsertBusySendForce stores one force attempt (SR-4.1): every call, refused
// ones and repeats included, is its own row under its own force_id. The
// attempt names the published current Launch its caller examined, as
// GetBusySendPublishedLaunch read it: ConversationID, LaunchID, that Launch's
// own LaunchToken and Provider. In one write transaction the store reads the
// published current Launch again and inserts only when it is that same
// Launch, so the attempt never names a staged candidate's token or a Launch
// published after the caller's read: a different Launch is
// ErrBusySendLaunchChanged and a missing row ErrSpawnNotFound, with nothing
// written. A write lock not taken within the store's wait is ErrStoreBusy,
// with nothing written, as a busy GetBusySendPublishedLaunch is; a failed
// COMMIT is unconfirmed, never ErrStoreBusy. It stores only attempts that
// began no hand-over (no HandoverID, no carried Messages); a force that
// carries Messages is not built in this release. It runs no Active Check
// and starts no work. Every force attempt must be stored through it, with
// the Launch GetBusySendPublishedLaunch read; no other path inserts into
// busy_send_forces.
func (s *Store) InsertBusySendForce(attempt BusySendForce) error {
	if attempt.ForceID == "" || attempt.InstanceID == "" || attempt.OwnerCallID == "" || attempt.LaunchID == "" {
		return fmt.Errorf("store: insert busy-send force: force_id, instance_id, owner_call_id and launch_id are required")
	}
	if attempt.HandoverID != "" || len(attempt.CarriedPairs) > 0 {
		return fmt.Errorf("store: insert busy-send force: an attempt that began a hand-over is not supported")
	}
	err := s.inWriteTx(DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		p, err := readExaminedBusySendLaunch(ctx, conn, attempt.InstanceID)
		if err != nil {
			return err
		}
		if p.launch != attempt.LaunchID || p.conversation != attempt.ConversationID ||
			p.token != attempt.LaunchToken || p.provider != attempt.Provider {
			return ErrBusySendLaunchChanged
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO busy_send_forces
		    (force_id, store_id, instance_id, conversation_id, launch_id, launch_token, provider, owner_call_id, revision,
		     call_deadline_ms, call_ended_at_ms, outcome, reason, started_at_ms, ended_at_ms)
		    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			attempt.ForceID, s.StoreID(), attempt.InstanceID, p.conversation, p.launch,
			nullableStringArg(p.token), p.provider, attempt.OwnerCallID, attempt.Revision,
			attempt.CallDeadlineMillis, positiveInt64Arg(attempt.CallEndedAtMillis), string(attempt.Outcome),
			nullableStringArg(string(attempt.Reason)), attempt.StartedAtMillis, positiveInt64Arg(attempt.EndedAtMillis))
		return err
	})
	if errors.Is(err, ErrSpawnNotFound) || errors.Is(err, ErrBusySendLaunchChanged) {
		return err
	}
	if err != nil {
		return fmt.Errorf("store: insert busy-send force: %w", err)
	}
	return nil
}

// ReadBusySendForce reads one force attempt by force_id (SR-4.1), from
// durable facts only: no Active Check, provider, tmux or process probe, and
// no write. The attempt needs no agent row, so it reads the same after
// restarts, Launch End and the agent's deletion. An unknown force_id is
// BusySendLookupUnknown and an expired attempt BusySendLookupRetentionExpired,
// both with no ForceResult; a read failure is an error, never either answer.
// Every read of a force attempt by force_id must use it, so no reader turns a
// store failure into unknown or starts work.
func (s *Store) ReadBusySendForce(forceID string) (BusySendForceLookup, error) {
	var out BusySendForceLookup
	err := s.readWithin(DefaultLockWait, "store: read busy-send force", func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return err
		}
		defer rollbackConn(ctx, conn)
		var err error
		out, err = readBusySendForceByID(ctx, conn, s.StoreID(), forceID)
		return err
	})
	if err != nil {
		return BusySendForceLookup{}, err
	}
	return out, nil
}

// readBusySendForceByID is ReadBusySendForce's read inside its caller's
// transaction, shared with the delivery observation's force summary.
func readBusySendForceByID(ctx context.Context, conn *sql.Conn, storeID, forceID string) (BusySendForceLookup, error) {
	var f BusySendForce
	var conversation, launch, token, provider, handover, reason sql.NullString
	var callEnded, ended, expired sql.NullInt64
	var outcome, retention string
	err := conn.QueryRowContext(ctx, `SELECT force_id, instance_id, conversation_id, launch_id, launch_token, provider,
	        owner_call_id, revision, handover_id, call_deadline_ms, call_ended_at_ms, outcome, reason,
	        started_at_ms, ended_at_ms, retention, expired_at_ms
	    FROM busy_send_forces WHERE force_id = ? AND store_id = ?`, forceID, storeID).Scan(
		&f.ForceID, &f.InstanceID, &conversation, &launch, &token, &provider, &f.OwnerCallID, &f.Revision,
		&handover, &f.CallDeadlineMillis, &callEnded, &outcome, &reason, &f.StartedAtMillis, &ended,
		&retention, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		return BusySendForceLookup{Lookup: BusySendLookupUnknown}, nil
	}
	if err != nil {
		return BusySendForceLookup{}, err
	}
	if BusySendRetention(retention) == BusySendExpired {
		return BusySendForceLookup{Lookup: BusySendLookupRetentionExpired}, nil
	}
	f.ConversationID, f.LaunchID, f.LaunchToken, f.Provider = conversation.String, launch.String, token.String, provider.String
	f.HandoverID, f.Outcome, f.Reason = handover.String, BusySendForceOutcome(outcome), BusySendForceReason(reason.String)
	f.CallEndedAtMillis, f.EndedAtMillis, f.Retention = callEnded.Int64, ended.Int64, BusySendRetention(retention)
	if f.CarriedPairs, err = readBusySendForcePairs(ctx, conn, forceID); err != nil {
		return BusySendForceLookup{}, err
	}
	return BusySendForceLookup{Lookup: BusySendLookupFound, ForceResult: &f}, nil
}

// readBusySendForcePairs reads a force's carried confirmed pairs in carried
// order; an unconfirmed carried Message has no pair and is left out.
func readBusySendForcePairs(ctx context.Context, conn *sql.Conn, forceID string) ([]BusySendPair, error) {
	rows, err := conn.QueryContext(ctx, `SELECT r.caller_uuid, r.delivery_request_id
	    FROM busy_send_force_members m JOIN busy_send_results r ON r.message_id = m.message_id
	    WHERE m.force_id = ? ORDER BY m.member_index`, forceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pairs := []BusySendPair{}
	for rows.Next() {
		var p BusySendPair
		if err := rows.Scan(&p.CallerUUID, &p.DeliveryRequestID); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}
