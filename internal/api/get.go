package api

import (
	"time"

	"github.com/gabemahoney/claude-director/internal/store"
)

// SpawnRow is the JSON-friendly projection of store.Spawn. Field names
// match the SRD §4.2 column names so callers reading the verb output can
// cross-reference against the schema doc without translation.
type SpawnRow struct {
	ClaudeInstanceID string            `json:"claude_instance_id"`
	ParentID         string            `json:"parent_id"`
	State            string            `json:"state"`
	CWD              string            `json:"cwd"`
	TmuxSessionName  string            `json:"tmux_session_name"`
	ClaudeArgs       []string          `json:"claude_args"`
	RelayMode        string            `json:"relay_mode"`
	JSONLPath        string            `json:"jsonl_path"`
	ClaudeSessionID  string            `json:"claude_session_id"`
	Labels           map[string]string `json:"labels"`
	StartedAt        time.Time         `json:"started_at"`
	LastSeenAt       time.Time         `json:"last_seen_at"`
	EndedAt          *time.Time        `json:"ended_at,omitempty"`
}

// Get returns the full Spawn row for the given claude_instance_id. Missing
// rows surface store.ErrSpawnNotFound for the CLI to translate.
func Get(s *store.Store, instanceID string) (SpawnRow, error) {
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
	return out, nil
}
