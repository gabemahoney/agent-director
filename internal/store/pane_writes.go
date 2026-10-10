package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file holds the store writes of b.146 step 2b, which close a
// permission request at the pane: a pane answer through send-keys (rule 8:
// its intent, then sent, or the intent released), a caller's record of an
// answer made outside agent-director (record-pane-answer, rule 13), and the
// PostToolUse close of a request whose tool ran (rule 13). None reads Claude
// Code's screen: the verdict each records is a caller's claim (pane_as) or
// the inference that a tool which ran was allowed.

// PaneIntent is one pane answer's intent (b.146 rule 8): the verdict its
// caller claims (allow or deny), the process that sends its key, and when the
// intent was written, as RecordPaneIntent stamped it inside its transaction.
// The sender is the zero identity when that process could not read its own
// identity; a reader then judges the intent by its time.
type PaneIntent struct {
	As     string
	Sender ProcessIdentity
	At     time.Time
}

// PaneCheck is a pane write's precondition, run inside the write's
// transaction once the write lock is taken and before the write (b.146 rules
// 8 and 13): it is given the request's Spawn and every permission request of
// it, read inside the transaction, and a non-nil error rolls the transaction
// back, writing nothing, and is returned as it is. Under the write lock no
// relay hook can ack and no other writer can change a request, so what it
// judges stays true until the write commits.
type PaneCheck func(sp Spawn, requests []PermissionRow) error

// runPaneCheck reads instanceID's row and every one of its permission
// requests on conn, inside the caller's transaction, and runs check on them.
// No row is an error wrapping ErrSpawnNotFound. A nil check reads nothing.
func runPaneCheck(ctx context.Context, conn *sql.Conn, instanceID string, check PaneCheck) error {
	if check == nil {
		return nil
	}
	sp, err := scanSpawn(conn.QueryRowContext(ctx, getSpawnSQL, instanceID), getSpawnErrs)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return err
	}
	rows, err := queryPermissionRows(func(q string, args ...any) (*sql.Rows, error) {
		return conn.QueryContext(ctx, q, args...)
	}, permissionRequestsForSpawnSQL, "store: permission requests", instanceID)
	if err != nil {
		return err
	}
	return check(sp, rows)
}

// paneIntentSQL is RecordPaneIntent's statement: the intent, guarded by the
// request still awaiting an answer.
const paneIntentSQL = `UPDATE permission_requests AS pr
	   SET pane_answer = 'intent', pane_as = ?,
	       pane_sender_pid = ?, pane_sender_starttime = ?, pane_sender_pidns = ?,
	       pane_intent_at = ?, hook_gone_at = COALESCE(pr.hook_gone_at, ?)
	 WHERE pr.claude_instance_id = ? AND pr.request_token = ? AND ` + awaitingAnswerSQL

// RecordPaneIntent is a pane answer's first write (b.146 rule 8, step 1): in
// ONE transaction, begun with BEGIN IMMEDIATE and waiting at most maxWait for
// the write lock, it runs check (PaneCheck: send-keys' rule-7 checks and its
// capture and hash comparison of the pane), then reads now (time.Now when nil)
// and records the intent on the request with requestToken: pane_answer
// intent, pane_as as, sender's pid, start time and pid namespace (a zero field
// as NULL), pane_intent_at that instant, and hook_gone_at that instant unless
// it is already set (a pane answer is given only once the request's relay hook
// is gone, and a writing verb records when it found it so, b.146 rule 8). It
// writes only while the request still awaits an answer (awaitingAnswerSQL), so
// nothing lands on a request acked, answered at the pane or closed meanwhile.
//
// The instant is read once the write lock is taken and check has run, so a
// reader that cannot check the sender counts the intent as in progress from
// its commit (b.146 problem 2), never from before a wait for the lock that
// could have used up that hold while another caller was still to commit.
//
// The intent is committed before the caller types its key, so a sender that
// dies after typing leaves intent, never none (control c_paneintent).
//
// It returns the intent as recorded (as, sender and the instant at the
// column's millisecond resolution, which RecordPaneSent and ReleasePaneIntent
// match the request by) and true when the intent was written; the zero
// PaneIntent and false, with no error, when no request matched; an error from
// check as it is; an error wrapping ErrStoreBusy, with nothing written, when
// the lock was not taken in time; any other failure wrapped. It emits no
// trail event.
func (s *Store) RecordPaneIntent(instanceID, requestToken, as string, sender ProcessIdentity, now func() time.Time, maxWait time.Duration, check PaneCheck) (PaneIntent, bool, error) {
	if now == nil {
		now = time.Now
	}
	var (
		intent  PaneIntent
		written bool
	)
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		if err := runPaneCheck(ctx, conn, instanceID, check); err != nil {
			return err
		}
		at := time.UnixMilli(now().UnixMilli()).UTC()
		args := append([]any{as}, processIdentityArgs(sender)...)
		args = append(args, millisArg(at), millisArg(at), instanceID, requestToken)
		res, err := conn.ExecContext(ctx, paneIntentSQL, args...)
		if err != nil {
			return fmt.Errorf("store: record pane intent: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: record pane intent rows affected: %w", err)
		}
		written = n > 0
		intent = PaneIntent{As: as, Sender: sender, At: at}
		return nil
	})
	if err != nil || !written {
		return PaneIntent{}, false, err
	}
	return intent, true, nil
}

