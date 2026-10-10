package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// ApplyHookTransition is ApplyHookTransitionResult without the outcome: it
// returns whether the gated write applied, and why not.
func (s *Store) ApplyHookTransition(instanceID string, gate HookGate, newState string, softRefresh bool, triggeringEventName, jsonlPath string, jsonlPresent bool) (HookApplied, error) {
	_, applied, err := s.ApplyHookTransitionResult(instanceID, gate, newState, softRefresh, triggeringEventName, jsonlPath, jsonlPresent)
	return applied, err
}

// ApplyHookTransitionResult is the gated write of an ordinary hook (every
// event but SessionStart, which RecordSessionStartIdentity writes, the main
// agent's idle-prompt Notification, which ApplyHookWaitingIfWorking writes,
// and a relayed PermissionRequest, whose move to check_permission is in
// InsertRelayRequest's transaction). The transition follows SRD §5.2:
//   - When newState is non-empty, the row's state moves to newState and
//     last_seen_at is bumped; a state other than ended clears ended_at.
//   - When newState is `ended`, ended_at is also set to CURRENT_TIMESTAMP,
//     and the row's open permission requests are closed in the same
//     transaction (applyEndedTransition; b.146 rule 12).
//   - When softRefresh=true, state stays; last_seen_at is bumped.
//
// Each branch also clears the liveness_unverified_since/liveness_note markers
// and idle_since (any hook after the idle-prompt Notification clears it,
// b.146 problem 3) and advances row_version by one; the ended transition and
// every transition to a state other than pending set launch_started_at to
// NULL, and a soft refresh leaves it unchanged (SR-5.2).
//
// The gate (SR-22.9): every branch's UPDATE carries hookGateSQL in its own
// WHERE, so it applies only when gate.ParentPID and gate.ParentStart are the
// row's recorded pane process, and records the parent's start time when the
// row's pane_starttime is NULL. An applied write on a row that records no
// Claude session id also records gate.SessionID, with jsonlPath under the
// presence rule (hookSessionRecordSet); a row with a session id keeps it. The
// session id is never a gate. gate.SessionStart and gate.Examined are not
// used here.
//
// Multi-row retention (SR-5.1/SR-5.2): when newState is `working` and a
// permission request of this Spawn still awaits an answer
// (OpenPermissionRequestsForSpawn; b.146 rule 9: not closed by find-missing's
// mark, and not acked and not answered at the pane, or, recorded before
// schema v7, undecided), the transition is
// held and the state is not written; the Spawn stays at check_permission
// until no request awaits an answer. The hold checks the gate first: a hook
// whose gate holds is applied (HookApplied.Applied) with UpsertNoChange and a
// no-op ad.spawn.state_transition (prior == new, SR-A-2.2), and its only
// write is clearing idle_since when it is set (any later hook clears it,
// b.146 problem 3); one whose gate does not hold reports its reason and
// emits nothing.
//
// Not applied: when the UPDATE matched no row, one read after it decides the
// reason (notAppliedReason): no row with the id yields HookApplied{} and no
// trail event (state-tracking hooks fail open per SRD §3.2, so a hook racing
// `delete` produces no visible error); a row with no pane_pid yields
// HookReasonNoPaneRecorded; any other row yields HookReasonPIDMismatch. A
// write that did not apply changes nothing: no state, no last_seen_at, no
// liveness-note clear.
//
// triggeringEventName is a free-form string identifying what caused this
// transition: the canonical Claude Code lifecycle event name (e.g.
// "PostToolUse").
//
// The outcome (SR-A-2.1): UpsertUpdated when the UPDATE affected a row;
// UpsertNoChange when it did not (gate, no row) or the retention guard held
// it; UpsertError, with a wrapped error and a zero HookApplied, on any SQL
// error. An ad.spawn.state_transition event is emitted after every applied
// write (including a same-state transition, SR-A-2.2); a trail-emit failure
// does not fail the store call (SR-A-3.2).
func (s *Store) ApplyHookTransitionResult(instanceID string, gate HookGate, newState string, softRefresh bool, triggeringEventName, jsonlPath string, jsonlPresent bool) (UpsertOutcome, HookApplied, error) {
	if !softRefresh && newState == StateWorking {
		held, outcome, applied, err := s.holdWorkingTransition(instanceID, gate, triggeringEventName)
		if held || err != nil {
			return outcome, applied, err
		}
	}
	if !softRefresh && newState == StateEnded {
		return s.applyEndedTransition(instanceID, gate, triggeringEventName, jsonlPath, jsonlPresent)
	}

	// Capture priorState for the trail before the UPDATE. SELECT and UPDATE
	// are separate non-transactional statements (transactions cause
	// SQLITE_BUSY under concurrent workloads, recovery.go): the gate and
	// every condition live in the UPDATE, so the read decides nothing but the
	// trail's prior_state. A row deleted between them makes the UPDATE match
	// nothing and the read after it reports no row.
	priorState, found, serr := s.selectPriorState(instanceID)
	if serr != nil {
		return UpsertError, HookApplied{}, serr
	}
	if !found {
		return UpsertNoChange, HookApplied{}, nil
	}

	var (
		set       string
		setArgs   []any
		trailNew  = newState
		errPrefix string
	)
	switch {
	case softRefresh:
		set = `last_seen_at = CURRENT_TIMESTAMP, ` + idleClear
		trailNew = priorState
		errPrefix = "store: soft refresh"
	default:
		// Non-terminal transitions clear ended_at. A resumed row reports in
		// from pending like a fresh spawn's (SR-22.3): resume's move to
		// pending already cleared ended_at, so the clear is then a no-op. It
		// stays for the row's own agent reaching a row find-missing marked
		// missing before its agent reported in (SR-22.9's gate admits no
		// other process), so a row a hook moves to a live state never keeps
		// its old ended_at. A target other than pending clears the launch
		// start in the same statement; a pending target (no hook passes one
		// today) leaves it (SR-5.2).
		set = `state = ?, last_seen_at = CURRENT_TIMESTAMP, ended_at = NULL, ` + idleClear
		if newState != StatePending {
			set += `, ` + launchStartClear
		}
		setArgs = []any{newState}
		errPrefix = "store: state transition"
	}
	return s.applyGatedHookWrite(instanceID, gate, set, setArgs, priorState, trailNew, softRefresh,
		triggeringEventName, jsonlPath, jsonlPresent, errPrefix)
}

