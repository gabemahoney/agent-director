package api

import (
	"database/sql"
	"errors"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// PermissionRequestInfo is the open permission_requests row projection
// surfaced on `get` when the spawn is in state `check_permission`.
// ToolInput is the raw JSON string from the DB column — callers parse
// it themselves; the verb MUST NOT re-encode it as a nested object.
type PermissionRequestInfo struct {
	RequestID   int64     `json:"request_id"`
	ToolName    string    `json:"tool_name"`
	ToolInput   string    `json:"tool_input"`
	RequestedAt time.Time `json:"requested_at"`
}

// SpawnRow is the JSON-friendly projection of store.Spawn. Field names
// match the SRD §4.2 column names so callers reading the verb output can
// cross-reference against the schema doc without translation.
type SpawnRow struct {
	ClaudeInstanceID  string                 `json:"claude_instance_id"`
	ParentID          string                 `json:"parent_id"`
	State             string                 `json:"state"`
	CWD               string                 `json:"cwd"`
	TmuxSessionName   string                 `json:"tmux_session_name"`
	ClaudeArgs        []string               `json:"claude_args"`
	RelayMode         string                 `json:"relay_mode"`
	JSONLPath         string                 `json:"jsonl_path"`
	ClaudeSessionID   string                 `json:"claude_session_id"`
	Labels            map[string]string      `json:"labels"`
	StartedAt         time.Time              `json:"started_at"`
	LastSeenAt        time.Time              `json:"last_seen_at"`
	EndedAt           *time.Time             `json:"ended_at,omitempty"`
	PermissionRequest *PermissionRequestInfo `json:"permission_request,omitempty"`
}

// GetStore is the narrow store surface Get needs. Matches the existing
// methods on *store.Store; defined as an interface so api.Get's
// permission-fetch branch is testable without raw SQL fixtures.
type GetStore interface {
	GetSpawn(instanceID string) (store.Spawn, error)
	GetPermissionRequest(instanceID string) (store.PermissionRow, error)
}

// Get returns the full Spawn row for the given claude_instance_id. Missing
// rows surface store.ErrSpawnNotFound for the CLI to translate.
//
// When the spawn's state is `check_permission` AND an open
// permission_requests row exists for it (Decision == ""), the response's
// PermissionRequest pointer is populated. Otherwise the pointer stays
// nil and omitempty drops it from the JSON output.
//
// Req-review M1: the open-row predicate gates on `pr.Decision == ""`,
// NOT just on sql.ErrNoRows — a decided row from a prior cycle is
// treated identically to "no row." This matters when the same spawn
// re-enters check_permission after a previous decision was written.
func Get(s GetStore, instanceID string) (SpawnRow, error) {
	row, err := s.GetSpawn(instanceID)
	if err != nil {
		return SpawnRow{}, err
	}
	out := SpawnRow{
		ClaudeInstanceID: row.ClaudeInstanceID,
		ParentID:         row.ParentID,
		State:            row.State,
		CWD:              row.CWD,
		TmuxSessionName:  row.TmuxSessionName,
		ClaudeArgs:       row.ClaudeArgs,
		RelayMode:        row.RelayMode,
		JSONLPath:        row.JSONLPath,
		ClaudeSessionID:  row.ClaudeSessionID,
		Labels:           row.Labels,
		StartedAt:        row.StartedAt,
		LastSeenAt:       row.LastSeenAt,
		EndedAt:          row.EndedAt,
	}
	// Normalize: callers reading `claude_args:null` cannot distinguish
	// from `[]`; always emit a non-nil slice for the JSON output.
	if out.ClaudeArgs == nil {
		out.ClaudeArgs = []string{}
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}

	if out.State == "check_permission" {
		pr, err := s.GetPermissionRequest(instanceID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// No open row — field stays nil.
		case err != nil:
			return SpawnRow{}, err
		case pr.Decision == "":
			out.PermissionRequest = &PermissionRequestInfo{
				RequestID:   pr.RequestID,
				ToolName:    pr.ToolName,
				ToolInput:   pr.ToolInput,
				RequestedAt: pr.CreatedAt,
			}
		}
		// pr.Decision != "" → decided in a prior cycle, treat as absent.
	}

	return out, nil
}
