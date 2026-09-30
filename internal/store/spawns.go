package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrSpawnNotFound is returned by lookup-by-id methods when no row matches
// the supplied claude_instance_id. Callers use errors.Is to detect.
var ErrSpawnNotFound = errors.New("ErrSpawnNotFound")

// ErrPrimaryKeyCollision is returned by InsertPending when SQLite reports a
// PRIMARY KEY or UNIQUE constraint violation. Detected via *sqlite.Error
// Code() against SQLITE_CONSTRAINT_PRIMARYKEY / SQLITE_CONSTRAINT_UNIQUE,
// so callers don't string-match the driver's prose.
var ErrPrimaryKeyCollision = errors.New("store: primary key collision")

// State constants mirror the SRD §5.1 enum. They live here (the package
// that owns the column's text values) so the rest of the codebase has
// exactly one source of truth for valid state strings.
//
// StatePending means a launch (spawn, reuse or resume) is in progress and
// the agent has not reported in yet (Claude Code's SessionStart); it may be
// loading or waiting at a startup prompt, and a resumed pending row keeps its
// session id and history (SR-22.1). The writes that set it are a spawn's
// insert (InsertPending) and resume's move (MoveToPending). The agent's
// report-in (SessionStart, pending -> waiting) ends it, as does any other
// write that sets another state, such as find-missing's mark or a failed
// resume's restore (RestoreAfterFailedResume).
const (
	StatePending         = "pending"
	StateWaiting         = "waiting"
	StateWorking         = "working"
	StateAskUser         = "ask_user"
	StateCheckPermission = "check_permission"
	StateEnded           = "ended"
	StateMissing         = "missing"
)

// liveStates is the set of state values find-missing considers "alive"
// (anything except the terminal ones). The collision pre-check on a
// caller-supplied claude_instance_id tests the row's state against this set
// (IsLiveState) so an `ended` Spawn's id can be reused (the resume verb
// handles that case).
var liveStates = []string{
	StatePending, StateWaiting, StateWorking, StateAskUser, StateCheckPermission,
}

// IsLiveState reports whether state is one of the live (non-terminal)
// states in liveStates. It is a read-only view of that set for callers
// outside the package, such as test fixtures seeding a live row's defaults.
func IsLiveState(state string) bool {
	for _, st := range liveStates {
		if st == state {
			return true
		}
	}
	return false
}

// Spawn mirrors a row of the `spawns` table for callers outside this
// package. ClaudeArgs and Labels are presented as their materialized Go
// shapes; the JSON encoding stays inside the package.
type Spawn struct {
	ClaudeInstanceID string
	ParentID         string
	State            string
	CWD              string
	TmuxSessionName  string
	ClaudeArgs       []string
	RelayMode        string
	JSONLPath        string
	ClaudeSessionID  string
	Labels           map[string]string
	StartedAt        time.Time
	LastSeenAt       time.Time
	EndedAt          *time.Time

	// ExtraEnv holds the spawn's captured extra environment (schema v3,
	// extra_env column). Decoded like Labels: empty-string / '{}' / a
	// migrated pre-v3 NULL/default all decode to an empty NON-NIL map,
	// never nil. Store-internal only — never surfaced in any API row shape.
	ExtraEnv map[string]string

	// PID, ProcStarttime, LivenessUnverifiedSince, and LivenessNote are the
	// schema-v3 identity/liveness columns, scanned via COALESCE per the
	// jsonl_path/claude_session_id precedent.
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
	// returns a Spawn. InsertPending takes LaunchStartedAtMillis, NoPreTrust,
	// Identity.Token and Identity.Socket from a Spawn; no write takes the
	// others from one. The SR-5.5 columns never fail a read.

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
	// than the integer 0 (SR-5.5). The insert records the spawn caller's
	// pre-trust choice here (1 = opted out, 0 = allowed); resume's move and
	// restore, hooks, find-missing and every other write leave it unchanged
	// (SR-5.2), so every resume of a life follows the spawn that began it.
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
}

