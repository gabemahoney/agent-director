package apitest

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/store"
)

// SpawnColumns is one spawns row read straight from the table by
// ReadSpawnColumns, for assertions on columns no verb shows (SR-5.1, SR-20.3).
// Each field holds the column's raw driver value, keeping its storage class:
// nil for NULL, int64, float64, string or []byte. NULL is therefore
// distinguishable from 0 and "", and a hand-edited value from a well-formed
// one. The TIMESTAMP columns come back as their stored text, never parsed.
type SpawnColumns struct {
	ParentID                any
	State                   any
	CWD                     any
	TmuxSessionName         any
	AgentArgs               any // raw stored text
	RelayMode               any
	JSONLPath               any
	SessionID               any
	Labels                  any // raw stored text
	StartedAt               any // raw stored text
	LastSeenAt              any // raw stored text
	EndedAt                 any // raw stored text
	PID                     any
	ProcStarttime           any
	LivenessUnverifiedSince any
	LivenessNote            any
	ExtraEnv                any // raw stored text

	// The twelve schema-v5 spawns columns.
	RowVersion          any
	LaunchStartedAt     any
	LifeNumber          any
	NoPreTrust          any
	LaunchToken         any
	TmuxSocket          any
	TmuxServerPID       any
	TmuxServerStarted   any
	TmuxServerStarttime any
	PaneID              any
	PanePID             any
	PaneStarttime       any

	// The three schema-v6 spawns columns (b.kdf): the launch owner.
	LaunchOwnerPID       any
	LaunchOwnerStarttime any
	LaunchOwnerPIDNS     any

	// The schema-v7 spawns column (b.146 problem 3): when the main agent's
	// idle-prompt Notification last landed, as stored text.
	IdleSince any

	// The two schema-v9 spawns columns: the row's agent (claude, codex or cursor) and
	// when its agent's SessionEnd was reported (Unix ms; nil = no end report).
	Agent         any
	EndReportedAt any

	// The schema-v9 spawns column subagent_tracking (b.xjn): 1 when the row's
	// current launch registered the agent's subagent events, 0 for a row from
	// before the upgrade. A default seed reads 1 (int64).
	SubagentTracking any
}

// ReadSpawnColumns reads every column of instanceID's spawns row from the
// store file at dbPath through a raw connection, decoding nothing, so it works
// on rows GetSpawn cannot decode. A missing row returns an error wrapping
// store.ErrSpawnNotFound. It never creates a store file.
func ReadSpawnColumns(dbPath, instanceID string) (SpawnColumns, error) {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return SpawnColumns{}, fmt.Errorf("ReadSpawnColumns: %w", err)
	}
	defer raw.Close() //nolint:errcheck

	// The unary + makes each TIMESTAMP column an expression with no declared
	// type, so the driver returns its stored value instead of a parsed time.
	const q = `SELECT parent_id, state, cwd, tmux_session_name, agent_args,
	                  relay_mode, jsonl_path, session_id, labels,
	                  +started_at, +last_seen_at, +ended_at, pid, proc_starttime,
	                  liveness_unverified_since, liveness_note, extra_env,
	                  row_version, launch_started_at, life_number, no_pre_trust,
	                  launch_token, tmux_socket, tmux_server_pid,
	                  tmux_server_started, tmux_server_starttime, pane_id,
	                  pane_pid, pane_starttime, launch_owner_pid,
	                  launch_owner_starttime, launch_owner_pidns, idle_since,
		                  agent, end_reported_at, subagent_tracking
	             FROM spawns
	            WHERE instance_id = ?`
	var c SpawnColumns
	err = raw.QueryRow(q, instanceID).Scan(
		&c.ParentID, &c.State, &c.CWD, &c.TmuxSessionName, &c.AgentArgs,
		&c.RelayMode, &c.JSONLPath, &c.SessionID, &c.Labels,
		&c.StartedAt, &c.LastSeenAt, &c.EndedAt, &c.PID, &c.ProcStarttime,
		&c.LivenessUnverifiedSince, &c.LivenessNote, &c.ExtraEnv,
		&c.RowVersion, &c.LaunchStartedAt, &c.LifeNumber, &c.NoPreTrust,
		&c.LaunchToken, &c.TmuxSocket, &c.TmuxServerPID,
		&c.TmuxServerStarted, &c.TmuxServerStarttime, &c.PaneID,
		&c.PanePID, &c.PaneStarttime, &c.LaunchOwnerPID,
		&c.LaunchOwnerStarttime, &c.LaunchOwnerPIDNS, &c.IdleSince,
		&c.Agent, &c.EndReportedAt, &c.SubagentTracking,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SpawnColumns{}, fmt.Errorf("ReadSpawnColumns: %w: %s", store.ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return SpawnColumns{}, fmt.Errorf("ReadSpawnColumns: %w", err)
	}
	return c, nil
}

// HistoryEntry is one session_history entry as ReadSessionHistoryAllLives
// returns it, from any life.
type HistoryEntry struct {
	SessionID  string
	JSONLPath  sql.NullString // Valid false = NULL, distinct from ""
	LifeNumber int64          // the life the entry belongs to (SR-5.9)
	RecordedAt string         // recorded_at as stored text
}

