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
// pair; when the request is closed (its Spawn is ended or missing, or
// find-missing's mark closed it before its relay hook acked the verdict
// recorded on it; b.146 rule 12); or when a request recorded before schema v7
// is still open past its relay window but the Spawn is not shown to be
// sitting on it alone (pkg/api's decide, b.t6e). SRD §6.2: typically means
// the Spawn isn't currently sitting on a PermissionRequest hook (or one was
// already decided).
var ErrNoOpenPermissionRequest = errors.New("ErrNoOpenPermissionRequest")

// ErrAlreadyDecided is returned by decide() when a row exists but
// its decision column is already non-NULL. SRD §6.2: first decide
// wins; subsequent calls report this so the caller knows their write
// was not applied. The relay hook's fail-closed deny at its timeout
// (DecisionReasonTimeout) is a first decide too.
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

// The pane_answer values the store compares (schema v7). PaneAnswerNone is
// the column default: no pane answer recorded through agent-director.
// PaneAnswerIntent is a pane answer begun whose keys may or may not have been
// typed (b.146 step 2b). Either still leaves a request awaiting an answer;
// step 2b's values for a completed pane answer close it.
const (
	PaneAnswerNone   = "none"
	PaneAnswerIntent = "intent"
)

// PermissionRow is the materialized shape returned by GetPermissionRequest,
// GetPermissionRequestByToken, OpenPermissionRequestsForSpawn and
// PermissionRequestsForSpawn. Empty Decision / DecisionReason mean "not yet
// decided" (the column is NULL); the polling loop treats that as "keep
// waiting". A zero-value DecidedAt likewise means the underlying decided_at
// column is NULL (open row).
//
// The schema-v7 fields (b.146 step 2) follow. Zero values mean NULL.
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

	// Hook is the relay hook that recorded the request, as it read itself at
	// its start (rule 14): hook_pid, hook_starttime, hook_pidns. It is zero
	// when the hook could not read its own identity, and a reader then cannot
	// tell whether the hook is gone.
	Hook ProcessIdentity
	// ToolUseID and AgentID are the hook input's tool_use_id and agent_id
	// ("" = none given).
	ToolUseID string
	AgentID   string
	// DeliveredAt is the hook's ack (rule 3): it is committed before the hook
	// writes the request's answer to Claude Code.
	DeliveredAt time.Time
	// SettledAt is the hook's kill instant plus the reserve (rule 4): the
	// time a reader falls back to when it cannot check the hook process.
	// Zero on a request recorded before schema v7 (PreV7).
	SettledAt time.Time
	// HookGoneAt is when a reader first found the request fallen back.
	HookGoneAt time.Time
	// AttemptedDecision and AttemptedAt are the verdict a refused decide tried
	// to record, stored and shown, never acted on.
	AttemptedDecision string
	AttemptedAt       time.Time
	// PaneAnswer is pane_answer: PaneAnswerNone unless a pane answer was
	// recorded (b.146 step 2b). PaneAs is that answer's claimed verdict and
	// PaneSender the process that sent it.
	PaneAnswer string
	PaneAs     string
	PaneSender ProcessIdentity
	// ClosedAt is when find-missing's mark closed the request (b.146
	// rule 12): it still awaited an answer when its Spawn was marked missing.
	// The mark denies such a request with decision_reason find_missing when
	// it was undecided, and keeps the verdict of one that was decided but not
	// acked. Zero when no mark closed it.
	ClosedAt time.Time
}

// PreV7 reports whether r was recorded before schema v7, by a relay hook that
// records no settle instant: its relay hook's identity and delivery are not
// on record, and readers judge it by its created_at and the relay window, as
// before (b.146 rule 5's compatibility clause).
func (r PermissionRow) PreV7() bool { return r.SettledAt.IsZero() }

// Closed reports whether find-missing's mark closed r (ClosedAt set): r no
// longer awaits an answer, whatever its decision and delivery.
func (r PermissionRow) Closed() bool { return !r.ClosedAt.IsZero() }

// awaitingAnswerSQL is the WHERE fragment, on a permission_requests row
// named pr, that holds while the request still awaits an answer (b.146
// rule 9): one that find-missing's mark has not closed (closed_at NULL, rule
// 12) and that, recorded from schema v7 on, is not acked and has no completed
// pane answer (pane_answer none or intent), or, recorded before v7 (no
// settled_at), is undecided. It has no placeholders.
const awaitingAnswerSQL = `pr.closed_at IS NULL
    AND CASE WHEN pr.settled_at IS NULL THEN pr.decision IS NULL
             ELSE pr.delivered_at IS NULL AND pr.pane_answer IN ('none', 'intent') END`

