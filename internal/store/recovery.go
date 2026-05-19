package store

import (
	"fmt"
	"strings"
	"time"
)

// ListLiveSpawnIDs returns the claude_instance_id of every row in a
// live (non-terminal) state. The result is the input set find-missing
// diffs against the prober's view (SRD §4.4). Including `pending` is
// intentional per SRD §5.2: a Spawn whose tmux session vanished
// before SessionStart fired is still "live" from the DB's view and
// should be reconciled to `missing`.
//
// Order is unspecified. Callers that need stable ordering sort the
// result themselves.
func (s *Store) ListLiveSpawnIDs() ([]string, error) {
	placeholders := make([]string, len(liveStates))
	args := make([]any, len(liveStates))
	for i, st := range liveStates {
		placeholders[i] = "?"
		args[i] = st
	}
	q := "SELECT claude_instance_id FROM spawns WHERE state IN (" +
		strings.Join(placeholders, ",") + ")"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list live ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: list live ids scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list live ids iterate: %w", err)
	}
	return ids, nil
}

// MarkSpawnMissing transitions a row from any live state to `missing`
// and records ended_at. find-missing calls this per row in its
// set-difference output (SRD §5.2).
//
// The function is a no-op (returns nil) when no row matches the id
// or when the row is already terminal — the cron path must be
// idempotent: an aborted previous run that already marked some rows
// missing should not error out on the next sweep.
func (s *Store) MarkSpawnMissing(instanceID string) error {
	const q = `UPDATE spawns
	              SET state = ?, last_seen_at = CURRENT_TIMESTAMP,
	                  ended_at = CURRENT_TIMESTAMP
	            WHERE claude_instance_id = ?
	              AND state NOT IN (?, ?)`
	_, err := s.db.Exec(q, StateMissing, instanceID, StateEnded, StateMissing)
	if err != nil {
		return fmt.Errorf("store: mark spawn missing: %w", err)
	}
	return nil
}

// DeleteTerminalOlderThan removes rows in terminal states (ended /
// missing) whose ended_at is older than `now - older`. Returns the
// count of removed rows + their IDs. Per SRD §12 the expire verb
// goes through this primitive — it does NOT compose `list`.
//
// `older` is a positive duration. A zero or negative duration removes
// every terminal row regardless of ended_at (used by
// `expire --older-than 0d`).
//
// SQLite's RETURNING clause (available in 3.35+) returns the deleted
// IDs in a single round-trip. modernc/sqlite is built against a recent
// SQLite, so the clause is safe to depend on.
func (s *Store) DeleteTerminalOlderThan(older time.Duration) (int, []string, error) {
	// Compute the deadline timestamp in Go and pass as a literal string
	// (SQLite's datetime arithmetic with `-N seconds` works but mixing
	// it with NULL ended_at values is awkward; an explicit timestamp
	// keeps the predicate tight). For older<=0 we pass a far-future
	// deadline so every terminal row qualifies.
	var deadline time.Time
	if older > 0 {
		deadline = time.Now().UTC().Add(-older)
	} else {
		deadline = time.Now().UTC().Add(24 * time.Hour)
	}

	const q = `DELETE FROM spawns
	            WHERE state IN (?, ?)
	              AND ended_at IS NOT NULL
	              AND ended_at < ?
	         RETURNING claude_instance_id`
	rows, err := s.db.Query(q, StateEnded, StateMissing, deadline.Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, nil, fmt.Errorf("store: expire query: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, nil, fmt.Errorf("store: expire scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("store: expire iterate: %w", err)
	}
	return len(ids), ids, nil
}

// DeleteSpawn removes a row by id. Returns ErrSpawnNotFound when the
// id matches no row. Per SRD §12 the `delete` verb (admin path)
// composes this primitive per id. Does NOT touch tmux sessions or
// JSONL transcripts; the caller is expected to have already torn
// those down or accept the orphan.
func (s *Store) DeleteSpawn(instanceID string) error {
	const q = `DELETE FROM spawns WHERE claude_instance_id = ?`
	res, err := s.db.Exec(q, instanceID)
	if err != nil {
		return fmt.Errorf("store: delete spawn: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete spawn rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	return nil
}