// idleClear is the SET fragment every applied hook write carries except the
// main agent's idle-prompt Notification, which sets idle_since: any later
// hook clears it (b.146 problem 3).
const idleClear = `idle_since = NULL`

// idleSet is the idle-prompt Notification's SET fragment: the time it landed
// (b.146 problem 3).
const idleSet = `idle_since = CURRENT_TIMESTAMP`

// gatedHookUpdate returns the one UPDATE of a gated hook write on
// instanceID's spawns row, with its arguments: set (whose placeholders take
// setArgs) followed by what every applied hook writes — the liveness markers
// NULLed, the session record (hookSessionRecordSet with jsonlPath and
// jsonlPresent), the pane start time when NULL (hookPaneStartSet) and the
// row_version advance — guarded by the gate (hookGateSQL).
func gatedHookUpdate(instanceID string, gate HookGate, set string, setArgs []any, jsonlPath string, jsonlPresent bool) (string, []any) {
	recordSet, recordArgs := hookSessionRecordSet(gate, jsonlPath, jsonlPresent)
	q := `UPDATE spawns
	         SET ` + set + `,
	             liveness_unverified_since = NULL, liveness_note = NULL,
	             ` + recordSet + hookPaneStartSet + `,
	             ` + rowVersionAdvance + `
	       WHERE claude_instance_id = ? AND ` + hookGateSQL
	args := append(append([]any{}, setArgs...), recordArgs...)
	args = append(args, gate.ParentStart, instanceID)
	args = append(args, hookGateArgs(gate)...)
	return q, args
}

