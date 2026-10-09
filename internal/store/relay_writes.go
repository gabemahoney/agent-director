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

// This file holds the relay's store writes of b.146 step 2: the relay hook's
// first write (the request and check_permission in one transaction, rule 1),
// its ack (rule 3), its timeout deny (rule 3), and the readers' and decide's
// writes of the facts they found (hook_gone_at, a refused decide's attempted
// verdict; rules 8 and 9). Each takes a bound on its wait for the write lock,
// so its caller's deadline holds whatever [store] busy_timeout_ms is set to
// (the b.146 property).

// RelayRequest is what the relay hook records with a new permission request
// (b.146 rules 1, 2, 4 and 14).
type RelayRequest struct {
	// RequestToken is the request's UUIDv4 token, minted by the hook.
	RequestToken string
	// ToolName and ToolInput are the hook input's tool_name and tool_input
	// (the raw JSON text).
	ToolName  string
	ToolInput string
	// ToolUseID and AgentID are the hook input's tool_use_id and agent_id;
	// "" stores NULL.
	ToolUseID string
	AgentID   string
	// Hook is the relay hook process as it read itself at its start: its pid,
	// start time and pid namespace (as probe.SelfPIDNamespace read it; "" on
	// darwin, which has none). It is the zero identity when the hook could not
	// read its start time or namespace, and readers then cannot tell whether
	// it is gone.
	Hook ProcessIdentity
	// SettledAt is the hook's kill instant plus the reserve: the time readers
	// fall back to when they cannot check the hook process. It must not be
	// zero: a request with no settled_at reads as one recorded before schema
	// v7.
	SettledAt time.Time
	// TranscriptPath and TranscriptPresent are the hook input's
	// transcript_path and whether it exists on disk: the session record
	// every applied hook writes on a row that records no session id yet
	// (hookSessionRecordSet, with the gate's SessionID).
	TranscriptPath    string
	TranscriptPresent bool
}

// errRelayNotApplied rolls InsertRelayRequest's transaction back when its
// gated spawns write matched no row; the caller then reads why.
var errRelayNotApplied = errors.New("store: relay request not applied")