// InsertPending writes a new row in `pending` state. Used by spawn.Launch
// (SRD §7.4 step 2). The caller is expected to have already validated the
// row and minted any defaults — InsertPending does no semantic checks
// beyond what SQLite's constraints enforce.
//
// On PRIMARY KEY collision (claude_instance_id already exists) the error
// chain contains the bare driver error; spawn.Launch maps this back to
// ErrInstanceIdCollision for surface parity with the TOCTOU pre-check.
//
// The insert is one of the writes that begin a launch (SR-5.2): in the same
// statement it writes launch_started_at from sp.LaunchStartedAtMillis
// (milliseconds from the caller's injected clock), launch_token from
// sp.Identity.Token and tmux_socket from sp.Identity.Socket. A zero value
// writes NULL, following LaunchIdentity's zero-means-NULL convention. It
// also records the caller's pre-trust choice: no_pre_trust is 1 when
// sp.NoPreTrust is true and 0 when it is false. No later write changes that
// column (see Spawn.NoPreTrust), so the choice holds for the row's life. The
// server and pane identity columns (tmux_server_pid, tmux_server_started,
// tmux_server_starttime, pane_id, pane_pid, pane_starttime) stay NULL
// whatever sp.Identity carries: RecordLaunchIdentity writes them after the
// create (SR-3.6). The new row starts at row_version 0 (the column default).
func (s *Store) InsertPending(sp Spawn) error {
	argsJSON, err := encodeArgs(sp.ClaudeArgs)
	if err != nil {
		return fmt.Errorf("store: encode claude_args: %w", err)
	}
	labelsJSON, err := encodeLabels(sp.Labels)
	if err != nil {
		return fmt.Errorf("store: encode labels: %w", err)
	}
	extraEnvJSON, err := encodeExtraEnv(sp.ExtraEnv)
	if err != nil {
		return fmt.Errorf("store: encode extra_env: %w", err)
	}

	const stmt = `
        INSERT INTO spawns (
            claude_instance_id, parent_id, state, cwd, tmux_session_name,
            claude_args, relay_mode, labels, extra_env,
            launch_started_at, launch_token, tmux_socket, no_pre_trust
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
	_, err = s.db.Exec(stmt,
		sp.ClaudeInstanceID, parent, StatePending,
		sp.CWD, sp.TmuxSessionName,
		argsJSON, sp.RelayMode, labelsJSON, extraEnvJSON,
		positiveInt64Arg(sp.LaunchStartedAtMillis),
		nullableStringArg(sp.Identity.Token), nullableStringArg(sp.Identity.Socket),
		noPreTrust,
	)
	if err != nil {
		var serr *sqlite.Error
		if errors.As(err, &serr) {
			switch serr.Code() {
			case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_UNIQUE:
				return fmt.Errorf("%w: %s", ErrPrimaryKeyCollision, sp.ClaudeInstanceID)
			}
		}
		return fmt.Errorf("store: insert pending: %w", err)
	}
	return nil
}

// GetSpawn returns the full row for the given claude_instance_id. Missing
// rows yield ErrSpawnNotFound; other failures wrap the driver error.
func (s *Store) GetSpawn(instanceID string) (Spawn, error) {
	q := `SELECT ` + spawnColumns + ` FROM spawns WHERE claude_instance_id = ?`
	sp, err := scanSpawn(s.db.QueryRow(q, instanceID), getSpawnErrs)
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return Spawn{}, err
	}
	return sp, nil
}

// spawnColumns is the one column list every read returning a Spawn selects,
// in scanSpawn's order. The v5 columns come last so the pre-v5 columns keep
// their indices (and their scan-error texts). started_at and ended_at are
// selected a second time through CAST so the driver hands back the stored
// text instead of parsing the TIMESTAMP column (SR-5.3). The three SR-5.5
// columns are selected bare and decoded in Go; the other v5 columns follow
// the v3 identity columns' rule (NULL is the zero value; any other stored
// value scans normally).
const spawnColumns = `
        claude_instance_id, COALESCE(parent_id, ''), state, cwd,
        tmux_session_name, claude_args, relay_mode,
        COALESCE(jsonl_path, ''), COALESCE(claude_session_id, ''),
        labels, started_at, last_seen_at, ended_at,
        COALESCE(pid, 0), COALESCE(proc_starttime, ''),
        COALESCE(liveness_unverified_since, ''),
        COALESCE(liveness_note, ''), extra_env,
        CAST(started_at AS TEXT), CAST(ended_at AS TEXT),
        COALESCE(row_version, 0), launch_started_at,
        COALESCE(life_number, 0), no_pre_trust, launch_token,
        COALESCE(tmux_socket, ''), COALESCE(tmux_server_pid, 0),
        COALESCE(tmux_server_started, 0), COALESCE(tmux_server_starttime, ''),
        COALESCE(pane_id, ''), COALESCE(pane_pid, 0),
        COALESCE(pane_starttime, '')`

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
// started_at/ended_at or malformed labels, claude_args or extra_env fails the
// read); the SR-5.5 columns never fail it. The Scan error is wrapped with %w,
// so callers can still detect sql.ErrNoRows.
func scanSpawn(sc rowScanner, errs spawnReadErrs) (Spawn, error) {
	var (
		sp              Spawn
		argsJSON        string
		labelsJSON      string
		endedAt         sql.NullTime
		extraEnvJSON    string
		startedAtText   string
		endedAtText     sql.NullString
		launchStartedAt any
		noPreTrust      any
		launchToken     any
	)
	err := sc.Scan(
		&sp.ClaudeInstanceID, &sp.ParentID, &sp.State, &sp.CWD,
		&sp.TmuxSessionName, &argsJSON, &sp.RelayMode,
		&sp.JSONLPath, &sp.ClaudeSessionID,
		&labelsJSON, &sp.StartedAt, &sp.LastSeenAt, &endedAt,
		&sp.PID, &sp.ProcStarttime, &sp.LivenessUnverifiedSince,
		&sp.LivenessNote, &extraEnvJSON,
		&startedAtText, &endedAtText,
		&sp.RowVersion, &launchStartedAt,
		&sp.LifeNumber, &noPreTrust, &launchToken,
		&sp.Identity.Socket, &sp.Identity.ServerPID,
		&sp.Identity.ServerStart, &sp.Identity.ServerStarttime,
		&sp.Identity.PaneID, &sp.Identity.PanePID,
		&sp.Identity.PaneStarttime,
	)
	if err != nil {
		return Spawn{}, fmt.Errorf("%s: %w", errs.scan, err)
	}
	if endedAt.Valid {
		t := endedAt.Time
		sp.EndedAt = &t
	}
	if sp.ClaudeArgs, err = decodeArgs(argsJSON); err != nil {
		return Spawn{}, fmt.Errorf("%s claude_args: %w", errs.decode, err)
	}
	if sp.Labels, err = decodeLabels(labelsJSON); err != nil {
		return Spawn{}, fmt.Errorf("%s labels: %w", errs.decode, err)
	}
	if sp.ExtraEnv, err = decodeExtraEnv(extraEnvJSON); err != nil {
		return Spawn{}, fmt.Errorf("%s extra_env: %w", errs.decode, err)
	}
	sp.LaunchStartedAtMillis = decodeLaunchStartedAt(launchStartedAt)
	sp.NoPreTrust = decodeNoPreTrust(noPreTrust)
	sp.Identity.Token = decodeLaunchToken(launchToken)
	sp.EndedAtText = endedAtText.String
	sp.Snapshot = RowSnapshot{
		RowVersion:      sp.RowVersion,
		StartedAt:       startedAtText,
		ClaudeSessionID: sp.ClaudeSessionID,
		PID:             sp.PID,
		ProcStarttime:   sp.ProcStarttime,
		TmuxSessionName: sp.TmuxSessionName,
	}
	return sp, nil
}

// GetSpawnState is a narrow lookup returning only the state column. It
// serves pause's wait loop and api.Status's fallback for a store without
// SpawnStatus; production Status reads through SpawnStatus instead.
func (s *Store) GetSpawnState(instanceID string) (string, error) {
	const q = `SELECT state FROM spawns WHERE claude_instance_id = ?`
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
// to 9999 UTC all read as 0 (decodeLaunchStartedAt, SR-5.5).
func (s *Store) SpawnStatus(instanceID string) (state string, launchStartedAtMillis int64, err error) {
	const q = `SELECT state, launch_started_at FROM spawns WHERE claude_instance_id = ?`
	var launchStartedAt any
	err = s.db.QueryRow(q, instanceID).Scan(&state, &launchStartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return "", 0, fmt.Errorf("store: spawn status: %w", err)
	}
	return state, decodeLaunchStartedAt(launchStartedAt), nil
}

// SpawnState returns the state of the row with the given claude_instance_id
// in one read, with exists false and no error when there is no such row. It
// is the collision pre-check's read (spawn.CollisionChecker; SR-9.3): it
// tells no row, a live row (IsLiveState) and a finished row apart. err is
// set only when the store cannot be read.
func (s *Store) SpawnState(instanceID string) (state string, exists bool, err error) {
	const q = `SELECT state FROM spawns WHERE claude_instance_id = ?`
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
	err := s.db.QueryRow(`SELECT state FROM spawns WHERE claude_instance_id = ?`, instanceID).Scan(&state)
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
// server and pane identity columns and the version advance, guarded by the
// launch's state, version and token (SR-3.6, SR-5.3).
const recordLaunchIdentitySQL = `UPDATE spawns
    SET tmux_server_pid       = ?,
        tmux_server_started   = ?,
        tmux_server_starttime = ?,
        pane_id               = ?,
        pane_pid              = ?,
        pane_starttime        = ?,
        ` + rowVersionAdvance + `
  WHERE claude_instance_id = ? AND state = ? AND row_version = ? AND launch_token = ?`

// RecordLaunchIdentity is the identity write after a create reply (SR-3.6,
// SR-5.3). It applies only while the row is pending with row_version equal to
// launchVersion (the version the write that began the launch produced) and
// launch_token equal to token. It then writes, in one statement, the tmux
// server's identity (tmux_server_pid, tmux_server_started,
// tmux_server_starttime) and the agent's pane (pane_id, pane_pid,
// pane_starttime) from id, a zero value as NULL (an unreadable start time
// passed as "" is stored as NULL), and advances row_version by one. It never
// writes id.Token, id.Socket, launch_started_at, state or any other column.
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

// condNotApplied tells CondChanged from CondAbsent after a conditional write
// matched no row, by reading whether a row with the id exists (SR-5.3). It
// only reads. errPrefix names the write in a driver error.
func (s *Store) condNotApplied(instanceID, errPrefix string) (CondResult, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM spawns WHERE claude_instance_id = ?`, instanceID).Scan(&one)
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
	const q = `UPDATE spawns SET parent_id = ?, ` + rowVersionAdvance + ` WHERE claude_instance_id = ?`
	var parent any
	if parentID != "" {
		parent = parentID
	} else {
		parent = nil
	}
	res, err := s.db.Exec(q, parent, instanceID)
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