// applyGatedHookWrite runs gatedHookUpdate's statement for set and reports it
// as ApplyHookTransitionResult does: UpsertUpdated and one
// ad.spawn.state_transition (priorState to trailNew, soft_refresh as given)
// when it applied; UpsertNoChange with notAppliedReason's reason when it
// matched no row; UpsertError with a wrapped error (named by errPrefix) on a
// failure.
func (s *Store) applyGatedHookWrite(instanceID string, gate HookGate, set string, setArgs []any, priorState, trailNew string, softRefresh bool, triggeringEventName, jsonlPath string, jsonlPresent bool, errPrefix string) (UpsertOutcome, HookApplied, error) {
	q, args := gatedHookUpdate(instanceID, gate, set, setArgs, jsonlPath, jsonlPresent)
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return UpsertError, HookApplied{}, fmt.Errorf("%s: %w", errPrefix, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return UpsertError, HookApplied{}, fmt.Errorf("%s rows affected: %w", errPrefix, err)
	}
	if n == 0 {
		applied, err := s.hookNotApplied(instanceID, gate, errPrefix)
		if err != nil {
			return UpsertError, HookApplied{}, err
		}
		return UpsertNoChange, applied, nil
	}
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"claude_instance_id":    instanceID,
		"prior_state":           priorState,
		"new_state":             trailNew,
		"triggering_event_name": triggeringEventName,
		"soft_refresh":          softRefresh,
		"source":                "ad_spawn_store",
	})
	return UpsertUpdated, HookApplied{Applied: true}, nil
}

// endedTransitionSet is the ended transition's SET fragment (SRD §5.2): the
// state (its one placeholder), last_seen_at and ended_at to
// CURRENT_TIMESTAMP, and the launch-start and idle clears.
const endedTransitionSet = `state = ?, last_seen_at = CURRENT_TIMESTAMP,
       ended_at = CURRENT_TIMESTAMP, ` + launchStartClear + `, ` + idleClear

// errEndedNotApplied rolls applyEndedTransition's transaction back when its
// gated write matched no row; the caller then reads why.
var errEndedNotApplied = errors.New("store: ended transition not applied")

// applyEndedTransition is ApplyHookTransitionResult's move to ended (a
// terminal SessionEnd: the row's own agent exited) with the close of the
// row's open permission requests (b.146 rule 12), in ONE transaction, both or
// neither. The transaction is begun with BEGIN IMMEDIATE (inImmediateTx,
// waiting the store's busy timeout for the write lock, as every hook write
// does), so it holds the write lock before its first read. Inside it:
//
//  1. It reads the row's state, the trail's prior state. No row: not applied.
//  2. The gated write (gatedHookUpdate with endedTransitionSet: the gate in
//     its own WHERE, SR-22.9), as every ordinary hook write: state ended,
//     last_seen_at and ended_at to CURRENT_TIMESTAMP, launch_started_at,
//     idle_since and the liveness markers NULLed, the session record and the
//     pane start time when NULL, row_version advanced by one. A gate that
//     does not hold matches no row: not applied.
//  3. The close (closeOrphanedRequests, as find-missing's mark closes a
//     missing row's): every request of the row that still awaits an answer
//     (awaitingAnswerSQL) gets closed_at, so none awaits an answer once the
//     row is resumed (none holds its moves to working, shows on get and list,
//     refuses send-keys or blocks the check_permission repair); an undecided
//     one is also denied with decision_reason ended, so a relay hook still
//     polling for it reads a fail-closed deny; a decided one whose relay hook
//     has not acked its verdict keeps it.
//
// Not applied rolls back and is reported as ApplyHookTransitionResult's (one
// read after the rollback, hookNotApplied). Any other failure rolls the whole
// transaction back, the row and its requests as they were, and is returned
// wrapped with a zero HookApplied and UpsertError. After the commit, and only
// then, it emits, in request-id order, one ad.row_mutation.committed (writer
// hook, decision_reason ended) per request it denied, then the
// ad.spawn.state_transition (the prior state to ended), fail-open.
func (s *Store) applyEndedTransition(instanceID string, gate HookGate, triggeringEventName, jsonlPath string, jsonlPresent bool) (UpsertOutcome, HookApplied, error) {
	const errPrefix = "store: ended transition"
	q, args := gatedHookUpdate(instanceID, gate, endedTransitionSet, []any{StateEnded}, jsonlPath, jsonlPresent)
	var (
		prior  string
		closed []closedRequest
	)
	err := s.inImmediateTx(func(ctx context.Context, conn *sql.Conn) error {
		err := conn.QueryRowContext(ctx, `SELECT state FROM spawns WHERE claude_instance_id = ?`, instanceID).Scan(&prior)
		if errors.Is(err, sql.ErrNoRows) {
			return errEndedNotApplied
		}
		if err != nil {
			return fmt.Errorf("prior state: %w", err)
		}
		res, err := conn.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("rows affected: %w", err)
		} else if n == 0 {
			return errEndedNotApplied
		}
		closed, err = closeOrphanedRequests(ctx, conn, instanceID, DecisionReasonEnded)
		if err != nil {
			return fmt.Errorf("close permission requests: %w", err)
		}
		return nil
	})
	if errors.Is(err, errEndedNotApplied) {
		applied, err := s.hookNotApplied(instanceID, gate, errPrefix)
		if err != nil {
			return UpsertError, HookApplied{}, err
		}
		return UpsertNoChange, applied, nil
	}
	if err != nil {
		return UpsertError, HookApplied{}, fmt.Errorf("%s: %w", errPrefix, err)
	}

	for _, c := range closed {
		c.emitDeny(instanceID, DecisionReasonEnded, WriterProcessHook)
	}
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"claude_instance_id":    instanceID,
		"prior_state":           prior,
		"new_state":             StateEnded,
		"triggering_event_name": triggeringEventName,
		"soft_refresh":          false,
		"source":                "ad_spawn_store",
	})
	return UpsertUpdated, HookApplied{Applied: true}, nil
}

