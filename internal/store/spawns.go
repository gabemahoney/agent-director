package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrSpawnNotFound is returned by lookup-by-id methods when no row matches
// the supplied instance_id. Callers use errors.Is to detect.
var ErrSpawnNotFound = errors.New("ErrSpawnNotFound")

// ErrPrimaryKeyCollision is returned by InsertPending when SQLite reports a
// PRIMARY KEY or UNIQUE constraint violation. Detected via *sqlite.Error
// Code() against SQLITE_CONSTRAINT_PRIMARYKEY / SQLITE_CONSTRAINT_UNIQUE,
// so callers don't string-match the driver's prose.
var ErrPrimaryKeyCollision = errors.New("store: primary key collision")

// ErrReuseArchive marks a failure of reuse's archive of the old session
// (ResetForReuse, SR-10.3, SR-5.8): the error chain holds it and the driver
// error, so a caller tells an archive failure from any other failure of the
// reuse change with errors.Is. It is the store's own marker, not a catalogued
// sentinel, and is never aliased in pkg/api (SR-1.5).
var ErrReuseArchive = errors.New("store: reuse archive failed")

// The agents a row can record (spawns.agent, introduced in schema v9, SR-3.1).
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
	AgentCursor = "cursor"
)

// ErrRowAgentInvalid is returned, wrapped, by InsertPending and ResetForReuse
// when the row's agent is not exactly claude, codex or cursor (SR-3.1, SR-3.6). The
// check runs before anything is written, so the database is unchanged. It is
// the store's own marker, not a catalogued sentinel, and is never aliased in
// pkg/api (the public agent errors come with the agent input). Callers use
// errors.Is to detect it.
var ErrRowAgentInvalid = errors.New("store: row agent must be claude, codex or cursor")

// validateRowAgent returns an error wrapping ErrRowAgentInvalid unless agent
// is exactly AgentClaude, AgentCodex or AgentCursor. The empty string, which is the column
// default and no agent, and any other spelling are refused.
func validateRowAgent(agent string) error {
	if agent != AgentClaude && agent != AgentCodex && agent != AgentCursor {
		return fmt.Errorf("%w: %q", ErrRowAgentInvalid, agent)
	}
	return nil
}

// State constants mirror the SRD §5.1 enum. They live here (the package
// that owns the column's text values) so the rest of the codebase has
// exactly one source of truth for valid state strings.
//
// StatePending means a launch (spawn, reuse or resume) is in progress and
// the agent has not reported in yet (its SessionStart hook); it may be
// loading or waiting at a startup prompt, and a resumed pending row keeps its
// session id and history (SR-22.1). The writes that set it are a spawn's
// insert (InsertPending), reuse's reset (ResetForReuse) and resume's move
// (MoveToPending). The agent's report-in (SessionStart, pending -> waiting)
// ends it, as does any other write that sets another state, such as
// find-missing's mark or a failed resume's restore (RestoreAfterFailedResume).
const (
	StatePending         = "pending"
	StateWaiting         = "waiting"
	StateWorking         = "working"
	StateAskUser         = "ask_user"
	StateCheckPermission = "check_permission"
	// StateStuck is a displayed state only; it must never be written to state.
	StateStuck   = "stuck"
	StateEnded   = "ended"
	StateMissing = "missing"
)

// liveStates is the set of live state values (anything except the terminal
// ones): the rows find-missing reads and judges (ListLiveSpawnIdentities).
// The collision pre-check on a caller-supplied instance_id tests the
// row's state against this set (IsLiveState): a live row collides, while a
// finished row's id is reused only by a spawn with the reuse opt-in (SR-10.2)
// and otherwise collides at the pre-check (resume relaunches a finished row
// under its own life instead). A terminal state is not proof
// that the agent is dead: `missing` is the sweep's judgement on the evidence
// available to it, not proof that the agent has exited, and neither `ended`
// nor `missing` means that the row is safe to delete.
var liveStates = []string{
	StatePending, StateWaiting, StateWorking, StateAskUser, StateCheckPermission,
}

// IsLiveState reports whether state is one of the live (non-terminal)
// states in liveStates. It is a read-only view of that set for callers
// outside the package, such as test fixtures seeding a live row's defaults.
// false for `ended` or `missing` says only that the row is finished, not
// that its agent has exited: `missing` is the sweep's judgement on the
// evidence available to it.
func IsLiveState(state string) bool {
	for _, st := range liveStates {
		if st == state {
			return true
		}
	}
	return false
}