// paneIntentOwnerSQL is the WHERE fragment that matches a request whose
// intent is still intent's (b.146 rule 8): pane_answer intent, written at
// intent.At by intent.Sender. Its placeholders take paneIntentOwnerArgs.
const paneIntentOwnerSQL = `pane_answer = 'intent' AND pane_intent_at = ?
	   AND pane_sender_pid IS ? AND pane_sender_starttime IS ? AND pane_sender_pidns IS ?`

// paneIntentOwnerArgs returns the bound arguments of paneIntentOwnerSQL.
func paneIntentOwnerArgs(intent PaneIntent) []any {
	return append([]any{millisArg(intent.At)}, processIdentityArgs(intent.Sender)...)
}

// paneSentSQL is RecordPaneSent's statement.
const paneSentSQL = `UPDATE permission_requests
	   SET pane_answer = 'sent', decision = pane_as, decision_reason = ?, decided_at = CURRENT_TIMESTAMP
	 WHERE claude_instance_id = ? AND request_token = ? AND closed_at IS NULL AND ` + paneIntentOwnerSQL + `
	 RETURNING request_id, tool_name, decision`

// RecordPaneSent is a pane answer's last write (b.146 rule 8, step 3), once
// its key was sent: one statement that records pane_answer sent, decision the
// intent's claimed verdict (pane_as), decision_reason pane and decided_at, only
// while the request still carries this sender's intent (paneIntentOwnerSQL:
// nothing closed it meanwhile at the pane, such as the PostToolUse close of
// its tool) and no close of its Spawn's requests closed it (closed_at IS NULL:
// a close between the intent and sent wins, b.146 rule 12, and keeps its
// verdict). It waits at most maxWait for the write lock; a lock not taken in
// time is an error wrapping ErrStoreBusy, with nothing written.
//
// It returns true when written, which emits ad.row_mutation.committed (writer
// send_keys), fail-open; false, with no error, when the request no longer
// carries the intent or was closed.
func (s *Store) RecordPaneSent(instanceID, requestToken string, intent PaneIntent, maxWait time.Duration) (bool, error) {
	var (
		requestID int64
		toolName  string
		decision  string
		written   bool
	)
	args := append([]any{DecisionReasonPane, instanceID, requestToken}, paneIntentOwnerArgs(intent)...)
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		err := conn.QueryRowContext(ctx, paneSentSQL, args...).Scan(&requestID, &toolName, &decision)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: record pane sent: %w", err)
		}
		written = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if written {
		emitDecisionCommitted(instanceID, requestToken, requestID, toolName, decision, DecisionReasonPane, WriterProcessSendKeys)
	}
	return written, nil
}

// paneReleaseSQL is ReleasePaneIntent's statement.
const paneReleaseSQL = `UPDATE permission_requests
	   SET pane_sender_pid = NULL, pane_sender_starttime = NULL, pane_sender_pidns = NULL, pane_intent_at = NULL
	 WHERE claude_instance_id = ? AND request_token = ? AND ` + paneIntentOwnerSQL

// ReleasePaneIntent ends a pane answer's claim on its request when its call
// ends without recording sent (b.146 rule 8; problem 2): the key send failed,
// or its sent write did. One statement clears the sender and pane_intent_at,
// only while the request still carries this sender's intent; pane_answer
// stays intent, since the key may have been typed. A released intent is no
// pane answer in progress, so a retry with a fresh pane hash is accepted at
// once, also when the sender is a long-lived process (an MCP server or a Go
// caller). It waits at most maxWait for the write lock; a lock not taken in
// time is an error wrapping ErrStoreBusy, with nothing written. It returns
// whether it wrote, and emits no trail event.
func (s *Store) ReleasePaneIntent(instanceID, requestToken string, intent PaneIntent, maxWait time.Duration) (bool, error) {
	var written bool
	args := append([]any{instanceID, requestToken}, paneIntentOwnerArgs(intent)...)
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, paneReleaseSQL, args...)
		if err != nil {
			return fmt.Errorf("store: release pane intent: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: release pane intent rows affected: %w", err)
		}
		written = n > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return written, nil
}

// paneOutsideSQL is RecordPaneOutside's statement.
const paneOutsideSQL = `UPDATE permission_requests AS pr
	   SET pane_answer = 'outside', pane_as = ?, decision = ?, decision_reason = ?, decided_at = CURRENT_TIMESTAMP
	 WHERE pr.claude_instance_id = ? AND pr.request_token = ? AND ` + awaitingAnswerSQL + `
	 RETURNING request_id, tool_name`