// permissionColumns is the one column list every read returning a
// PermissionRow selects from a permission_requests row named pr, in
// scanPermissionRow's order.
const permissionColumns = `pr.request_id, pr.claude_instance_id, pr.tool_name, pr.tool_input,
       COALESCE(pr.decision, ''), COALESCE(pr.decision_reason, ''),
       pr.created_at, pr.request_token, pr.decided_at,
       COALESCE(pr.hook_pid, 0), COALESCE(pr.hook_starttime, ''), COALESCE(pr.hook_pidns, ''),
       COALESCE(pr.tool_use_id, ''), COALESCE(pr.agent_id, ''),
       pr.delivered_at, pr.settled_at, pr.hook_gone_at,
       COALESCE(pr.attempted_decision, ''), pr.attempted_at,
       pr.pane_answer, COALESCE(pr.pane_as, ''),
       COALESCE(pr.pane_sender_pid, 0), COALESCE(pr.pane_sender_starttime, ''), COALESCE(pr.pane_sender_pidns, ''),
       pr.closed_at`

// scanPermissionRow scans one row selected with permissionColumns. The Scan
// error is returned as is, so callers can still detect sql.ErrNoRows.
func scanPermissionRow(sc rowScanner) (PermissionRow, error) {
	var (
		r                                                         PermissionRow
		decidedAt                                                 sql.NullTime
		deliveredAt, settledAt, hookGoneAt, attemptedAt, closedAt sql.NullInt64
	)
	err := sc.Scan(&r.RequestID, &r.ClaudeInstanceID, &r.ToolName, &r.ToolInput,
		&r.Decision, &r.DecisionReason, &r.CreatedAt, &r.RequestToken, &decidedAt,
		&r.Hook.PID, &r.Hook.Starttime, &r.Hook.PIDNamespace,
		&r.ToolUseID, &r.AgentID,
		&deliveredAt, &settledAt, &hookGoneAt,
		&r.AttemptedDecision, &attemptedAt,
		&r.PaneAnswer, &r.PaneAs,
		&r.PaneSender.PID, &r.PaneSender.Starttime, &r.PaneSender.PIDNamespace,
		&closedAt)
	if err != nil {
		return PermissionRow{}, err
	}
	if decidedAt.Valid {
		r.DecidedAt = decidedAt.Time
	}
	r.DeliveredAt = millisTime(deliveredAt)
	r.SettledAt = millisTime(settledAt)
	r.HookGoneAt = millisTime(hookGoneAt)
	r.AttemptedAt = millisTime(attemptedAt)
	r.ClosedAt = millisTime(closedAt)
	return r, nil
}