// Spawn mirrors a row of the `spawns` table for callers outside this
// package. AgentArgs and Labels are presented as their materialized Go
// shapes; the JSON encoding stays inside the package.
type Spawn struct {
	// CodexStartup is admitted fresh-history provenance; it contains no usage.
	// It is never taken from a caller's Spawn by insert/reuse writes.
	CodexStartup    *CodexStartup
	ClaudeCapacity  *ClaudeCapacity
	InstanceID      string
	ParentID        string
	State           string
	CWD             string
	TmuxSessionName string
	AgentArgs       []string
	RelayMode       string
	JSONLPath       string
	SessionID       string
	Labels          map[string]string
	StartedAt       time.Time
	LastSeenAt      time.Time
	EndedAt         *time.Time

	// ExtraEnv holds the spawn's captured extra environment (schema v3,
	// extra_env column). Decoded like Labels: empty-string / '{}' / a
	// migrated pre-v3 NULL/default all decode to an empty NON-NIL map,
	// never nil. Store-internal only — never surfaced in any API row shape.
	ExtraEnv map[string]string

	// PID, ProcStarttime, LivenessUnverifiedSince, and LivenessNote are the
	// schema-v3 identity/liveness columns, scanned via COALESCE per the
	// jsonl_path/session_id precedent.
	//
	// NULL semantics: a zero value ("" for the string fields, 0 for PID)
	// means the underlying SQL column is NULL. Writers MUST store NULL for
	// the cleared/unset state, never an empty string or 0 — this keeps
	// SR-8.2's "cleared = set NULL" semantics unambiguous under COALESCE
	// scanning (a real pid is always ≥1).
	PID                     int
	ProcStarttime           string
	LivenessUnverifiedSince string
	LivenessNote            string

	// The schema-v5 fields (SR-5.1, Appendix F.4), filled by every read that
	// returns a Spawn. InsertPending and ResetForReuse take
	// LaunchStartedAtMillis, NoPreTrust, Identity.Token and Identity.Socket
	// from a Spawn; no write takes the others from one. The SR-5.5 columns never fail a read.

	// RowVersion is row_version: advanced by one by every write (SR-5.2).
	RowVersion int64
	// LaunchStartedAtMillis is launch_started_at in milliseconds since the
	// epoch; 0 = absent (NULL, a stored value that is not an integer, or an
	// integer outside the years 0 to 9999 UTC; see decodeLaunchStartedAt,
	// SR-5.5).
	LaunchStartedAtMillis int64
	// LifeNumber is life_number, the row's current life (SR-5.9).
	LifeNumber int64
	// NoPreTrust is no_pre_trust (SR-5.1): true for any stored value other
	// than the integer 0 (SR-5.5). The insert and reuse's reset each record
	// their caller's pre-trust choice here (1 = opted out, 0 = allowed), and
	// an applied reuse restore (RestoreAfterFailedReuse) writes the old life's
	// value back as stored. Resume's move and restore, hooks, find-missing and
	// every other write leave it unchanged (SR-5.2), so every resume follows
	// the latest spawn until the next spawn or reuse.
	// Rows that existed at the migration carry the column default, 0, and so
	// read as pre-trust allowed.
	NoPreTrust bool
	// EndedAtText is ended_at exactly as stored, never parsed and re-formatted;
	// "" = NULL. resume's restore writes it back byte for byte (SR-5.3).
	EndedAtText string
	// Snapshot is the row's change-detection key (SR-5.3).
	Snapshot RowSnapshot
	// Identity is the eight launch-identity columns (SR-3.3 to SR-3.6, SR-5.1);
	// Identity.Token is "" unless the stored token is well formed (SR-5.5).
	Identity LaunchIdentity

	// LaunchOwner is the schema-v6 launch owner (b.kdf): the process that
	// began the row's current launch, while that launch holds the row.
	// InsertPending and ResetForReuse take it from a Spawn; every read that
	// returns a Spawn fills it, the zero value for NULL (a row from before
	// schema v6 included).
	LaunchOwner LaunchOwner

	// IdleSince is the schema-v7 idle_since column (b.146 problem 3), as
	// stored ("" = NULL): set by the main agent's idle-prompt Notification
	// (ApplyHookWaitingIfWorking), NULLed by any later hook. find-missing's
	// repair of a stale check_permission row picks waiting when it is set.
	IdleSince string

	// Agent is the schema-v9 agent column (SR-3.1): claude, codex or cursor. Every read
	// that returns a Spawn fills it; InsertPending and ResetForReuse write it
	// from a Spawn, refusing any other value (ErrRowAgentInvalid). Rows that
	// existed at the v9 migration read claude.
	Agent string

	// EndReportedAtMillis is the schema-v9 end_reported_at column (SR-3.1):
	// when a Codex row's verified agent process sent a SessionEnd, in
	// milliseconds since the epoch; 0 = absent (NULL, or a stored value that is
	// not a positive integer). Every read that returns a Spawn fills it. No
	// write takes it from a Spawn: it is NULL on insert and after reuse's
	// reset, set by ApplyHookEndReport (a Codex SessionEnd), cleared by the
	// SessionStart identity write and by resume's move to pending, and saved
	// and restored with the row's raw life.
	EndReportedAtMillis int64

	// SubagentTracking is the schema-v9 subagent_tracking column (b.xjn, SR-9):
	// true when the settings the row's current launch was given register the
	// agent's subagent start and stop events. Every read that returns a Spawn
	// fills it; true only for the integer 1 (1 means tracked), and false for 0
	// and any other stored value (any other value reads as untracked), so no stored value
	// fails a read (see decodeSubagentTracking). InsertPending, ResetForReuse
	// and MoveToPending each write it for the launch they begin, from the
	// launch's agent and taking nothing else from a Spawn: 1 for a claude
	// launch, whose synthesized settings register the subagent events, and 0
	// for a codex launch, whose settings register no subagent events
	// (subagentTrackedParamSQL, subagentTrackedColumnSQL); a row from before the
	// v9 migration reads false until its next launch. Never on SpawnRow,
	// ListRow or any verb output.
	SubagentTracking bool

	// ReportedState is the state status, get and list report (b.3ie): the
	// settled state a read with a SettleRead that settles derives
	// (SettleRead.stateExpr; GetSpawnSettled, ListSpawns with
	// ListFilters.Settle), and State itself for every other read. Every read
	// fills it, so it is never "" on a row a read returned. Guards never read
	// it: they act on State, the raw state.
	PermissionEpoch  int64
	StuckVersion     int64
	StuckObservation string

	ReportedState string
	// FamilyState is the independently settled aggregate of this stored family.
	FamilyState string
}

// The tracking flag every launch write stores (b.xjn, SR-9): the launch's
// agent decides it. A claude launch's synthesized settings always register
// SubagentStart and SubagentStop, so it stores 1; a codex launch's settings
// register neither, so it stores 0. InsertPending and ResetForReuse state the
// launch's agent as one parameter of the statement (subagentTrackedParamSQL, the
// agent the statement writes: ResetForReuse can change it); MoveToPending keeps
// the row's agent, so it reads the column (subagentTrackedColumnSQL).
const (
	subagentTrackedParamSQL  = "CASE ? WHEN '" + AgentClaude + "' THEN 1 ELSE 0 END"
	subagentTrackedColumnSQL = "CASE agent WHEN '" + AgentClaude + "' THEN 1 ELSE 0 END"
)

