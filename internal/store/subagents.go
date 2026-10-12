package store

import (
	"fmt"
)

// The subagents and subagent_launches tables (b.xjn, SR-10.1, SR-10.3,
// SR-10.5) are read and written only by this package. This file holds their
// fixed value sets and the two caps with their eviction. The hook writes, the
// listing-created records and the closes call the eviction helpers inside their
// own BEGIN IMMEDIATE transaction, on the store's one connection, so the
// write lock is held before anything is read; the helpers take the caller's
// transaction or connection and never begin or end one.

// The value sets of a subagent entry's state (SR-6.1). Adding a value later is
// an additive change.
const (
	// SubagentStateRunning is an entry that has started and not stopped or been
	// judged lost. close_reason and lost_at are NULL exactly while running.
	SubagentStateRunning = "running"
	// SubagentStateStopped is an entry its agent reported finished.
	SubagentStateStopped = "stopped"
	// SubagentStateLost is an entry judged gone without a stop report.
	SubagentStateLost = "lost"
)

// The value set of close_reason (SR-6.1), NULL while the entry is running.
const (
	// SubagentCloseStopEvent: the agent reported the subagent finished.
	SubagentCloseStopEvent = "stop_event"
	// SubagentCloseAbsentFromSnapshot: a snapshot of the agent's running
	// background tasks, which covers the entry, did not list it.
	SubagentCloseAbsentFromSnapshot = "absent_from_snapshot"
	// SubagentCloseAgentGone: the entry's agent row ended, went missing or began
	// a new launch.
	SubagentCloseAgentGone = "agent_gone"
)

// The value set of parent_type (SR-6.1), NULL while the parent is unknown.
const (
	// SubagentParentAgent: the agent itself launched the subagent, so parent_id
	// is the instance id.
	SubagentParentAgent = "agent"
	// SubagentParentSubagent: another subagent launched it, so parent_id is that
	// subagent's id.
	SubagentParentSubagent = "subagent"
)

// The value set of run_mode (SR-6.1), NULL while unknown.
const (
	SubagentRunModeBackground = "background"
	SubagentRunModeForeground = "foreground"
)

// The values of the *_source columns (SR-10.1): which source set a value that
// has more than one source. subagent_type_source takes start, details or
// snapshot; run_mode_source takes details or snapshot; stopped_at_source takes
// stop or details. Each is NULL while its value is unknown.
const (
	SubagentSourceStart    = "start"
	SubagentSourceDetails  = "details"
	SubagentSourceSnapshot = "snapshot"
	SubagentSourceStop     = "stop"
)

// SubagentCap is the most entries (records and observations together) one row
// holds in subagents, and the most claimable launches it holds in
// subagent_launches (SR-10.3). One constant bounds both tables.
const SubagentCap = 300

// evictSubagentEntrySQL is the entry eviction's one statement, without a gate:
// when the row holds SubagentCap entries or more, it deletes exactly one,
// chosen by rank, then oldest activity_at, then lowest created_seq (SR-10.3):
// rank 0 is a closed observation (not running, not confirmed), rank 1 a closed
// record (not running, confirmed) and rank 2 a running entry, taken only when
// nothing closed remains. Below the cap the count condition fails and nothing
// is deleted. Placeholders: the id, the id, the cap. A hook's eviction appends
// the fragment subagentGateExists returns (evictSubagentEntryGated).
const evictSubagentEntrySQL = `DELETE FROM subagents
  WHERE created_seq = (
        SELECT created_seq FROM subagents
         WHERE instance_id = ?
         ORDER BY CASE WHEN state <> '` + SubagentStateRunning + `' AND confirmed = 0 THEN 0
                       WHEN state <> '` + SubagentStateRunning + `' THEN 1
                       ELSE 2 END,
                  activity_at, created_seq
         LIMIT 1)
    AND (SELECT COUNT(*) FROM subagents WHERE instance_id = ?) >= ?`

