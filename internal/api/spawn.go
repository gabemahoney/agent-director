package api

import (
	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/spawn"
	"github.com/gabemahoney/claude-director/internal/store"
)

// SpawnResult is the typed return shape of Spawn. The CLI marshals this
// directly to its single-JSON-object stdout (SRD §12.3); the MCP server
// returns it as the tool result.
type SpawnResult struct {
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// Spawn is the verb-handler entry point for `claude-director spawn`. It
// runs the SRD §7.1 → §7.4 pipeline: Resolve (template merge, currently
// a no-op stub), Validate, ApplyDefaults (with collision check), Launch
// (fire-and-forget tmux new-session + pending row insert).
//
// All SRD §13.1 errors flow up through this function untouched — the
// CLI / MCP layer matches them via errors.Is and emits the canonical
// err_name envelope.
func Spawn(s *store.Store, tmuxClient spawn.TmuxClient, cfg config.Config, params spawn.SpawnParams) (SpawnResult, error) {
	r, err := spawn.Resolve(params, cfg)
	if err != nil {
		return SpawnResult{}, err
	}
	if err := spawn.Validate(&r); err != nil {
		return SpawnResult{}, err
	}
	if err := spawn.ApplyDefaults(&r, cfg, s); err != nil {
		return SpawnResult{}, err
	}
	id, err := spawn.Launch(s, tmuxClient, r, cfg)
	if err != nil {
		return SpawnResult{}, err
	}
	return SpawnResult{ClaudeInstanceID: id}, nil
}