// ApplyHookWaitingIfWorking is the gated write of the main agent's
// idle-prompt Notification (b.svb; b.146 problem 3). Claude Code sends that
// Notification only while its main agent is idle at the prompt, so a row
// still working then was left there by a hook with no Stop after it (a
// background fork's PreToolUse after the turn's Stop): the row returns to
// waiting. So does a row with relay_mode on in check_permission none of whose
// permission requests still awaits an answer (OpenPermissionRequestsForSpawn's
// rule): the agent's turn ended after its last request was answered, and its
// Stop was lost (b.146 problem 3). A row in any other state, a row with the
// relay off in check_permission (no request records its dialog), or one with
// a request that still awaits an answer, gets a soft refresh. Idle is the main agent's alone: before Claude Code 2.1.288
// (anthropics/claude-code#93672) the Notification also fires while a
// background subagent is still running, and a row that subagent's hook moved
// to working then reads waiting, its tool possibly still running, until the
// subagent's next tool hook.
//
// Every applied write of this Notification, a soft refresh included, sets
// idle_since to the current time, and any later hook clears it: find-missing's
// repair of a stale check_permission row picks waiting when it is set and
// working when it is not (b.146 rule 9, problem 3).
//
// Each return to waiting is one statement whose WHERE carries the state
// condition (state working, or state check_permission with relay_mode on and
// no request that still awaits an answer) with the gate (hookGateSQL), so it
// applies only to
// such a row, for the row's own agent, at the moment it runs; no read
// decides it. The working statement runs first, then the check_permission
// one. Each writes what a non-terminal ApplyHookTransitionResult transition
// writes (the state; last_seen_at bumped; ended_at, launch_started_at and the
// liveness markers NULLed; the session record; the pane start time when NULL;
// row_version advanced by one), sets idle_since, and emits
// ad.spawn.state_transition from its state to waiting with soft_refresh
// false. The retention hold does not apply: it holds transitions to working
// only.
//
// When neither statement matches a row (the row is in another state, the gate
// does not hold, or no row has the id), the write is a soft refresh that also
// sets idle_since, which tells those apart and reports and emits exactly as
// ApplyHookTransitionResult's soft refresh does. A row that becomes working
// between the statements gets the soft refresh: the Notification is then
// ordered before the hook that moved the row.
//
// The outcome and errors are ApplyHookTransitionResult's.
func (s *Store) ApplyHookWaitingIfWorking(instanceID string, gate HookGate, triggeringEventName, jsonlPath string, jsonlPresent bool) (UpsertOutcome, HookApplied, error) {
	const errPrefix = "store: waiting-if-working transition"
	set := `state = ?, last_seen_at = CURRENT_TIMESTAMP, ended_at = NULL, ` + launchStartClear + `, ` + idleSet
	for _, from := range []struct{ state, cond string }{
		{StateWorking, `state = '` + StateWorking + `'`},
		{StateCheckPermission, `state = '` + StateCheckPermission + `' AND relay_mode = 'on' AND NOT EXISTS (
		     SELECT 1 FROM permission_requests pr
		      WHERE pr.claude_instance_id = spawns.claude_instance_id AND ` + awaitingAnswerSQL + `)`},
	} {
		q, args := gatedHookUpdate(instanceID, gate, set, []any{StateWaiting}, jsonlPath, jsonlPresent)
		moved, err := s.execGuarded(q+` AND `+from.cond, args, errPrefix)
		if err != nil {
			return UpsertError, HookApplied{}, err
		}
		if moved {
			_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
				"claude_instance_id":    instanceID,
				"prior_state":           from.state,
				"new_state":             StateWaiting,
				"triggering_event_name": triggeringEventName,
				"soft_refresh":          false,
				"source":                "ad_spawn_store",
			})
			return UpsertUpdated, HookApplied{Applied: true}, nil
		}
	}
	priorState, found, err := s.selectPriorState(instanceID)
	if err != nil {
		return UpsertError, HookApplied{}, err
	}
	if !found {
		return UpsertNoChange, HookApplied{}, nil
	}
	return s.applyGatedHookWrite(instanceID, gate, `last_seen_at = CURRENT_TIMESTAMP, `+idleSet, nil,
		priorState, priorState, true, triggeringEventName, jsonlPath, jsonlPresent, "store: idle soft refresh")
}

