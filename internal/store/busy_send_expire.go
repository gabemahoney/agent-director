package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// busySendExpirePage is the most rows one retention page selects and one
// retention write transaction updates, so admissions and hooks waiting on the
// write lock wait for at most that many indexed updates.
const busySendExpirePage = 32

// busySendMessageExpirableSQL is the one rule for a Message m that may expire,
// which its page selection and its guarded write both apply (SR-5.5; Epic
// t1.xis.52 decisions 1 and 2), with the cutoff in milliseconds as its one
// argument: retained, Final strictly before the cutoff, confirmed or not, and
// in custody no later issue can need: closed, or currently a member of a
// prompt already issued (a b.7si Waiting Item included). A Held or claimed
// Message, or one whose current membership's prompt is not issued, is kept
// until that ends. Only its current handover_id counts, so a released earlier
// attempt never defers its expiry.
const busySendMessageExpirableSQL = `m.retention = 'retained' AND m.result_final = 1 AND m.final_at_ms < ?
    AND ((m.place = 'closed' AND m.handover_id IS NULL)
      OR (m.place = 'issued' AND m.handover_id IS NOT NULL AND EXISTS (
          SELECT 1 FROM busy_send_handover_members hm
            JOIN busy_send_prompts p ON p.handover_id = hm.handover_id AND p.prompt_index = hm.prompt_index
           WHERE hm.message_id = m.message_id AND hm.handover_id = m.handover_id AND p.emission = 'issued')))`

// busySendForceExpirableSQL is the one rule for a force f that may expire,
// as busySendMessageExpirableSQL is for a Message (SR-4.1): retained and
// ended strictly before the cutoff, never running or pending.
const busySendForceExpirableSQL = `f.retention = 'retained' AND f.outcome NOT IN ('running', 'pending')
    AND f.ended_at_ms IS NOT NULL AND f.ended_at_ms < ?`

// busySendRetentionTable is one table the retention pass expires, in
// primary-key keyset pages.
type busySendRetentionTable struct {
	name string
	// lastKey reads the table's largest key, NULL for an empty table.
	lastKey string
	// page selects the next page's keys and revisions, with arguments (after,
	// last, cutoff, limit).
	page string
	// expire is one key's guarded write, with arguments (expiredAt, key,
	// revision, cutoff): it applies only while the row still holds the
	// selected revision and still meets the rule it was selected by.
	expire string
}

// busySendRetentionTables are the tables the pass expires, in order:
// Messages, then forces.
var busySendRetentionTables = []busySendRetentionTable{
	{
		name:    "Messages",
		lastKey: `SELECT MAX(message_id) FROM busy_send_messages`,
		page: `SELECT m.message_id, m.revision FROM busy_send_messages AS m
		    WHERE m.message_id > ? AND m.message_id <= ? AND ` + busySendMessageExpirableSQL + `
		    ORDER BY m.message_id LIMIT ?`,
		// Expiry clears exactly what v21's CHECKs require of an expired
		// Message and keeps every other fact, its pair, custody and hand-over
		// rows included (SR-5.5, SR-2.1).
		expire: `UPDATE busy_send_messages AS m SET retention = 'expired', expired_at_ms = ?,
		    text = NULL, text_sha256 = NULL, result_class = NULL, result_reason = NULL,
		    evidence_kind = NULL, record_id = NULL, record_sha256 = NULL, revision = revision + 1
		    WHERE m.message_id = ? AND m.revision = ? AND ` + busySendMessageExpirableSQL,
	},
	{
		name:    "forces",
		lastKey: `SELECT MAX(force_id) FROM busy_send_forces`,
		page: `SELECT f.force_id, f.revision FROM busy_send_forces AS f
		    WHERE f.force_id > ? AND f.force_id <= ? AND ` + busySendForceExpirableSQL + `
		    ORDER BY f.force_id LIMIT ?`,
		expire: `UPDATE busy_send_forces AS f SET retention = 'expired', expired_at_ms = ?, revision = revision + 1
		    WHERE f.force_id = ? AND f.revision = ? AND ` + busySendForceExpirableSQL,
	},
}