// boolInt is a bool as the integer a 0/1 column stores.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// InsertPending writes a new row in `pending` state. Used by spawn.Launch
// (SRD §7.4 step 2). The caller is expected to have already validated the
// row and minted any defaults — InsertPending does no semantic checks
// beyond what SQLite's constraints enforce, and the row's agent: sp.Agent must
// be exactly claude, codex or cursor, or it returns an error wrapping
// ErrRowAgentInvalid before writing anything (SR-3.1, SR-3.6). The insert
// writes the agent explicitly and leaves end_reported_at NULL.
//
// On PRIMARY KEY collision (instance_id already exists, in any
// state) it returns ErrPrimaryKeyCollision; spawn.Launch maps that to
// ErrInstanceIdCollision. The collision pre-check has already refused every
// existing row a plain spawn may not use, a finished one included, so this
// is where a row inserted after the pre-check's read (a race) is caught.
//
// The insert is one of the writes that begin a launch (SR-5.2): in the same
// statement it writes launch_started_at from sp.LaunchStartedAtMillis
// (milliseconds from the caller's injected clock), launch_token from
// sp.Identity.Token and tmux_socket from sp.Identity.Socket. A zero value
// writes NULL, following LaunchIdentity's zero-means-NULL convention. It
// also records the caller's pre-trust choice: no_pre_trust is 1 when
// sp.NoPreTrust is true and 0 when it is false. Only reuse's reset (which
// begins a fresh launch) and an applied reuse restore change that column later
// (see Spawn.NoPreTrust), so the choice holds until the next spawn or reuse. The
// server and pane identity columns (tmux_server_pid, tmux_server_started,
// tmux_server_starttime, pane_id, pane_pid, pane_starttime) stay NULL
// whatever sp.Identity carries: RecordLaunchIdentity writes them after the
// create (SR-3.6). The launch owner columns (launch_owner_pid,
// launch_owner_starttime, launch_owner_pidns) take sp.LaunchOwner, a zero
// value as NULL (b.kdf): the spawn records itself as the owner of the launch
// it begins. The new row starts at row_version 0 (the column default). It is
// an immediate write (b.3ie): settled_state is pending and state_changed_ms
// the statement's own clock, so the new row reports pending at once whatever
// the settle time. It also writes subagent_tracking (SR-9), the flag of the
// launch it begins, from sp.Agent: 1 for claude, 0 for codex or cursor
// (subagentTrackedParamSQL).
func (s *Store) InsertPending(sp Spawn) error {
	if err := validateRowAgent(sp.Agent); err != nil {
		return fmt.Errorf("store: insert pending: %w", err)
	}
	argsJSON, err := encodeArgs(sp.AgentArgs)
	if err != nil {
		return fmt.Errorf("store: encode agent_args: %w", err)
	}
	labelsJSON, err := encodeLabels(sp.Labels)
	if err != nil {
		return fmt.Errorf("store: encode labels: %w", err)
	}
	extraEnvJSON, err := encodeExtraEnv(sp.ExtraEnv)
	if err != nil {
		return fmt.Errorf("store: encode extra_env: %w", err)
	}

	// The insert is an immediate write (settle.go, b.3ie): pending is the
	// row's settled state at once, stamped with the statement's own clock.
	const stmt = `
        INSERT INTO spawns (
            instance_id, parent_id, state, cwd, tmux_session_name,
            agent_args, relay_mode, labels, extra_env,
            launch_started_at, launch_token, tmux_socket, no_pre_trust,
            launch_owner_pid, launch_owner_starttime, launch_owner_pidns,
            settled_state, state_changed_ms, agent, subagent_tracking
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ` + nowMsSQL + `, ?, ` + subagentTrackedParamSQL + `)
    `
	var parent any
	if sp.ParentID != "" {
		parent = sp.ParentID
	} else {
		parent = nil
	}
	noPreTrust := 0
	if sp.NoPreTrust {
		noPreTrust = 1
	}
	args := []any{
		sp.InstanceID, parent, StatePending,
		sp.CWD, sp.TmuxSessionName,
		argsJSON, sp.RelayMode, labelsJSON, extraEnvJSON,
		positiveInt64Arg(sp.LaunchStartedAtMillis),
		nullableStringArg(sp.Identity.Token), nullableStringArg(sp.Identity.Socket),
		noPreTrust,
	}
	args = append(args, launchOwnerArgs(sp.LaunchOwner)...)
	args = append(args, StatePending, sp.Agent, sp.Agent)
	_, err = s.execFamily(sp.InstanceID, stmt, args...)
	if err != nil {
		var serr *sqlite.Error
		if errors.As(err, &serr) {
			switch serr.Code() {
			case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_UNIQUE:
				return fmt.Errorf("%w: %s", ErrPrimaryKeyCollision, sp.InstanceID)
			}
		}
		return fmt.Errorf("store: insert pending: %w", err)
	}
	return nil
}

// GetSpawn returns the full row for the given instance_id. Missing
// rows yield ErrSpawnNotFound; other failures wrap the driver error.
func (s *Store) GetSpawn(instanceID string) (Spawn, error) {
	sp, err := scanSpawn(s.db.QueryRow(getSpawnSQL, instanceID), getSpawnErrs)
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return Spawn{}, err
	}
	return sp, nil
}

// getSpawnSQL is the read of one row by id.
const getSpawnSQL = `SELECT ` + spawnColumns + ` FROM spawns WHERE instance_id = ?`

// GetSpawnWithin is GetSpawn with its waits bounded by maxWait: for the
// store's one connection, when another call of this process holds it, and
// for a lock another connection holds (DefaultLockWait: as GetSpawn waits). A
// read that waits longer returns an error wrapping ErrStoreBusy. decide reads
// it under a max_wait_ms bound (b.146 decision 9 B). Results otherwise as
// GetSpawn's.
func (s *Store) GetSpawnWithin(instanceID string, maxWait time.Duration) (Spawn, error) {
	var sp Spawn
	err := s.readWithin(maxWait, "", func(ctx context.Context, conn *sql.Conn) error {
		var err error
		sp, err = scanSpawn(conn.QueryRowContext(ctx, getSpawnSQL, instanceID), getSpawnErrs)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return Spawn{}, err
	}
	return sp, nil
}

// GetSpawnSettled is GetSpawn with the state get reports (b.3ie): the row's
// ReportedState as r derives it (SettleRead.stateExpr), in the same
// statement, from r's clock, which the caller took before this read. With no
// settle time it is GetSpawn exactly, the same statement, and ReportedState is
// State. It never waits for a state to settle. Guards read GetSpawn instead.
func (s *Store) GetSpawnSettled(instanceID string, r SettleRead) (Spawn, error) {
	return s.getSpawnSettled(instanceID, r, nil)
}

func (s *Store) getSpawnSettled(instanceID string, r SettleRead, proofs []CursorMissingProof) (Spawn, error) {
	if !r.on() && len(proofs) == 0 {
		return s.GetSpawn(instanceID)
	}
	cols, args, err := cursorMissingColumns(proofs)
	if err != nil {
		return Spawn{}, err
	}
	q := `SELECT ` + cols
	if r.on() {
		expr, stateArgs := r.stateExpr()
		expr, stateArgs, err = cursorMissingState(expr, stateArgs, proofs)
		if err != nil {
			return Spawn{}, err
		}
		q += `, ` + expr
		args = append(args, stateArgs...)
		familyExpr, familyArgs := r.familyExpr()
		q += `, ` + familyExpr
		args = append(args, familyArgs...)
	}
	q += ` FROM spawns WHERE instance_id = ?`
	sp, err := scanSpawnRow(s.db.QueryRowContext(s.readContext(), q, append(args, instanceID)...), getSpawnErrs, r.on())
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return Spawn{}, err
	}
	if err := validateCursorMissingRead(sp, proofs); err != nil {
		return Spawn{}, err
	}
	return sp, nil
}