// holdWorkingTransition is the multi-row retention guard of a working
// transition (ApplyHookTransitionResult). held is false when no permission
// request still awaits an answer (OpenPermissionRequestsForSpawn), and the
// caller writes the transition. When one does, the transition is held and the
// state is not written: the gate is read first (readHookGateRow), and only a
// hook whose gate holds gets its one write, idle_since cleared when it is set
// (clearIdleSinceSQL; any later hook clears it, b.146 problem 3), the no-op
// ad.spawn.state_transition (prior == new, SR-A-2.2) and an applied result; a
// missing row or a gate that does not hold reports notAppliedReason and emits
// nothing.
func (s *Store) holdWorkingTransition(instanceID string, gate HookGate, triggeringEventName string) (held bool, outcome UpsertOutcome, applied HookApplied, err error) {
	openRows, err := s.OpenPermissionRequestsForSpawn(instanceID)
	if err != nil {
		return false, UpsertError, HookApplied{}, fmt.Errorf("store: working transition open-row check: %w", err)
	}
	if len(openRows) == 0 {
		return false, "", HookApplied{}, nil
	}
	r, err := s.readHookGateRow(instanceID, gate, "store: working transition hold")
	if err != nil {
		return true, UpsertError, HookApplied{}, err
	}
	if !r.found || !r.gateMatches {
		return true, UpsertNoChange, notAppliedReason(r), nil
	}
	if _, err := s.execGuarded(clearIdleSinceSQL, append([]any{instanceID}, hookGateArgs(gate)...),
		"store: working transition hold idle clear"); err != nil {
		return true, UpsertError, HookApplied{}, err
	}
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"claude_instance_id":    instanceID,
		"prior_state":           r.state,
		"new_state":             r.state,
		"triggering_event_name": triggeringEventName,
		"soft_refresh":          false,
		"source":                "ad_spawn_store",
	})
	return true, UpsertNoChange, HookApplied{Applied: true}, nil
}

// clearIdleSinceSQL is a held working transition's one write: idle_since
// NULLed, with the version advance, only on a row that has it set and for
// which the hook's gate holds.
const clearIdleSinceSQL = `UPDATE spawns
    SET ` + idleClear + `, ` + rowVersionAdvance + `
  WHERE claude_instance_id = ? AND idle_since IS NOT NULL AND ` + hookGateSQL

