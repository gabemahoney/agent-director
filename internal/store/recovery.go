package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// LiveSpawnIdentity is the per-row identity the find-missing verdict
// engine needs to decide liveness: the instance id plus the recorded
// process identity (pid + proc_starttime) captured at SessionStart.
//
// Zero values follow the COALESCE scan convention (spawns.go): PID==0
// and ProcStarttime=="" mean the underlying columns are NULL (no
// recorded identity — a NULL-pid row that falls back to the environ
// probe-set diff). The read deliberately carries NO liveness fields;
// the guarded SetLivenessUnverified setter's transitioned return is
// the sole NULL→set signal (PM decision, SR-8.1).
type LiveSpawnIdentity struct {
	ClaudeInstanceID string
	PID              int
	ProcStarttime    string
}

// ListLiveSpawnIdentities returns a LiveSpawnIdentity for every row in a
// live (non-terminal) state. The result is the input set find-missing's
// per-row verdict engine works against (SRD §4.4). Including `pending`
// is intentional per SRD §5.2: a Spawn whose tmux session vanished
// before SessionStart fired is still "live" from the DB's view and
// should be reconciled to `missing`.
//
// pid / proc_starttime are scanned via COALESCE(pid, 0) /
// COALESCE(proc_starttime, '') so a row with no recorded identity
// yields the zero values rather than a scan error.
//
// Order is unspecified. Callers that need stable ordering sort the
// result themselves.
func (s *Store) ListLiveSpawnIdentities() ([]LiveSpawnIdentity, error) {
	placeholders := make([]string, len(liveStates))
	args := make([]any, len(liveStates))
	for i, st := range liveStates {
		placeholders[i] = "?"
		args[i] = st
	}
	q := "SELECT claude_instance_id, COALESCE(pid, 0), COALESCE(proc_starttime, '') " +
		"FROM spawns WHERE state IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list live identities: %w", err)
	}
	defer rows.Close()
	var ids []LiveSpawnIdentity
	for rows.Next() {
		var it LiveSpawnIdentity
		if err := rows.Scan(&it.ClaudeInstanceID, &it.PID, &it.ProcStarttime); err != nil {
			return nil, fmt.Errorf("store: list live identities scan: %w", err)
		}
		ids = append(ids, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list live identities iterate: %w", err)
	}
	return ids, nil
}

// SetLivenessUnverified records that a live row's process could not be
// verified (e.g. an EACCES/EPERM environ probe): it writes both
// liveness_unverified_since (= now, via CURRENT_TIMESTAMP — the store's
// existing timestamp convention) and liveness_note in a single guarded
// UPDATE, but ONLY when liveness_unverified_since is currently NULL.
//
// The write is guarded on the state set (live states only, matching
// MarkSpawnMissing's WHERE discipline) so absent or terminal-state rows
// are fail-open no-ops. Repeat calls on an already-set row preserve the
// original timestamp and return transitioned=false; the first NULL→set
// write returns transitioned=true. That transitioned bool is the sole
// NULL→set signal find-missing uses to emit exactly one probe_eacces
// tick (SR-8.1/8.4). Emits no trail events — emission stays in the
// caller.
func (s *Store) SetLivenessUnverified(instanceID, note string) (bool, error) {
	placeholders := make([]string, len(liveStates))
	args := make([]any, 0, 2+len(liveStates))
	args = append(args, note, instanceID)
	for i, st := range liveStates {
		placeholders[i] = "?"
		args = append(args, st)
	}
	q := `UPDATE spawns
	         SET liveness_unverified_since = CURRENT_TIMESTAMP,
	             liveness_note = ?
	       WHERE claude_instance_id = ?
	         AND liveness_unverified_since IS NULL
	         AND state IN (` + strings.Join(placeholders, ",") + `)`
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return false, fmt.Errorf("store: set liveness unverified: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: set liveness unverified rows affected: %w", err)
	}
	return n > 0, nil
}

