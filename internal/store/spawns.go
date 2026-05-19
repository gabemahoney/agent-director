package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrSpawnNotFound is returned by lookup-by-id methods when no row matches
// the supplied claude_instance_id. Callers use errors.Is to detect.
var ErrSpawnNotFound = errors.New("ErrSpawnNotFound")

// State constants mirror the SRD §5.1 enum. They live here (the package
// that owns the column's text values) so the rest of the codebase has
// exactly one source of truth for valid state strings.
const (
	StatePending          = "pending"
	StateWaiting          = "waiting"
	StateWorking          = "working"
	StateAskUser          = "ask_user"
	StateCheckPermission  = "check_permission"
	StateEnded            = "ended"
	StateMissing          = "missing"
)

// liveStates is the set of state values find-missing considers "alive"
// (anything except the terminal ones). The collision check on a
// caller-supplied claude_instance_id uses NOT IN over this set so an
// `ended` Spawn's id can be reused (the resume verb handles that case).
var liveStates = []string{
	StatePending, StateWaiting, StateWorking, StateAskUser, StateCheckPermission,
}

// Spawn mirrors a row of the `spawns` table for callers outside this
// package. ClaudeArgs and Labels are presented as their materialized Go
// shapes; the JSON encoding stays inside the package.
type Spawn struct {
	ClaudeInstanceID string
	ParentID         string
	State            string
	CWD              string
	TmuxSessionName  string
	ClaudeArgs       []string
	RelayMode        string
	JSONLPath        string
	ClaudeSessionID  string
	Labels           map[string]string
	StartedAt        time.Time
	LastSeenAt       time.Time
	EndedAt          *time.Time
}

// InsertPending writes a new row in `pending` state. Used by spawn.Launch
// (SRD §7.4 step 2). The caller is expected to have already validated the
// row and minted any defaults — InsertPending does no semantic checks
// beyond what SQLite's constraints enforce.
//
// On PRIMARY KEY collision (claude_instance_id already exists) the error
// chain contains the bare driver error; spawn.Launch maps this back to
// ErrInstanceIdCollision for surface parity with the TOCTOU pre-check.
func (s *Store) InsertPending(sp Spawn) error {
	argsJSON, err := encodeArgs(sp.ClaudeArgs)
	if err != nil {
		return fmt.Errorf("store: encode claude_args: %w", err)
	}
	labelsJSON, err := encodeLabels(sp.Labels)
	if err != nil {
		return fmt.Errorf("store: encode labels: %w", err)
	}

	const stmt = `
        INSERT INTO spawns (
            claude_instance_id, parent_id, state, cwd, tmux_session_name,
            claude_args, relay_mode, labels
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    `
	var parent any
	if sp.ParentID != "" {
		parent = sp.ParentID
	} else {
		parent = nil
	}
	_, err = s.db.Exec(stmt,
		sp.ClaudeInstanceID, parent, StatePending,
		sp.CWD, sp.TmuxSessionName,
		argsJSON, sp.RelayMode, labelsJSON,
	)
	if err != nil {
		return fmt.Errorf("store: insert pending: %w", err)
	}
	return nil
}

// GetSpawn returns the full row for the given claude_instance_id. Missing
// rows yield ErrSpawnNotFound; other failures wrap the driver error.
func (s *Store) GetSpawn(instanceID string) (Spawn, error) {
	const q = `
        SELECT claude_instance_id, COALESCE(parent_id, ''), state, cwd,
               tmux_session_name, claude_args, relay_mode,
               COALESCE(jsonl_path, ''), COALESCE(claude_session_id, ''),
               labels, started_at, last_seen_at, ended_at
          FROM spawns
         WHERE claude_instance_id = ?
    `
	row := s.db.QueryRow(q, instanceID)
	var (
		sp         Spawn
		argsJSON   string
		labelsJSON string
		endedAt    sql.NullTime
	)
	err := row.Scan(
		&sp.ClaudeInstanceID, &sp.ParentID, &sp.State, &sp.CWD,
		&sp.TmuxSessionName, &argsJSON, &sp.RelayMode,
		&sp.JSONLPath, &sp.ClaudeSessionID,
		&labelsJSON, &sp.StartedAt, &sp.LastSeenAt, &endedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return Spawn{}, fmt.Errorf("store: get spawn: %w", err)
	}
	if endedAt.Valid {
		sp.EndedAt = &endedAt.Time
	}
	if sp.ClaudeArgs, err = decodeArgs(argsJSON); err != nil {
		return Spawn{}, fmt.Errorf("store: decode claude_args: %w", err)
	}
	if sp.Labels, err = decodeLabels(labelsJSON); err != nil {
		return Spawn{}, fmt.Errorf("store: decode labels: %w", err)
	}
	return sp, nil
}

