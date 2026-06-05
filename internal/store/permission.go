package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// Canonical decision_reason values per SR-1.3. A single source of truth so
// all callers (relay timeout, find-missing reconciler, operator verb) use the
// exact string the schema expects.
const (
	DecisionReasonOperator    = "operator"
	DecisionReasonTimeout     = "timeout"
	DecisionReasonFindMissing = "find_missing"
)

// WriterProcess constants identify which process path wrote a
// permission_requests row. Passed to UpsertOpenPermissionRequest and
// DecidePermissionRequest so ad.row_mutation.committed events carry a
// first-class discriminator. The closed set is:
//
//   - WriterProcessHook        — the hook verb's relay path
//   - WriterProcessDecide      — the decide verb
//   - WriterProcessFindMissing — the find-missing reconciler
//
// New writers must extend this set deliberately rather than inventing
// ad-hoc strings.
const (
	WriterProcessHook        = "hook"
	WriterProcessDecide      = "decide"
	WriterProcessFindMissing = "find_missing"
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

// ErrPermissionRequestNotFound is returned by GetPermissionRequestByToken when
// no permission_requests row exists for the supplied request_token. Per SR-3.5
// the lookup is token-only (no instance_id filter); per SR-7.4 callers detect
// this sentinel with errors.Is and translate it into the verb-layer
// "not found" response. sql.ErrNoRows must not leak across this boundary.
var ErrPermissionRequestNotFound = errors.New("ErrPermissionRequestNotFound")

// ErrAmbiguousRequest is returned by DecidePermissionRequest when requestToken
// is empty and more than one open row exists for the Spawn. Defense-in-depth
// per SR-6.6; the primary fail-closed boundary is the verb-layer check in
// Task E.
var ErrAmbiguousRequest = errors.New("ErrAmbiguousRequest")

// PermissionRow is the materialized shape returned by GetPermissionRequest,
// GetPermissionRequestByToken, and OpenPermissionRequestsForSpawn. Empty
// Decision / DecisionReason mean "not yet decided" (the column is NULL); the
// polling loop treats that as "keep waiting". A zero-value DecidedAt likewise
// means the underlying decided_at column is NULL (open row).
type PermissionRow struct {
	RequestID        int64
	ClaudeInstanceID string
	RequestToken     string
	ToolName         string
	ToolInput        string
	Decision         string
	DecisionReason   string
	DecidedAt        time.Time
	CreatedAt        time.Time
}

// UpsertOpenPermissionRequest INSERTs one row per (instanceID, requestToken)
// pair. The v2 schema's composite UNIQUE(claude_instance_id, request_token)
// allows parallel rows for the same Spawn to coexist (SR-3.1). A second call
// with the same pair returns ErrRequestTokenCollision; the first row is
// unmodified.
//
// The new row has decision=NULL; the polling loop sees that as "still open"
// and keeps waiting. Only DecidePermissionRequest writes the decision columns.
//
// cap controls post-INSERT eviction of closed (decision IS NOT NULL) rows.
// When cap > 0 and the total row count exceeds cap after the INSERT, the
// oldest closed rows (ordered by decided_at ASC) are deleted to bring the
// count back to cap. cap == 0 disables eviction entirely. cap < 0 is treated
// identically to cap == 0 (eviction disabled) — negative cap handling belongs
// at the call site.
//
// The INSERT and optional DELETE run inside a single transaction; a collision
// on the UNIQUE constraint causes an immediate rollback and surfaces
// ErrRequestTokenCollision.
func (s *Store) UpsertOpenPermissionRequest(instanceID, requestToken, toolName, toolInputJSON string, cap int, writerProcess string) error {
	_, err := s.UpsertOpenPermissionRequestResult(instanceID, requestToken, toolName, toolInputJSON, cap, writerProcess)
	return err
}

// UpsertOpenPermissionRequestResult is the outcome-aware variant of
// UpsertOpenPermissionRequest. It returns a UpsertOutcome alongside the
// error so callers that emit trail events can record the exact result
// without inferring it from error presence alone (SR-A-2.1).
//
//   - UpsertInserted — the INSERT committed successfully.
//   - UpsertError    — any error (begin, insert, evict, commit, or collision).
func (s *Store) UpsertOpenPermissionRequestResult(instanceID, requestToken, toolName, toolInputJSON string, cap int, writerProcess string) (UpsertOutcome, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return UpsertError, fmt.Errorf("store: upsert permission begin tx: %w", err)
	}

	insRes, err := tx.Exec(`
		INSERT INTO permission_requests
		  (claude_instance_id, request_token, tool_name, tool_input)
		VALUES (?, ?, ?, ?)
	`, instanceID, requestToken, toolName, toolInputJSON)
	if err != nil {
		_ = tx.Rollback()
		var serr *sqlite.Error
		if errors.As(err, &serr) && serr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return UpsertError, fmt.Errorf("%w: (%s, %s)", ErrRequestTokenCollision, instanceID, requestToken)
		}
		return UpsertError, fmt.Errorf("store: upsert permission insert: %w", err)
	}

	if cap > 0 {
		var currentCount int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM permission_requests`).Scan(&currentCount); err != nil {
			_ = tx.Rollback()
			return UpsertError, fmt.Errorf("store: upsert permission count: %w", err)
		}
		if currentCount > cap {
			excess := currentCount - cap
			_, err = tx.Exec(`
				DELETE FROM permission_requests
				 WHERE rowid IN (
				     SELECT rowid FROM permission_requests
				      WHERE decision IS NOT NULL
				      ORDER BY decided_at ASC
				      LIMIT ?
				 )
			`, excess)
			if err != nil {
				_ = tx.Rollback()
				return UpsertError, fmt.Errorf("store: upsert permission evict: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return UpsertError, fmt.Errorf("store: upsert permission commit: %w", err)
	}

	// Emit row-mutation event for the successful insert. LastInsertId is
	// captured before returning so the event carries the DB-assigned PK.
	// A trail-emit failure must not fail the store call (SR-A-3.2).
	requestID, _ := insRes.LastInsertId()
	_ = trail.Emit(context.Background(), "ad.row_mutation.committed", map[string]any{
		"claude_instance_id": instanceID,
		"request_token":      requestToken,
		"request_id":         requestID,
		"tool_name":          toolName,
		"decision":           nil,
		"decision_reason":    nil,
		"writer_process":     writerProcess,
		"mutation_kind":      "insert",
		"source":             "ad_store",
	})

	return UpsertInserted, nil
}

// GetPermissionRequest reads the current state of a specific permission request
// identified by the (instanceID, requestToken) pair. Returns:
//
//   - (row, nil) when a row exists. Decision/DecisionReason are empty strings
//     when the underlying columns are NULL (open row), and DecidedAt is the
//     zero time.Time value when decided_at is NULL.
//   - (zero, sql.ErrNoRows) when no row exists for the pair.
//
// The function is read-only — the polling loop calls it once per iteration
// and never writes here.
func (s *Store) GetPermissionRequest(instanceID, requestToken string) (PermissionRow, error) {
	const q = `
		SELECT request_id, claude_instance_id, tool_name, tool_input,
		       COALESCE(decision, ''), COALESCE(decision_reason, ''),
		       created_at, request_token, decided_at
		  FROM permission_requests
		 WHERE claude_instance_id = ? AND request_token = ?
	`
	row := s.db.QueryRow(q, instanceID, requestToken)
	var r PermissionRow
	var decidedAt sql.NullTime
	err := row.Scan(&r.RequestID, &r.ClaudeInstanceID, &r.ToolName, &r.ToolInput,
		&r.Decision, &r.DecisionReason, &r.CreatedAt, &r.RequestToken, &decidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PermissionRow{}, sql.ErrNoRows
	}
	if err != nil {
		return PermissionRow{}, fmt.Errorf("store: get permission: %w", err)
	}
	if decidedAt.Valid {
		r.DecidedAt = decidedAt.Time
	}
	return r, nil
}

// GetPermissionRequestByToken reads the current state of a permission request
// identified by request_token alone (no claude_instance_id filter). Per SR-3.5
// the request_token UUIDv4 is globally selective, so callers — notably the
// `get-permission` verb wrapper added in Task B — can resolve a row without
// prior knowledge of the owning Spawn. Returns:
//
//   - (row, nil) when a row exists. Decision/DecisionReason may be empty
//     strings when the column is NULL (not yet decided); DecidedAt is the
//     zero value when decided_at is NULL.
//   - (zero, ErrPermissionRequestNotFound) when no row exists for the token.
//     sql.ErrNoRows is translated here and MUST NOT leak to callers (SR-7.4).
//
// Read-only: no INSERT/UPDATE/DELETE. Parameterized ? placeholder; never
// string-concatenated.
func (s *Store) GetPermissionRequestByToken(requestToken string) (PermissionRow, error) {
	const q = `
		SELECT request_id, claude_instance_id, tool_name, tool_input,
		       COALESCE(decision, ''), COALESCE(decision_reason, ''),
		       created_at, request_token, decided_at
		  FROM permission_requests
		 WHERE request_token = ?
	`
	row := s.db.QueryRow(q, requestToken)
	var r PermissionRow
	var decidedAt sql.NullTime
	err := row.Scan(&r.RequestID, &r.ClaudeInstanceID, &r.ToolName, &r.ToolInput,
		&r.Decision, &r.DecisionReason, &r.CreatedAt, &r.RequestToken, &decidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PermissionRow{}, ErrPermissionRequestNotFound
	}
	if err != nil {
		return PermissionRow{}, fmt.Errorf("store: get permission by token: %w", err)
	}
	if decidedAt.Valid {
		r.DecidedAt = decidedAt.Time
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
		       created_at, request_token, decided_at
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
		var decidedAt sql.NullTime
		if err := rows.Scan(&r.RequestID, &r.ClaudeInstanceID, &r.ToolName, &r.ToolInput,
			&r.Decision, &r.DecisionReason, &r.CreatedAt, &r.RequestToken, &decidedAt); err != nil {
			return nil, fmt.Errorf("store: open permission requests scan: %w", err)
		}
		if decidedAt.Valid {
			r.DecidedAt = decidedAt.Time
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
func (s *Store) DecidePermissionRequest(instanceID, requestToken, decision, reason string, writerProcess string) (bool, error) {
	if requestToken == "" {
		rows, err := s.OpenPermissionRequestsForSpawn(instanceID)
		if err != nil {
			return false, fmt.Errorf("store: decide permission ambiguity check: %w", err)
		}
		if len(rows) > 1 {
			return false, fmt.Errorf("%w: %s has %d open rows", ErrAmbiguousRequest, instanceID, len(rows))
		}
		// 0 or 1 open rows → fall through to UPDATE; UPDATE matches zero rows when token is
		// empty, returning (false, nil) which callers translate to ErrNoOpenPermissionRequest.
	}

	// RETURNING lets us capture request_id and tool_name in the same round-trip
	// so the trail event carries them without a separate SELECT.
	const q = `
		UPDATE permission_requests
		   SET decision = ?, decision_reason = ?, decided_at = CURRENT_TIMESTAMP
		 WHERE claude_instance_id = ? AND request_token = ? AND decision IS NULL
		 RETURNING request_id, tool_name
	`
	var reasonArg any
	if reason != "" {
		reasonArg = reason
	} else {
		reasonArg = nil
	}

	var requestID int64
	var toolName string
	err := s.db.QueryRow(q, decision, reasonArg, instanceID, requestToken).Scan(&requestID, &toolName)
	if errors.Is(err, sql.ErrNoRows) {
		// RowsAffected == 0: either the row was already decided or no row exists.
		// ErrAlreadyDecided / ErrNoOpenPermissionRequest are distinguished by the
		// caller via a follow-up GetPermissionRequest. Must NOT emit.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: decide permission: %w", err)
	}

	// Emit row-mutation event for the successful first-call-wins update.
	// A trail-emit failure must not fail the store call (SR-A-3.2).
	var decisionReasonField any
	if reason != "" {
		decisionReasonField = reason
	}
	_ = trail.Emit(context.Background(), "ad.row_mutation.committed", map[string]any{
		"claude_instance_id": instanceID,
		"request_token":      requestToken,
		"request_id":         requestID,
		"tool_name":          toolName,
		"decision":           decision,
		"decision_reason":    decisionReasonField,
		"writer_process":     writerProcess,
		"mutation_kind":      "update",
		"source":             "ad_store",
	})

	return true, nil
}
