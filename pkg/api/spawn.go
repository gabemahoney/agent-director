package api

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// SpawnResult is the typed return shape of Spawn: the new row's id and what
// the launch's pre-trust did (pre_trust), plus DroppedArgs on a switching
// reuse. The CLI marshals this directly to
// its single-JSON-object stdout (SRD §12.3); the MCP server returns it as the
// tool result.
type SpawnResult struct {
	// InstanceID is the id under which the new Spawn is tracked.
	// Nondeterministic when SpawnParams.InstanceID was empty — a
	// UUID4 is minted per call.
	InstanceID string `json:"instance_id"`
	// PreTrust is what the launch's folder-trust pre-trust did, always one
	// of three values (SR-22.6):
	//   - "ok": the folder trust took effect (a claude agent: the entry was
	//     written to .claude.json; a codex agent: the launch carries the
	//     folder trust and the hook-trust bypass).
	//   - "skipped": pre-trust was off for this launch because the caller
	//     passed SpawnParams.NoPreTrust; nothing was attempted.
	//   - "failed": pre-trust was attempted and the entry was not written
	//     (a claude agent: the .claude.json file is missing, or could not be
	//     read, parsed or written, or the extra env sets CLAUDE_CONFIG_DIR to
	//     a path that is not absolute; a codex agent: the extra env sets
	//     CODEX_HOME to a path that is not absolute, and the launch carries
	//     neither the folder trust nor the hook-trust bypass); the agent may
	//     stop at its folder-trust prompt.
	//
	// A pre-trust failure never fails the spawn.
	PreTrust string `json:"pre_trust"`
	// DroppedArgs lists original resume tokens removed for a switching reuse,
	// in argv order. Omitted when empty; the switched agent starts fresh.
	DroppedArgs []string `json:"dropped_args,omitempty"`
}

// spawnTmux is plain spawn's tmux surface (Appendix F.5): the create, the
// label by id and the session kill of the create-and-label step, the label
// scan's one lookup, and the one re-lookup of the requested name after
// "duplicate session". The Client's TmuxClient satisfies it.
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
// is not scanned, and a finished row never reaches the scan: without the
// reuse opt-in the pre-check refuses it (ErrInstanceIdCollision) before
// anything is written, and with it the reuse path takes it. pc, now and lg
// are the Client's start-time reader, clock and logger, which Launch uses for
// the identity write, the launch start and the identity write's WARN line.
//
// When the create answers "duplicate session", Launch returns a
// *spawn.HeldNameError with the new pending row's facts, and runSpawn hands
// it to the held-name path (spawnHeldName, SR-9.4): the conditional end write
// of the new row first (its time from now; on a store error the row stays
// pending and lg gets one WARN line), then one lookup of the requested name
// on the launch socket (with pc), then the classified error naming the
// blocking session, which runSpawn returns, and exactly one
// ad.launch.name_held. The HeldNameError itself never reaches the caller.
//
// With the reuse opt-in (ReuseFinished) and a caller-supplied id, s is also
// the reuse store (runSpawnWithReuseStore): its ReadForReuse is the one
// pre-check read, in place of collisions.
func runSpawn(s *store.Store, collisions spawn.CollisionChecker, t spawnTmux, pc ProcChecker, cfg config.Config, now func() time.Time, lg *log.Logger, params spawn.SpawnParams) (SpawnResult, error) {
	return runSpawnWithReuseStore(s, collisions, s, t, pc, cfg, now, lg, params)
}