// RecordPaneOutside is record-pane-answer's write (b.146 rule 13): in ONE
// transaction, begun with BEGIN IMMEDIATE and waiting at most maxWait for the
// write lock, it runs check (PaneCheck: the request fallen back, no pane
// answer in progress, its hook gone long enough, and the capture and hash
// comparison of the pane), then records pane_answer outside, pane_as claim,
// decision claim (NULL for unknown), decision_reason pane_outside and
// decided_at on the request with requestToken, only while it still awaits an
// answer (awaitingAnswerSQL). The claim is stored, never checked.
//
// It returns true when written, which emits ad.row_mutation.committed
// (writer record_pane_answer), fail-open; false, with no error, when no
// request matched; check's error as it is; an error wrapping ErrStoreBusy,
// with nothing written, when the lock was not taken in time.
func (s *Store) RecordPaneOutside(instanceID, requestToken, claim string, maxWait time.Duration, check PaneCheck) (bool, error) {
	var (
		requestID int64
		toolName  string
		written   bool
	)
	var decision any
	if claim == "allow" || claim == "deny" {
		decision = claim
	}
	err := s.inWriteTx(maxWait, func(ctx context.Context, conn *sql.Conn) error {
		if err := runPaneCheck(ctx, conn, instanceID, check); err != nil {
			return err
		}
		err := conn.QueryRowContext(ctx, paneOutsideSQL, claim, decision, DecisionReasonPaneOutside,
			instanceID, requestToken).Scan(&requestID, &toolName)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: record pane outside: %w", err)
		}
		written = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if written {
		d, _ := decision.(string)
		emitDecisionCommitted(instanceID, requestToken, requestID, toolName, d, DecisionReasonPaneOutside, WriterProcessRecordPaneAnswer)
	}
	return written, nil
}

// toolRanCandidatesSQL reads the requests of one Spawn, recorded from schema
// v7 on, whose tool_use_id is the given one and which still await an answer.
const toolRanCandidatesSQL = `SELECT ` + permissionColumns + `
	  FROM permission_requests pr
	 WHERE pr.claude_instance_id = ? AND pr.tool_use_id = ? AND pr.settled_at IS NOT NULL AND ` + awaitingAnswerSQL + `
	 ORDER BY pr.request_id ASC`

// closeToolRanSQL is CloseToolRanRequests' statement, run once per request.
const closeToolRanSQL = `UPDATE permission_requests AS pr
	   SET pane_answer = 'tool_ran', decision = 'allow', decision_reason = ?, decided_at = CURRENT_TIMESTAMP,
	       hook_gone_at = COALESCE(pr.hook_gone_at, ?)
	 WHERE pr.request_id = ? AND pr.claude_instance_id = ? AND pr.tool_use_id = ? AND ` + awaitingAnswerSQL + `
	   AND EXISTS (SELECT 1 FROM spawns WHERE claude_instance_id = ? AND ` + hookGateSQL + `)
	 RETURNING request_token, tool_name`

// CloseToolRanRequests is the PostToolUse close (b.146 rule 13): Claude
// Code's PostToolUse or PostToolUseFailure for toolUseID means the tool of
// the request carrying that tool_use_id ran, so its dialog was answered allow.
// It reads instanceID's requests recorded from schema v7 on whose tool_use_id
// is toolUseID and which still await an answer, and closes each one whose
// relay hook gone reports gone (fallen back: the caller judges the hook
// process, or its settle instant when the process cannot be checked, BEFORE
// the guarded write reads the record; a request whose hook may still answer
// it is left alone) with one guarded statement: pane_answer tool_ran,
// decision allow, decision_reason tool_ran, decided_at, and hook_gone_at at
// unless already set, only while the request still awaits an answer and the
// hook's gate holds (hookGateSQL: the hook's parent is the row's recorded
// pane process, SR-22.9). A nil gone closes nothing. An empty toolUseID
// reads and closes nothing.
//
// Each statement waits for the write lock as every hook write does (the
// store's busy timeout). It returns the tokens it closed; each emits
// ad.row_mutation.committed (writer hook), fail-open. An error stops the loop
// and is returned with the tokens closed before it.
func (s *Store) CloseToolRanRequests(instanceID string, gate HookGate, toolUseID string, at time.Time, gone func(PermissionRow) bool) ([]string, error) {
	if toolUseID == "" || gone == nil {
		return nil, nil
	}
	candidates, err := queryPermissionRows(s.db.Query, toolRanCandidatesSQL, "store: tool-ran candidates", instanceID, toolUseID)
	if err != nil {
		return nil, err
	}
	var closed []string
	for _, pr := range candidates {
		if !gone(pr) {
			continue
		}
		args := append([]any{DecisionReasonToolRan, millisArg(at), pr.RequestID, instanceID, toolUseID, instanceID},
			hookGateArgs(gate)...)
		var token, toolName string
		err := s.db.QueryRow(closeToolRanSQL, args...).Scan(&token, &toolName)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return closed, fmt.Errorf("store: close tool-ran request: %w", err)
		}
		emitDecisionCommitted(instanceID, token, pr.RequestID, toolName, "allow", DecisionReasonToolRan, WriterProcessHook)
		closed = append(closed, token)
	}
	return closed, nil
}