// spawnColumns is the one column list every read returning a Spawn selects,
// in scanSpawn's order. The v5 and v6 columns come last so the pre-v5 columns
// keep their indices (and their scan-error texts). ended_at is selected a
// second time through CAST so the driver hands back the stored text instead of
// parsing the TIMESTAMP column (SR-5.3). The v5 part ends with lifeColumns, the
// snapshot and launch-identity fragment the live-row read shares; its
// started_at, session_id, pid, proc_starttime and tmux_session_name
// are the same row's values the Spawn's own fields read. The SR-5.5 columns are selected bare and decoded in
// Go; the other v5 columns follow the v3 identity columns' rule (NULL is the
// zero value; any other stored value scans normally). The v6 launch-owner
// columns (launchOwnerColumns) come after lifeColumns, by the same rule, and
// the v7 idle_since, then the v9 agent, end_reported_at and subagent_tracking,
// last.
const spawnColumns = `
        instance_id, COALESCE(parent_id, ''), state, cwd,
        tmux_session_name, agent_args, relay_mode,
        COALESCE(jsonl_path, ''), COALESCE(session_id, ''),
        labels, started_at, last_seen_at, ended_at,
        COALESCE(pid, 0), COALESCE(proc_starttime, ''),
        COALESCE(liveness_unverified_since, ''),
        COALESCE(liveness_note, ''), extra_env,
        CAST(ended_at AS TEXT), launch_started_at,
        COALESCE(life_number, 0), no_pre_trust,` + lifeColumns + `,
        ` + launchOwnerColumns + `,
        COALESCE(idle_since, ''), agent, end_reported_at, subagent_tracking, family_state, codex_startup, claude_capacity, permission_epoch, stuck_version, COALESCE(stuck_observation, '')`

// rowScanner is the Scan method shared by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// spawnReadErrs holds a read's error prefixes, so GetSpawn and ListSpawns
// keep their own error texts while sharing scanSpawn.
type spawnReadErrs struct {
	scan   string // wraps a Scan failure
	decode string // followed by the column name for a JSON decode failure
}

var (
	getSpawnErrs   = spawnReadErrs{scan: "store: get spawn", decode: "store: decode"}
	listSpawnsErrs = spawnReadErrs{scan: "store: list spawns scan", decode: "store: list spawns decode"}
)

// scanSpawn scans one row selected with spawnColumns into a Spawn. The
// pre-v5 fields and failures are exactly as before (an unparseable
// started_at/ended_at or malformed labels, agent_args or extra_env fails the
// read); the SR-5.5 columns never fail it. The Scan error is wrapped with %w,
// so callers can still detect sql.ErrNoRows. ReportedState is State.
func scanSpawn(sc rowScanner, errs spawnReadErrs) (Spawn, error) {
	return scanSpawnRow(sc, errs, false)
}

// scanSpawnRow is scanSpawn, with ReportedState scanned from one more column
// after spawnColumns, the reported state (SettleRead.stateExpr; b.3ie), when
// reported is set, and taken from State otherwise. The reported state comes
// last, so every other column keeps its index (and its scan-error text).
func scanSpawnRow(sc rowScanner, errs spawnReadErrs, reported bool) (Spawn, error) {
	var (
		sp              Spawn
		argsJSON        string
		labelsJSON      string
		endedAt         sql.NullTime
		extraEnvJSON    string
		endedAtText     sql.NullString
		launchStartedAt any
		noPreTrust      any
		endReportedAt   any
		subagentTrack   any
		codexStartup    any
		claudeCapacity  any
		life            lifeScan
	)
	dest := append([]any{
		&sp.InstanceID, &sp.ParentID, &sp.State, &sp.CWD,
		&sp.TmuxSessionName, &argsJSON, &sp.RelayMode,
		&sp.JSONLPath, &sp.SessionID,
		&labelsJSON, &sp.StartedAt, &sp.LastSeenAt, &endedAt,
		&sp.PID, &sp.ProcStarttime, &sp.LivenessUnverifiedSince,
		&sp.LivenessNote, &extraEnvJSON,
		&endedAtText, &launchStartedAt,
		&sp.LifeNumber, &noPreTrust,
	}, life.dest()...)
	dest = append(dest, launchOwnerDest(&sp.LaunchOwner)...)
	dest = append(dest, &sp.IdleSince, &sp.Agent, &endReportedAt, &subagentTrack, &sp.FamilyState, &codexStartup, &claudeCapacity, &sp.PermissionEpoch, &sp.StuckVersion, &sp.StuckObservation)
	if reported {
		dest = append(dest, &sp.ReportedState, &sp.FamilyState)
	}
	err := sc.Scan(dest...)
	if err != nil {
		return Spawn{}, fmt.Errorf("%s: %w", errs.scan, err)
	}
	if !reported {
		sp.ReportedState = sp.State
	}
	if endedAt.Valid {
		t := endedAt.Time
		sp.EndedAt = &t
	}
	if sp.AgentArgs, err = decodeArgs(argsJSON); err != nil {
		return Spawn{}, fmt.Errorf("%s agent_args: %w", errs.decode, err)
	}
	if sp.Labels, err = decodeLabels(labelsJSON); err != nil {
		return Spawn{}, fmt.Errorf("%s labels: %w", errs.decode, err)
	}
	if sp.ExtraEnv, err = decodeExtraEnv(extraEnvJSON); err != nil {
		return Spawn{}, fmt.Errorf("%s extra_env: %w", errs.decode, err)
	}
	sp.LaunchStartedAtMillis = decodeLaunchStartedAt(launchStartedAt)
	sp.CodexStartup = decodeCodexStartup(codexStartup)
	sp.ClaudeCapacity = decodeClaudeCapacity(claudeCapacity)
	sp.NoPreTrust = decodeNoPreTrust(noPreTrust)
	sp.EndReportedAtMillis = decodeEndReportedAt(endReportedAt)
	sp.SubagentTracking = decodeSubagentTracking(subagentTrack)
	sp.EndedAtText = endedAtText.String
	sp.Snapshot, sp.Identity = life.result()
	sp.RowVersion = sp.Snapshot.RowVersion
	return sp, nil
}