// sessionStartPrior is what a SessionStart write replaces, read at the
// snapshot the caller examined: the prior state (for the trail) and the
// outgoing transcript path and life (for the rotation archive).
type sessionStartPrior struct {
	state     string
	jsonlPath string
	life      int64
}

// RecordSessionStartIdentity is the gated SessionStart write (SR-22.9,
// SR-5.3): the row's own agent reporting in. It sets state waiting whatever
// the prior state, and in the same statement records the payload's Claude
// session id (gate.SessionID; "" keeps the column), the transcript path under
// the presence rule, pid and proc_starttime as the hook's parent
// (gate.ParentPID, gate.ParentStart: the pane process, SR-3.8), pane_starttime
// when NULL; it bumps last_seen_at, clears ended_at, both liveness columns
// (SessionStart is proof of life, SR-8.2), idle_since (any hook clears it,
// b.146 problem 3) and launch_started_at, and advances
// row_version by exactly one (SR-5.2).
//
// The statement's WHERE carries the gate (hookGateSQL) and the snapshot
// condition on gate.Examined (snapshotMatchSQL), so the write applies only
// when the hook's parent is the row's recorded pane process and the row is
// still the one the caller examined; no write lands between a check and it.
//
// The transcript path (b.v2c AC1): the hook stats the path first. Reported
// and present sets it; reported but not yet on disk sets NULL so the row never
// asserts a dead pointer (find-missing heals it, AC3); not reported keeps the
// column.
//
// The rotation archive (b.v2c AC6, SR-5.9) runs in the same transaction, only
// when the write applied: when the examined session id is non-empty and
// differs from a non-empty gate.SessionID, the outgoing pair is archived
// through upsertSessionHistoryEntry on the transaction, in the life the row
// had. The outgoing session id is gate.Examined's; its path and life come from
// a read taken before the transaction under the same snapshot condition, so
// the snapshot pins them (every write advances row_version). The archive is
// fail-open: a history error still commits the SessionStart write and emits
// ad.session.archive_failed. After the commit the write emits
// ad.spawn.state_transition (prior state to waiting) and, when it archived,
// ad.session.archived. The transaction's first statement is the write, so it
// never upgrades a read lock (the SQLITE_BUSY note in recovery.go).
//
// Not applied: nothing is written and nothing is emitted. One read after the
// statement decides the result: no row yields HookApplied{}; a row for which
// the gate holds (so only the snapshot changed) yields HookApplied{} with
// snapshotChanged true, and the caller may re-read and retry; otherwise
// notAppliedReason's HookReasonNoPaneRecorded or HookReasonPIDMismatch. A
// driver error returns a wrapped error with a zero HookApplied.
func (s *Store) RecordSessionStartIdentity(instanceID string, gate HookGate, jsonlPath string, jsonlPresent bool) (applied HookApplied, snapshotChanged bool, err error) {
	const errPrefix = "store: record session start identity"

	prior, pinned, err := s.readSessionStartPrior(instanceID, gate.Examined, errPrefix)
	if err != nil {
		return HookApplied{}, false, err
	}
	if !pinned {
		// The row is gone or no longer holds the examined snapshot, so the
		// gated statement cannot match it.
		return s.sessionStartNotApplied(instanceID, gate, errPrefix)
	}

	// jsonl_path: bind the path to SET it, "" with the explicit NULL form to
	// clear it, NULL through COALESCE to keep it.
	jsonlSet, jsonlArgs := `jsonl_path = COALESCE(?, jsonl_path)`, []any{nil}
	switch {
	case jsonlPath != "" && jsonlPresent:
		jsonlArgs = []any{jsonlPath}
	case jsonlPath != "":
		jsonlSet, jsonlArgs = `jsonl_path = NULL`, nil
	}
	q := `UPDATE spawns
	         SET state                     = ?,
	             last_seen_at              = CURRENT_TIMESTAMP,
	             ended_at                  = NULL,
	             claude_session_id         = COALESCE(?, claude_session_id),
	             ` + jsonlSet + `,
	             pid                       = ?,
	             proc_starttime            = ?,
	             liveness_unverified_since = NULL,
	             liveness_note             = NULL,
	             ` + idleClear + `,
	             ` + hookPaneStartSet + `,
	             ` + launchStartClear + `,
	             ` + rowVersionAdvance + `
	       WHERE claude_instance_id = ? AND ` + hookGateSQL + ` AND ` + snapshotMatchSQL
	args := append([]any{StateWaiting, nullableStringArg(gate.SessionID)}, jsonlArgs...)
	args = append(args, positiveIntArg(gate.ParentPID), nullableStringArg(gate.ParentStart),
		gate.ParentStart, instanceID)
	args = append(args, hookGateArgs(gate)...)
	args = append(args, snapshotMatchArgs(gate.Examined)...)

	tx, err := s.db.Begin()
	if err != nil {
		return HookApplied{}, false, fmt.Errorf("%s: begin: %w", errPrefix, err)
	}
	res, err := tx.Exec(q, args...)
	if err != nil {
		_ = tx.Rollback()
		return HookApplied{}, false, fmt.Errorf("%s: %w", errPrefix, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return HookApplied{}, false, fmt.Errorf("%s rows affected: %w", errPrefix, err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.sessionStartNotApplied(instanceID, gate, errPrefix)
	}

	outgoing := gate.Examined.ClaudeSessionID
	archive := outgoing != "" && gate.SessionID != "" && outgoing != gate.SessionID
	var archiveErr error
	if archive {
		archiveErr = upsertSessionHistoryEntry(tx, instanceID, outgoing, prior.jsonlPath, prior.life)
	}
	if err := tx.Commit(); err != nil {
		return HookApplied{}, false, fmt.Errorf("%s: commit: %w", errPrefix, err)
	}

	event := gate.Event
	if event == "" {
		event = "SessionStart"
	}
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"claude_instance_id":    instanceID,
		"prior_state":           prior.state,
		"new_state":             StateWaiting,
		"triggering_event_name": event,
		"soft_refresh":          false,
		"source":                "ad_spawn_store",
	})
	switch {
	case archiveErr != nil:
		_ = trail.Emit(context.Background(), "ad.session.archive_failed", map[string]any{
			"claude_instance_id": instanceID,
			"claude_session_id":  outgoing,
			"error":              archiveErr.Error(),
			"source":             "ad_spawn_store",
		})
	case archive:
		_ = trail.Emit(context.Background(), "ad.session.archived", map[string]any{
			"claude_instance_id": instanceID,
			"prior_session_id":   outgoing,
			"new_session_id":     gate.SessionID,
			"source":             "ad_spawn_store",
		})
	}
	return HookApplied{Applied: true}, false, nil
}