// queryPermissionRows runs q (selecting permissionColumns) with args through
// query and scans every row; errPrefix names the read in an error. It
// returns an empty slice, not nil, when no row matches.
func queryPermissionRows(query func(string, ...any) (*sql.Rows, error), q, errPrefix string, args ...any) ([]PermissionRow, error) {
	rows, err := query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	defer rows.Close()
	out := []PermissionRow{}
	for rows.Next() {
		r, err := scanPermissionRow(rows)
		if err != nil {
			return nil, fmt.Errorf("%s scan: %w", errPrefix, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s iterate: %w", errPrefix, err)
	}
	return out, nil
}

// UpsertOpenPermissionRequest INSERTs one row per (instanceID, requestToken)
// pair, gated like every hook write (SR-22.9): the INSERT applies only when
// gate's parent process is the row's recorded pane process, a condition of the
// INSERT's own statement. The v2 schema's composite
// UNIQUE(claude_instance_id, request_token) allows parallel rows for the same
// Spawn to coexist (SR-3.1). A second call with the same pair returns
// ErrRequestTokenCollision; the first row is unmodified.
//
// The new row has decision=NULL and records no relay hook identity and no
// settled_at, so readers judge it as a request recorded before schema v7, by
// its created_at and the relay window. The relay hook records its requests
// with InsertRelayRequest instead; this insert is the seeding primitive of
// test support and fixtures.
//
// cap controls post-INSERT eviction of closed requests (evictClosedRequests:
// those no longer awaiting an answer, oldest decided_at first, with one
// exempt request per Spawn). cap == 0 disables eviction entirely. cap < 0 is
// treated identically to cap == 0 (eviction disabled) — negative cap handling
// belongs at the call site.
//
// The INSERT and optional DELETE run inside a single transaction; a collision
// on the UNIQUE constraint causes an immediate rollback and surfaces
// ErrRequestTokenCollision. A gate that does not hold records no request and
// reports why (HookApplied, as ApplyHookTransitionResult).
func (s *Store) UpsertOpenPermissionRequest(instanceID string, gate HookGate, requestToken, toolName, toolInputJSON string, cap int, writerProcess string) (HookApplied, error) {
	_, applied, err := s.UpsertOpenPermissionRequestResult(instanceID, gate, requestToken, toolName, toolInputJSON, cap, writerProcess)
	return applied, err
}

// UpsertOpenPermissionRequestResult is the outcome-aware variant of
// UpsertOpenPermissionRequest. It returns a UpsertOutcome alongside the
// error so callers that emit trail events can record the exact result
// without inferring it from error presence alone (SR-A-2.1).
//
//   - UpsertInserted — the INSERT committed successfully (HookApplied.Applied).
//   - UpsertNoChange — the gate did not hold, or no row has the id: nothing
//     was written, no event is emitted, and HookApplied carries the reason
//     from one read after the statement (notAppliedReason).
//   - UpsertError    — any error (begin, insert, evict, commit, or collision).
//
// The INSERT is INSERT … SELECT … WHERE EXISTS (the row with the gate), so
// the gate is in the write's own statement (SR-22.9; decision A8).
func (s *Store) UpsertOpenPermissionRequestResult(instanceID string, gate HookGate, requestToken, toolName, toolInputJSON string, cap int, writerProcess string) (UpsertOutcome, HookApplied, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return UpsertError, HookApplied{}, fmt.Errorf("store: upsert permission begin tx: %w", err)
	}

	insArgs := append([]any{instanceID, requestToken, toolName, toolInputJSON, instanceID}, hookGateArgs(gate)...)
	insRes, err := tx.Exec(`
		INSERT INTO permission_requests
		  (claude_instance_id, request_token, tool_name, tool_input)
		SELECT ?, ?, ?, ?
		 WHERE EXISTS (SELECT 1 FROM spawns
		                WHERE claude_instance_id = ? AND `+hookGateSQL+`)
	`, insArgs...)
	if err != nil {
		_ = tx.Rollback()
		var serr *sqlite.Error
		if errors.As(err, &serr) && serr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return UpsertError, HookApplied{}, fmt.Errorf("%w: (%s, %s)", ErrRequestTokenCollision, instanceID, requestToken)
		}
		return UpsertError, HookApplied{}, fmt.Errorf("store: upsert permission insert: %w", err)
	}
	inserted, err := insRes.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return UpsertError, HookApplied{}, fmt.Errorf("store: upsert permission insert rows affected: %w", err)
	}
	if inserted == 0 {
		_ = tx.Rollback()
		applied, err := s.hookNotApplied(instanceID, gate, "store: upsert permission")
		if err != nil {
			return UpsertError, HookApplied{}, err
		}
		return UpsertNoChange, applied, nil
	}

	if err := evictClosedRequests(context.Background(), tx, cap); err != nil {
		_ = tx.Rollback()
		return UpsertError, HookApplied{}, fmt.Errorf("store: upsert permission: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return UpsertError, HookApplied{}, fmt.Errorf("store: upsert permission commit: %w", err)
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

	return UpsertInserted, HookApplied{Applied: true}, nil
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
	r, err := scanPermissionRow(s.db.QueryRow(getPermissionRequestSQL, instanceID, requestToken))
	if errors.Is(err, sql.ErrNoRows) {
		return PermissionRow{}, sql.ErrNoRows
	}
	if err != nil {
		return PermissionRow{}, fmt.Errorf("store: get permission: %w", err)
	}
	return r, nil
}

// getPermissionRequestSQL is the read of one request by (instance, token).
const getPermissionRequestSQL = `SELECT ` + permissionColumns + `
	  FROM permission_requests pr
	 WHERE pr.claude_instance_id = ? AND pr.request_token = ?`

// GetPermissionRequestWithin is GetPermissionRequest with its waits bounded
// by maxWait (readWithin: for the store's connection, when another call of
// this process holds it, and for a lock another connection holds;
// DefaultLockWait: as GetPermissionRequest waits), so a caller with its own
// deadline, such as decide under a max_wait_ms bound and its wait for the
// relay hook's ack (b.146 decision 9 B, rule 16), never reads past it. A read
// that waits longer returns an error wrapping ErrStoreBusy. Results otherwise
// as GetPermissionRequest's.
func (s *Store) GetPermissionRequestWithin(instanceID, requestToken string, maxWait time.Duration) (PermissionRow, error) {
	var r PermissionRow
	err := s.readWithin(maxWait, "store: get permission", func(ctx context.Context, conn *sql.Conn) error {
		var err error
		r, err = scanPermissionRow(conn.QueryRowContext(ctx, getPermissionRequestSQL, instanceID, requestToken))
		return err
	})
	if err != nil {
		return PermissionRow{}, err
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
	const q = `SELECT ` + permissionColumns + `
		  FROM permission_requests pr
		 WHERE pr.request_token = ?`
	r, err := scanPermissionRow(s.db.QueryRow(q, requestToken))
	if errors.Is(err, sql.ErrNoRows) {
		return PermissionRow{}, ErrPermissionRequestNotFound
	}
	if err != nil {
		return PermissionRow{}, fmt.Errorf("store: get permission by token: %w", err)
	}
	return r, nil
}

// OpenPermissionRequestsForSpawn returns the given Spawn's open requests, the
// ones that still await an answer (b.146 rule 9; awaitingAnswerSQL), ordered
// by created_at ASC: a request recorded from schema v7 on that its relay hook
// has not acked, with no completed pane answer and not closed by
// find-missing's mark, decided or not (a recorded verdict its hook has not
// acked has not reached the agent); a request recorded before v7 while it is
// undecided. Returns an empty slice (not nil) when none exists; nil error on
// the empty-result case.
//
// Used by ApplyHookTransitionResult's working hold, get's and list's
// permission_requests, and the ErrAmbiguousRequest guard in
// DecidePermissionRequest.
func (s *Store) OpenPermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error) {
	const q = `SELECT ` + permissionColumns + `
		  FROM permission_requests pr
		 WHERE pr.claude_instance_id = ? AND ` + awaitingAnswerSQL + `
		 ORDER BY pr.created_at ASC, pr.request_id ASC`
	return queryPermissionRows(s.db.Query, q, "store: open permission requests", instanceID)
}

// PermissionRequestsForSpawn returns ALL permission_requests rows for the given
// Spawn — decided and undecided alike — ordered by created_at ASC. Returns an
// empty slice (not nil) when no rows exist; nil error on the empty-result case.
//
// It differs from OpenPermissionRequestsForSpawn, which filters to
// `decision IS NULL`. The send_keys relay-guard release (Epic t1.kk3.up)
// evaluates deliverability across every row, decided or not (SR-4.2
// "whether or not a decision was recorded"): a row decided in-window may
// still have a live poller about to deliver it, so a decided-in-window row
// keeps the guard shut unless another of the Spawn's requests has fallen
// back (still open after its relay hook settled; b.ceq, see pkg/api
// evaluateRelayGuard). This all-rows variant supplies that evaluation set.
// pkg/api's decide reads it too, to tell whether a fallen-back request is the
// Spawn's newest and only open one (b.t6e, see pkg/api fallenBackUnshown);
// under a max_wait_ms bound through PermissionRequestsForSpawnWithin.
func (s *Store) PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error) {
	return queryPermissionRows(s.db.Query, permissionRequestsForSpawnSQL, "store: permission requests", instanceID)
}

