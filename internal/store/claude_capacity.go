package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeWindow retains capacity provenance only. Usage is read from the
// transcript on every public read, and must still identify this response.
type ClaudeWindow struct {
	Model        string    `json:"model"`
	Window       int64     `json:"window"`
	ResponseUUID string    `json:"response_uuid"`
	ObservedAt   time.Time `json:"observed_at"`
	Invalidated  bool      `json:"invalidated"`
}

type ClaudeCapacity struct {
	LaunchToken string                  `json:"launch_token"`
	SessionID   string                  `json:"session_id"`
	JSONLPath   string                  `json:"jsonl_path"`
	PID         int                     `json:"pid"`
	Starttime   string                  `json:"starttime"`
	Version     string                  `json:"version"`
	Provider    string                  `json:"provider"`
	Main        *ClaudeWindow           `json:"main"`
	Children    map[string]ClaudeWindow `json:"children"`
}

const maxClaudeCapacityBytes = 128 << 10

func (w ClaudeWindow) valid() bool {
	return w.Model != "" && len(w.Model) <= 256 && w.Window > 0 && w.ResponseUUID != "" && len(w.ResponseUUID) <= 128 &&
		!w.ObservedAt.IsZero() && w.ObservedAt.Year() >= 0 && w.ObservedAt.Year() <= 9999
}

func (p ClaudeCapacity) valid() bool {
	if !(CodexStartup{p.LaunchToken, p.SessionID, p.JSONLPath, p.PID, p.Starttime}).valid() || p.Version == "" || len(p.Version) > 64 || (p.Provider != "anthropic" && p.Provider != "unknown") || len(p.Children) > 128 {
		return false
	}
	if p.Main != nil && !p.Main.valid() {
		return false
	}
	for id, w := range p.Children {
		if !validClaudeChildID(id) || !w.valid() {
			return false
		}
	}
	return true
}

func validClaudeChildID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ClaudeChildPath derives only the native path of an already visible child.
// The callback cannot provide a path or cause new subagent discovery.
func ClaudeChildPath(parentPath, session, child string) string {
	if !filepath.IsAbs(parentPath) || filepath.Base(parentPath) != session+".jsonl" || !validClaudeChildID(child) {
		return ""
	}
	return filepath.Join(filepath.Dir(parentPath), session, "subagents", "agent-"+child+".jsonl")
}

func decodeClaudeCapacity(raw any) *ClaudeCapacity {
	s, ok := raw.(string)
	if !ok || len(s) > maxClaudeCapacityBytes {
		return nil
	}
	var p ClaudeCapacity
	d := json.NewDecoder(strings.NewReader(s))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || !p.valid() || d.Decode(new(any)) != io.EOF {
		return nil
	}
	return &p
}

func (e SpawnContext) CurrentClaudeCapacity() *ClaudeCapacity {
	p := e.BoundClaudeCapacity()
	if p == nil || p.Version != "2.1.280" || p.Provider != "anthropic" {
		return nil
	}
	return p
}

// BoundClaudeCapacity also returns invalidated provenance so a repeated
// callback cannot re-pair the same response after a configuration change.
func (e SpawnContext) BoundClaudeCapacity() *ClaudeCapacity {
	p := e.ClaudeCapacity
	if e.Agent != AgentClaude || !IsLiveState(e.State) || e.State == StatePending || e.EndReportedAtMillis != 0 || p == nil || !p.valid() ||
		e.LaunchToken != p.LaunchToken || e.SessionID != p.SessionID || e.JSONLPath != p.JSONLPath || e.PID != p.PID || e.ProcStarttime != p.Starttime || e.PanePID != p.PID || e.PaneStarttime != p.Starttime {
		return nil
	}
	return p
}

// RecordClaudeCapacity cannot change state, liveness, session or permissions.
// Its direct native parent and the exact selected launch/session/path are
// checked by the write itself. It accepts no usage fields.
// With a capacity snapshot, false also means contention: callers must reread
// and recompute the observation, never retry the previously computed JSON.
func (s *Store) RecordClaudeCapacity(instanceID string, g HookGate, p ClaudeCapacity) (bool, error) {
	if !p.valid() {
		return false, nil
	}
	b, err := json.Marshal(p)
	if err != nil || len(b) > maxClaudeCapacityBytes {
		return false, err
	}
	args := []any{string(b), instanceID, p.LaunchToken, p.SessionID, p.JSONLPath, p.PID, p.Starttime}
	args = append(args, hookGateArgs(g)...)
	compare, snapshot := 0, "null"
	if g.ClaudeCapacitySnapshot != nil {
		compare, snapshot = 1, *g.ClaudeCapacitySnapshot
	}
	args = append(args, compare, snapshot)
	result, err := s.db.Exec(`UPDATE spawns SET claude_capacity=? WHERE instance_id=? AND agent='claude'
 AND launch_token=? AND session_id=? AND jsonl_path=? AND pid=? AND proc_starttime=?
 AND state IN ('working','waiting','ask_user','check_permission') AND `+hookGateSQL+`
 AND (? = 0 OR claude_capacity IS NULLIF(?, 'null'))`, args...)
	if err != nil {
		return false, fmt.Errorf("store: Claude capacity: %w", err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// Capacity metadata is part of the authorized v12→v13 transaction;
// older, released migrations are unchanged and existing rows get no capacity.
func migrateClaudeCapacity(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(spawns)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def sql.NullString
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if !strings.EqualFold(name, "claude_capacity") {
			continue
		}
		if name != "claude_capacity" || kind != "TEXT" || notnull != 0 || pk != 0 || def.Valid {
			rows.Close()
			return fmt.Errorf("%w: store: v12→v13 incompatible Claude capacity column", ErrSchemaMismatch)
		}
		found = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		_, err = tx.Exec(`ALTER TABLE spawns ADD COLUMN claude_capacity TEXT`)
		return err
	}
	rows, err = tx.Query(`SELECT claude_capacity FROM spawns WHERE claude_capacity IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw any
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		if decodeClaudeCapacity(raw) == nil {
			return fmt.Errorf("%w: store: v12→v13 invalid Claude capacity value", ErrSchemaMismatch)
		}
	}
	return rows.Err()
}
