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

// evictSubagentEntrySQL is evictSubagentEntryAtCap's one statement: when the
// row holds SubagentCap entries or more, it deletes exactly one, chosen by
// rank, then oldest activity_at, then lowest created_seq (SR-10.3): rank 0 is a
// closed observation (not running, not confirmed), rank 1 a closed record (not
// running, confirmed) and rank 2 a running entry, taken only when nothing
// closed remains. Below the cap the count condition fails and nothing is
// deleted. Placeholders: the id, the id, the cap.
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
func evictSubagentEntryAtCap(q querier, instanceID string) (bool, error) {
	return execEvict(q, "evict subagent entry", evictSubagentEntrySQL, instanceID, instanceID, SubagentCap)
}

// evictSubagentLaunchSQL is evictSubagentLaunchAtCap's one statement: when the
// row holds SubagentCap claimable launches or more, it deletes the one with the
// oldest opened_at, ties to the lowest created_seq (SR-10.3). Placeholders: the
// id, the id, the cap.
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
// q, the caller's open transaction or connection, as evictSubagentEntryAtCap.
func evictSubagentLaunchAtCap(q querier, instanceID string) (bool, error) {
	return execEvict(q, "evict subagent launch", evictSubagentLaunchSQL, instanceID, instanceID, SubagentCap)
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