// permissionRequestsForSpawnSQL is the read of every request of one Spawn.
const permissionRequestsForSpawnSQL = `SELECT ` + permissionColumns + `
	  FROM permission_requests pr
	 WHERE pr.claude_instance_id = ?
	 ORDER BY pr.created_at ASC`

// PermissionRequestsForSpawnWithin is PermissionRequestsForSpawn with its
// waits bounded by maxWait, as GetPermissionRequestWithin's: a read that
// waits longer returns an error wrapping ErrStoreBusy. decide reads it under
// a max_wait_ms bound (b.146 decision 9 B).
func (s *Store) PermissionRequestsForSpawnWithin(instanceID string, maxWait time.Duration) ([]PermissionRow, error) {
	var out []PermissionRow
	err := s.readWithin(maxWait, "", func(ctx context.Context, conn *sql.Conn) error {
		var err error
		out, err = queryPermissionRows(func(q string, args ...any) (*sql.Rows, error) {
			return conn.QueryContext(ctx, q, args...)
		}, permissionRequestsForSpawnSQL, "store: permission requests", instanceID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// decideRelayRequestSQL is DecideRelayRequest's one statement: the verdict,
// guarded by the request being undecided, unacked, without a pane answer and
// not closed, a request recorded before schema v7 also being deliverable by
// its created_at, and its Spawn being live.
const decideRelayRequestSQL = `UPDATE permission_requests
	   SET decision = ?, decision_reason = ?, decided_at = CURRENT_TIMESTAMP
	 WHERE claude_instance_id = ? AND request_token = ? AND decision IS NULL
	   AND delivered_at IS NULL AND pane_answer = 'none' AND closed_at IS NULL
	   AND (settled_at IS NOT NULL OR created_at > ?)
	   AND NOT EXISTS (SELECT 1 FROM spawns
	                    WHERE claude_instance_id = ? AND ` + finishedStateGuardSQL + `)
	 RETURNING request_id, tool_name`

// DecideRelayRequest is decide's verdict write (SRD §6.2; b.146 rules 6, 16
// and decision 9 B): one statement, in its own transaction, that records
// decision (reason "" as NULL) on the request with requestToken, first call
// wins. It writes only while the request:
//
//   - is undecided (decision IS NULL), not acked (delivered_at IS NULL), has
//     no pane answer recorded (pane_answer 'none') and was not closed by
//     find-missing's mark (closed_at IS NULL; the mark also decides every
//     request it closes undecided, so this only restates rule 12);
//   - for a request recorded before schema v7 (no settled_at), is still
//     deliverable: its created_at is strictly after legacyCutoff, the
//     created_at boundary pkg/api's single authority
//     (api.RelayDeliverabilityCutoff) computes and passes in, never restated
//     here (SR-4.4). A request recorded from v7 on is judged by its relay
//     hook instead, which pkg/api checks before this write;
//   - belongs to a Spawn that is not ended or missing (b.146 rule 12): a
//     request of a finished row is closed.
//
// Every condition is in the same statement, so nothing lands between a check
// and the write.
//
// The write waits at most maxWait for the store's write lock
// (DefaultLockWait: the store's busy timeout). When the lock is not taken in
// that time it returns an error wrapping ErrStoreBusy and has recorded
// nothing (decision 9 B).
//
// Returns (true, nil) on a write, which emits the same
// ad.row_mutation.committed as DecidePermissionRequest's; (false, nil) when
// the request was not written (absent, decided, acked, answered at the pane,
// closed, undeliverable, or its Spawn finished), which the caller tells apart
// with follow-up reads; (_, err) on a failure.
func (s *Store) DecideRelayRequest(instanceID, requestToken, decision, reason, writerProcess string, legacyCutoff time.Time, maxWait time.Duration) (bool, error) {
	var (
		requestID int64
		toolName  string
		written   bool
	)
	// SQLite's CURRENT_TIMESTAMP / created_at columns are stored as UTC text in
	// "2006-01-02 15:04:05" form; compare against the cutoff in the same UTC
	// text form so the string comparison agrees with the stored representation.
	args := append([]any{decision, nullableStringArg(reason), instanceID, requestToken,
		storeTimestamp(legacyCutoff), instanceID}, finishedStateGuardArgs()...)
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		err := conn.QueryRowContext(ctx, decideRelayRequestSQL, args...).Scan(&requestID, &toolName)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: decide permission: %w", err)
		}
		written = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if written {
		emitDecisionCommitted(instanceID, requestToken, requestID, toolName, decision, reason, writerProcess)
	}
	return written, nil
}

// emitDecisionCommitted emits the ad.row_mutation.committed event of one
// committed decision write on a permission request (SR-A-2.1): decide's, the
// relay hook's timeout deny, and find-missing's close. reason "" is emitted
// as null. A trail-emit failure must not fail the store call (SR-A-3.2).
func emitDecisionCommitted(instanceID, requestToken string, requestID int64, toolName, decision, reason, writerProcess string) {
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
	emitDecisionCommitted(instanceID, requestToken, requestID, toolName, decision, reason, writerProcess)
	return true, nil
}