// relayInsertSQL is the request insert of InsertRelayRequest.
const relayInsertSQL = `INSERT INTO permission_requests
	  (claude_instance_id, request_token, tool_name, tool_input,
	   hook_pid, hook_starttime, hook_pidns, tool_use_id, agent_id, settled_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// InsertRelayRequest is the relay hook's first write (b.146 rule 1): in ONE
// transaction it moves instanceID's row to check_permission, as the
// PermissionRequest hook's gated transition does, and inserts the open
// request req, so a death leaves both or neither. The transaction is begun
// with BEGIN IMMEDIATE and waits at most maxWait for the write lock (b.146
// problem 1: the hook cuts that wait at its own deadline less the reserve);
// when the lock is not taken in that time it returns an error wrapping
// ErrStoreBusy and has written nothing.
//
// The row write is gated (SR-22.9; hookGateSQL): it applies only when gate's
// parent process is the row's recorded pane process. It sets state
// check_permission, bumps last_seen_at, clears ended_at, launch_started_at,
// the liveness markers and idle_since (any hook clears idle_since, b.146
// problem 3), records the session and pane start time as every ordinary hook
// does, and advances row_version by one. When the gate does not hold (or no
// row has the id) nothing is written and HookApplied carries the reason from
// one read after the rollback (notAppliedReason), with UpsertNoChange.
//
// The request carries req's hook identity, tool_use_id, agent_id and
// settled_at, decision NULL and pane_answer 'none'. A token that already
// exists for the row fails the whole transaction with
// ErrRequestTokenCollision. cap evicts closed requests after the insert, as
// UpsertOpenPermissionRequest does (evictClosedRequests).
//
// After the commit it emits ad.spawn.state_transition (the prior state to
// check_permission, triggering event gate.Event) and the request's
// ad.row_mutation.committed insert (writer hook), fail-open.
func (s *Store) InsertRelayRequest(instanceID string, gate HookGate, req RelayRequest, cap int, maxWait time.Duration) (UpsertOutcome, HookApplied, error) {
	var (
		prior     string
		requestID int64
	)
	q, args := gatedHookUpdate(instanceID, gate,
		`state = ?, last_seen_at = CURRENT_TIMESTAMP, ended_at = NULL, `+launchStartClear+`, `+idleClear,
		[]any{StateCheckPermission}, req.TranscriptPath, req.TranscriptPresent)
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		err := conn.QueryRowContext(ctx, `SELECT state FROM spawns WHERE claude_instance_id = ?`, instanceID).Scan(&prior)
		if errors.Is(err, sql.ErrNoRows) {
			return errRelayNotApplied
		}
		if err != nil {
			return fmt.Errorf("store: relay request prior state: %w", err)
		}
		res, err := conn.ExecContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("store: relay request state write: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("store: relay request state write rows affected: %w", err)
		} else if n == 0 {
			return errRelayNotApplied
		}
		insArgs := append([]any{instanceID, req.RequestToken, req.ToolName, req.ToolInput}, processIdentityArgs(req.Hook)...)
		insArgs = append(insArgs, nullableStringArg(req.ToolUseID), nullableStringArg(req.AgentID), millisArg(req.SettledAt))
		ins, err := conn.ExecContext(ctx, relayInsertSQL, insArgs...)
		if err != nil {
			var serr *sqlite.Error
			if errors.As(err, &serr) && serr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
				return fmt.Errorf("%w: (%s, %s)", ErrRequestTokenCollision, instanceID, req.RequestToken)
			}
			return fmt.Errorf("store: relay request insert: %w", err)
		}
		requestID, _ = ins.LastInsertId()
		return evictClosedRequests(ctx, conn, cap)
	})
	if errors.Is(err, errRelayNotApplied) {
		applied, err := s.hookNotApplied(instanceID, gate, "store: relay request")
		if err != nil {
			return UpsertError, HookApplied{}, err
		}
		return UpsertNoChange, applied, nil
	}
	if err != nil {
		return UpsertError, HookApplied{}, err
	}

	event := gate.Event
	if event == "" {
		event = "PermissionRequest"
	}
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"claude_instance_id":    instanceID,
		"prior_state":           prior,
		"new_state":             StateCheckPermission,
		"triggering_event_name": event,
		"soft_refresh":          false,
		"source":                "ad_spawn_store",
	})
	_ = trail.Emit(context.Background(), "ad.row_mutation.committed", map[string]any{
		"claude_instance_id": instanceID,
		"request_token":      req.RequestToken,
		"request_id":         requestID,
		"tool_name":          req.ToolName,
		"decision":           nil,
		"decision_reason":    nil,
		"writer_process":     WriterProcessHook,
		"mutation_kind":      "insert",
		"source":             "ad_store",
	})
	return UpsertInserted, HookApplied{Applied: true}, nil
}

// ackRelayDecisionSQL is AckRelayDecision's one statement.
const ackRelayDecisionSQL = `UPDATE permission_requests
	   SET delivered_at = ?
	 WHERE claude_instance_id = ? AND request_token = ?
	   AND decision IS NOT NULL AND delivered_at IS NULL AND pane_answer = 'none'
	 RETURNING decision, COALESCE(decision_reason, '')`

// AckRelayDecision is the relay hook's ack (b.146 rule 3): one statement
// that records at as the request's delivered_at, only while the request is
// decided, not yet acked and has no pane answer recorded, and returns the
// decision and reason it acked, which are what the hook then writes to
// Claude Code. It waits at most maxWait for the write lock (rule 4: the hook
// caps the wait so a committed ack leaves its reserve); when the lock is not
// taken in that time it returns an error wrapping ErrStoreBusy and has
// written nothing.
//
// check, when not nil, is the ack's precondition, run inside its transaction
// once the write lock is taken and before the statement (b.146 problem 7: the
// relay hook's parent check, so a Claude Code that died while the hook waited
// for the lock gets no ack). A non-nil error from it rolls the transaction
// back, writing nothing, and is returned as it is.
//
// A request find-missing's mark closed (closed_at set) is acked like any
// other: its verdict, or the mark's deny of one that was undecided, still
// reaches a relay hook whose parent is alive.
//
// acked is false, with no error, when the statement matched no request
// (absent, undecided, already acked, or answered at the pane). Only an acked
// decision may be written to Claude Code. It emits no trail event: the hook's
// ad.resume.observed records the delivery.
func (s *Store) AckRelayDecision(instanceID, requestToken string, at time.Time, maxWait time.Duration, check func() error) (decision, reason string, acked bool, err error) {
	err = s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		err := conn.QueryRowContext(ctx, ackRelayDecisionSQL, millisArg(at), instanceID, requestToken).Scan(&decision, &reason)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: ack relay decision: %w", err)
		}
		acked = true
		return nil
	})
	if err != nil {
		return "", "", false, err
	}
	return decision, reason, acked, nil
}

// denyRelayTimeoutSQL is DenyRelayTimeout's one statement.
const denyRelayTimeoutSQL = `UPDATE permission_requests
	   SET decision = ?, decision_reason = ?, decided_at = CURRENT_TIMESTAMP, delivered_at = ?
	 WHERE claude_instance_id = ? AND request_token = ?
	   AND decision IS NULL AND pane_answer = 'none'
	 RETURNING request_id, tool_name`

// DenyRelayTimeout is the relay hook's timeout deny (b.146 rule 3): one
// guarded statement that records decision deny, decision_reason timeout,
// decided_at now and delivered_at at, its own ack, only while the request is
// undecided and has no pane answer recorded. It waits at most maxWait for the
// write lock (rule 4); when the lock is not taken in that time it returns an
// error wrapping ErrStoreBusy and has written nothing.
//
// check, when not nil, is the deny's precondition, run inside its
// transaction once the write lock is taken and before the statement, as
// AckRelayDecision's (b.146 problem 7): a non-nil error rolls the transaction
// back, writing nothing, and is returned as it is.
//
// denied is false, with no error, when the request was not undecided (a
// verdict landed first, which the hook then acks instead) or is gone. A
// written deny emits ad.row_mutation.committed (writer hook), fail-open.
func (s *Store) DenyRelayTimeout(instanceID, requestToken string, at time.Time, maxWait time.Duration, check func() error) (denied bool, err error) {
	var (
		requestID int64
		toolName  string
	)
	err = s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		err := conn.QueryRowContext(ctx, denyRelayTimeoutSQL, "deny", DecisionReasonTimeout, millisArg(at),
			instanceID, requestToken).Scan(&requestID, &toolName)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: relay timeout deny: %w", err)
		}
		denied = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if denied {
		emitDecisionCommitted(instanceID, requestToken, requestID, toolName, "deny", DecisionReasonTimeout, WriterProcessHook)
	}
	return denied, nil
}

// recordRefusedDecisionSQL is RecordRefusedDecision's one statement.
const recordRefusedDecisionSQL = `UPDATE permission_requests
	   SET attempted_decision = ?, attempted_at = ?, hook_gone_at = COALESCE(hook_gone_at, ?)
	 WHERE claude_instance_id = ? AND request_token = ?`

// RecordRefusedDecision stores the verdict a decide refused as fallen back
// tried to record (b.146 rule 15): attempted_decision and attempted_at,
// replacing an earlier attempt's, and, when hookGoneAt is not zero, sets
// hook_gone_at to it unless it is already set (decide is a writing verb and
// always writes it, rule 8). It writes nothing else: the attempt is shown,
// never acted on. It waits at most maxWait for the write lock; when the lock
// is not taken in that time it returns an error wrapping ErrStoreBusy and has
// written nothing. It emits no trail event (ad.decide.called records the
// call).
func (s *Store) RecordRefusedDecision(instanceID, requestToken, decision string, at, hookGoneAt time.Time, maxWait time.Duration) error {
	return s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, recordRefusedDecisionSQL, decision, millisArg(at), millisArg(hookGoneAt),
			instanceID, requestToken); err != nil {
			return fmt.Errorf("store: record refused decision: %w", err)
		}
		return nil
	})
}

// recordHookGoneSQL is RecordHookGone's statement, run once per request.
const recordHookGoneSQL = `UPDATE permission_requests
	   SET hook_gone_at = COALESCE(hook_gone_at, ?)
	 WHERE request_id = ?
	 RETURNING hook_gone_at`

// RecordHookGone records at as hook_gone_at, the first time a reader found
// the request fallen back (b.146 rules 8 and 15), on each request of
// requestIDs whose hook_gone_at is not yet set, in one transaction, and
// returns the stored hook_gone_at of each request it found, an earlier
// reader's value included. A request that no longer exists is left out.
//
// It waits at most maxWait for the write lock. A reading verb (get, list,
// get-permission) passes 0: it writes only if the lock is free at that
// moment and never waits (b.146 problem 4); when it is not, the call returns
// an error wrapping ErrStoreBusy, having written nothing, and the reader
// skips the write.
func (s *Store) RecordHookGone(at time.Time, maxWait time.Duration, requestIDs ...int64) (map[int64]time.Time, error) {
	out := make(map[int64]time.Time, len(requestIDs))
	if len(requestIDs) == 0 {
		return out, nil
	}
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		for _, id := range requestIDs {
			var stored sql.NullInt64
			err := conn.QueryRowContext(ctx, recordHookGoneSQL, millisArg(at), id).Scan(&stored)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("store: record hook gone: %w", err)
			}
			out[id] = millisTime(stored)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// contextExecer is the statement surface evictClosedRequests runs on: a
// transaction (*sql.Tx) or a connection inside one (*sql.Conn).
type contextExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// evictClosedRequestsSQL deletes the oldest closed requests: those that no
// longer await an answer (awaitingAnswerSQL), oldest decided_at first, except
// each Spawn's newest request while the Spawn has a request that still awaits
// one. Its one placeholder is the number to delete.
const evictClosedRequestsSQL = `DELETE FROM permission_requests
	 WHERE rowid IN (
	     SELECT pr.rowid FROM permission_requests pr
	      WHERE NOT (` + awaitingAnswerSQL + `)
	        AND pr.request_id NOT IN (
	            SELECT MAX(pr.request_id) FROM permission_requests pr
	             GROUP BY pr.claude_instance_id
	            HAVING SUM(` + awaitingAnswerSQL + `) > 0
	        )
	      ORDER BY pr.decided_at ASC
	      LIMIT ?
	 )`

// evictClosedRequests is the cap eviction a request insert runs inside its
// transaction (UpsertOpenPermissionRequest, InsertRelayRequest). When cap > 0
// and the table holds more than cap requests, the oldest closed requests (no
// longer awaiting an answer, ordered by decided_at ASC) are deleted to bring
// the count back to cap. One closed request per Spawn is exempt: the Spawn's
// newest request (highest request_id) while the Spawn has a request that
// still awaits an answer, which is then older than it. pkg/api's decide
// refuses to advise a pane answer for a request recorded before schema v7
// once a later request of its Spawn is recorded (b.t6e, see pkg/api
// fallenBackUnshown), so evicting every later request would erase that
// signal; keeping the newest one keeps it for as long as the older request
// awaits an answer. Like requests awaiting an answer, an exempt request can
// leave the count above cap; there is at most one per Spawn. cap <= 0
// disables eviction (a negative cap is the call site's to handle).
func evictClosedRequests(ctx context.Context, ex contextExecer, cap int) error {
	if cap <= 0 {
		return nil
	}
	var count int
	if err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM permission_requests`).Scan(&count); err != nil {
		return fmt.Errorf("store: permission request count: %w", err)
	}
	if count <= cap {
		return nil
	}
	if _, err := ex.ExecContext(ctx, evictClosedRequestsSQL, count-cap); err != nil {
		return fmt.Errorf("store: permission request evict: %w", err)
	}
	return nil
}