// ClearLivenessUnverified NULLs both liveness columns for a row. It is
// idempotent: a row already clear (or absent) is a no-op. find-missing
// calls it for the verified-alive case and immediately after
// MarkSpawnMissing in the marking path (MarkSpawnMissing itself is NOT
// widened — SR-11/SR-8.2). Emits no trail events.
func (s *Store) ClearLivenessUnverified(instanceID string) error {
	const q = `UPDATE spawns
	              SET liveness_unverified_since = NULL,
	                  liveness_note = NULL
	            WHERE claude_instance_id = ?`
	if _, err := s.db.Exec(q, instanceID); err != nil {
		return fmt.Errorf("store: clear liveness unverified: %w", err)
	}
	return nil
}

// MarkSpawnMissing transitions a row from any live state to `missing`
// and records ended_at. find-missing calls this per row in its
// set-difference output (SRD §5.2).
//
// Returns the prior state captured immediately before the UPDATE, and nil
// error on success. Returns ("", nil) when no row matches the id or the row
// is already terminal — the cron path must be idempotent: an aborted
// previous run that already marked some rows missing should not error out on
// the next sweep. Callers can distinguish "write happened" from "no-op" by
// checking whether the returned prior state is non-empty.
//
// Prior state is captured via a separate SELECT before the UPDATE (the same
// non-transactional pattern as ApplyHookTransitionResult — transactions cause
// SQLITE_BUSY under concurrent workloads with modernc.org/sqlite).
func (s *Store) MarkSpawnMissing(instanceID string) (string, error) {
	// Capture prior state before the write. If no row exists (deleted
	// concurrently), return ("", nil) — fail-open, no emit.
	priorState, found, err := s.selectPriorState(instanceID)
	if err != nil {
		return "", fmt.Errorf("store: mark spawn missing prior state: %w", err)
	}
	if !found {
		return "", nil
	}

	const q = `UPDATE spawns
	              SET state = ?, last_seen_at = CURRENT_TIMESTAMP,
	                  ended_at = CURRENT_TIMESTAMP
	            WHERE claude_instance_id = ?
	              AND state NOT IN (?, ?)`
	res, err := s.db.Exec(q, StateMissing, instanceID, StateEnded, StateMissing)
	if err != nil {
		return "", fmt.Errorf("store: mark spawn missing: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("store: mark spawn missing rows affected: %w", err)
	}
	if n == 0 {
		// Row was already terminal — no-op. Return ("", nil) to signal no write.
		return "", nil
	}
	return priorState, nil
}

// CloseOrphanedPermissionRequests denies all open permission_requests rows for
// a Spawn that has been marked missing. Each open row receives its own
// per-row UPDATE via DecidePermissionRequest with DecisionReasonFindMissing so
// the relay polling loop observes a fail-closed deny rather than spinning to
// its own internal timeout. No-op if the Spawn has no open rows.
//
// Call this immediately after MarkSpawnMissing for each missing ID. Per-row
// errors are surfaced to the caller; the caller decides whether to log-and-
// continue or abort the sweep.
func (s *Store) CloseOrphanedPermissionRequests(instanceID string) error {
	rows, err := s.OpenPermissionRequestsForSpawn(instanceID)
	if err != nil {
		return fmt.Errorf("store: close orphaned permission requests: %w", err)
	}
	for _, row := range rows {
		ok, err := s.DecidePermissionRequest(instanceID, row.RequestToken, "deny", DecisionReasonFindMissing, WriterProcessFindMissing)
		if err != nil {
			return fmt.Errorf("store: close orphaned permission request (token=%s): %w", row.RequestToken, err)
		}
		// Emit one ad.find_missing.tick per successfully closed row (SR-A-2.5).
		// The Epic 3 ad.row_mutation.committed event fires in DecidePermissionRequest
		// for the same write; this event adds the reconciliation_reason layer.
		// A trail-emit failure must not fail the store call (SR-A-3.2).
		if ok {
			_ = trail.Emit(context.Background(), "ad.find_missing.tick", map[string]any{
				"claude_instance_id":    instanceID,
				"request_token":         row.RequestToken,
				"prior_state":           nil,
				"new_state":             nil,
				"reconciliation_reason": "permission_orphan_closeout",
				"source":                "ad_find_missing",
			})
		}
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
