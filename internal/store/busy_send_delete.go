package store

import (
	"context"
	"database/sql"
	"errors"
)

// execBusySendDelete keeps each existing row-deletion guard, core retirement
// and family refresh in one transaction. Every production spawns deletion must
// use this helper, preserving its caller's guards, so core retirement commits
// with the row deletion. A miss retires nothing. A core write
// failure rolls back the row deletion and its FK actions too. No core rows is
// valid for pre-v21 history; deletion never requires a published binding.
func (s *Store) execBusySendDelete(instanceID, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := s.inImmediateTx(instanceID, func(ctx context.Context, conn *sql.Conn) error {
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
		return retireBusySendDeleted(ctx, conn, instanceID, storeNowMillis())
	})
	if errors.Is(err, errFamilyWriteNotApplied) {
		err = nil
	}
	return result, err
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
// or old-issuer exclusion (SR-6.2/6.3). Existing Launch End evidence is immutable.
func retireBusySendDeleted(ctx context.Context, conn *sql.Conn, instanceID string, at int64) error {
	launches, err := busySendDeletionRows(ctx, conn, `SELECT launch_id, revision FROM busy_send_launches
	    WHERE instance_id = ? AND phase IN ('current', 'candidate', 'uncertain')`, instanceID)
	if err != nil {
		return err
	}
	for _, launch := range launches {
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