// evictSubagentEntryAtCap makes room for one more entry of instanceID: when the
// row holds SubagentCap entries (records and observations, of every launch), it
// deletes exactly one (SR-10.3) and reports true; the closed observation with
// the oldest activity_at, else, when no observation is closed, the closed
// record with the oldest activity_at, else, when nothing closed remains, the
// running entry with the oldest activity_at; ties go to the lowest created_seq.
// Below the cap it deletes nothing and reports false. It is one statement on q,
// the caller's open transaction or connection, so it commits or rolls back with
// the caller's write; the caller inserts its entry after it.
//
// This form carries no hook gate. It is for a caller that is not a hook event
// and for the cap's own tests; a hook's eviction is evictSubagentEntryGated,
// which every entry write of a hook uses (SR-3.1).
func evictSubagentEntryAtCap(q querier, instanceID string) (bool, error) {
	return execEvict(q, "evict subagent entry", evictSubagentEntrySQL, instanceID, instanceID, SubagentCap)
}

// evictSubagentEntryGated is evictSubagentEntryAtCap for a hook event (SR-3.1):
// the same statement with the hook gate and the four row conditions in its own
// WHERE (the fragment subagentGateExists returns), so it deletes nothing for a foreign parent, a
// pane-less, ended, missing or untracked row, or a row with no launch_token.
// The caller's gated insert follows it in the same transaction and rolls the
// whole transaction back when it inserts nothing, so an eviction never stands
// without its insert.
func evictSubagentEntryGated(q querier, instanceID string, gate HookGate) (bool, error) {
	frag, gateArgs := subagentGateExists(instanceID, gate, "")
	args := append([]any{instanceID, instanceID, SubagentCap}, gateArgs...)
	return execEvict(q, "evict subagent entry", evictSubagentEntrySQL+` AND `+frag, args...)
}

// evictSubagentLaunchSQL is the claimable-launch eviction's one statement,
// without a gate: when the row holds SubagentCap claimable launches or more, it
// deletes the one with the oldest opened_at, ties to the lowest created_seq
// (SR-10.3). Placeholders: the id, the id, the cap.
const evictSubagentLaunchSQL = `DELETE FROM subagent_launches
  WHERE created_seq = (
        SELECT created_seq FROM subagent_launches
         WHERE instance_id = ?
         ORDER BY opened_at, created_seq
         LIMIT 1)
    AND (SELECT COUNT(*) FROM subagent_launches WHERE instance_id = ?) >= ?`

// evictSubagentLaunchAtCap makes room for one more claimable launch of
// instanceID: when the row holds SubagentCap of them it deletes the one with
// the oldest opened_at, ties to the lowest created_seq (SR-10.3), and reports
// true; below the cap it deletes nothing and reports false. One statement on
// q, the caller's open transaction or connection, as evictSubagentEntryAtCap,
// and ungated as it is; a hook's eviction is evictSubagentLaunchGated.
func evictSubagentLaunchAtCap(q querier, instanceID string) (bool, error) {
	return execEvict(q, "evict subagent launch", evictSubagentLaunchSQL, instanceID, instanceID, SubagentCap)
}

// evictSubagentLaunchGated is evictSubagentLaunchAtCap for a hook event
// (SR-3.1), gated as evictSubagentEntryGated is.
func evictSubagentLaunchGated(q querier, instanceID string, gate HookGate) (bool, error) {
	frag, gateArgs := subagentGateExists(instanceID, gate, "")
	args := append([]any{instanceID, instanceID, SubagentCap}, gateArgs...)
	return execEvict(q, "evict subagent launch", evictSubagentLaunchSQL+` AND `+frag, args...)
}

// subagentRowLiveSQL is the three row conditions of SR-3.1 that need no
// argument, on the spawns row aliased sp: the row is live (neither ended nor
// missing, SR-3.4), its current launch is tracked (SR-9), and its launch_token
// is not null.
const subagentRowLiveSQL = `sp.state NOT IN ('` + StateEnded + `', '` + StateMissing + `')
        AND sp.subagent_tracking = 1 AND sp.launch_token IS NOT NULL`

// subagentGateExists returns the EXISTS fragment every statement that creates,
// changes or deletes an entry or a claimable launch for a hook event carries in
// its own WHERE (SR-3.1), with its arguments: the spawns row of instanceID,
// aliased sp, passes the hook gate (hookGateSQL) and the row conditions
// (subagentRowLiveSQL). A non-empty belongsTo names a table of the outer
// statement whose launch_token column must equal the row's current
// launch_token, read in the same statement, so the statement acts only on what
// belongs to the current launch. Nothing is checked before the statement: the
// row's state, pane and launch_token are decided in it.
func subagentGateExists(instanceID string, gate HookGate, belongsTo string) (string, []any) {
	frag := `EXISTS (SELECT 1 FROM spawns sp WHERE ` + subagentRowGateSQL
	if belongsTo != "" {
		frag += ` AND sp.launch_token = ` + belongsTo + `.launch_token`
	}
	frag += `)`
	return frag, subagentRowGateArgs(instanceID, gate)
}

