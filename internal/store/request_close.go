package store

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"
)

// This file holds the close of a Spawn's open permission requests (b.146 rule
// 12): a request whose asking agent is gone, or judged gone, is closed for
// good, in the transaction of the write that ends the agent's launch, so it
// no longer awaits an answer once the row is resumed. Three writes run it:
// find-missing's mark (MarkMissingIfSameLife, reason find_missing), the
// terminal SessionEnd's move to ended (ApplyHookTransitionResult, reason
// ended) and, as a backstop for a request an earlier release left open on a
// finished row, resume's move to pending (MoveToPending, reason ended). The
// same close proves every request of the row gone (b.146 step 2c): its agent
// is gone, so no dialog of it is left on the pane.

// denyOrphanedRequestsSQL is the close's first statement: every request of
// the row that still awaits an answer (awaitingAnswerSQL) and is undecided is
// denied with the close's reason and marked closed. It returns what it closed
// for the trail.
var denyOrphanedRequestsSQL = `UPDATE permission_requests AS pr
    SET decision = ?, decision_reason = ?, decided_at = CURRENT_TIMESTAMP, closed_at = ?
  WHERE pr.claude_instance_id = ? AND pr.decision IS NULL AND ` + awaitingAnswerSQL + `
  RETURNING request_id, request_token, tool_name`

// closeDecidedRequestsSQL is the close's second statement: every request of
// the row that still awaits an answer after the first, a decided one whose
// relay hook has not acked its verdict, is marked closed, its decision,
// decision_reason and decided_at kept. It returns what it closed for the
// trail.
var closeDecidedRequestsSQL = `UPDATE permission_requests AS pr
    SET closed_at = ?
  WHERE pr.claude_instance_id = ? AND ` + awaitingAnswerSQL + `
  RETURNING request_id, request_token, tool_name`

// proveAgentGoneSQL is the close's third statement (b.146 step 2c): every
// request of the row not yet proven gone, closed by the first two statements,
// acked, answered at the pane or closed before, is proven gone, agent_gone.
const proveAgentGoneSQL = `UPDATE permission_requests
    SET proven_gone_at = ?, proven_gone_how = ?
  WHERE claude_instance_id = ? AND proven_gone_at IS NULL`

// closedRequest is one permission request closeOrphanedRequests closed;
// denied when the close also denied it (it was undecided).
type closedRequest struct {
	requestID int64
	token     string
	toolName  string
	denied    bool
}

// closeOrphanedRequests closes every request of instanceID that still awaits
// an answer, on conn, inside the transaction its caller holds: the undecided
// ones denied with decision_reason reason (denyOrphanedRequestsSQL), then the
// decided ones marked closed (closeDecidedRequestsSQL), both with one
// closed_at. Then it proves every request of instanceID not yet proven gone,
// whether it was just closed or no longer awaited an answer, with that same
// instant and proven_gone_how agent_gone (proveAgentGoneSQL; b.146 step 2c).
// It returns the requests it closed in request-id order and emits nothing:
// its caller emits once its transaction commits (emitDeny).
func closeOrphanedRequests(ctx context.Context, conn *sql.Conn, instanceID, reason string) ([]closedRequest, error) {
	at := millisArg(time.Now())
	denied, err := queryClosedRequests(ctx, conn, true, denyOrphanedRequestsSQL, "deny", reason, at, instanceID)
	if err != nil {
		return nil, err
	}
	kept, err := queryClosedRequests(ctx, conn, false, closeDecidedRequestsSQL, at, instanceID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, proveAgentGoneSQL, at, ProvenGoneAgentGone, instanceID); err != nil {
		return nil, fmt.Errorf("prove requests gone: %w", err)
	}
	closed := append(denied, kept...)
	slices.SortFunc(closed, func(a, b closedRequest) int { return cmp.Compare(a.requestID, b.requestID) })
	return closed, nil
}

// queryClosedRequests runs one of the close's statements, q with args, on
// conn and returns the requests it returned, each marked denied as given.
func queryClosedRequests(ctx context.Context, conn *sql.Conn, denied bool, q string, args ...any) ([]closedRequest, error) {
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var closed []closedRequest
	for rows.Next() {
		c := closedRequest{denied: denied}
		if err := rows.Scan(&c.requestID, &c.token, &c.toolName); err != nil {
			return nil, err
		}
		closed = append(closed, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return closed, nil
}

// emitDeny emits the ad.row_mutation.committed of c's deny
// (emitDecisionCommitted: decision deny, decision_reason reason, writer
// writerProcess) when the close denied c; nothing for a request it only
// closed, whose verdict it kept. Called after the close's transaction
// commits; fail-open.
func (c closedRequest) emitDeny(instanceID, reason, writerProcess string) {
	if c.denied {
		emitDecisionCommitted(instanceID, c.token, c.requestID, c.toolName, "deny", reason, writerProcess)
	}
}
