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
}

type BusyConversationFact struct {
	ID, Publication, Provider, Coverage string
	Generation, Revision, AcceptedSeq   int64
	RetiredReason, ReplacedBy           sql.NullString
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
	rows, err := tx.Query(`SELECT conversation_id, publication, provider, busy_send, generation, revision, accepted_seq, retired_reason, replaced_by FROM busy_conversations WHERE instance_id = ? ORDER BY generation`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c BusyConversationFact
		if err = rows.Scan(&c.ID, &c.Publication, &c.Provider, &c.Coverage, &c.Generation, &c.Revision, &c.AcceptedSeq, &c.RetiredReason, &c.ReplacedBy); err != nil {
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
