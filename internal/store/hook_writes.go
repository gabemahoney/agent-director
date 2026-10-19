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
// permission request of this Spawn holds the row (holdsRowSQL: it still
// awaits an answer, b.146 rule 9: not closed by a close of the Spawn's
// requests, and not acked and not answered at the pane, or, recorded before
// schema v7, undecided; and Claude Code has not proven its dialog gone,
// b.omt), the transition is held and the state is not written; the Spawn
// stays at check_permission until no request holds it. A request proven gone
// (proven_gone_at set: its tool ran, its agent's turn ended after it, or its
// agent is gone; b.146 step 2c) does not hold it, though it may still await
// an answer in agent-director's records (one a person answered at the pane).
// A read (holdWorkingTransition) picks the branch, and the UPDATE to working
// carries the same condition in its own WHERE (noRequestHoldsRowSQL), so a
// request recorded between the two (InsertRelayRequest, which moves the row
// to check_permission in its own transaction) is never overwritten with
// working (b.36h); a move to working that loses that race is held too.
// Verified Cursor waiting transitions use the same write-time hold guard,
// including Stop/Interrupt. A held Cursor report applies a gated soft refresh
// (session/path, liveness, activity and row version) with state/settle kept;
// Claude/Codex Stop and Interrupt retain their ordinary waiting transitions. A
// held transition checks the gate first (reportWorkingHeld): a hook whose
// gate holds is applied (HookApplied.Applied) with UpsertNoChange and a no-op
// ad.spawn.state_transition (prior == new, SR-A-2.2), and its only write is
// clearing idle_since when it is set (any later hook clears it, b.146
// problem 3); one whose gate does not hold reports its reason and emits
// nothing.
//
// Not applied: when the UPDATE matched no row, one read after it decides the
// reason (notAppliedReason): no row with the id yields HookApplied{} and no
// trail event (state-tracking hooks fail open per SRD §3.2, so a hook racing
// `delete` produces no visible error); a row with no pane_pid yields
// HookReasonNoPaneRecorded; any other row yields HookReasonPIDMismatch,
// except that a move to working on a row for which the gate holds at that
// read is reported held, as above. Usually a request recorded after the
// hold's read stopped its UPDATE; rarely the gate came to hold only after the
// UPDATE (e.g. the pane recorded between the two statements). The read does
// not tell the two apart, and neither writes the state. A write that did not
// apply changes nothing: no state, no last_seen_at, no liveness-note clear.
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
	working := !softRefresh && newState == StateWorking
	// Verified Cursor lifecycle reports cannot supersede a genuine dialog hold.
	// Other agents retain their deliberate Stop/Interrupt move to waiting.
	guarded := working || (!softRefresh && newState == StateWaiting && gate.Cursor != nil)
	if working && gate.Cursor == nil {
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
		// today) leaves it (SR-5.2). The state is written with its settle
		// columns (hookWriteStateSet, b.3ie): decided in this one statement,
		// never from priorState, which feeds the trail only.
		stateSet, stateArgs := s.hookWriteStateSet(newState)
		set = stateSet + `, last_seen_at = CURRENT_TIMESTAMP, ended_at = NULL, ` + idleClear
		if newState != StatePending {
			set += `, ` + launchStartClear
		}
		setArgs = stateArgs
		errPrefix = "store: state transition"
	}
	// Test seam (Store.hookBeforeUpdateForTest; nil in production): runs
	// between the read above and the UPDATE.
	if s.hookBeforeUpdateForTest != nil {
		s.hookBeforeUpdateForTest()
	}
	// A move to working keeps the settle columns in its SET (b.3ie) and adds
	// the hold condition to its WHERE (applyWorkingWrite, b.36h).
	if guarded {
		return s.applyWorkingWrite(instanceID, gate, set, setArgs, priorState, newState, triggeringEventName,
			jsonlPath, jsonlPresent, errPrefix)
	}
	return s.applyGatedHookWrite(instanceID, gate, set, setArgs, priorState, trailNew, softRefresh,
		triggeringEventName, jsonlPath, jsonlPresent, errPrefix)
}

