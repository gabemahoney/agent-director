package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Canonical decision_reason values per SR-1.3. A single source of truth so
// all callers (relay timeout, find-missing reconciler, operator verb) use the
// exact string the schema expects.
const (
	DecisionReasonOperator    = "operator"
	DecisionReasonTimeout     = "timeout"
	DecisionReasonFindMissing = "find_missing"
)

// ErrNoOpenPermissionRequest is returned by decide() when no row
// exists in permission_requests for the given (instance_id, request_token)
// pair. SRD §6.2: typically means the Spawn isn't currently sitting on a
// PermissionRequest hook (or one was already decided).
var ErrNoOpenPermissionRequest = errors.New("ErrNoOpenPermissionRequest")

// ErrAlreadyDecided is returned by decide() when a row exists but
// its decision column is already non-NULL. SRD §6.2: first decide
// wins; subsequent calls report this so the caller knows their write
// was not applied.
var ErrAlreadyDecided = errors.New("ErrAlreadyDecided")

// ErrRequestTokenCollision is returned by UpsertOpenPermissionRequest when a
// row with the same (claude_instance_id, request_token) pair already exists.
// Callers detect it with errors.Is; the underlying UNIQUE constraint row is
// unmodified.
var ErrRequestTokenCollision = errors.New("ErrRequestTokenCollision")

// ErrAmbiguousRequest is returned by DecidePermissionRequest when requestToken
// is empty and more than one open row exists for the Spawn. Defense-in-depth
// per SR-6.6; the primary fail-closed boundary is the verb-layer check in
// Task E.
var ErrAmbiguousRequest = errors.New("ErrAmbiguousRequest")

// PermissionRow is the materialized shape returned by GetPermissionRequest and
// OpenPermissionRequestsForSpawn. Empty Decision / DecisionReason mean "not
// yet decided" (the column is NULL); the polling loop treats that as "keep
// waiting".
type PermissionRow struct {
	RequestID        int64
	ClaudeInstanceID string
	RequestToken     string
	ToolName         string
	ToolInput        string
	Decision         string
	DecisionReason   string
	CreatedAt        time.Time
}

// UpsertOpenPermissionRequest INSERTs one row per (instanceID, requestToken)
// pair. The old DELETE-then-INSERT transaction is gone; the v2 schema's
// composite UNIQUE(claude_instance_id, request_token) allows parallel rows
// for the same Spawn to coexist (SR-3.1). A second call with the same pair
// returns ErrRequestTokenCollision; the first row is unmodified.
//
// The new row has decision=NULL; the polling loop sees that as "still open"
// and keeps waiting. Only DecidePermissionRequest writes the decision columns.
func (s *Store) UpsertOpenPermissionRequest(instanceID, requestToken, toolName, toolInputJSON string) error {
	_, err := s.db.Exec(`
		INSERT INTO permission_requests
		  (claude_instance_id, request_token, tool_name, tool_input)
		VALUES (?, ?, ?, ?)
	`, instanceID, requestToken, toolName, toolInputJSON)
	if err != nil {
		var serr *sqlite.Error
		if errors.As(err, &serr) && serr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return fmt.Errorf("%w: (%s, %s)", ErrRequestTokenCollision, instanceID, requestToken)
		}
		return fmt.Errorf("store: upsert permission insert: %w", err)
	}
	return nil
}

// GetPermissionRequest reads the current state of a specific permission request
// identified by the (instanceID, requestToken) pair. Returns:
//
//   - (row, nil) when a row exists. Decision/DecisionReason may be empty
//     strings when the column is NULL (not yet decided).
//   - (zero, sql.ErrNoRows) when no row exists for the pair.
//
// The function is read-only — the polling loop calls it once per iteration
// and never writes here.
func (s *Store) GetPermissionRequest(instanceID, requestToken string) (PermissionRow, error) {
	const q = `
		SELECT request_id, claude_instance_id, tool_name, tool_input,
		       COALESCE(decision, ''), COALESCE(decision_reason, ''),
		       created_at, request_token
		  FROM permission_requests
		 WHERE claude_instance_id = ? AND request_token = ?
	`
	row := s.db.QueryRow(q, instanceID, requestToken)
	var r PermissionRow
	err := row.Scan(&r.RequestID, &r.ClaudeInstanceID, &r.ToolName, &r.ToolInput,
		&r.Decision, &r.DecisionReason, &r.CreatedAt, &r.RequestToken)
	if errors.Is(err, sql.ErrNoRows) {
		return PermissionRow{}, sql.ErrNoRows
	}
	if err != nil {
		return PermissionRow{}, fmt.Errorf("store: get permission: %w", err)
	}
	return r, nil
}