// runSpawnWithReuseStore is runSpawn with rs, the reuse store, as its own
// parameter: the injection point a white-box test uses to wrap the reuse
// store's reads and writes (failures, interleavings) while s stays the store
// a fresh spawn inserts into. runSpawn passes s for both.
//
// The reuse branch is taken only when the caller opted in (ReuseFinished)
// and supplied a non-empty instance id, after the control-character check,
// template resolution; validation follows the snapshot read. A minted id,
// and every call without
// the opt-in, take exactly the plain path above (without the opt-in the
// pre-check refuses a finished row too, with ErrInstanceIdCollision, before
// Launch resolves its socket, pre-trusts or inserts). On the branch,
// rs.ReadForReuse is the one pre-check read, before Validate, used only for
// switching argument eligibility there: its saved answer feeds ApplyDefaults
// through reusePreCheck, so the live-row collision (ErrInstanceIdCollision, a
// pending row included, with no tmux call and nothing written) and the read
// failure (spawn.PreCheckReadError, ErrInternal) keep their single mappings,
// and collisions is not read. No row hands over to the ordinary fresh spawn:
// the label scan, then Launch, whose insert collision in a race is
// ErrInstanceIdCollision (SR-9.3). A finished row goes to spawnReuse.
func runSpawnWithReuseStore(s *store.Store, collisions spawn.CollisionChecker, rs reuseStore, t spawnTmux, pc ProcChecker, cfg config.Config, now func() time.Time, lg *log.Logger, params spawn.SpawnParams) (SpawnResult, error) {
	if err := validateExplicitInstanceID(params.InstanceID); err != nil {
		return SpawnResult{}, err
	}
	r, err := spawn.Resolve(params, cfg)
	if err != nil {
		return SpawnResult{}, err
	}
	reuse := r.ReuseFinished && r.InstanceID != ""
	var reused reusePreCheck
	if reuse {
		reused.row, reused.found, reused.err = rs.ReadForReuse(r.InstanceID)
		collisions = reused
		r.DropResumeArgs = reused.err == nil && reused.found &&
			(reused.row.State == "ended" || reused.row.State == "missing") && reused.row.Agent != r.Agent
	}
	if err := spawn.Validate(&r); err != nil {
		return SpawnResult{}, err
	}
	idCheck, err := spawn.ApplyDefaults(&r, cfg, collisions)
	if err != nil {
		return SpawnResult{}, err
	}
	if reuse && idCheck == spawn.IDFinishedRow {
		return spawnReuse(reuseDeps{rs: rs, t: t, pc: pc, cfg: cfg, storeID: s.StoreID(), storePath: s.Path(), now: now, lg: lg, who: callerIdentity()}, r, reused.row)
	}
	if idCheck == spawn.IDNoRow {
		if err := scanForLeftover(t, pc, s.StoreID(), r.InstanceID); err != nil {
			return SpawnResult{}, err
		}
	}
	id, preTrust, err := spawn.Launch(s, t, pc, r, idCheck == spawn.IDMinted, cfg, now, lg)
	var held *spawn.HeldNameError
	if errors.As(err, &held) {
		return SpawnResult{}, spawnHeldName(s, t, pc, now, lg, held)
	}
	if err != nil {
		return SpawnResult{}, err
	}
	return SpawnResult{InstanceID: id, PreTrust: string(preTrust), DroppedArgs: r.DroppedArgs}, nil
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

// Spawn launches a tracked agent inside a new tmux session.
// The call returns the instance_id, DroppedArgs (original resume arguments
// removed on a switching reuse; omitted when empty), and pre_trust (what
// the launch's
// folder-trust pre-trust did: ok, skipped or failed; a failure never fails
// the spawn) without waiting for the agent; the row is pending from its
// insert until the agent reports in (its SessionStart hook), then
// waiting. Use [Client.Status] or [Client.Get] to observe progress. The
// session is labelled for this launch when it is created, and the
// session-creating call is bounded by the create timeout. If it times out,
// Spawn returns ErrTmuxUnresponsive (UNAVAILABLE, transient): the session may
// have been created and the new row stays pending; do not retry until get
// shows the row ended or missing, since a retried spawn without an explicit
// id would start a second agent. Then retry an explicit InstanceID with
// ReuseFinished and the row's agent (SpawnParams.Agent; the error's
// description names it, and get shows it): without ReuseFinished, the spawn
// collides with the id's finished row (ErrInstanceIdCollision).
//
// The spawn names its agent, claude, codex or cursor (case-sensitive): SpawnParams.Agent,
// else the template's agent, else the call is refused with ErrAgentMissing; no
// config key or environment variable supplies one. Agent_args inherited from
// a template belong to the template's agent: a spawn of another agent that
// supplies no AgentArgs of its own is refused with ErrAgentArgsMismatch, and a
// supplied list, an empty one included, replaces the template's and is never
// refused for its owner. A codex agent launches the interactive Codex program
// in the same tmux session, with the same row, labels and delivery contract as
// a claude one; every refusal below applies to it, and agent-director's own
// per-launch settings (hooks, hook trust, the typing setting) are passed on its
// command line, never written to a Codex folder.
//
// With an explicit InstanceID that has no row, Spawn first makes one
// tmux lookup for a session of this agent-director store still labelled with
// that id. One left over from an earlier life refuses the spawn with
// ErrTmuxSessionConflict (CONFLICT: permanent until a human looks; see the
// docs/operator-actions.md), and nothing is written. A minted id is not
// looked up.
//
// If the requested tmux session name is already held when the session is
// created, Spawn ends its new row at once (get shows it ended unless the
// error's description says it could not be) and returns
// ErrTmuxSessionConflict naming the blocking session: its tmux id and whether
// its label names this instance id. If that session vanished before it was
// looked at the error is ErrTmuxSessionCreate, and if tmux could not be read
// or run it is ErrTmuxUnresponsive or ErrTmuxNotAvailable. The blocking
// session is never ended, read or typed into. A session of another row, or
// of another agent-director store, is another agent and must not be ended; a
// session left over from an earlier life of this id, or one with no valid
// instance id, is for a human to end (docs/operator-actions.md), after
// which the id is spawned again with ReuseFinished (reuse_finished over MCP,
// --reuse-finished on the CLI) and the row's agent (agent over MCP, --agent
// on the CLI; the description names it).
//
// With ReuseFinished, a finished row whose recorded tmux session name cannot
// be used (it is empty, contains a control character, or contains a
// character tmux stores differently) gets ErrInternal (an error matching no
// catalogued sentinel) with no tmux call, and nothing is changed; removing
// the row is a human's decision (see "Operator actions" in agent-director's
// docs/operator-actions.md).
//
// CLI: agent-director spawn
//
// Errors:
//   - ErrCwdMissing: params.CWD was not supplied.
//   - ErrCwdNotAPath: CWD is not a valid filesystem path.
//   - ErrCwdNotFound: CWD does not exist on disk.
//   - ErrCwdNotADirectory: CWD exists but is a file, not a directory.
//   - ErrRelayModeInvalid: RelayMode is not "on", "off", or "".
//   - ErrAgentMissing: the call gives no Agent and its template names none.
//   - ErrAgentInvalid: Agent is not claude, codex or cursor (case-sensitive).
//   - ErrAgentArgsMismatch: the template's agent_args are for another agent
//     than this spawn's and the call supplies no AgentArgs of its own.
//   - ErrSpawnDeniedFlag: a refused argument was passed in AgentArgs: for a
//     claude spawn --settings, --output-format, print (-p/--print, clusters
//     included), or an existing-session selector; selectors are dropped only
//     on a verified finished-agent switch. Exact denied long tokens owned as
//     option values are always refused. For a codex spawn a Codex subcommand
//     in command position, --remote, or a setting agent-director owns (a
//     -c/--config key at, above or under hooks, tui.disable_paste_burst or
//     features.hooks, --disable hooks, and while disable_ask_user is on the
//     question-tool keys and --enable default_mode_request_user_input).
//     Cursor accepts model selection, explicit agent/plan/ask mode and one
//     initial prompt. Unsupported or malformed Cursor arguments are refused;
//     remove the named argument and any owned value, then retry. Explicit mode is established through
//     ACP before prompt admission; later runtime failures are asynchronous.
//   - ErrPermissionsUnsupported: a codex or cursor spawn carries Permissions allow,
//     deny or ask entries (merged with the template's).
//   - ErrReservedEnvKey: ExtraEnv (merged with the template's) contains a
//     key whose name before the first '=' (the name tmux sets) is reserved:
//     it starts with AGENT_DIRECTOR_, or it is HOME (the key HOME, or one
//     such as "HOME=/x") with any value, an empty one included. The message
//     quotes the key. An extra_env HOME would move the agent's hook to
//     another agent-director store, so set an absolute CLAUDE_CONFIG_DIR
//     (a claude agent) or CODEX_HOME (a codex agent) instead to give the
//     agent its own config; the message names the spawn's agent's variable,
//     both when no agent is known yet. Also returned,
//     after those checks, for a key that is not a valid env-var name: it is
//     empty, or it contains '=' or a NUL byte. tmux splits each KEY=VALUE
//     entry at its first '=', so such a key would set a different variable
//     (the key "CLAUDE_CONFIG_DIR=/x" sets CLAUDE_CONFIG_DIR). The key is
//     refused, never rewritten; the message quotes it, says it is not a
//     valid env-var name and says what is wrong with it. A key that is both
//     reserved and malformed (such as "HOME=/x") gets the reserved-name
//     message. Nothing is written or launched.
//   - ErrInvalidFlags: InstanceID contains an ASCII control character.
//   - ErrInstanceIdCollision: without ReuseFinished, a row already exists
//     for the explicit InstanceID, in any state (the pre-check refuses
//     it before anything is written; the insert's primary key refuses a row
//     created after the pre-check). With ReuseFinished, the row is live
//     (pending included), or it changed or was removed after this spawn
//     examined it (a lost race); nothing was changed.
//   - ErrTmuxSessionNameEmpty: TmuxSessionName was supplied but is empty.
//   - ErrTmuxSessionNameInvalid: TmuxSessionName contains illegal characters.
//   - ErrTmuxSessionNameTooLong: TmuxSessionName exceeds 64 bytes.
//   - ErrTmuxNotAvailable: the tmux binary cannot be run, or the tmux socket
//     is not accessible to this user (at session creation the row stays
//     pending); or the per-user socket directory cannot be created or fails
//     tmux's own check, before anything is written (nothing launched). Also,
//     after "duplicate session", tmux was unavailable at the re-lookup of the
//     requested name, or the re-lookup found a different tmux server; the new
//     row is ended (the description says if it could not be). With the reuse
//     opt-in (ReuseFinished) on a finished row: at the lookup, before
//     anything is changed, this is not the tmux server the agent was launched
//     on, tmux cannot be run or the socket is not accessible, or the socket's
//     directory is unusable; or, after the row was reset, tmux was unavailable
//     at session creation or at the re-lookup after "duplicate session" (or
//     the re-lookup found a different tmux server), then the row is restored
//     (the description says what the restore did).
//   - [ErrTmuxSessionCreate]: session creation failed other than by timing
//     out, by tmux being unavailable or by "duplicate session", or a created
//     session could not be labelled; the row stays pending. Also, after
//     "duplicate session", the session holding the requested name was gone
//     by the re-lookup; the new row is ended (the description says if it
//     could not be), and a retry of the id (the description names it) uses
//     ReuseFinished and the row's agent once get shows the row ended or
//     missing and the name is free. With the reuse opt-in: after the row was
//     reset, session creation failed, a created session could not be labelled, or after
//     "duplicate session" no session held the requested name when it was
//     looked up again; then the row is restored (the description says what
//     the restore did). A name found held is never this error.
//   - ErrTmuxUnresponsive: the session-creating call timed out or gave a
//     reply that does not parse with a non-zero exit: the session may have
//     been created and the row stays pending; do not retry until get shows
//     the row ended or missing, then retry an explicit InstanceID with
//     ReuseFinished and the row's agent. Also, with an explicit InstanceID that has no
//     row, the label scan's lookup could not be read; nothing was written.
//     Also, after "duplicate session", the re-lookup of the requested name
//     could not be read; the new row is ended (the description says if it
//     could not be), and a retry of the id (the description names it) uses
//     ReuseFinished and the row's agent once get shows the row ended or
//     missing and the name is free. With the reuse opt-in: at the lookup, before anything is
//     changed, the row's own session or agent appears to still be stopping
//     (the row ended less than the stopping window ago) or still starting
//     (younger than the starting-session bound), this id's own abandoned
//     launch (a session of an earlier launch of this id while the row's
//     latest launch records no session of its own) appears to still be
//     starting, tmux's answer could not be
//     read, or more than one session's
//     name matches the requested name; retry later. After "duplicate
//     session", the same cases at the re-lookup, then the row is restored:
//     retry later if it was restored or removed; if it could not be
//     restored, or changed after the reset, do not retry until get shows
//     the row ended or missing.
//     Or the session-creating call timed out after the row was reset: the
//     session may have been created, the row was reset and stays pending; do
//     not retry until get shows the row ended or missing.
//   - ErrTmuxSessionConflict: with an explicit InstanceID that has no
//     row, the label scan found a session of an earlier life of that id,
//     labelled by this store, or conflicting labels; nothing was written, and
//     a human must look (docs/operator-actions.md). Also, after "duplicate
//     session", the requested name is held by a session left over from an
//     earlier life of this id, by another row's session (a different
//     instance id), by a session of another agent-director store, or by one
//     with no valid instance id, or the re-lookup found conflicting labels;
//     the new row is ended (the description says if it could not be). The
//     error names the blocking session. With the reuse opt-in, at the lookup
//     before anything is changed: a session left over from an earlier life
//     of this id, this row's own old session past the stopping window and
//     the starting-session bound ("this row's own id"), this id's own
//     abandoned launch past the starting-session bound, conflicting labels,
//     or the requested name held by another row's session, another
//     agent-director store's session or one with no valid instance id; after
//     "duplicate session", the same cases for the session holding the
//     requested name, then the row is restored. For a leftover, this row's
//     own old session, this id's own abandoned launch, conflicting labels or
//     a session with no valid instance id, a human must look
//     (docs/operator-actions.md); another row's or another agent-director store's
//     session is another agent and must not be ended.
//   - ErrTemplateNotFound: the named template file does not exist.
//   - ErrTemplateMalformed: the template TOML could not be parsed, or it
//     sets an unknown key, one key under names differing only in letter
//     case, a relay_mode other than on/off, or an agent other than claude,
//     codex or cursor.
//   - ErrTemplateAgentMissing: the template sets agent_args but names no
//     agent.
//   - ErrTemplateNameUnsafe: the template name contains path-unsafe characters.
//
// Nondeterminism: .instance_id — a UUID4 is minted when
// SpawnParams.InstanceID is empty; the value differs on every call.
func (c *Client) Spawn(params SpawnParams) (SpawnResult, error) {
	if err := c.checkClosed(); err != nil {
		return SpawnResult{}, err
	}
	return runSpawn(c.st, c.st, c.tmuxClient, c.procChecker, c.cfg, c.now, c.logger, params)
}
