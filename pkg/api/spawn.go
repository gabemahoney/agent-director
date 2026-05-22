package api

import (
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
)

// SpawnResult is the typed return shape of Spawn. The CLI marshals this
// directly to its single-JSON-object stdout (SRD §12.3); the MCP server
// returns it as the tool result.
type SpawnResult struct {
	// ClaudeInstanceID is the id under which the new Spawn is tracked.
	// Nondeterministic when SpawnParams.ClaudeInstanceID was empty — a
	// UUID4 is minted per call.
	ClaudeInstanceID string `json:"claude_instance_id"`
}

// runSpawn is the unexported verb handler called by (c *Client).Spawn.
// It takes internal types directly and is not part of the public API surface;
// external consumers use the Client method instead.
func runSpawn(s *store.Store, tmuxClient spawn.TmuxClient, cfg config.Config, params spawn.SpawnParams) (SpawnResult, error) {
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

// Spawn launches a tracked Claude Code instance inside a new tmux session.
// The call returns immediately with the claude_instance_id; the Spawn's state
// transitions from pending to waiting when the first SessionStart hook fires.
// Use [Client.Status] or [Client.Get] to observe progress.
//
// CLI: agent-director spawn
//
// Errors:
//   - ErrCwdMissing: params.CWD was not supplied.
//   - ErrCwdNotAPath: CWD is not a valid filesystem path.
//   - ErrCwdNotFound: CWD does not exist on disk.
//   - ErrCwdNotADirectory: CWD exists but is a file, not a directory.
//   - ErrRelayModeInvalid: RelayMode is not "on", "off", or "".
//   - ErrSpawnDeniedFlag: a denied claude flag was passed in ClaudeArgs.
//   - ErrReservedEnvKey: ExtraEnv contains a reserved AGENT_DIRECTOR_* key.
//   - ErrInstanceIdCollision: ClaudeInstanceID is already in use by a live row.
//   - ErrTmuxSessionNameEmpty: TmuxSessionName was supplied but is empty.
//   - ErrTmuxSessionNameInvalid: TmuxSessionName contains illegal characters.
//   - ErrTmuxSessionNameTooLong: TmuxSessionName exceeds 64 bytes.
//   - ErrTmuxNotAvailable: tmux binary is not on PATH or returns an error.
//   - [ErrTmuxSessionCreate]: tmux new-session exited non-zero.
//   - ErrTemplateNotFound: the named template file does not exist.
//   - ErrTemplateMalformed: the template TOML could not be parsed.
//   - ErrTemplateNameUnsafe: the template name contains path-unsafe characters.
//
// Nondeterminism: .claude_instance_id — a UUID4 is minted when
// SpawnParams.ClaudeInstanceID is empty; the value differs on every call.
func (c *Client) Spawn(params SpawnParams) (SpawnResult, error) {
	if err := c.checkClosed(); err != nil {
		return SpawnResult{}, err
	}
	return runSpawn(c.st, c.tmuxClient, c.cfg, params)
}