// ReadSessionHistoryAllLives returns every session_history entry of
// instanceID from every life, reading the table at dbPath directly (never the
// store's history read, which returns one life). Order is the store's history
// order: newest recorded_at first, ties broken by most recently inserted
// first. No entries gives an empty non-nil slice. It never creates a store file.
func ReadSessionHistoryAllLives(dbPath, instanceID string) ([]HistoryEntry, error) {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("ReadSessionHistoryAllLives: %w", err)
	}
	defer raw.Close() //nolint:errcheck

	const q = `SELECT session_id, jsonl_path, life_number,
	                  CAST(recorded_at AS TEXT)
	             FROM session_history
	            WHERE instance_id = ?
	         ORDER BY recorded_at DESC, history_id DESC`
	rows, err := raw.Query(q, instanceID)
	if err != nil {
		return nil, fmt.Errorf("ReadSessionHistoryAllLives: query: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	out := make([]HistoryEntry, 0)
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.SessionID, &e.JSONLPath, &e.LifeNumber, &e.RecordedAt); err != nil {
			return nil, fmt.Errorf("ReadSessionHistoryAllLives: scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ReadSessionHistoryAllLives: iterate: %w", err)
	}
	return out, nil
}

// SubagentEntryColumns is one subagents row as ReadSubagentRows returns it:
// every column's raw driver value, keeping its storage class (nil for NULL,
// int64 or string), so NULL is distinct from 0 and "". CreatedSeq is the
// table's created_seq.
type SubagentEntryColumns struct {
	LifeNumber         int64
	CreatedSeq         int64
	InstanceID         any
	SubagentID         any
	LaunchToken        any
	Confirmed          any
	SessionID          any
	SubagentType       any
	Description        any
	Model              any
	ParentType         any
	ParentID           any
	ToolUseID          any
	RunMode            any
	FirstStartedAt     any // Unix ms
	StartedAt          any // Unix ms
	StoppedAt          any // Unix ms
	LostAt             any // Unix ms
	State              any
	CloseReason        any
	OpenedAt           any // Unix ms
	ActivityAt         any // Unix ms
	SubagentTypeSource any
	RunModeSource      any
	StoppedAtSource    any
}

// SubagentLaunchColumns is one subagent_launches row (a claimable launch) as
// ReadSubagentRows returns it, each column's raw driver value.
type SubagentLaunchColumns struct {
	CreatedSeq   int64
	InstanceID   any
	LaunchToken  any
	ToolUseID    any
	SubagentType any
	OpenedAt     any // Unix ms
}

// SubagentRows is every subagents row (Entries) and every subagent_launches row
// (Launches) of one instance, each in created_seq order. Both slices are
// non-nil and empty when the instance has none.
type SubagentRows struct {
	Entries  []SubagentEntryColumns
	Launches []SubagentLaunchColumns
}

// ReadSubagentRows reads every entry and every claimable launch of instanceID
// straight from the store file at dbPath through a raw connection, decoding
// nothing, so a test asserts what was stored with NULL distinct from zero. An
// id with no spawns row (or none of either kind of row) gives empty slices and
// no error. It never creates a store file.
func ReadSubagentRows(dbPath, instanceID string) (SubagentRows, error) {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return SubagentRows{}, fmt.Errorf("ReadSubagentRows: %w", err)
	}
	defer raw.Close() //nolint:errcheck

	out := SubagentRows{Entries: []SubagentEntryColumns{}, Launches: []SubagentLaunchColumns{}}

	const entriesQ = `SELECT created_seq, instance_id, subagent_id, launch_token,
	                         confirmed, session_id, subagent_type, description,
	                         model, parent_type, parent_id, tool_use_id, run_mode,
	                         first_started_at, started_at, stopped_at, lost_at,
	                         state, close_reason, opened_at, activity_at,
	                         subagent_type_source, run_mode_source,
	                         stopped_at_source, life_number
	                    FROM subagents
	                   WHERE instance_id = ?
	                ORDER BY created_seq`
	rows, err := raw.Query(entriesQ, instanceID)
	if err != nil {
		return SubagentRows{}, fmt.Errorf("ReadSubagentRows: query entries: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var e SubagentEntryColumns
		if err := rows.Scan(&e.CreatedSeq, &e.InstanceID, &e.SubagentID, &e.LaunchToken,
			&e.Confirmed, &e.SessionID, &e.SubagentType, &e.Description,
			&e.Model, &e.ParentType, &e.ParentID, &e.ToolUseID, &e.RunMode,
			&e.FirstStartedAt, &e.StartedAt, &e.StoppedAt, &e.LostAt,
			&e.State, &e.CloseReason, &e.OpenedAt, &e.ActivityAt,
			&e.SubagentTypeSource, &e.RunModeSource, &e.StoppedAtSource, &e.LifeNumber); err != nil {
			return SubagentRows{}, fmt.Errorf("ReadSubagentRows: scan entry: %w", err)
		}
		out.Entries = append(out.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return SubagentRows{}, fmt.Errorf("ReadSubagentRows: iterate entries: %w", err)
	}

	const launchesQ = `SELECT created_seq, instance_id, launch_token, tool_use_id,
	                          subagent_type, opened_at
	                     FROM subagent_launches
	                    WHERE instance_id = ?
	                 ORDER BY created_seq`
	lrows, err := raw.Query(launchesQ, instanceID)
	if err != nil {
		return SubagentRows{}, fmt.Errorf("ReadSubagentRows: query launches: %w", err)
	}
	defer lrows.Close() //nolint:errcheck
	for lrows.Next() {
		var l SubagentLaunchColumns
		if err := lrows.Scan(&l.CreatedSeq, &l.InstanceID, &l.LaunchToken, &l.ToolUseID,
			&l.SubagentType, &l.OpenedAt); err != nil {
			return SubagentRows{}, fmt.Errorf("ReadSubagentRows: scan launch: %w", err)
		}
		out.Launches = append(out.Launches, l)
	}
	if err := lrows.Err(); err != nil {
		return SubagentRows{}, fmt.Errorf("ReadSubagentRows: iterate launches: %w", err)
	}
	return out, nil
}