// busySendRetentionCandidate is one selected row: its key and the revision
// its guarded write compares.
type busySendRetentionCandidate struct {
	key      string
	revision int64
}

// ExpireBusySendRetention is expire's retention pass (SR-5.5, SR-4.1,
// SR-2.1): it expires every Message whose Result became Final before cutoff,
// confirmed or not, in custody no later issue can need
// (busySendMessageExpirableSQL), and then every force that ended before
// cutoff, recording expiredAt. An expired Message loses its text, digest,
// Result and receipt identity and keeps its pair, so its pair is never free
// again, and the non-content facts b.7si reads (text length, acceptance,
// custody and hand-overs). An expired force keeps its row, outcome, times and
// carried membership. A non-Final Message and a running or pending force are
// never touched, and nothing is delivered, scheduled or retried.
//
// The store never reads the clock: both times are the caller's, compared and
// recorded in whole milliseconds, and a cutoff later than any stored time
// selects every eligible record. Each table is read in key order up to its
// largest key at entry, in pages of at most busySendExpirePage rows read
// outside the write lock; each page is then written in one BEGIN IMMEDIATE
// transaction whose guarded writes recheck the selected revision and rule, so
// a row changed since its read (an admission, claim or Launch End) is skipped.
// Admission reads pair ownership in its own write transaction, so a repeat
// send racing the pass gets the owner's Result or retention_expired, never a
// new acceptance. A second pass over unchanged records changes nothing.
//
// A failure stops the pass and is returned as a wrapped error, which may hold
// ErrStoreBusy or an unconfirmed commit; pages committed before it stay
// expired, so the failure never means nothing changed, and rerunning the pass
// is safe.
func (s *Store) ExpireBusySendRetention(cutoff, expiredAt time.Time) error {
	const errPrefix = "store: expire busy-send retention"
	cut, at := cutoff.UnixMilli(), expiredAt.UnixMilli()
	lastKeys := make([]sql.NullString, len(busySendRetentionTables))
	if err := s.readWithin(DefaultLockWait, errPrefix+": read last keys", func(ctx context.Context, conn *sql.Conn) error {
		for i, t := range busySendRetentionTables {
			if err := conn.QueryRowContext(ctx, t.lastKey).Scan(&lastKeys[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for i, t := range busySendRetentionTables {
		if !lastKeys[i].Valid {
			continue
		}
		if err := s.expireBusySendTable(t, lastKeys[i].String, cut, at); err != nil {
			return fmt.Errorf("%s: %s: %w", errPrefix, t.name, err)
		}
	}
	return nil
}

// expireBusySendTable expires t's eligible rows with keys up to last, a page
// at a time. Every writer makes its keys non-empty (UUIDs), so the first page
// starts after "".
// The next page starts after the last key read, so a row its guarded write
// skipped is not selected again.
func (s *Store) expireBusySendTable(t busySendRetentionTable, last string, cutoff, at int64) error {
	after := ""
	for {
		page, err := s.readBusySendRetentionPage(t, after, last, cutoff)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		if err := s.inWriteTx(DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
			for _, c := range page {
				// The error never names the key: an unconfirmed Message's id
				// is never shown (SR-1.3).
				if _, err := conn.ExecContext(ctx, t.expire, at, c.key, c.revision, cutoff); err != nil {
					return fmt.Errorf("guarded write: %w", err)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if len(page) < busySendExpirePage {
			return nil
		}
		after = page[len(page)-1].key
	}
}

// readBusySendRetentionPage reads the next page of t's eligible keys after
// after, up to last, with their revisions, and closes its rows before
// returning, so no read is open while the page is written.
func (s *Store) readBusySendRetentionPage(t busySendRetentionTable, after, last string, cutoff int64) ([]busySendRetentionCandidate, error) {
	var page []busySendRetentionCandidate
	err := s.readWithin(DefaultLockWait, "read page", func(ctx context.Context, conn *sql.Conn) error {
		page = page[:0]
		rows, err := conn.QueryContext(ctx, t.page, after, last, cutoff, busySendExpirePage)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c busySendRetentionCandidate
			if err := rows.Scan(&c.key, &c.revision); err != nil {
				return err
			}
			page = append(page, c)
		}
		return rows.Err()
	})
	return page, err
}
