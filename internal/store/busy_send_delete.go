package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// execBusySendDelete keeps each existing row-deletion guard, core retirement
// and family refresh in one transaction. Every production spawns deletion must
// use this helper (or DeleteBusySendCovered), preserving its caller's guards,
// so core retirement commits with the row deletion. A miss retires nothing. A
// core write failure rolls back the row deletion and its FK actions too. No
// core rows is valid for pre-v21 history; deletion never requires a published
// binding. custody is the caller's custody rule, checked under the write lock
// before the delete; its error writes nothing.
func (s *Store) execBusySendDelete(instanceID string, custody busySendDeleteCustody, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := s.inImmediateTx(instanceID, func(ctx context.Context, conn *sql.Conn) error {
		if err := custody(ctx, conn, instanceID); err != nil {
			return err
		}
		var err error
		result, err = conn.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return errFamilyWriteNotApplied
		}
		return retireBusySendDeleted(ctx, conn, s.StoreID(), instanceID, storeNowMillis())
	})
	if errors.Is(err, errFamilyWriteNotApplied) {
		err = nil
	}
	return result, err
}

// DeleteBusySendCovered is administrator deletion of a row whose published
// conversation is covered (SR-6.3; M1's Delete under G4; sf-answer-2), with
// ev, positive exit evidence for every Launch of that conversation. In one
// transaction: the evidence is validated against the scope read under the
// write lock; the row is deleted only while it still holds examined, so no
// identity, state or Launch written since the judgement is deleted unjudged;
// E1's retirement records record_deleted, with its custody disposition, for
// every Launch End not yet recorded and retires the conversation
// (retireBusySendDeleted), keeping any earlier end cause; every Launch's Exit
// is applied; and the
// conversation's never-emitted custody becomes Final not_started
// record_deleted (retireBusySendCustody). Intent-bearing Messages are already
// Final sent_unconfirmed, and every Final Result, confirmed pairs and lookups
// included, is kept.
//
// It returns CondApplied when the row was deleted; CondAbsent for no row;
// CondChanged when the row no longer holds examined. Evidence that no longer
// matches is an error holding ErrBusySendExitNotEstablished. In each of these
// nothing is written. It signals and stops no process.
func (s *Store) DeleteBusySendCovered(instanceID string, examined RowSnapshot, ev BusySendExitEvidence) (CondResult, error) {
	res := CondApplied
	err := s.inImmediateTx(instanceID, func(ctx context.Context, conn *sql.Conn) error {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM spawns WHERE instance_id = ?)`, instanceID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			res = CondAbsent
			return errFamilyWriteNotApplied
		}
		scope, err := validateBusySendExitEvidence(ctx, conn, s.StoreID(), instanceID, &ev)
		if err != nil {
			return err
		}
		now := storeNowMillis()
		r, err := conn.ExecContext(ctx, `DELETE FROM spawns WHERE instance_id = ? AND `+snapshotMatchSQL,
			append([]any{instanceID}, snapshotMatchArgs(examined)...)...)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil || n == 0 {
			res = CondChanged
			if err == nil {
				err = errFamilyWriteNotApplied
			}
			return err
		}
		if err := retireBusySendDeleted(ctx, conn, s.StoreID(), instanceID, now); err != nil {
			return err
		}
		if err := exitBusySendLaunches(ctx, conn, s.StoreID(), scope, now); err != nil {
			return err
		}
		return retireBusySendCustody(ctx, conn, scope.ConversationID, BusySendReasonRecordDeleted, now)
	})
	if errors.Is(err, errFamilyWriteNotApplied) {
		return res, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: delete covered spawn: %w", err)
	}
	return CondApplied, nil
}

// busySendDeleteCustody is a deletion's custody rule, run under its write
// lock before the delete.
type busySendDeleteCustody func(ctx context.Context, conn *sql.Conn, instanceID string) error

// ErrBusySendCustodyHeld is a deletion refused because the row's published
// conversation still holds custody (readBusySendCustodyHeld): expire keeps
// such a row (SR-5.5). Nothing was written.
var ErrBusySendCustodyHeld = errors.New("store: busy-send custody held")

// refuseCoveredBusySend is DeleteSpawn's rule: retirement without exit
// evidence is allowed only for a conversation that is not covered, which
// never held custody. A covered one is deleted only by DeleteBusySendCovered
// (ErrBusySendExitNotEstablished here). A row with no consistent published
// binding (pre-core history) holds no custody.
func refuseCoveredBusySend(ctx context.Context, conn *sql.Conn, instanceID string) error {
	p, err := readBusySendPublished(ctx, conn, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if p.coverage == BusySendCovered {
		return fmt.Errorf("%w: %s's conversation is covered", ErrBusySendExitNotEstablished, instanceID)
	}
	return nil
}

// keepBusySendCustody is expire's rule (SR-5.5): a row whose published
// conversation still holds custody is kept (ErrBusySendCustodyHeld), however
// old, rechecked here so an admission that raced expire's first read keeps
// it too. Its retention and tombstones are E3's.
func keepBusySendCustody(ctx context.Context, conn *sql.Conn, instanceID string) error {
	held, err := readBusySendCustodyHeld(ctx, conn, instanceID)
	if err != nil {
		return err
	}
	if held {
		return fmt.Errorf("%w: %s", ErrBusySendCustodyHeld, instanceID)
	}
	return nil
}

type busySendDeletionRow struct {
	id       string
	revision int64
}

// busySendDeletionRows captures revisions before the guarded writes below.
// Each query is bounded by the core's two partial unique indexes: at most one
// published/current and one candidate/pending row per instance.
func busySendDeletionRows(ctx context.Context, conn *sql.Conn, query, instanceID string) ([]busySendDeletionRow, error) {
	rows, err := conn.QueryContext(ctx, query, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []busySendDeletionRow
	for rows.Next() {
		var row busySendDeletionRow
		if err := rows.Scan(&row.id, &row.revision); err != nil {
			return nil, err
		}
		found = append(found, row)
	}
	return found, rows.Err()
}

// retireBusySendDeleted fences the removed instance's core publications but
// retains every record, including pairs, forces and pause claims. A new spawn
// of the same id gets fresh records and cannot inherit their authority.
// record_deleted is only a logical Launch End: it NEVER proves physical exit
// or old-issuer exclusion (SR-6.2/6.3). Existing Launch End evidence is
// immutable. A current Launch whose End it records first gets the inbox's
// custody disposition (applyBusySendLaunchEnd), as every Launch End does, so
// no Message it handed over stays non-Final after its Launch is gone.
func retireBusySendDeleted(ctx context.Context, conn *sql.Conn, storeID, instanceID string, at int64) error {
	launches, err := busySendDeletionRows(ctx, conn, `SELECT launch_id, revision FROM busy_send_launches
	    WHERE instance_id = ? AND phase IN ('current', 'candidate', 'uncertain')`, instanceID)
	if err != nil {
		return err
	}
	for _, launch := range launches {
		var unended bool
		if err := conn.QueryRowContext(ctx, `SELECT phase = 'current' AND ended_at_ms IS NULL FROM busy_send_launches
		    WHERE launch_id = ?`, launch.id).Scan(&unended); err != nil {
			return err
		}
		if unended {
			if err := applyBusySendLaunchEnd(ctx, conn, storeID, launch.id, at); err != nil {
				return err
			}
		}
		if err := busySendChange(ctx, conn, `UPDATE busy_send_launches SET phase = 'superseded',
	        ended_at_ms = COALESCE(ended_at_ms, ?), end_cause = COALESCE(end_cause, ?), revision = revision + 1
	        WHERE launch_id = ? AND revision = ? AND instance_id = ? AND phase IN ('current', 'candidate', 'uncertain')`,
			at, string(BusySendLaunchRecordDeleted), launch.id, launch.revision, instanceID); err != nil {
			return err
		}
	}
	conversations, err := busySendDeletionRows(ctx, conn, `SELECT conversation_id, revision FROM busy_conversations
	    WHERE instance_id = ? AND publication IN ('published', 'candidate')`, instanceID)
	if err != nil {
		return err
	}
	for _, conversation := range conversations {
		if err := busySendChange(ctx, conn, `UPDATE busy_conversations SET
	        retired_at_ms = CASE WHEN publication = 'published' THEN ? ELSE NULL END,
	        retired_reason = CASE WHEN publication = 'published' THEN 'record_deleted' ELSE NULL END,
	        publication = CASE WHEN publication = 'published' THEN 'retired' ELSE 'abandoned' END,
	        revision = revision + 1
	        WHERE conversation_id = ? AND revision = ? AND instance_id = ? AND publication IN ('published', 'candidate')`,
			at, conversation.id, conversation.revision, instanceID); err != nil {
			return err
		}
	}
	return nil
}