// OpenPermissionRequestsForSpawn returns all open (decision IS NULL) rows for
// the given Spawn, ordered by created_at ASC. Returns an empty slice (not nil)
// when no open rows exist; nil error on the empty-result case.
//
// Used by ApplyHookTransition (Task D-1) and the ErrAmbiguousRequest guard in
// DecidePermissionRequest.
func (s *Store) OpenPermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error) {
	const q = `
		SELECT request_id, claude_instance_id, tool_name, tool_input,
		       COALESCE(decision, ''), COALESCE(decision_reason, ''),
		       created_at, request_token
		  FROM permission_requests
		 WHERE claude_instance_id = ? AND decision IS NULL
		 ORDER BY created_at ASC
	`
	rows, err := s.db.Query(q, instanceID)
	if err != nil {
		return nil, fmt.Errorf("store: open permission requests: %w", err)
	}
	defer rows.Close()
	out := []PermissionRow{}
	for rows.Next() {
		var r PermissionRow
		if err := rows.Scan(&r.RequestID, &r.ClaudeInstanceID, &r.ToolName, &r.ToolInput,
			&r.Decision, &r.DecisionReason, &r.CreatedAt, &r.RequestToken); err != nil {
			return nil, fmt.Errorf("store: open permission requests scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: open permission requests iterate: %w", err)
	}
	return out, nil
}

// DecidePermissionRequest is the race-free first-call-wins UPDATE per SRD §6.2.
// The WHERE clause carries `decision IS NULL AND request_token = ?` so a second
// decide on the same request returns RowsAffected()==0 and the caller
// distinguishes ErrAlreadyDecided from ErrNoOpenPermissionRequest via a
// follow-up GetPermissionRequest.
//
// ErrAmbiguousRequest guard (SR-6.6 defense-in-depth): when requestToken is
// empty and N>1 open rows exist for the Spawn, the function refuses rather than
// silently target an arbitrary row. 0 or 1 open rows fall through to the UPDATE
// (0 → existing no-op (false, nil); 1 → legacy single-row path). The primary
// fail-closed boundary is the verb-layer check in Task E.
//
// Returns (true, nil) on a successful write; (false, nil) when no row was
// updated; (_, err) on a hard SQL failure.
func (s *Store) DecidePermissionRequest(instanceID, requestToken, decision, reason string) (bool, error) {
	if requestToken == "" {
		rows, err := s.OpenPermissionRequestsForSpawn(instanceID)
		if err != nil {
			return false, fmt.Errorf("store: decide permission ambiguity check: %w", err)
		}
		if len(rows) > 1 {
			return false, ErrAmbiguousRequest
		}
		// 0 rows → UPDATE with request_token="" matches nothing → (false, nil)
		// 1 row  → fall through; Task C/E will replace "" with the real token
	}

	const q = `
		UPDATE permission_requests
		   SET decision = ?, decision_reason = ?, decided_at = CURRENT_TIMESTAMP
		 WHERE claude_instance_id = ? AND request_token = ? AND decision IS NULL
	`
	var reasonArg any
	if reason != "" {
		reasonArg = reason
	} else {
		reasonArg = nil
	}
	res, err := s.db.Exec(q, decision, reasonArg, instanceID, requestToken)
	if err != nil {
		return false, fmt.Errorf("store: decide permission: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: decide permission rows affected: %w", err)
	}
	return n > 0, nil
}
