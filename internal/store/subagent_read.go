package store

import "fmt"

// SubagentRow is one subagent record as the store holds it (SR-6.1): a
// confirmed entry of the subagents table. It carries the SR-6.1 fields and
// nothing internal (no launch_token, confirmed, opened_at, activity_at,
// created_seq or *_source column). A nil pointer is a NULL in the table
// (unknown or not applicable), distinct from an empty string or a zero; times
// are the stored integer milliseconds since the epoch. The value sets of State,
// CloseReason, ParentType and RunMode are the Subagent* constants of this
// package.
type SubagentRow struct {
	// SubagentID is the subagent's id, unique within its agent row.
	SubagentID string
	// InstanceID is the agent row the subagent belongs to.
	InstanceID string
	// SessionID is the session the subagent ran in; nil while unknown.
	SessionID *string
	// SubagentType is the subagent's type; nil while unknown.
	SubagentType *string
	// Description is the launching call's short description; nil while unknown.
	Description *string
	// Model is the model the subagent runs; nil while unknown.
	Model *string
	// ParentType is SubagentParentAgent or SubagentParentSubagent; nil while the
	// parent is unknown.
	ParentType *string
	// ParentID is the instance id or the parent subagent's id, as ParentType
	// says; nil while the parent is unknown.
	ParentID *string
	// ToolUseID is the id of the launching tool call; nil while unknown.
	ToolUseID *string
	// RunMode is SubagentRunModeBackground or SubagentRunModeForeground; nil
	// while unknown.
	RunMode *string
	// FirstStartedAt, StartedAt, StoppedAt and LostAt are integer milliseconds
	// since the epoch; nil while unknown or not reached.
	FirstStartedAt *int64
	StartedAt      *int64
	StoppedAt      *int64
	LostAt         *int64
	// State is SubagentStateRunning, SubagentStateStopped or SubagentStateLost.
	State string
	// CloseReason is one of the SubagentClose* values; nil exactly while State
	// is running.
	CloseReason *string
}

// listSubagentRecordsSQL reads the tracking flag of one agent row with every
// confirmed entry of the row, in one statement, so the flag and the records
// come from one consistent read. The LEFT JOIN returns the row once with NULL
// entry columns when it has no confirmed entry, and returns no row when the
// row does not exist. Observations (confirmed = 0) are never selected. Order:
// first_started_at ascending with NULL last, then subagent_id.
const listSubagentRecordsSQL = `SELECT sp.subagent_tracking,
        sa.subagent_id, sa.session_id, sa.subagent_type, sa.description,
        sa.model, sa.parent_type, sa.parent_id, sa.tool_use_id, sa.run_mode,
        sa.first_started_at, sa.started_at, sa.stopped_at, sa.lost_at,
        sa.state, sa.close_reason
   FROM spawns sp
   LEFT JOIN subagents sa
          ON sa.instance_id = sp.instance_id AND sa.confirmed = 1
  WHERE sp.instance_id = ?
  ORDER BY sa.first_started_at IS NULL, sa.first_started_at, sa.subagent_id`

// ListSubagentRecords returns whether the row of instanceID tracks subagents
// (its spawns.subagent_tracking flag, read as decodeSubagentTracking reads it:
// only the integer 1 is tracked, any other stored value, a text or NULL one
// included, is false and never an error) and every record of the row: every
// confirmed entry, of every launch and life of the row, in any state. It never
// returns an observation (an unconfirmed entry) nor a claimable launch. The
// records are ordered by first_started_at ascending, a NULL one last, then by
// subagent_id; with none it returns tracked as stored and an empty slice (a
// pre-upgrade row is tracked false with no records).
//
// It is one statement, a consistent read of the flag and the records, and
// changes nothing: no write, no trail line, no row_version change. Like
// GetSpawn it waits for another process's write lock for the store's busy
// timeout. A row that does not exist returns an error matching
// ErrSpawnNotFound. api.ListSubagentsStore names this method.
func (s *Store) ListSubagentRecords(instanceID string) (bool, []SubagentRow, error) {
	rows, err := s.db.Query(listSubagentRecordsSQL, instanceID)
	if err != nil {
		return false, nil, fmt.Errorf("store: list subagents: %w", err)
	}
	defer rows.Close()

	var (
		tracked bool
		found   bool
		out     = []SubagentRow{}
	)
	for rows.Next() {
		var (
			flag  any
			id    *string
			state *string
			r     SubagentRow
		)
		if err := rows.Scan(&flag, &id, &r.SessionID, &r.SubagentType, &r.Description,
			&r.Model, &r.ParentType, &r.ParentID, &r.ToolUseID, &r.RunMode,
			&r.FirstStartedAt, &r.StartedAt, &r.StoppedAt, &r.LostAt,
			&state, &r.CloseReason); err != nil {
			return false, nil, fmt.Errorf("store: list subagents scan: %w", err)
		}
		found = true
		tracked = decodeSubagentTracking(flag)
		if id == nil {
			// The LEFT JOIN's one row of an agent row without a record.
			continue
		}
		r.SubagentID = *id
		if state != nil {
			r.State = *state
		}
		r.InstanceID = instanceID
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return false, nil, fmt.Errorf("store: list subagents: %w", err)
	}
	if !found {
		return false, nil, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	return tracked, out, nil
}
