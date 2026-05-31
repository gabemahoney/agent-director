package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNoOpenPermissionRequest is returned by decide() when no row
// exists in permission_requests for the given claude_instance_id.
// SRD §6.2: typically means the Spawn isn't currently sitting on a
// PermissionRequest hook (or one was already preempted by another).
var ErrNoOpenPermissionRequest = errors.New("ErrNoOpenPermissionRequest")

// ErrAlreadyDecided is returned by decide() when a row exists but
// its decision column is already non-NULL. SRD §6.2: first decide
// wins; subsequent calls report this so the caller knows their write
// was not applied.
var ErrAlreadyDecided = errors.New("ErrAlreadyDecided")

// PermissionRow is the materialized shape returned by GetPermissionRequest.
// Empty Decision / DecisionReason mean "not yet decided" (the
// column is NULL); the polling loop treats that as "keep waiting".
type PermissionRow struct {
	RequestID        int64
	ClaudeInstanceID string
	ToolName         string
	ToolInput        string
	Decision         string
	DecisionReason   string
	CreatedAt        time.Time
}

// UpsertOpenPermissionRequest replaces any existing permission_requests
// row for the given Spawn with a fresh one. Implemented as
// DELETE-then-INSERT inside a single transaction so the UNIQUE
// constraint on claude_instance_id can't trip between the two
// statements — SRD §6.2 invariant: at most one outstanding request
// per Spawn.
//
// The new row has decision=NULL and decision_reason=NULL; the polling
// loop sees that as "still open" and keeps waiting. Only the decide
// verb's UPDATE writes the decision columns.
func (s *Store) UpsertOpenPermissionRequest(instanceID, toolName, toolInputJSON string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: upsert permission begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op if Commit succeeded

	if _, err := tx.Exec(`DELETE FROM permission_requests WHERE claude_instance_id = ?`, instanceID); err != nil {
		return fmt.Errorf("store: upsert permission delete: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO permission_requests
		  (claude_instance_id, tool_name, tool_input)
		VALUES (?, ?, ?)
	`, instanceID, toolName, toolInputJSON); err != nil {
		return fmt.Errorf("store: upsert permission insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: upsert permission commit: %w", err)
	}
	return nil
}

// GetPermissionRequest reads the current state of a Spawn's
// outstanding permission request. Returns:
//
//   - (row, nil) when a row exists. Decision/DecisionReason may be
//     empty strings when the column is NULL (not yet decided).
//   - (zero, sql.ErrNoRows) when no row exists. Callers translate
//     this to ErrNoOpenPermissionRequest at the verb seam.
//
// The function is read-only — the polling loop calls it once per
// iteration and never writes here.
func (s *Store) GetPermissionRequest(instanceID string) (PermissionRow, error) {
	const q = `
		SELECT request_id, claude_instance_id, tool_name, tool_input,
		       COALESCE(decision, ''), COALESCE(decision_reason, ''),
		       created_at
		  FROM permission_requests
		 WHERE claude_instance_id = ?
	`
	row := s.db.QueryRow(q, instanceID)
	var r PermissionRow
	err := row.Scan(&r.RequestID, &r.ClaudeInstanceID, &r.ToolName, &r.ToolInput,
		&r.Decision, &r.DecisionReason, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PermissionRow{}, sql.ErrNoRows
	}
	if err != nil {
		return PermissionRow{}, fmt.Errorf("store: get permission: %w", err)
	}
	return r, nil
}

// DecidePermissionRequest is the race-free first-call-wins UPDATE per
// SRD §6.2. The WHERE clause carries `decision IS NULL` so a second
// decide on the same request returns RowsAffected()==0 and the
// caller distinguishes ErrAlreadyDecided from ErrNoOpenPermissionRequest
// via a follow-up GetPermissionRequest.
//
// Returns (true, nil) on a successful write; (false, nil) when no
// row was updated (either the row is absent or already decided —
// the verb layer disambiguates); (_, err) on a hard SQL failure.
func (s *Store) DecidePermissionRequest(instanceID, decision, reason string) (bool, error) {
	const q = `
		UPDATE permission_requests
		   SET decision = ?, decision_reason = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE claude_instance_id = ? AND decision IS NULL
	`
	var reasonArg any
	if reason != "" {
		reasonArg = reason
	} else {
		reasonArg = nil
	}
	res, err := s.db.Exec(q, decision, reasonArg, instanceID)
	if err != nil {
		return false, fmt.Errorf("store: decide permission: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: decide permission rows affected: %w", err)
	}
	return n > 0, nil
}