// GetSpawnState is a narrow lookup returning only the state column. It
// serves pause's wait loop and api.Status's fallback for a store without
// SpawnStatus; production Status reads through SpawnStatus instead.
func (s *Store) GetSpawnState(instanceID string) (string, error) {
	const q = `SELECT state FROM spawns WHERE instance_id = ?`
	var state string
	err := s.db.QueryRow(q, instanceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return "", fmt.Errorf("store: get state: %w", err)
	}
	return state, nil
}

// SpawnStatus returns the row's state and its launch start in milliseconds
// (0 = absent) in one read by primary key, for status (SR-22.2, SR-16.1).
// It decodes no structured column and never fails because of a stored
// launch start value: NULL, a non-integer and an integer outside the years 0
// to 9999 UTC all read as 0 (decodeLaunchStartedAt, SR-5.5). The state is the
// raw state: SpawnStatusSettled with no settle time.
func (s *Store) SpawnStatus(instanceID string) (state string, launchStartedAtMillis int64, err error) {
	_, state, launchStartedAtMillis, err = s.SpawnStatusSettled(instanceID, SettleRead{})
	return state, launchStartedAtMillis, err
}

// SpawnStatusSettled is SpawnStatus with the state status reports (b.3ie):
// reported is the row's state as r derives it (SettleRead.stateExpr), in the
// same statement, from r's clock, which the caller took before this read; raw
// is the row's raw state. With no settle time the statement is SpawnStatus's
// exactly and reported is raw. It never waits for a state to settle.
func (s *Store) SpawnStatusSettled(instanceID string, r SettleRead) (reported, raw string, launchStartedAtMillis int64, err error) {
	return s.spawnStatusSettled(instanceID, r, nil)
}

func (s *Store) spawnStatusSettled(instanceID string, r SettleRead, proofs []CursorMissingProof) (reported, raw string, launchStartedAtMillis int64, err error) {
	reported, raw, launchStartedAtMillis, _, err = s.spawnStatusContext(instanceID, r, proofs)
	return
}

// SpawnContext identifies only the current row's session evidence. It never
// falls back to another life or an archived session and contains no reading.
type SpawnContext struct {
	FamilyState         string
	Agent               string
	State               string
	SessionID           string
	JSONLPath           string
	LaunchToken         string
	PID                 int
	ProcStarttime       string
	PanePID             int
	PaneStarttime       string
	EndReportedAtMillis int64
	CodexStartup        *CodexStartup
	ClaudeCapacity      *ClaudeCapacity
}

// SpawnStatusContext reads status and its evidence identity in one statement.
// Evidence columns are decoded tolerantly so they cannot fail a status read.
func (s *Store) SpawnStatusContext(instanceID string, r SettleRead) (reported, raw string, launchStartedAtMillis int64, evidence SpawnContext, err error) {
	return s.spawnStatusContext(instanceID, r, nil)
}

func (s *Store) spawnStatusContext(instanceID string, r SettleRead, proofs []CursorMissingProof) (reported, raw string, launchStartedAtMillis int64, evidence SpawnContext, err error) {
	if len(proofs) > 0 {
		row, err := s.getSpawnSettled(instanceID, r, proofs)
		return row.ReportedState, row.State, row.LaunchStartedAtMillis, row.ContextEvidence(), err
	}
	var (
		launchStartedAt                                                     any
		agent, sessionID, path                                              any
		token, pid, start, panePID, paneStart, endReport, startup, capacity any
		q                                                                   = `SELECT state, launch_started_at, family_state, ` + spawnContextColumns + ` FROM spawns WHERE instance_id = ?`
		args                                                                = []any{instanceID}
		dest                                                                = []any{&raw, &launchStartedAt, &evidence.FamilyState, &agent, &sessionID, &path, &token, &pid, &start, &panePID, &paneStart, &endReport, &startup, &capacity}
	)
	if r.on() {
		expr, exprArgs := r.stateExpr()
		familyExpr, familyArgs := r.familyExpr()
		q = `SELECT state, launch_started_at, family_state, ` + spawnContextColumns + `, ` + expr + `, ` + familyExpr + ` FROM spawns WHERE instance_id = ?`
		args = append(exprArgs, familyArgs...)
		args = append(args, instanceID)
		dest = append(dest, &reported, &evidence.FamilyState)
	}
	err = s.db.QueryRowContext(s.readContext(), q, args...).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, SpawnContext{}, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return "", "", 0, SpawnContext{}, fmt.Errorf("store: spawn status: %w", err)
	}
	if !r.on() {
		reported = raw
	}
	evidence.Agent, _ = agent.(string)
	evidence.SessionID, _ = sessionID.(string)
	evidence.JSONLPath, _ = path.(string)
	evidence.State = raw
	evidence.LaunchToken = decodeLaunchToken(token)
	if n, ok := pid.(int64); ok && n > 0 && int64(int(n)) == n {
		evidence.PID = int(n)
	}
	if n, ok := panePID.(int64); ok && n > 0 && int64(int(n)) == n {
		evidence.PanePID = int(n)
	}
	evidence.ProcStarttime, _ = start.(string)
	evidence.PaneStarttime, _ = paneStart.(string)
	evidence.EndReportedAtMillis = decodeEndReportedAt(endReport)
	evidence.CodexStartup = decodeCodexStartup(startup)
	evidence.ClaudeCapacity = decodeClaudeCapacity(capacity)
	return reported, raw, decodeLaunchStartedAt(launchStartedAt), evidence, nil
}

// SpawnStatusWithFamily reads both independent reports in one snapshot,
// without decoding unrelated row fields or acquiring a writer lock.
func (s *Store) SpawnStatusWithFamily(instanceID string, r SettleRead) (reported, raw, family string, launchStartedAtMillis int64, err error) {
	return s.spawnStatusWithFamily(instanceID, r, nil)
}

