package apitest

import (
	"database/sql"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/store"
)

// BusySendFacts is the canonical persisted coverage observer (reusable fixture).
// It includes unpublished/retained records so a staged launch cannot hide a
// partial publication behind spawns' projection. No provider work is performed.
type BusySendFacts struct {
	Binding       string
	Coverage      store.BusySendCoverage
	Conversations []BusyConversationFact
	Launches      []BusyLaunchFact
	Messages      []BusyMessageFact
}

type BusyConversationFact struct {
	ID, Publication, Provider, Coverage string
	Generation, Revision, AcceptedSeq   int64
	RetiredReason, ReplacedBy           sql.NullString
	Defaults                            store.BusySendAgentDefaults
	ReservedCount, ReservedBytes        int64
}

// BusyMessageFact is one Message targeting the instance: its custody, its
// immutable options and exact stored text, and its confirmed pair (NULL for
// an unconfirmed Message).
type BusyMessageFact struct {
	ID, Conversation, SendMode, Place string
	Seq                               int64
	Text                              []byte
	Confirmed, Reserved               bool
	CallerUUID, RequestID             sql.NullString
}

type BusyLaunchFact struct {
	ID, Conversation, Provider, Coverage, Kind, Phase string
	Life, Revision                                    int64
	Token, EndCause                                   sql.NullString
	EndedAt                                           sql.NullInt64
}

// ReadBusySendFacts observes all core facts for id in one read transaction.
func ReadBusySendFacts(path, id string) (BusySendFacts, error) {
	var out BusySendFacts
	db, err := openRawStore(path)
	if err != nil {
		return out, err
	}
	defer db.Close() //nolint:errcheck
	tx, err := db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err = tx.QueryRow(`SELECT COALESCE(busy_conversation_id, ''), busy_send FROM spawns WHERE instance_id = ?`, id).Scan(&out.Binding, &out.Coverage); err != nil {
		return out, err
	}
	rows, err := tx.Query(`SELECT conversation_id, publication, provider, busy_send, generation, revision, accepted_seq, retired_reason, replaced_by,
	    default_send_mode, default_confirmed, confirm_from_transcripts, reserved_count, reserved_bytes FROM busy_conversations WHERE instance_id = ? ORDER BY generation`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c BusyConversationFact
		if err = rows.Scan(&c.ID, &c.Publication, &c.Provider, &c.Coverage, &c.Generation, &c.Revision, &c.AcceptedSeq, &c.RetiredReason, &c.ReplacedBy,
			&c.Defaults.SendMode, &c.Defaults.Confirmed, &c.Defaults.ConfirmFromTranscripts, &c.ReservedCount, &c.ReservedBytes); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Conversations = append(out.Conversations, c)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(`SELECT launch_id, conversation_id, provider, busy_send, kind, phase, life_number, revision, launch_token, end_cause, ended_at_ms FROM busy_send_launches WHERE instance_id = ? ORDER BY recorded_at_ms, launch_id`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var l BusyLaunchFact
		if err = rows.Scan(&l.ID, &l.Conversation, &l.Provider, &l.Coverage, &l.Kind, &l.Phase, &l.Life, &l.Revision, &l.Token, &l.EndCause, &l.EndedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Launches = append(out.Launches, l)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(`SELECT m.message_id, m.conversation_id, m.send_mode, m.place, m.accepted_seq, m.text, m.confirmed, m.reserved,
	    p.caller_uuid, p.delivery_request_id FROM busy_send_messages m LEFT JOIN busy_send_results p ON p.message_id = m.message_id
	    WHERE m.instance_id = ? ORDER BY m.accepted_at_ms, m.accepted_seq`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var m BusyMessageFact
		if err = rows.Scan(&m.ID, &m.Conversation, &m.SendMode, &m.Place, &m.Seq, &m.Text, &m.Confirmed, &m.Reserved, &m.CallerUUID, &m.RequestID); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Messages = append(out.Messages, m)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// SeedBusySendCoverage creates contradictory membership/conversation facts
// without changing the binary's covered set. Launch and conversation may differ
// (an uncovered Launch in a covered conversation); no row version advances.
func SeedBusySendCoverage(path, id string, launch, conversation store.BusySendCoverage) error {
	db, err := openRawStore(path)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, q := range []struct {
		sql   string
		value store.BusySendCoverage
	}{
		{`UPDATE spawns SET busy_send=? WHERE instance_id=?`, launch},
		{`UPDATE busy_send_launches SET busy_send=? WHERE instance_id=? AND phase='current'`, launch},
		{`UPDATE busy_conversations SET busy_send=? WHERE instance_id=? AND publication='published'`, conversation},
	} {
		r, e := tx.Exec(q.sql, string(q.value), id)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return fmt.Errorf("SeedBusySendCoverage: expected one row, got %d", n)
		}
	}
	return tx.Commit()
}

// SeedBusySendRoute records the current Launch's route kind and wake file as
// the route that registers them (a later release) would, so admission sees a
// reachable route and touches wakePath. No route or file is created.
func SeedBusySendRoute(path, id, routeKind, wakePath string) error {
	return execBusySendSeed(path, "SeedBusySendRoute", `UPDATE busy_send_launches SET route_kind = ?, wake_path = ?
	    WHERE instance_id = ? AND phase = 'current'`, routeKind, wakePath, id)
}

// SeedBusySendReservation sets the published conversation's held reservation
// (reserved_count, reserved_bytes), as custody that can return to Held would
// leave it, so a capacity limit is reached without that many sends.
func SeedBusySendReservation(path, id string, count, bytes int64) error {
	return execBusySendSeed(path, "SeedBusySendReservation", `UPDATE busy_conversations SET reserved_count = ?, reserved_bytes = ?
	    WHERE instance_id = ? AND publication = 'published'`, count, bytes, id)
}

// SeedBusySendDefaults sets the published conversation's Agent Defaults and
// confirm_from_transcripts, as an earlier launch that chose them would have
// published them.
func SeedBusySendDefaults(path, id string, d store.BusySendAgentDefaults) error {
	return execBusySendSeed(path, "SeedBusySendDefaults", `UPDATE busy_conversations SET default_send_mode = ?, default_confirmed = ?,
	    confirm_from_transcripts = ? WHERE instance_id = ? AND publication = 'published'`, string(d.SendMode), d.Confirmed, d.ConfirmFromTranscripts, id)
}

// execBusySendSeed runs one seed's update, which must change exactly one row.
func execBusySendSeed(path, name, query string, args ...any) error {
	db, err := openRawStore(path)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	r, err := db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%s: %d rows, %v; want one", name, n, err)
	}
	return nil
}

// SeedBusySendCandidateProcess models a historical timed-out creation whose
// pane identity was saved without candidate publication. It is intentionally
// separate from SeedSpawn: it never changes the published core facts or token.
func SeedBusySendCandidateProcess(path, id string, pid int, start string) error {
	db, err := openRawStore(path)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	_, err = db.Exec(`UPDATE spawns SET pane_id='%99',pane_pid=?,pane_starttime=? WHERE instance_id=? AND EXISTS(SELECT 1 FROM busy_send_launches l WHERE l.instance_id=spawns.instance_id AND l.launch_token=spawns.launch_token AND l.phase='uncertain')`, pid, start, id)
	return err
}
