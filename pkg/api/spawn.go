package api

import (
	"fmt"
	"log"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
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

// spawnTmux is plain spawn's tmux surface (Appendix F.5): the create, the
// label by id and the session kill of the create-and-label step, and the
// label scan's one lookup. The Client's TmuxClient satisfies it.
type spawnTmux interface {
	spawn.LaunchTmux
	tmux.LookupClient
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
//
// collisions is the reader for the collision pre-check in ApplyDefaults; s
// is the store Launch inserts the row into and whose id (s.StoreID()) every
// label carries. Client.Spawn passes its own store for both. They are
// separate so a white-box test can substitute a failing reader for the
// pre-check alone (SR-20.2). The pre-check still runs before pre-trust, the
// insert and every tmux call.
//
// A caller-supplied id whose pre-check finds no row gets the label scan
// (scanForLeftover) next, before Launch resolves its socket, mints its token,
// pre-trusts and inserts, so a refusal writes nothing (SR-9.3). A minted id
// and a finished row are not scanned. pc, now and lg are the Client's
// start-time reader, clock and logger, which Launch uses for the identity
// write, the launch start and the identity write's WARN line.
func runSpawn(s *store.Store, collisions spawn.CollisionChecker, t spawnTmux, pc ProcChecker, cfg config.Config, now func() time.Time, lg *log.Logger, params spawn.SpawnParams) (SpawnResult, error) {
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
	idCheck, err := spawn.ApplyDefaults(&r, cfg, collisions)
	if err != nil {
		return SpawnResult{}, err
	}
	if idCheck == spawn.IDNoRow {
		if err := scanForLeftover(t, pc, s.StoreID(), r.ClaudeInstanceID); err != nil {
			return SpawnResult{}, err
		}
	}
	id, err := spawn.Launch(s, t, pc, r, cfg, now, lg)
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
	if hasControlChar(id) {
		return fmt.Errorf("%w: the instance id contains a control character", ErrInvalidFlags)
	}
	return nil
}

// hasControlChar reports whether id contains an ASCII control character, any
// byte 0x00-0x1f or 0x7f: the one predicate behind spawn's explicit-id check
// (SR-9.1) and resume's refusal of such a row (SR-3.13). An id with one can
// never carry a valid @ad_owner label.
func hasControlChar(id string) bool {
	for i := 0; i < len(id); i++ {
		if b := id[i]; b <= 0x1f || b == 0x7f {
			return true
		}
	}
	return false
}

// Spawn launches a tracked Claude Code instance inside a new tmux session.
// The call returns the claude_instance_id without waiting for the agent; the
// row is pending from its insert until the agent reports in (Claude Code's
// SessionStart), then waiting. Use [Client.Status] or [Client.Get] to observe
// progress. The session is labelled for this launch when it is created, and
// the session-creating call is bounded by the create timeout. If it times
// out, Spawn returns ErrTmuxUnresponsive (UNAVAILABLE, transient): the
// session may have been created and the new row stays pending; do not retry
// until get shows the row ended or missing, since a retried spawn without an
// explicit id would start a second agent.
//
// With an explicit ClaudeInstanceID that has no row, Spawn first makes one
// tmux lookup for a session of this agent-director store still labelled with
// that id. One left over from an earlier life refuses the spawn with
// ErrTmuxSessionConflict (CONFLICT: permanent until a human looks; see the
// README's "Operator actions"), and nothing is written. A minted id is not
// looked up.
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
//   - ErrTmuxNotAvailable: the tmux binary cannot be run, or the tmux socket
//     is not accessible to this user (at session creation the row stays
//     pending); or the per-user socket directory cannot be created or fails
//     tmux's own check, before anything is written (nothing launched).
//   - [ErrTmuxSessionCreate]: session creation failed ("duplicate session"
//     included), or a created session could not be labelled; the row stays
//     pending.
//   - ErrTmuxUnresponsive: the session-creating call timed out or gave a
//     reply that does not parse with a non-zero exit: the session may have
//     been created and the row stays pending; do not retry until get shows
//     the row ended or missing. Also, with an explicit ClaudeInstanceID
//     that has no row, the label scan's lookup could not be read; nothing
//     was written.
//   - ErrTmuxSessionConflict: with an explicit ClaudeInstanceID that has no
//     row, the label scan found a session of an earlier life of that id,
//     labelled by this store, or conflicting labels; nothing was written, and
//     a human must look (README "Operator actions").
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
	return runSpawn(c.st, c.st, c.tmuxClient, c.procChecker, c.cfg, c.now, c.logger, params)
}