func (s *Store) spawnStatusWithFamily(instanceID string, r SettleRead, proofs []CursorMissingProof) (reported, raw, family string, launchStartedAtMillis int64, err error) {
	if len(proofs) > 0 {
		row, err := s.getSpawnSettled(instanceID, r, proofs)
		return row.ReportedState, row.State, row.FamilyState, row.LaunchStartedAtMillis, err
	}
	var launchStartedAt any
	expr, args := r.stateExpr()
	familyExpr, familyArgs := r.familyExpr()
	args = append(args, familyArgs...)
	args = append(args, instanceID)
	q := `SELECT ` + expr + `, state, ` + familyExpr + `, launch_started_at FROM spawns WHERE instance_id = ?`
	err = s.db.QueryRowContext(s.readContext(), q, args...).Scan(&reported, &raw, &family, &launchStartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", 0, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return "", "", "", 0, fmt.Errorf("store: spawn status: %w", err)
	}
	return reported, raw, family, decodeLaunchStartedAt(launchStartedAt), nil
}

const spawnContextColumns = `agent, session_id, jsonl_path, launch_token, pid, proc_starttime, pane_pid, pane_starttime, end_reported_at, codex_startup, claude_capacity`

// SpawnState returns the state of the row with the given instance_id
// in one read, with exists false and no error when there is no such row. It
// is the collision pre-check's read (spawn.CollisionChecker; SR-9.3): it
// tells no row, a live row (IsLiveState) and a finished row apart. err is
// set only when the store cannot be read.
func (s *Store) SpawnState(instanceID string) (state string, exists bool, err error) {
	const q = `SELECT state FROM spawns WHERE instance_id = ?`
	err = s.db.QueryRow(q, instanceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: spawn state: %w", err)
	}
	return state, true, nil
}

// rowVersionAdvance is the SET fragment every statement that updates a
// spawns row carries, so the row's version advances by exactly one in the
// same statement as the rest of the write (SR-5.2).
const rowVersionAdvance = `row_version = row_version + 1`

// launchStartClear is the SET fragment every write that sets a row's state
// to anything other than pending carries, in the same statement (SR-5.2).
const launchStartClear = `launch_started_at = NULL`

// selectPriorState reads the current state column for instanceID without
// a transaction. Returns ("", false, nil) when no row exists (caller should
// fail-open and not emit). Returns ("", false, err) on a driver error.
// Returns (state, true, nil) when a row was found.
func (s *Store) selectPriorState(instanceID string) (string, bool, error) {
	var state string
	err := s.db.QueryRow(`SELECT state FROM spawns WHERE instance_id = ?`, instanceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: select prior state: %w", err)
	}
	return state, true, nil
}

// nullableStringArg returns the string as a bound arg, or nil (SQL NULL) when
// empty — the SetParentID house style for "" == NULL columns.
func nullableStringArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// positiveIntArg returns the int as a bound arg when positive, or nil (SQL
// NULL) otherwise — matching the COALESCE(pid, 0) scan convention where 0 means
// NULL (a real pid is always ≥1).
func positiveIntArg(n int) any {
	if n > 0 {
		return n
	}
	return nil
}

// positiveInt64Arg is positiveIntArg for int64 columns (launch_started_at,
// tmux_server_started), where 0 means absent.
func positiveInt64Arg(n int64) any {
	if n > 0 {
		return n
	}
	return nil
}

// recordLaunchIdentitySQL is RecordLaunchIdentity's one statement: the six
// server and pane identity columns, the end of the launch's hold and the
// version advance, guarded by the launch's state, version and token (SR-3.6,
// SR-5.3; b.kdf).
const recordLaunchIdentitySQL = `UPDATE spawns
    SET tmux_server_pid       = ?,
        tmux_server_started   = ?,
        tmux_server_starttime = ?,
        pane_id               = ?,
        pane_pid              = ?,
        pane_starttime        = ?,
        ` + launchOwnerClear + `,
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND state = ? AND row_version = ? AND launch_token = ?`

// RecordLaunchIdentity is the identity write after a create reply (SR-3.6,
// SR-5.3). It applies only while the row is pending with row_version equal to
// launchVersion (the version the write that began the launch produced) and
// launch_token equal to token. It then writes, in one statement, the tmux
// server's identity (tmux_server_pid, tmux_server_started,
// tmux_server_starttime) and the agent's pane (pane_id, pane_pid,
// pane_starttime) from id, a zero value as NULL (an unreadable start time
// passed as "" is stored as NULL), NULLs the launch owner (launch_owner_pid,
// launch_owner_starttime, launch_owner_pidns: it is the launch's last write,
// so it ends the launch's hold on the row, b.kdf), and advances row_version
// by one. It never writes id.Token, id.Socket, launch_started_at, state or any
// other column.
//
// It returns CondApplied when the write applied; CondChanged, having written
// nothing, when the row exists but is no longer pending with that version and
// token (another write came first, such as find-missing's mark, or another
// launch began; no hook can, because a row that records no pane matches no
// hook, SR-22.9); CondAbsent when no row
// has the id. The two are told apart by an existence read after the guarded
// statement matched no row; nothing is written a second time. A driver error
// is returned wrapped, with a zero CondResult.
//
// Used by plain spawn after its create, and later by resume and reuse. It is
// on the concrete *Store only, like InsertPending (Appendix F.4).
func (s *Store) RecordLaunchIdentity(instanceID string, launchVersion int64, token string, id LaunchIdentity) (CondResult, error) {
	res, err := s.db.Exec(recordLaunchIdentitySQL,
		positiveIntArg(id.ServerPID), positiveInt64Arg(id.ServerStart),
		nullableStringArg(id.ServerStarttime), nullableStringArg(id.PaneID),
		positiveIntArg(id.PanePID), nullableStringArg(id.PaneStarttime),
		instanceID, StatePending, launchVersion, token,
	)
	if err != nil {
		return 0, fmt.Errorf("store: record launch identity: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: record launch identity rows affected: %w", err)
	}
	if n > 0 {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, "store: record launch identity")
}

// releaseLaunchOwnerSQL is ReleaseLaunchOwner's one statement: the launch
// owner NULLed and the version advance, guarded by pending, the launch's
// token and a recorded owner (b.kdf).
const releaseLaunchOwnerSQL = `UPDATE spawns
    SET ` + launchOwnerClear + `,
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND state = ? AND launch_token = ? AND launch_owner_pid IS NOT NULL`

