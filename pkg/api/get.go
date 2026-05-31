package api

import (
	"time"
)

// PermissionRequestInfo is the open permission_requests row projection
// surfaced on `get` when the spawn is in state `check_permission`.
// ToolInput is the raw JSON string from the DB column — callers parse
// it themselves; the verb MUST NOT re-encode it as a nested object.
type PermissionRequestInfo struct {
	// RequestID is the autoincrement primary key of the permission_requests row.
	RequestID int64 `json:"request_id"`
	// RequestToken is the UUIDv4 token minted by runRelay for this request.
	// Callers pass it back to the decide verb to target a specific row.
	RequestToken string `json:"request_token"`
	// ToolName is the Claude Code tool that triggered the permission request
	// (e.g. "Bash", "Write").
	ToolName string `json:"tool_name"`
	// ToolInput is the raw JSON string of the tool's input as stored in the DB.
	// It is NOT a nested JSON object — callers unmarshal it themselves.
	ToolInput string `json:"tool_input"`
	// RequestedAt is the RFC3339 timestamp when the permission request row was
	// created (created_at column).
	RequestedAt time.Time `json:"requested_at"`
}

// SpawnRow is the JSON-friendly projection of store.Spawn. Field names
// match the SRD §4.2 column names so callers reading the verb output can
// cross-reference against the schema doc without translation.
type SpawnRow struct {
	// ClaudeInstanceID is the stable id of the Spawn (UUID4 or caller-supplied).
	ClaudeInstanceID string `json:"claude_instance_id"`
	// ParentID is the claude_instance_id of the Spawn that launched this one
	// (read from AGENT_DIRECTOR_INSTANCE_ID env at spawn time). Empty when
	// launched from a plain shell.
	ParentID string `json:"parent_id"`
	// State is the current lifecycle state. One of: pending, waiting, working,
	// ask_user, check_permission, ended, missing.
	State string `json:"state"`
	// CWD is the canonicalized working directory the Spawn's Claude was started in.
	CWD string `json:"cwd"`
	// TmuxSessionName is the tmux session under which the Spawn is running.
	TmuxSessionName string `json:"tmux_session_name"`
	// ClaudeArgs is the verbatim argv passed through to claude after --settings.
	// Always a non-nil slice (possibly empty) for JSON-stability.
	ClaudeArgs []string `json:"claude_args"`
	// RelayMode is "on" or "off" — whether this Spawn participates in the
	// permission-relay flow.
	RelayMode string `json:"relay_mode"`
	// JSONLPath is the last known JSONL transcript path. Empty until a future
	// Epic persists it; resume composes the path on demand from cwd + claude_session_id.
	JSONLPath string `json:"jsonl_path"`
	// ClaudeSessionID is the Claude Code session UUID extracted from the
	// SessionStart hook's transcript_path basename. Empty until the first
	// SessionStart fires.
	ClaudeSessionID string `json:"claude_session_id"`
	// Labels are the caller-supplied key-value tags set at spawn time.
	// Always a non-nil map (possibly empty) for JSON-stability.
	Labels map[string]string `json:"labels"`
	// StartedAt is the time the row was inserted (spawn time).
	StartedAt time.Time `json:"started_at"`
	// LastSeenAt is the time of the most recent hook UPSERT for this Spawn.
	LastSeenAt time.Time `json:"last_seen_at"`
	// EndedAt is set when state moves to ended. Omitted from JSON (omitempty)
	// while the Spawn is live.
	EndedAt *time.Time `json:"ended_at,omitempty"`
	// PermissionRequests is the slice of open permission requests awaiting
	// orchestrator decisions. Populated only when state is check_permission;
	// always a non-nil slice (encodes as [] when empty, never null, never
	// omitted). Callers use the request_token of each element to target a
	// specific row with the decide verb.
	PermissionRequests []PermissionRequestInfo `json:"permission_requests"`
}

// GetStore is the narrow store surface Get needs. Matches the existing
// methods on *store.Store; defined as an interface so api.Get's
// permission-fetch branch is testable without raw SQL fixtures.
type GetStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	OpenPermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
}

// Get returns the full Spawn row for the given claude_instance_id. Missing
// rows surface store.ErrSpawnNotFound for the CLI to translate.
//
// When the spawn's state is `check_permission`, all open (undecided)
// permission_requests rows are fetched and projected into the
// PermissionRequests slice. For all other states the slice is left as
// an empty non-nil slice (encodes as []).
//
// Open-rows-only contract: closed (decided) rows are never visible in
// the PermissionRequests output. OpenPermissionRequestsForSpawn enforces
// this at the SQL layer (decision IS NULL predicate).
func Get(s GetStore, instanceID string) (SpawnRow, error) {
	row, err := s.GetSpawn(instanceID)
	if err != nil {
		return SpawnRow{}, err
	}
	out := SpawnRow{
		ClaudeInstanceID:   row.ClaudeInstanceID,
		ParentID:           row.ParentID,
		State:              row.State,
		CWD:                row.CWD,
		TmuxSessionName:    row.TmuxSessionName,
		ClaudeArgs:         row.ClaudeArgs,
		RelayMode:          row.RelayMode,
		JSONLPath:          row.JSONLPath,
		ClaudeSessionID:    row.ClaudeSessionID,
		Labels:             row.Labels,
		StartedAt:          row.StartedAt,
		LastSeenAt:         row.LastSeenAt,
		EndedAt:            row.EndedAt,
		PermissionRequests: []PermissionRequestInfo{},
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
		prs, err := s.OpenPermissionRequestsForSpawn(instanceID)
		if err != nil {
			return SpawnRow{}, err
		}
		for _, pr := range prs {
			out.PermissionRequests = append(out.PermissionRequests, PermissionRequestInfo{
				RequestID:    pr.RequestID,
				RequestToken: pr.RequestToken,
				ToolName:     pr.ToolName,
				ToolInput:    pr.ToolInput,
				RequestedAt:  pr.CreatedAt,
			})
		}
	}

	return out, nil
}

// Get returns the full DB row for a tracked Spawn: state, cwd, tmux session
// name, relay mode, session id, labels, timestamps, and (when applicable) the
// open permission-requests slice.
//
// CLI: agent-director get
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for claudeInstanceID.
//
// Nondeterminism: none.
func (c *Client) Get(claudeInstanceID string) (SpawnRow, error) {
	if err := c.checkClosed(); err != nil {
		return SpawnRow{}, err
	}
	return Get(c.st, claudeInstanceID)
}
