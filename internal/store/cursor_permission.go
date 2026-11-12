package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// CursorCheck examines independent kernel/pane/pipe evidence after the write
// lock is acquired. The store separately checks the whole persisted binding.
// This is gateway authority; it never fabricates a HookGate parent process.
type CursorCheck func(Spawn) error

func (s *Store) cursorAuthority(ctx context.Context, conn *sql.Conn, b CursorRuntime, until time.Time, check CursorCheck) (Spawn, error) {
	return s.cursorAuthorityForPrompt(ctx, conn, b, until, check, "")
}

func (s *Store) cursorAuthorityForPrompt(ctx context.Context, conn *sql.Conn, b CursorRuntime, until time.Time, check CursorCheck, pendingPath string) (Spawn, error) {
	row, e := scanSpawn(conn.QueryRowContext(ctx, getSpawnSQL, b.InstanceID), getSpawnErrs)
	if e != nil {
		return row, e
	}
	if b.SessionID == "" || b.Terminated || row.Agent != AgentCursor || row.Identity.Token != b.Token || row.LifeNumber != b.Life || row.Identity.PanePID != b.GatewayPID || row.Identity.PaneStarttime != b.GatewayStart || row.SessionID != b.SessionID || row.State == StateEnded || row.State == StateMissing || (row.State == StatePending && (pendingPath == "" || row.JSONLPath != pendingPath)) {
		return row, fmt.Errorf("store: Cursor launch authority changed")
	}
	var n int
	args := append(cursorBindingArgs(b), b.SessionID)
	e = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM cursor_runtimes WHERE `+cursorBindingMatchSQL+` AND bound_session_id=? AND bound_terminated=0`, args...).Scan(&n)
	if e != nil {
		return row, e
	}
	if n != 1 {
		return row, fmt.Errorf("store: Cursor connection authority changed")
	}
	if check == nil {
		return row, fmt.Errorf("store: independent Cursor evidence required")
	}
	if e = check(row); e != nil {
		return row, e
	}
	// The callback can itself take time: check the deadline at the mutation.
	if !until.IsZero() && !time.Now().Before(until) {
		return row, fmt.Errorf("store: Cursor request deadline passed")
	}
	return row, nil
}

// InsertCursorRequest atomically admits a validated non-question request and
// raises check_permission. Hook fields are NULL and origin remains unknown.
// A relay-off request settles immediately, making native pane availability
// truthful through the existing delivery reader, with no extra public shape.
func (s *Store) InsertCursorRequest(b CursorRuntime, req RelayRequest, cap int, until time.Time, maxWait time.Duration, check CursorCheck) error {
	if req.RequestToken == "" || req.ToolUseID == "" || req.SettledAt.IsZero() || req.Hook != (ProcessIdentity{}) {
		return fmt.Errorf("store: invalid Cursor request")
	}
	var id int64
	var prior string
	e := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		row, e := s.cursorAuthority(ctx, conn, b, until, check)
		if e != nil {
			return e
		}
		prior = row.State
		set, args := s.hookWriteStateEvidenceSet(StateCheckPermission, EvidenceAgentEvent)
		args = append(args, b.InstanceID)
		if _, e = conn.ExecContext(ctx, `UPDATE spawns SET `+set+`, last_seen_at=CURRENT_TIMESTAMP, ended_at=NULL, `+launchStartClear+`, `+idleClear+`, liveness_unverified_since=NULL,liveness_note=NULL, `+rowVersionAdvance+` WHERE instance_id=?`, args...); e != nil {
			return e
		}
		args = []any{b.InstanceID, req.RequestToken, req.ToolName, req.ToolInput, nil, nil, nil, req.ToolUseID, nil, millisArg(req.SettledAt), false, b.InstanceID}
		result, e := conn.ExecContext(ctx, relayInsertSQL, args...)
		if e != nil {
			return e
		}
		id, _ = result.LastInsertId()
		if e := evictClosedRequests(ctx, conn, cap); e != nil {
			return e
		}
		return s.refreshFamiliesAfterStateChange(connQuerier{ctx, conn}, prior, StateCheckPermission)
	})
	if e == nil {
		_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{"instance_id": b.InstanceID, "prior_state": prior, "new_state": StateCheckPermission, "triggering_event_name": "PermissionRequest", "soft_refresh": false, "source": "ad_spawn_store"})
		_ = trail.Emit(context.Background(), "ad.row_mutation.committed", map[string]any{"instance_id": b.InstanceID, "request_token": req.RequestToken, "request_id": id, "tool_name": req.ToolName, "decision": nil, "decision_reason": nil, "writer_process": WriterProcessCursorHost, "mutation_kind": "insert", "source": "ad_store"})
	}
	return e
}

// AckCursorDecision reuses first-writer decision/timeout/ack contracts in one
// authenticated transaction. Both allow and deny require this committed ack.
// at is sampled after lock/evidence checks; the finite deadline is immutable.
func (s *Store) AckCursorDecision(b CursorRuntime, token string, timeout bool, until, allowUntil time.Time, maxWait time.Duration, check CursorCheck) (string, bool, error) {
	var decision string
	var acked bool
	var timeoutID int64
	var timeoutName string
	var timeoutWritten bool
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		if err := permissionResponseDurability(ctx, conn); err != nil {
			return err
		}
		if _, e := s.cursorAuthority(ctx, conn, b, until, check); e != nil {
			return e
		}
		pr, e := scanPermissionRow(conn.QueryRowContext(ctx, getPermissionRequestSQL, b.InstanceID, token))
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if pr.Closed() || pr.ProvenGone() || pr.PaneAnswer != PaneAnswerNone || pr.SettledAt.IsZero() || !pr.SettledAt.Equal(until) || !time.Now().Before(pr.SettledAt) {
			return nil
		}
		timeout = timeout || !time.Now().Before(allowUntil)
		// A committed first-writer allow is not permission to acknowledge
		// after the approval deadline. Keep it unacknowledged for truthful
		// settlement/fallback instead of rewriting or emitting a late allow.
		if pr.Decision == "allow" && !time.Now().Before(allowUntil) {
			return nil
		}
		if timeout {
			e := conn.QueryRowContext(ctx, denyRelayTimeoutSQL, "deny", DecisionReasonTimeout, millisArg(time.Now()), b.InstanceID, token).Scan(&timeoutID, &timeoutName)
			if e == nil {
				decision = "deny"
				timeoutWritten = true
				acked = true
				return nil
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		var reason string
		e = conn.QueryRowContext(ctx, ackRelayDecisionSQL, millisArg(time.Now()), b.InstanceID, token).Scan(&decision, &reason)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		acked = true
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if timeoutWritten {
		emitDecisionCommitted(b.InstanceID, token, timeoutID, timeoutName, "deny", DecisionReasonTimeout, WriterProcessCursorHost)
	}
	return decision, acked, nil
}

// ClaimCursorPaneAnswer is the durable native arbiter. An external claim made
// by record-pane-answer never acquires ACP authority. A guarded send-keys
// intent is consumed only once it has reached sent; the controller waits for
// that transition. response_state makes consumption irreversible, including
// after an unconfirmed commit or gateway replacement. Native delivery is
// separate from relay delivered_at.
// A committed claim emits the established decision mutation event, fail-open.
func (s *Store) ClaimCursorPaneAnswer(b CursorRuntime, token, decision string, until time.Time, maxWait time.Duration, check CursorCheck) (bool, error) {
	if decision != "allow" && decision != "deny" {
		return false, fmt.Errorf("store: invalid Cursor pane verdict")
	}
	var claimed bool
	var requestID int64
	var toolName, committedDecision, reason string
	e := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		if err := permissionResponseDurability(ctx, conn); err != nil {
			return err
		}
		if _, e := s.cursorAuthority(ctx, conn, b, until, check); e != nil {
			return e
		}
		e := conn.QueryRowContext(ctx, `UPDATE permission_requests SET pane_answer='sent',pane_as=?,decision=?,decision_reason='pane',decided_at=CURRENT_TIMESTAMP,hook_gone_at=COALESCE(hook_gone_at,?),
 decision_origin='pane',response_state='native_claimed'
 WHERE instance_id=? AND request_token=? AND delivered_at IS NULL AND response_state IS NULL
 AND `+permissionResponseEligibleSQL+`
 AND ((pane_answer='none' AND (decision IS NULL OR settled_at<=?))
      OR (pane_answer='sent' AND decision_origin='pane' AND pane_as=?))
 RETURNING request_id,tool_name,decision,decision_reason`, decision, decision, millisArg(time.Now()), b.InstanceID, token, millisArg(time.Now()), decision).Scan(&requestID, &toolName, &committedDecision, &reason)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		claimed = true
		return nil
	})
	if e != nil {
		return false, e
	}
	if claimed {
		emitDecisionCommitted(b.InstanceID, token, requestID, toolName, committedDecision, reason, WriterProcessCursorHost)
	}
	return claimed, nil
}

// ErrCursorAnswerTurnEnded is CheckCursorAnswer's and IssueCursorAnswer's
// refusal of a claimed answer, not yet issued, whose request its prompt
// turn's end proved gone (ProveCursorTurnEnded) and that would otherwise still
// be eligible: the answer is moot and authorizes no bytes. Every other refusal
// is a plain error.
var ErrCursorAnswerTurnEnded = errors.New("store: Cursor answer refused: its prompt turn has ended")

// CheckCursorAnswer is the writer's final authority check, including the exact
// request's durable acknowledgement or native claim. The transport may repeat
// this check; only IssueCursorAnswer consumes the claim immediately before I/O.
// Another request's proof may advance the raw-state epoch after this claim;
// it does not retire this request's captured owner or unconsumed response.
func (s *Store) CheckCursorAnswer(b CursorRuntime, token string, relay bool, until time.Time, check CursorCheck) error {
	return s.cursorAnswer(b, token, relay, until, check, false)
}

// IssueCursorAnswer consumes a confirmed claim once, before transport. An
// error (including an unknown commit) authorizes no bytes. Repeated callbacks,
// reopen and gateway replacement cannot replay an issued claim. Provider I/O
// occurs only after this transaction has returned a confirmed commit.
func (s *Store) IssueCursorAnswer(b CursorRuntime, token string, relay bool, until time.Time, check CursorCheck) error {
	return s.cursorAnswer(b, token, relay, until, check, true)
}

func (s *Store) cursorAnswer(b CursorRuntime, token string, relay bool, until time.Time, check CursorCheck, issue bool) error {
	// Wait only within the caller's existing response deadline. Revalidate
	// authority and expiry after acquisition and independent evidence.
	return s.inWriteTx(max(time.Until(until), 0), func(ctx context.Context, conn *sql.Conn) error {
		if issue {
			if err := permissionResponseDurability(ctx, conn); err != nil {
				return err
			}
		}
		if _, e := s.cursorAuthority(ctx, conn, b, until, check); e != nil {
			return e
		}
		frag := `pane_answer='sent' AND decision_origin='pane' AND decision IN ('allow','deny') AND decision=pane_as
 AND response_state='native_claimed'`
		issued := ResponseNativeIssued
		if relay {
			frag = `delivered_at IS NOT NULL AND pane_answer='none' AND response_state='relay_claimed' AND ` + relayVerdictSQL
			issued = ResponseRelayIssued
		}
		where := `instance_id=? AND request_token=? AND ` + permissionResponseEligibleSQL + ` AND ` + frag
		var n int64
		var e error
		if issue {
			var res sql.Result
			res, e = conn.ExecContext(ctx, `UPDATE permission_requests SET response_state=? WHERE `+where, issued, b.InstanceID, token)
			if e == nil {
				n, e = res.RowsAffected()
			}
		} else {
			e = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM permission_requests WHERE `+where, b.InstanceID, token).Scan(&n)
		}
		if e != nil {
			return e
		}
		if !time.Now().Before(until) {
			return fmt.Errorf("store: Cursor answer authority retired")
		}
		if n == 1 {
			return nil
		}
		// Re-read without the proof condition: a claim refused only because
		// its turn's end proved the request gone is known unwritten.
		turnEnded := `instance_id=? AND request_token=? AND closed_at IS NULL AND proven_gone_how='` + ProvenGoneTurnEnd + `' AND ` + permissionOwnerSQL + ` AND ` + frag
		if e = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM permission_requests WHERE `+turnEnded, b.InstanceID, token).Scan(&n); e != nil {
			return e
		}
		if n == 1 {
			return ErrCursorAnswerTurnEnded
		}
		return fmt.Errorf("store: Cursor answer authority retired")
	})
}

// ProveCursorToolRan applies existing tool_ran proof semantics to one request,
// under current runtime authority. The gateway must first validate actual
// execution output; generic completion and plan acceptance are not evidence.
// Emit only after a committed mutation; an already complete proof is a no-op.
// The event preserves any earlier decision and non-null reason. A missing
// reason is enriched with tool_ran independently of verdict provenance;
// this historical enrichment grants no response authority.
// A committed proof also moves the row from check_permission to working, as
// Claude's PostToolUse does, but only when no request still holds the row,
// turnOpen says a prompt turn is open and the request's retained decision is
// not deny (proveCursorTool).
func (s *Store) ProveCursorToolRan(b CursorRuntime, token, tool string, turnOpen bool, check CursorCheck) error {
	return s.proveCursorTool(b, token, tool, true, turnOpen, check, nil)
}

// ProveCursorToolFailed proves only the exact completed invocation's dialog
// gone, under current runtime authority. The gateway must validate the captured
// MCP error completion first. Like Claude's PostToolUseFailure, tool_ran here
// records the invocation's end, not successful execution of the server action.
// No decision, delivery or pane-answer inference follows from this evidence.
// A committed proof moves the row from check_permission to working, as that
// hook does, but only when no request still holds the row, turnOpen says a
// prompt turn is open and the request's retained decision is not deny
// (proveCursorTool).
func (s *Store) ProveCursorToolFailed(b CursorRuntime, token, tool string, turnOpen bool, check CursorCheck) error {
	return s.proveCursorTool(b, token, tool, false, turnOpen, check, nil)
}

// CommitCursorProof extends the same proof transaction with exact durable
// source evidence. No ledger entry exists unless all request/state effects
// commit too. A matching event is idempotent; a reused identity is refused.
func (s *Store) CommitCursorProof(p CursorProof, turnOpen bool, check CursorCheck) error {
	p.Raw = append([]byte(nil), p.Raw...)
	if err := p.validate(); err != nil {
		return err
	}
	return s.proveCursorTool(p.Owner.Runtime, p.Token, p.Tool, p.Ran, turnOpen, check, &p)
}

// proveCursorTool writes the proof and, in the same transaction and only when
// the proof wrote, turnOpen is set and the retained decision is not deny,
// moves the row from check_permission to
// working when no request still holds it (noRequestHoldsRowSQL), with the
// columns an ordinary hook move to working writes. Cursor sends no mid-turn
// hook, so this is the Claude PostToolUse (ran) or PostToolUseFailure move,
// and the trail names that event. A proof written while no turn is open
// (turnOpen false), such as a late one after the prompt response or one
// flushed before the Stop report, moves nothing, since no later report would
// repair a working row whose turn has ended; the row keeps its state for the
// gateway's next report. A row in any other state keeps it. A request whose
// retained decision is deny moves nothing either, whether a delivered deny
// gets no proof or a failed one keeps its deny: the row keeps
// check_permission until the turn's Stop.
func (s *Store) proveCursorTool(b CursorRuntime, token, tool string, ran, turnOpen bool, check CursorCheck, proof *CursorProof) error {
	var written, moved bool
	var requestID int64
	var toolName, decision, reason string
	set, incomplete := "", ""
	if ran {
		set = `,
 pane_answer=CASE WHEN delivered_at IS NULL AND pane_answer IN ('none','intent') THEN 'tool_ran' ELSE pane_answer END,
 decision_origin=CASE WHEN decision IS NULL THEN 'observation' ELSE decision_origin END,
 decision_reason=COALESCE(decision_reason,'tool_ran'),
 decision=COALESCE(decision,'allow'),decided_at=COALESCE(decided_at,CURRENT_TIMESTAMP)`
		incomplete = ` OR (delivered_at IS NULL AND pane_answer IN ('none','intent'))
 OR decision IS NULL OR decision_reason IS NULL OR decided_at IS NULL`
	}
	err := s.inWriteTx(0, func(ctx context.Context, conn *sql.Conn) error {
		if _, e := s.cursorAuthority(ctx, conn, b, time.Time{}, check); e != nil {
			return e
		}
		if proof != nil {
			inserted, e := s.recordCursorProof(ctx, conn, *proof)
			if e != nil || !inserted {
				return e
			}
		}
		e := conn.QueryRowContext(ctx, `UPDATE permission_requests SET proven_gone_at=COALESCE(proven_gone_at,?),proven_gone_how=COALESCE(proven_gone_how,?)`+set+`
 WHERE instance_id=? AND request_token=? AND tool_use_id=? AND closed_at IS NULL
 AND (proven_gone_at IS NULL OR proven_gone_how IS NULL`+incomplete+`)
 RETURNING request_id,tool_name,COALESCE(decision,''),COALESCE(decision_reason,'')`, millisArg(time.Now()), ProvenGoneToolRan, b.InstanceID, token, tool).Scan(&requestID, &toolName, &decision, &reason)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		written = true
		if !turnOpen || decision == "deny" {
			return nil
		}
		stateSet, stateArgs := s.hookWriteStateEvidenceSet(StateWorking, EvidenceAgentEvent)
		stateArgs = append(stateArgs, b.InstanceID)
		result, e := conn.ExecContext(ctx, `UPDATE spawns SET `+stateSet+`, last_seen_at=CURRENT_TIMESTAMP, ended_at=NULL, `+launchStartClear+`, `+idleClear+`, liveness_unverified_since=NULL,liveness_note=NULL, `+rowVersionAdvance+`
 WHERE instance_id=? AND state='`+StateCheckPermission+`' AND `+noRequestHoldsRowSQL, stateArgs...)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		moved = true
		return s.refreshFamiliesAfterStateChange(connQuerier{ctx, conn}, StateCheckPermission, StateWorking)
	})
	if err == nil && written {
		emitDecisionCommitted(b.InstanceID, token, requestID, toolName, decision, reason, WriterProcessCursorHost)
	}
	if err == nil && moved {
		event := "PostToolUse"
		if !ran {
			event = "PostToolUseFailure"
		}
		_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{"instance_id": b.InstanceID, "prior_state": StateCheckPermission, "new_state": StateWorking, "triggering_event_name": event, "soft_refresh": false, "source": "ad_spawn_store"})
	}
	return err
}

// ProveCursorTurnEnded proves gone, turn_end, every request of b's instance
// among tokens that is still unproven and not closed, under b's launch and
// conversation authority, as ProveCursorToolRan checks it. The gateway calls
// it when b's prompt turn completes, with every request of the turn that has
// no deferred tool proof, answered or not (b.7qb): once the turn is over, a
// request still awaiting an answer no longer outranks the agent's own report
// (b.gwj), so the turn's Stop, or a rotation's RotateCursorSession, moves the
// row to waiting, and no request of the turn holds the next prompt
// (cursorHoldsDialogSQL), plain send-keys or pause.
// It writes no decision, delivery or pane answer; a request delivered or
// answered at the pane keeps those, and a closed or proven one keeps all it has.
// until bounds the wait for the write lock and the authority check. It emits
// no trail event: the Stop's ad.spawn.state_transition records the move.
func (s *Store) ProveCursorTurnEnded(b CursorRuntime, tokens []string, until time.Time, check CursorCheck) error {
	if len(tokens) == 0 {
		return nil
	}
	return s.inWriteTx(max(time.Until(until), 0), func(ctx context.Context, conn *sql.Conn) error {
		if _, e := s.cursorAuthority(ctx, conn, b, until, check); e != nil {
			return e
		}
		args := []any{millisArg(time.Now()), ProvenGoneTurnEnd, b.InstanceID}
		for _, token := range tokens {
			args = append(args, token)
		}
		_, e := conn.ExecContext(ctx, `UPDATE permission_requests AS pr SET proven_gone_at=?,proven_gone_how=?
 WHERE pr.instance_id=? AND pr.request_token IN (`+strings.TrimSuffix(strings.Repeat("?,", len(tokens)), ",")+`)
 AND pr.proven_gone_at IS NULL AND pr.closed_at IS NULL`, args...)
		return e
	})
}