// ReleaseLaunchOwner ends a launch's hold on its pending row (b.kdf, b.146
// rule 11) when the launch ends without its identity write
// (RecordLaunchIdentity, which ends the hold itself): a lost create reply, a
// create that failed or timed out and left the row pending, an identity write
// that did not apply or failed, or a restore that did not apply. Without it,
// a launch run by a long-lived process (the MCP server, or a Go caller of
// pkg/api) would hold the row for as long as that process lives, and
// find-missing would never judge it.
//
// It is one conditional statement that applies only while the row is pending,
// its launch_token equals token (so it never touches a later launch of the
// row) and it records a launch owner. In that statement it NULLs
// launch_owner_pid, launch_owner_starttime and launch_owner_pidns and advances
// row_version by one (SR-5.2). It writes no other column, makes no tmux call
// and emits no trail event.
//
// It returns CondApplied when the write applied; CondChanged, having written
// nothing, when the row exists but is not pending, carries another token or
// records no owner; CondAbsent when no row has the id. A driver error is
// returned wrapped, with a zero CondResult. It is on the concrete *Store,
// like RecordLaunchIdentity.
func (s *Store) ReleaseLaunchOwner(instanceID, token string) (CondResult, error) {
	const errPrefix = "store: release launch owner"
	applied, err := s.execGuarded(instanceID, releaseLaunchOwnerSQL, []any{instanceID, StatePending, token}, errPrefix)
	if err != nil {
		return 0, err
	}
	if applied {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, errPrefix)
}

// storeTimestampLayout is the text layout of SQLite's CURRENT_TIMESTAMP, the
// form the store's TIMESTAMP columns hold (UTC, whole seconds).
const storeTimestampLayout = "2006-01-02 15:04:05"

// storeTimestamp formats t in the store's CURRENT_TIMESTAMP layout: UTC,
// truncated to the whole second. A value a Go caller writes through it is
// byte-compatible with one SQLite's CURRENT_TIMESTAMP writes, so text
// comparisons against either (expire's cutoff) treat both alike.
func storeTimestamp(t time.Time) string {
	return t.UTC().Format(storeTimestampLayout)
}

// endHeldLaunchSQL is EndHeldLaunch's one statement: the move to ended, an
// immediate write (immediateStateSet, b.3ie), the launch start cleared and the
// version advance, guarded by the insert's state, version and launch start
// (SR-5.3, SR-5.6, SR-22.3).
const endHeldLaunchSQL = `UPDATE spawns
    SET ` + immediateStateSet + `,
        ended_at = ?,
        ` + launchStartClear + `,
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND state = ? AND row_version = 0 AND launch_started_at = ?`

// EndHeldLaunch is a plain spawn's end write after its create answered
// "duplicate session" (SR-9.4; PO 2026-09-27 HELD): that answer proves the
// launch created no session, so the spawn ends its own new row at once, before
// anything else. It is one conditional statement (SR-5.6) that applies only
// while the row is pending with row_version 0 and launch_started_at equal to
// insertLaunchStartedAtMillis, the launch start InsertPending wrote (SR-5.3; a
// delete followed by a fresh insert restarts the version at 0, and the launch
// start tells the two lives apart). It then sets state to ended (pending,
// version 0, to ended; SR-22.3) as an immediate write (settled_state ended and
// state_changed_ms the statement's clock, so ended reports at once; b.3ie),
// ended_at to endedAt in the store's
// CURRENT_TIMESTAMP layout (UTC, whole seconds, so expire's text comparison
// selects it as it does a row the ended hook transition wrote),
// launch_started_at to NULL, and advances row_version by one (SR-5.2). It
// writes no other column: the launch token, socket, server and pane identity
// (the server and pane identity columns NULL, since no session was created),
// last_seen_at, the request columns, life_number, no_pre_trust and parent_id
// keep the insert's values. It closes no subagent entry and deletes no
// claimable launch (SR-8.1): no agent of the held launch ran, so the row has
// none, and the insert's subagent_tracking flag is left as it is.
//
// It returns CondApplied when the write applied; CondChanged, having written
// nothing, when the row exists but no longer matches: another versioned write
// came first, or the row was deleted and inserted afresh with another launch
// start (a hook of the row's own agent cannot come first, since no session was
// created); CondAbsent when no row has the id. The two are told apart by an
// existence read after the guarded statement matched no row; nothing is
// written a second time. A driver error is returned wrapped, with a zero
// CondResult, never as a CondResult value.
//
// "ended" sticks (SR-9.4): since the hook gate (SR-22.9; WD 2026-09-29 HOOK)
// only the row's own agent's hooks move the row, and no agent of this launch
// exists. The row it leaves is ended with pane_pid NULL, which every later
// hook finds not applied as no_pane_recorded, so a leftover's hooks cannot
// revive it (PO 2026-09-27 REVIEW's fix 1b is retired).
//
// It emits no trail event (no ad.spawn.state_transition: only hook-driven
// writes emit one, SR-14) and makes no tmux call. It is on the concrete
// *Store only, like InsertPending and RecordLaunchIdentity (Appendix F.4).
func (s *Store) EndHeldLaunch(instanceID string, insertLaunchStartedAtMillis int64, endedAt time.Time) (CondResult, error) {
	args := append(immediateStateArgs(StateEnded), storeTimestamp(endedAt),
		instanceID, StatePending, insertLaunchStartedAtMillis)
	res, err := s.execFamily(instanceID, endHeldLaunchSQL, args...)
	if err != nil {
		return 0, fmt.Errorf("store: end held launch: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: end held launch rows affected: %w", err)
	}
	if n > 0 {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, "store: end held launch")
}

// adoptIdentitySet is the one identity-column assignment every adoption write
// carries (SR-3.6, SR-5.2): the six server and pane identity columns and the
// version advance, as a SET list whose placeholders take adoptIdentityArgs in
// order. AdoptIdentityIfUnchanged and AdoptIdentityIfSameLife share it; it
// never names launch_token, tmux_socket, the state, the liveness columns,
// launch_started_at, life_number, no_pre_trust or any request column.
const adoptIdentitySet = `tmux_server_pid       = ?,
        tmux_server_started   = ?,
        tmux_server_starttime = ?,
        pane_id               = ?,
        pane_pid              = ?,
        pane_starttime        = ?,
        ` + rowVersionAdvance