// readSessionStartPrior reads the prior state, transcript path and life of
// instanceID's row, only while the row holds the examined snapshot (SR-5.3);
// pinned is false when no row has the id or the row has changed since.
func (s *Store) readSessionStartPrior(instanceID string, examined RowSnapshot, errPrefix string) (prior sessionStartPrior, pinned bool, err error) {
	args := append([]any{instanceID}, snapshotMatchArgs(examined)...)
	err = s.db.QueryRow(`SELECT state, COALESCE(jsonl_path, ''), COALESCE(life_number, 0)
	                       FROM spawns
	                      WHERE claude_instance_id = ? AND `+snapshotMatchSQL, args...,
	).Scan(&prior.state, &prior.jsonlPath, &prior.life)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionStartPrior{}, false, nil
	}
	if err != nil {
		return sessionStartPrior{}, false, fmt.Errorf("%s: prior read: %w", errPrefix, err)
	}
	return prior, true, nil
}

// sessionStartNotApplied is RecordSessionStartIdentity's result when its
// statement matched no row, from one read of the row: no row yields
// HookApplied{}; a gate that holds means only the snapshot changed
// (snapshotChanged); otherwise notAppliedReason.
func (s *Store) sessionStartNotApplied(instanceID string, gate HookGate, errPrefix string) (HookApplied, bool, error) {
	r, err := s.readHookGateRow(instanceID, gate, errPrefix)
	if err != nil {
		return HookApplied{}, false, err
	}
	if r.found && r.gateMatches {
		return HookApplied{}, true, nil
	}
	return notAppliedReason(r), false, nil
}
