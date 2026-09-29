package api

import (
	"fmt"

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
//
// The explicit-instance-id check runs first, before template resolution,
// validation, defaults (including the collision pre-check) and launch, so a
// rejected id leaves no row, no tmux session, no pre-trust write and no
// trail side effect. It lives here rather than in internal/spawn because
// ErrInvalidFlags is declared in pkg/api and internal/spawn cannot import
// it. CLI, MCP, the Go client and the TypeScript client all reach spawn
// through this handler, so every surface returns the same error (SR-9.1).
func runSpawn(s *store.Store, tmuxClient spawn.TmuxClient, cfg config.Config, params spawn.SpawnParams) (SpawnResult, error) {
	if err := validateExplicitInstanceID(params.ClaudeInstanceID); err != nil {
		return SpawnResult{}, err
	}
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

// validateExplicitInstanceID rejects a caller-supplied instance id that
// contains an ASCII control character (any byte 0x00-0x1f or 0x7f) with
// ErrInvalidFlags (SR-9.1). Such an id can never carry a valid @ad_owner
// label: the label parser rejects control characters, and lookup lines are
// split on tabs and newlines. An empty id is accepted (ApplyDefaults mints a
// fresh UUID4). The description never contains the id, in any form, so a
// hostile id cannot inject text into the error envelope.
func validateExplicitInstanceID(id string) error {
	for i := 0; i < len(id); i++ {
		if b := id[i]; b <= 0x1f || b == 0x7f {
			return fmt.Errorf("%w: the instance id contains a control character", ErrInvalidFlags)
		}
	}
	return nil
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
//   - ErrInvalidFlags: ClaudeInstanceID contains an ASCII control character.
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