// adoptIdentityArgs returns the bound arguments for adoptIdentitySet, in its
// placeholder order, a zero value of id as NULL.
func adoptIdentityArgs(id LaunchIdentity) []any {
	return []any{
		positiveIntArg(id.ServerPID), positiveInt64Arg(id.ServerStart),
		nullableStringArg(id.ServerStarttime), nullableStringArg(id.PaneID),
		positiveIntArg(id.PanePID), nullableStringArg(id.PaneStarttime),
	}
}

// adoptIdentitySQL is AdoptIdentityIfUnchanged's one statement: the shared
// identity-column assignment, guarded by the row snapshot the verb examined
// (SR-3.6, SR-5.3).
const adoptIdentitySQL = `UPDATE spawns
    SET ` + adoptIdentitySet + `
  WHERE instance_id = ? AND ` + snapshotMatchSQL

// AdoptIdentityIfUnchanged is the adoption write (SR-3.6; LFR H2; Appendix
// F.3): when a lookup found Ours for a row that records no server identity or
// no pane (a lost create reply), the verb records what it found. It is one
// conditional statement that applies only while the row exists and its row
// snapshot equals examined, compared on the values exactly as stored
// (SR-5.3); every write advances row_version (SR-5.2), so an unchanged
// snapshot also means an unchanged state. It then writes the tmux server's
// identity (tmux_server_pid, tmux_server_started, tmux_server_starttime) and
// the agent's pane (pane_id, pane_pid, pane_starttime) from id, a zero value
// as NULL (so an adoption that found no pane records only the server
// identity), and advances row_version by exactly one. It never writes
// id.Token, id.Socket, the state or any other column, and emits no trail
// event: the verb records adopted on ad.provenance.disagree (SR-14).
//
// It returns CondApplied when the write applied; CondChanged, having written
// nothing, when the row exists but its snapshot differs; CondAbsent when no
// row has the id. A driver error is returned wrapped, with a zero CondResult,
// never as a CondResult value.
//
// Only kill, send-keys and pause call it (SR-3.6; LFR H2); find-missing
// writes its adoption through the snapshot-returning, live-state-guarded
// variant AdoptIdentityIfSameLife, which shares this write's identity-column
// assignment (adoptIdentitySet). read-pane, resume's pre-launch check, reuse
// and expire never write an adoption.
func (s *Store) AdoptIdentityIfUnchanged(instanceID string, examined RowSnapshot, id LaunchIdentity) (CondResult, error) {
	args := append(adoptIdentityArgs(id), instanceID)
	res, err := s.db.Exec(adoptIdentitySQL, append(args, snapshotMatchArgs(examined)...)...)
	if err != nil {
		return 0, fmt.Errorf("store: adopt identity: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: adopt identity rows affected: %w", err)
	}
	if n > 0 {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, "store: adopt identity")
}

// condNotApplied tells CondChanged from CondAbsent after a conditional write
// matched no row, by reading whether a row with the id exists (SR-5.3). It
// only reads. errPrefix names the write in a driver error.
func (s *Store) condNotApplied(instanceID, errPrefix string) (CondResult, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM spawns WHERE instance_id = ?`, instanceID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return CondAbsent, nil
	case err != nil:
		return 0, fmt.Errorf("%s: existence read: %w", errPrefix, err)
	}
	return CondChanged, nil
}

// SetParentID writes the parent_id column. No verb calls it: resume's parent
// id is written only by its move to pending (MoveToPending, SR-8.3). It is
// kept only for the test seeder apitest.SeedParentChild.
//
// An empty parent argument writes NULL (matches the original spawn
// path's "no caller env var" semantics). A non-empty value sets that
// id directly; the FK constraint with ON DELETE SET NULL means a
// later parent delete cascades naturally.
//
// A missing target row is treated as ErrSpawnNotFound so callers can
// distinguish "I asked to update a nonexistent row" from "the update
// silently no-op'd" (which would be the case if we just emitted an
// UPDATE without a row-count check).
//
// The write advances row_version by one and leaves launch_started_at
// unchanged (SR-5.2).
func (s *Store) SetParentID(instanceID, parentID string) error {
	const q = `UPDATE spawns SET parent_id = ?, ` + rowVersionAdvance + ` WHERE instance_id = ?`
	var parent any
	if parentID != "" {
		parent = parentID
	} else {
		parent = nil
	}
	res, err := s.execFamily(instanceID, q, parent, instanceID)
	if err != nil {
		return fmt.Errorf("store: set parent id: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set parent id rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	return nil
}

// encodeArgs serializes a string slice to a JSON array. nil → "[]" so the
// column always carries a valid JSON value.
func encodeArgs(args []string) (string, error) {
	if args == nil {
		return "[]", nil
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeArgs reads a JSON array column into a string slice. An empty
// string is treated as nil (the DEFAULT '[]' on the column means we
// rarely see this, but the test suite drops rows directly during setup).
func decodeArgs(blob string) ([]string, error) {
	if blob == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(blob), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// encodeLabels serializes a string map to a JSON object. nil → "{}".
func encodeLabels(labels map[string]string) (string, error) {
	if labels == nil {
		return "{}", nil
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeLabels reads a JSON object column into a string map. An empty
// string is treated as an empty map.
func decodeLabels(blob string) (map[string]string, error) {
	if blob == "" {
		return map[string]string{}, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(blob), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}

// encodeExtraEnv serializes the extra_env string map to a JSON object.
// Mirrors encodeLabels exactly: a nil map encodes to '{}', so the column
// always carries a valid JSON object and never NULL or an empty string
// (persist-all posture — no allowlist, no filtering; the store file is
// already 0600 in a 0700 dir, so no new exposure tier).
func encodeExtraEnv(extraEnv map[string]string) (string, error) {
	return encodeLabels(extraEnv)
}

// decodeExtraEnv reads the extra_env JSON object column (schema v3) into a
// string map. Mirrors decodeLabels exactly: an empty string, '{}', or a
// JSON null all decode to an empty NON-NIL map, so migrated pre-v3 rows and
// freshly inserted rows never nil-decode. Store-internal only — extra_env is
// never surfaced in any API row shape.
func decodeExtraEnv(blob string) (map[string]string, error) {
	return decodeLabels(blob)
}
