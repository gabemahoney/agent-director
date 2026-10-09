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
	ClaudeArgs              any // raw stored text
	RelayMode               any
	JSONLPath               any
	ClaudeSessionID         any
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
	const q = `SELECT parent_id, state, cwd, tmux_session_name, claude_args,
	                  relay_mode, jsonl_path, claude_session_id, labels,
	                  +started_at, +last_seen_at, +ended_at, pid, proc_starttime,
	                  liveness_unverified_since, liveness_note, extra_env,
	                  row_version, launch_started_at, life_number, no_pre_trust,
	                  launch_token, tmux_socket, tmux_server_pid,
	                  tmux_server_started, tmux_server_starttime, pane_id,
	                  pane_pid, pane_starttime, launch_owner_pid,
	                  launch_owner_starttime, launch_owner_pidns
	             FROM spawns
	            WHERE claude_instance_id = ?`
	var c SpawnColumns
	err = raw.QueryRow(q, instanceID).Scan(
		&c.ParentID, &c.State, &c.CWD, &c.TmuxSessionName, &c.ClaudeArgs,
		&c.RelayMode, &c.JSONLPath, &c.ClaudeSessionID, &c.Labels,
		&c.StartedAt, &c.LastSeenAt, &c.EndedAt, &c.PID, &c.ProcStarttime,
		&c.LivenessUnverifiedSince, &c.LivenessNote, &c.ExtraEnv,
		&c.RowVersion, &c.LaunchStartedAt, &c.LifeNumber, &c.NoPreTrust,
		&c.LaunchToken, &c.TmuxSocket, &c.TmuxServerPID,
		&c.TmuxServerStarted, &c.TmuxServerStarttime, &c.PaneID,
		&c.PanePID, &c.PaneStarttime, &c.LaunchOwnerPID,
		&c.LaunchOwnerStarttime, &c.LaunchOwnerPIDNS,
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
	ClaudeSessionID string
	JSONLPath       sql.NullString // Valid false = NULL, distinct from ""
	LifeNumber      int64          // the life the entry belongs to (SR-5.9)
	RecordedAt      string         // recorded_at as stored text
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

	const q = `SELECT claude_session_id, jsonl_path, life_number,
	                  CAST(recorded_at AS TEXT)
	             FROM session_history
	            WHERE claude_instance_id = ?
	         ORDER BY recorded_at DESC, history_id DESC`
	rows, err := raw.Query(q, instanceID)
	if err != nil {
		return nil, fmt.Errorf("ReadSessionHistoryAllLives: query: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	out := make([]HistoryEntry, 0)
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.ClaudeSessionID, &e.JSONLPath, &e.LifeNumber, &e.RecordedAt); err != nil {
			return nil, fmt.Errorf("ReadSessionHistoryAllLives: scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ReadSessionHistoryAllLives: iterate: %w", err)
	}
	return out, nil
}
