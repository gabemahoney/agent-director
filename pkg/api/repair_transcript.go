package api

import (
	"errors"
	"fmt"
	"os"
)

// ErrRepairTranscriptMissing is returned by the repair-transcript verb when the
// supplied jsonl_path does not exist on disk. Re-associating a row with a path
// that isn't there would just re-create the b.v2c dead-pointer bug, so the verb
// refuses. Detected via errors.Is.
var ErrRepairTranscriptMissing = errors.New("ErrRepairTranscriptMissing")

// RepairTranscriptStore is the narrow store surface RepairTranscript needs.
type RepairTranscriptStore interface {
	RepairTranscript(instanceID, sessionID, jsonlPath string) error
}

// RepairTranscriptParams is the typed parameter shape for the repair-transcript
// verb (b.v2c AC7).
type RepairTranscriptParams struct {
	// ClaudeInstanceID is the row to re-associate the transcript with.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// ClaudeSessionID is the session id of the orphaned transcript (its .jsonl
	// basename without extension).
	ClaudeSessionID string `json:"claude_session_id"`
	// JSONLPath is the absolute on-disk path of the orphaned transcript.
	JSONLPath string `json:"jsonl_path"`
}

// RepairTranscriptResult is the typed return shape.
type RepairTranscriptResult struct {
	// ClaudeInstanceID echoes the repaired row's id.
	ClaudeInstanceID string `json:"claude_instance_id"`
	// ClaudeSessionID is the session id now recorded on the row.
	ClaudeSessionID string `json:"claude_session_id"`
	// JSONLPath is the transcript path now recorded on the row.
	JSONLPath string `json:"jsonl_path"`
}

// repairTranscriptImpl is the unexported verb handler. It verifies the supplied
// transcript exists on disk, then delegates to the store, which archives the
// row's current (session id, jsonl_path) pair into session_history when it is
// being replaced by a different session id (so a repair never silently discards
// the pointer it overwrites) and sets the new pair.
func repairTranscriptImpl(s RepairTranscriptStore, params RepairTranscriptParams) (RepairTranscriptResult, error) {
	if params.ClaudeInstanceID == "" {
		return RepairTranscriptResult{}, fmt.Errorf("%w: claude_instance_id is required", ErrInvalidFlags)
	}
	if params.ClaudeSessionID == "" {
		return RepairTranscriptResult{}, fmt.Errorf("%w: claude_session_id is required", ErrInvalidFlags)
	}
	if params.JSONLPath == "" {
		return RepairTranscriptResult{}, fmt.Errorf("%w: jsonl_path is required", ErrInvalidFlags)
	}
	if _, err := os.Stat(params.JSONLPath); err != nil {
		return RepairTranscriptResult{}, fmt.Errorf("%w: %s (%v)",
			ErrRepairTranscriptMissing, params.JSONLPath, err)
	}
	if err := s.RepairTranscript(params.ClaudeInstanceID, params.ClaudeSessionID, params.JSONLPath); err != nil {
		return RepairTranscriptResult{}, err
	}
	return RepairTranscriptResult{
		ClaudeInstanceID: params.ClaudeInstanceID,
		ClaudeSessionID:  params.ClaudeSessionID,
		JSONLPath:        params.JSONLPath,
	}, nil
}

// RepairTranscript re-associates an orphaned Claude transcript with a tracked
// Spawn row (b.v2c AC7). It is the supported one-shot operator recovery path for
// history stranded by a session rotation: the operator names the instance, the
// recovered session id, and the transcript path. The verb verifies the file
// exists, archives the row's current session pair into session_history when it
// differs, then records the recovered (session id, jsonl_path) so a subsequent
// resume points `claude --resume` at the recovered transcript.
//
// CLI: agent-director repair-transcript
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for claude_instance_id.
//   - [ErrRepairTranscriptMissing]: the supplied jsonl_path does not exist.
//   - [ErrInvalidFlags]: a required parameter is empty.
//
// Nondeterminism: none.
func (c *Client) RepairTranscript(params RepairTranscriptParams) (RepairTranscriptResult, error) {
	if err := c.checkClosed(); err != nil {
		return RepairTranscriptResult{}, err
	}
	return repairTranscriptImpl(c.st, params)
}