// ApplyHookEndReport is the gated write of a Codex SessionEnd from the verified
// agent process (SR-6.5, MC-1): the end report. It is the soft refresh
// ApplyHookTransitionResult makes (state kept; last_seen_at bumped;
// idle_since and the liveness markers NULLed; the session record under the
// presence rule; the pane start time when NULL; row_version advanced by one;
// launch_started_at and the settle columns untouched) with one more column,
// end_reported_at, set to the statement's clock in milliseconds. The gate and
// every report are applyGatedHookWrite's: an applied write is UpsertUpdated and
// emits exactly one ad.spawn.state_transition (prior state equal to new state,
// soft_refresh true); a gate that does not hold changes nothing and reports
// HookReasonPIDMismatch or HookReasonNoPaneRecorded; no row yields
// HookApplied{} and no trail event. The report is recorded for any session id:
// gate.SessionID follows the ordinary session-record rule, never a condition.
//
// It is a soft refresh and does not prove the agent gone, so it neither closes
// the row's open permission requests nor the subagent records
// (closeSubagentsAgentGone is the five ending writes' alone); the move to
// ended is a different write.
func (s *Store) ApplyHookEndReport(instanceID string, gate HookGate, triggeringEventName, jsonlPath string, jsonlPresent bool) (UpsertOutcome, HookApplied, error) {
	priorState, found, err := s.selectPriorState(instanceID)
	if err != nil {
		return UpsertError, HookApplied{}, err
	}
	if !found {
		return UpsertNoChange, HookApplied{}, nil
	}
	if s.hookBeforeUpdateForTest != nil {
		s.hookBeforeUpdateForTest()
	}
	return s.applyGatedHookWrite(instanceID, gate,
		`last_seen_at = CURRENT_TIMESTAMP, `+idleClear+`, end_reported_at = `+nowMsSQL, nil,
		priorState, priorState, true, triggeringEventName, jsonlPath, jsonlPresent, "store: end report")
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
	       WHERE instance_id = ? AND ` + hookGateSQL
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
	res, err := s.execFamily(q, args...)
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
		"instance_id":           instanceID,
		"prior_state":           priorState,
		"new_state":             trailNew,
		"triggering_event_name": triggeringEventName,
		"soft_refresh":          softRefresh,
		"source":                "ad_spawn_store",
	})
	return UpsertUpdated, HookApplied{Applied: true}, nil
}

// applyWorkingWrite is applyGatedHookWrite for a move to working, or a
// verified Cursor move to waiting, that
// holdWorkingTransition's read did not hold: gatedHookUpdate's statement for
// set, with noRequestHoldsRowSQL joined to its WHERE, so a request recorded
// after that read (InsertRelayRequest's transaction, which moves the row to
// check_permission) still holds the row and is never overwritten with working
// (b.36h). Applied: UpsertUpdated and one ad.spawn.state_transition from
// priorState to newState. Matched no row: a gated soft refresh on Cursor;
// otherwise reportWorkingHeld, whose gate read
// tells a write that did not apply (no row, or a gate that does not hold:
// notAppliedReason, nothing emitted) from one reported held (the gate holds:
// usually a request held the row at the UPDATE, rarely the gate came to hold
// only after it, e.g. the pane recorded in between). A failure: UpsertError
// with an error wrapped by errPrefix.
func (s *Store) applyWorkingWrite(instanceID string, gate HookGate, set string, setArgs []any, priorState, newState, triggeringEventName, jsonlPath string, jsonlPresent bool, errPrefix string) (UpsertOutcome, HookApplied, error) {
	q, args := gatedHookUpdate(instanceID, gate, set, setArgs, jsonlPath, jsonlPresent)
	moved, err := s.execGuarded(q+` AND `+noRequestHoldsRowSQL, args, errPrefix)
	if err != nil {
		return UpsertError, HookApplied{}, err
	}
	if !moved {
		if gate.Cursor != nil {
			// A held report still records verified activity and advances the
			// version, so the gateway can distinguish acceptance from an
			// ignored hook. State and settle columns remain unchanged.
			prior, found, err := s.selectPriorState(instanceID)
			if err != nil {
				return UpsertError, HookApplied{}, err
			}
			if !found {
				return UpsertNoChange, HookApplied{}, nil
			}
			return s.applyGatedHookWrite(instanceID, gate,
				`last_seen_at = CURRENT_TIMESTAMP, `+idleClear, nil,
				prior, prior, true, triggeringEventName, jsonlPath, jsonlPresent, errPrefix)
		}
		return s.reportWorkingHeld(instanceID, gate, triggeringEventName)
	}
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"instance_id":           instanceID,
		"prior_state":           priorState,
		"new_state":             newState,
		"triggering_event_name": triggeringEventName,
		"soft_refresh":          false,
		"source":                "ad_spawn_store",
	})
	return UpsertUpdated, HookApplied{Applied: true}, nil
}

// noRequestHoldsRowSQL is the WHERE fragment, on a spawns row, that holds
// while none of the row's permission requests holds it (holdsRowSQL). Two
// hook writes join it to their own UPDATE's WHERE: the move to working
// (applyWorkingWrite; b.36h) and the idle-prompt Notification's move from
// check_permission to waiting (ApplyHookWaitingIfWorking). It has no
// placeholders.
const noRequestHoldsRowSQL = `NOT EXISTS (SELECT 1 FROM permission_requests pr
     WHERE pr.instance_id = spawns.instance_id AND ` + holdsRowSQL + `)`

// endedTransitionSet is the ended transition's SET fragment (SRD §5.2): the
// state as an immediate write (immediateStateSet, its placeholders
// immediateStateArgs(StateEnded); b.3ie: a terminal state reports at once),
// last_seen_at and ended_at to CURRENT_TIMESTAMP, and the launch-start and
// idle clears.
const endedTransitionSet = immediateStateSet + `, last_seen_at = CURRENT_TIMESTAMP,
       ended_at = CURRENT_TIMESTAMP, ` + launchStartClear + `, ` + idleClear

// errEndedNotApplied rolls applyEndedTransition's transaction back when its
// gated write matched no row; the caller then reads why.
var errEndedNotApplied = errors.New("store: ended transition not applied")

// applyEndedTransition is ApplyHookTransitionResult's move to ended (a
// terminal SessionEnd: the row's own agent exited) with the close of the
// row's open permission requests (b.146 rule 12) and of its running subagent
// entries and claimable launches (SR-8.1), in ONE transaction, all or
// nothing. The transaction is begun with BEGIN IMMEDIATE (inImmediateTx,
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
//     has not acked its verdict keeps it. Every request of the row not yet
//     proven gone, closed now or before, is proven gone, agent_gone (b.146
//     step 2c).
//  4. The subagent close (closeSubagentsAgentGone, SR-8.1): every running
//     subagent entry of the row, of any launch, turns lost with close_reason
//     agent_gone, and every claimable launch of the row is deleted, whatever
//     the row's subagent_tracking flag. Its instant is the one store-clock
//     read (storeNowMillis, at millisecond precision) taken once inside this
//     transaction after the write applied and shared with step 3's closed_at:
//     the entries' lost_at and activity_at equal the requests' closed_at. A
//     not-applied write reads no clock and closes nothing.
//
// Not applied rolls back and is reported as ApplyHookTransitionResult's (one
// read after the rollback, hookNotApplied). Any other failure, a failed
// subagent close included, rolls the whole transaction back, the row, its
// requests, its entries and its claimable launches as they were, and is
// returned wrapped with a zero HookApplied and UpsertError (the hook still
// exits 0). After the commit, and only
// then, it emits, in request-id order, one ad.row_mutation.committed (writer
// hook, decision_reason ended) per request it denied, then the
// ad.spawn.state_transition (the prior state to ended), fail-open.
func (s *Store) applyEndedTransition(instanceID string, gate HookGate, triggeringEventName, jsonlPath string, jsonlPresent bool) (UpsertOutcome, HookApplied, error) {
	const errPrefix = "store: ended transition"
	q, args := gatedHookUpdate(instanceID, gate, endedTransitionSet, immediateStateArgs(StateEnded), jsonlPath, jsonlPresent)
	var (
		prior  string
		closed []closedRequest
	)
	err := s.inImmediateTx(func(ctx context.Context, conn *sql.Conn) error {
		err := conn.QueryRowContext(ctx, `SELECT state FROM spawns WHERE instance_id = ?`, instanceID).Scan(&prior)
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
		// One store-clock read, inside the transaction after the write lock and
		// only once the write applied, shared by the request close's closed_at
		// and the subagent close's lost_at (SR-3.5).
		at := storeNowMillis()
		closed, err = closeOrphanedRequests(ctx, conn, instanceID, DecisionReasonEnded, at)
		if err != nil {
			return fmt.Errorf("close permission requests: %w", err)
		}
		return closeSubagentsAgentGone(connQuerier{ctx: ctx, conn: conn}, instanceID, at)
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
		"instance_id":           instanceID,
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
// permission requests holds it (holdsRowSQL: still awaits an answer and is
// not proven gone; b.omt): the agent's turn ended after its last request was
// answered, and its Stop was lost (b.146 problem 3). The hook's own turn_end
// proof runs before this write (the hook package's proveRequestsGone), so a
// request recorded before the hook started, answered at the pane by a person
// and so still awaiting an answer in agent-director's records, is proven gone
// by then and holds nothing. A row in any other state, a row with the relay
// off in check_permission (no request records its dialog), or one with a
// request that holds it, gets a soft refresh. Idle is the main agent's alone: before Claude Code 2.1.288
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
// no request that holds the row) with the gate (hookGateSQL), so it
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
	// Each statement writes waiting with its settle columns (b.3ie); its
	// WHERE already guarantees a change of value.
	stateSet, stateArgs := s.hookWriteStateSet(StateWaiting)
	set := stateSet + `, last_seen_at = CURRENT_TIMESTAMP, ended_at = NULL, ` + launchStartClear + `, ` + idleSet
	for _, from := range []struct{ state, cond string }{
		{StateWorking, `state = '` + StateWorking + `'`},
		{StateCheckPermission, `state = '` + StateCheckPermission + `' AND relay_mode = 'on' AND ` + noRequestHoldsRowSQL},
	} {
		q, args := gatedHookUpdate(instanceID, gate, set, stateArgs, jsonlPath, jsonlPresent)
		moved, err := s.execGuarded(q+` AND `+from.cond, args, errPrefix)
		if err != nil {
			return UpsertError, HookApplied{}, err
		}
		if moved {
			_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
				"instance_id":           instanceID,
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
// transition (ApplyHookTransitionResult): its read picks the branch. held is
// false when no permission request of the Spawn holds the row at the read
// (holdingRequestSQL: none still awaits an answer, or every one that does is
// proven gone; holdsRowSQL, b.omt), and the caller writes the transition
// (applyWorkingWrite). The read alone does not keep the row from working: a
// request recorded after it would be overwritten, so the caller's UPDATE
// carries the same condition (noRequestHoldsRowSQL; b.36h). When a request
// holds the row at the read, the transition is held and the state is not
// written (reportWorkingHeld).
//
// A request proven gone is left out because Claude Code itself has shown its
// dialog is no longer up (its tool ran, its agent's turn ended after it, or
// its agent is gone; b.146 step 2c), so a working write after it cannot put
// the row at working while that dialog may be on screen. Without that, a
// request a person answered at the pane, which nothing records, would hold
// every later move to working for the rest of the Spawn's life.
func (s *Store) holdWorkingTransition(instanceID string, gate HookGate, triggeringEventName string) (held bool, outcome UpsertOutcome, applied HookApplied, err error) {
	var holding bool
	if err := s.db.QueryRow(holdingRequestSQL, instanceID).Scan(&holding); err != nil {
		return false, UpsertError, HookApplied{}, fmt.Errorf("store: working transition hold check: %w", err)
	}
	if !holding {
		return false, "", HookApplied{}, nil
	}
	outcome, applied, err = s.reportWorkingHeld(instanceID, gate, triggeringEventName)
	return true, outcome, applied, err
}

// reportWorkingHeld is the report of a held working transition: one that
// holdWorkingTransition's read held, or one whose UPDATE (applyWorkingWrite)
// matched no row while the gate holds at this function's read: usually a
// request recorded after the hold's read stopped the UPDATE, rarely the gate
// came to hold only after it (e.g. the pane recorded between the two
// statements). The state is not written: the gate is read first
// (readHookGateRow), and only a hook whose gate holds gets its one write,
// idle_since cleared when it is set (clearIdleSinceSQL; any later hook clears
// it, b.146 problem 3), the no-op ad.spawn.state_transition (prior == new ==
// the state the gate read saw, soft_refresh false, SR-A-2.2) and
// UpsertNoChange with an applied result; a missing row or a gate that does
// not hold reports notAppliedReason and emits nothing.
func (s *Store) reportWorkingHeld(instanceID string, gate HookGate, triggeringEventName string) (UpsertOutcome, HookApplied, error) {
	r, err := s.readHookGateRow(instanceID, gate, "store: working transition hold")
	if err != nil {
		return UpsertError, HookApplied{}, err
	}
	if !r.found || !r.gateMatches {
		return UpsertNoChange, notAppliedReason(r), nil
	}
	if _, err := s.execGuarded(clearIdleSinceSQL, append([]any{instanceID}, hookGateArgs(gate)...),
		"store: working transition hold idle clear"); err != nil {
		return UpsertError, HookApplied{}, err
	}
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"instance_id":           instanceID,
		"prior_state":           r.state,
		"new_state":             r.state,
		"triggering_event_name": triggeringEventName,
		"soft_refresh":          false,
		"source":                "ad_spawn_store",
	})
	return UpsertNoChange, HookApplied{Applied: true}, nil
}

// holdingRequestSQL is holdWorkingTransition's read: whether a permission
// request of one Spawn (its one placeholder) holds the row (holdsRowSQL),
// scanned as a bool. It only picks the branch: the UPDATE to working carries
// the same condition (noRequestHoldsRowSQL), so a request recorded after the
// read still holds the row (b.36h).
const holdingRequestSQL = `SELECT EXISTS (SELECT 1 FROM permission_requests pr
     WHERE pr.instance_id = ? AND ` + holdsRowSQL + `)`

// clearIdleSinceSQL is a held working transition's one write: idle_since
// NULLed, with the version advance, only on a row that has it set and for
// which the hook's gate holds.
const clearIdleSinceSQL = `UPDATE spawns
    SET ` + idleClear + `, ` + rowVersionAdvance + `
  WHERE instance_id = ? AND idle_since IS NOT NULL AND ` + hookGateSQL

// sessionStartPrior is what a SessionStart write replaces, read at the
// snapshot the caller examined: the prior state (for the trail) and the
// outgoing transcript path and life (for the rotation archive).
type sessionStartPrior struct {
	agent       string
	launchToken string
	state       string
	jsonlPath   string
	life        int64
}

// RecordSessionStartIdentity is the gated SessionStart write (SR-22.9,
// SR-5.3): the row's own agent reporting in. It sets state waiting whatever
// the prior state, as a hook write (its settle columns by hookWriteStateSet,
// b.3ie: the move settles, out of pending, missing or ended too), and in the
// same statement records the payload's Claude
// session id (gate.SessionID; "" keeps the column), the transcript path under
// the presence rule, pid and proc_starttime as the hook's parent
// (gate.ParentPID, gate.ParentStart: the pane process, SR-3.8), pane_starttime
// when NULL; it bumps last_seen_at, clears ended_at, both liveness columns
// (SessionStart is proof of life, SR-8.2), idle_since (any hook clears it,
// b.146 problem 3), launch_started_at and end_reported_at (a Codex row's
// SessionEnd report, which a later SessionStart from the agent supersedes;
// SR-6.5), and advances row_version by exactly one (SR-5.2).
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
	// waiting with its settle columns (b.3ie): a hook write, so a move out of
	// any prior state, pending, missing and ended included, settles.
	stateSet, stateArgs := s.hookWriteStateSet(StateWaiting)
	q := `UPDATE spawns
	         SET ` + stateSet + `,
	             last_seen_at              = CURRENT_TIMESTAMP,
	             ended_at                  = NULL,
	             session_id                = COALESCE(?, session_id),
	             codex_startup             = ?,
	             claude_capacity           = NULL,
	             ` + jsonlSet + `,
	             pid                       = ?,
	             proc_starttime            = ?,
	             liveness_unverified_since = NULL,
	             liveness_note             = NULL,
	             ` + idleClear + `,
	             end_reported_at           = NULL,
	             ` + hookPaneStartSet + `,
	             ` + launchStartClear + `,
	             ` + rowVersionAdvance + `
	       WHERE instance_id = ? AND ` + hookGateSQL + ` AND ` + snapshotMatchSQL
	args := append(append(stateArgs, nullableStringArg(gate.SessionID), codexStartupArg(prior, gate, jsonlPath)), jsonlArgs...)
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

	outgoing := gate.Examined.SessionID
	archive := outgoing != "" && gate.SessionID != "" && outgoing != gate.SessionID
	var archiveErr error
	if archive {
		archiveErr = upsertSessionHistoryEntry(tx, instanceID, outgoing, prior.jsonlPath, prior.life)
	}
	if err := s.refreshFamilies(tx); err != nil {
		_ = tx.Rollback()
		return HookApplied{}, false, fmt.Errorf("%s: %w", errPrefix, err)
	}
	if err := tx.Commit(); err != nil {
		return HookApplied{}, false, fmt.Errorf("%s: commit: %w", errPrefix, err)
	}

	event := gate.Event
	if event == "" {
		event = "SessionStart"
	}
	_ = trail.Emit(context.Background(), "ad.spawn.state_transition", map[string]any{
		"instance_id":           instanceID,
		"prior_state":           prior.state,
		"new_state":             StateWaiting,
		"triggering_event_name": event,
		"soft_refresh":          false,
		"source":                "ad_spawn_store",
	})
	switch {
	case archiveErr != nil:
		_ = trail.Emit(context.Background(), "ad.session.archive_failed", map[string]any{
			"instance_id": instanceID,
			"session_id":  outgoing,
			"error":       archiveErr.Error(),
			"source":      "ad_spawn_store",
		})
	case archive:
		_ = trail.Emit(context.Background(), "ad.session.archived", map[string]any{
			"instance_id":      instanceID,
			"prior_session_id": outgoing,
			"new_session_id":   gate.SessionID,
			"source":           "ad_spawn_store",
		})
	}
	return HookApplied{Applied: true}, false, nil
}

// readSessionStartPrior reads the prior state, transcript path and life of
// instanceID's row, only while the row holds the examined snapshot (SR-5.3);
// pinned is false when no row has the id or the row has changed since.
func (s *Store) readSessionStartPrior(instanceID string, examined RowSnapshot, errPrefix string) (prior sessionStartPrior, pinned bool, err error) {
	args := append([]any{instanceID}, snapshotMatchArgs(examined)...)
	err = s.db.QueryRow(`SELECT state, COALESCE(jsonl_path, ''), COALESCE(life_number, 0), COALESCE(agent, ''), COALESCE(launch_token, '')
	                       FROM spawns
	                      WHERE instance_id = ? AND `+snapshotMatchSQL, args...,
	).Scan(&prior.state, &prior.jsonlPath, &prior.life, &prior.agent, &prior.launchToken)
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