// subagentRowGateSQL is subagentGateExists's condition on the spawns row aliased
// sp, without the EXISTS around it, for a statement that selects from spawns
// (an insert-select). Placeholders: the id, then hookGateArgs.
const subagentRowGateSQL = `sp.instance_id = ? AND ` + subagentRowLiveSQL + ` AND ` + hookGateSQL

// subagentRowGateArgs returns the arguments of subagentRowGateSQL.
func subagentRowGateArgs(instanceID string, gate HookGate) []any {
	return append([]any{instanceID}, hookGateArgs(gate)...)
}

// closeRunningSubagentsSQL is the liveness close's first statement (SR-8.1):
// every running entry of the row, record or observation, of any launch, becomes
// lost with close_reason agent_gone, lost_at and activity_at the instant. Every
// other column is left as it is. Placeholders: the state lost, the reason, the
// instant twice, the id, the state running.
const closeRunningSubagentsSQL = `UPDATE subagents
   SET state = ?, close_reason = ?, lost_at = ?, activity_at = ?
 WHERE instance_id = ? AND state = ?`

// closeClaimableLaunchesSQL is the liveness close's second statement (SR-8.1):
// every claimable launch of the row, of any launch, is deleted. Placeholder: the
// id.
const closeClaimableLaunchesSQL = `DELETE FROM subagent_launches WHERE instance_id = ?`

// closeSubagentsAgentGone is the close of SR-8.1, shared by the four writes that
// end or restart a row's launch: the terminal SessionEnd's move to ended
// (applyEndedTransition), find-missing's mark (MarkMissingIfSameLife), resume's
// move to pending (MoveToPending) and reuse's reset (ResetForReuse). On q, the
// caller's open transaction, it turns every running entry of instanceID (records
// and observations, any launch) lost with close_reason agent_gone, lost_at and
// activity_at at, and deletes every claimable launch of the row. Entries that
// are stopped or lost, every other column of a closed entry and every other
// row's entries and claimable launches are untouched.
//
// at is an instant in integer milliseconds, chosen by the caller: the store's
// own clock read once inside the transaction for the ended write and the mark
// (the value its request close takes too), the new launch's launch_started_at for
// the move and the reset (SR-3.5, SR-6.2). A non-positive at is a caller error
// and is always refused, with nothing written and no clock fallback, so the
// caller's transaction rolls back whole.
//
// The caller runs it only after its own write applied, inside that write's
// transaction, so the close and the write commit or roll back together: a write
// that did not apply closes nothing, and a close that fails undoes the write. It
// is independent of the row's subagent_tracking flag and of the hook gate (the
// writes it follows are the row's own liveness, not an entry event), emits no
// trail record, and logs nothing. Nothing is deleted from subagents. Its errors
// name the failed step only; each caller wraps them once with its own prefix.
func closeSubagentsAgentGone(q querier, instanceID string, at int64) error {
	if at <= 0 {
		return fmt.Errorf("close subagent entries: instant %d is not positive", at)
	}
	if _, err := q.Exec(closeRunningSubagentsSQL,
		SubagentStateLost, SubagentCloseAgentGone, at, at, instanceID, SubagentStateRunning); err != nil {
		return fmt.Errorf("close subagent entries: %w", err)
	}
	if _, err := q.Exec(closeClaimableLaunchesSQL, instanceID); err != nil {
		return fmt.Errorf("close subagent launches: %w", err)
	}
	return nil
}

// execEvict runs one eviction statement on q and reports whether it deleted a
// row.
func execEvict(q querier, what, stmt string, args ...any) (bool, error) {
	res, err := q.Exec(stmt, args...)
	if err != nil {
		return false, fmt.Errorf("store: %s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: %s rows affected: %w", what, err)
	}
	return n > 0, nil
}