// GetSpawnState is a narrow lookup returning only the state column. Used
// by api.Status to avoid materializing the full row.
func (s *Store) GetSpawnState(instanceID string) (string, error) {
	const q = `SELECT state FROM spawns WHERE claude_instance_id = ?`
	var state string
	err := s.db.QueryRow(q, instanceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return "", fmt.Errorf("store: get state: %w", err)
	}
	return state, nil
}

// LiveSpawnExists returns true when a row in a live state exists for the
// given claude_instance_id. The collision check in spawn.ApplyDefaults
// uses this; the row's existence in `ended`/`missing` does not block
// re-use of the id (resume covers that case).
func (s *Store) LiveSpawnExists(instanceID string) (bool, error) {
	placeholders := make([]any, 0, 1+len(liveStates))
	placeholders = append(placeholders, instanceID)
	q := `SELECT 1 FROM spawns WHERE claude_instance_id = ? AND state IN (`
	for i, st := range liveStates {
		if i > 0 {
			q += ","
		}
		q += "?"
		placeholders = append(placeholders, st)
	}
	q += `) LIMIT 1`
	row := s.db.QueryRow(q, placeholders...)
	var one int
	err := row.Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: live spawn lookup: %w", err)
	}
	return true, nil
}

// ApplyHookTransition writes the lifecycle UPSERT for a state-tracking
// hook. The transition follows SRD §5.2:
//   - When newState is non-empty, the row's state moves to newState and
//     last_seen_at is bumped.
//   - When newState is `ended`, ended_at is also set to CURRENT_TIMESTAMP.
//   - When softRefresh=true, only last_seen_at is bumped (state stays).
//
// The function is a no-op (returns nil) when no row matches the id —
// state-tracking hooks fail-open per SRD §3.2, so a hook racing against
// `delete` should not produce a visible error.
func (s *Store) ApplyHookTransition(instanceID, newState string, softRefresh bool) error {
	if softRefresh {
		const q = `UPDATE spawns SET last_seen_at = CURRENT_TIMESTAMP WHERE claude_instance_id = ?`
		_, err := s.db.Exec(q, instanceID)
		if err != nil {
			return fmt.Errorf("store: soft refresh: %w", err)
		}
		return nil
	}
	if newState == StateEnded {
		const q = `UPDATE spawns
                      SET state = ?, last_seen_at = CURRENT_TIMESTAMP,
                          ended_at = CURRENT_TIMESTAMP
                    WHERE claude_instance_id = ?`
		_, err := s.db.Exec(q, newState, instanceID)
		if err != nil {
			return fmt.Errorf("store: ended transition: %w", err)
		}
		return nil
	}
	const q = `UPDATE spawns
                  SET state = ?, last_seen_at = CURRENT_TIMESTAMP
                WHERE claude_instance_id = ?`
	_, err := s.db.Exec(q, newState, instanceID)
	if err != nil {
		return fmt.Errorf("store: state transition: %w", err)
	}
	return nil
}

// SetSessionID writes the claude_session_id column. Used by the
// SessionStart hook after extracting the UUID from transcript_path.
// A missing row is a no-op (fail-open per SRD §3.2).
func (s *Store) SetSessionID(instanceID, sessionID string) error {
	const q = `UPDATE spawns SET claude_session_id = ? WHERE claude_instance_id = ?`
	_, err := s.db.Exec(q, sessionID, instanceID)
	if err != nil {
		return fmt.Errorf("store: set session id: %w", err)
	}
	return nil
}

// encodeArgs serializes a string slice to a JSON array. nil → "[]" so the
// column always carries a valid JSON value.
func encodeArgs(args []string) (string, error) {
	if args == nil {
		return "[]", nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeArgs reads a JSON array column into a string slice. An empty
// string is treated as nil (the DEFAULT '[]' on the column means we
// rarely see this, but the test suite drops rows directly during setup).
func decodeArgs(blob string) ([]string, error) {
	if blob == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(blob), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// encodeLabels serializes a string map to a JSON object. nil → "{}".
func encodeLabels(labels map[string]string) (string, error) {
	if labels == nil {
		return "{}", nil
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeLabels reads a JSON object column into a string map. An empty
// string is treated as an empty map.
func decodeLabels(blob string) (map[string]string, error) {
	if blob == "" {
		return map[string]string{}, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(blob), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}
