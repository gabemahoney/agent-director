package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ReuseRow is reuse's pre-check read of one row (SR-10.2, SR-10.3, Appendix
// F.4): what spawn --reuse-finished examines before it resets the row, and
// the pre-reuse life it keeps for the restore (SR-10.4). It is read without
// decoding labels, claude_args or extra_env and without failing on any stored
// value of the SR-5.5 columns or the timestamp columns, so a row a hand edit
// made malformed can still be reused and restored byte for byte (SR-5.5).
type ReuseRow struct {
	// State is the row's state.
	State string
	// EndedAtText is ended_at exactly as stored, never parsed and
	// re-formatted; "" = NULL (SR-5.3).
	EndedAtText string
	// EndedAt is ended_at as the driver parses it, the way GetSpawn's
	// Spawn.EndedAt is read; nil when ended_at is NULL or holds a value the
	// driver does not parse as a time, so an unparseable ended_at never fails
	// the read (SR-5.5).
	EndedAt *time.Time
	// Snapshot is the row snapshot reuse examined: its reset applies only
	// while the row still holds it (SR-5.3, SR-10.3). It equals GetSpawn's
	// Snapshot for the same row.
	Snapshot RowSnapshot
	// Identity is the row's launch identity (SR-3.6). Identity.Token is ""
	// unless the stored token is well formed (SR-5.5). It equals GetSpawn's
	// Identity for the same row.
	Identity LaunchIdentity
	// Life is the pre-reuse life exactly as stored, which a failed reuse's
	// restore writes back (SR-10.4).
	Life RawLife
}

// RawLife is a row's pre-reuse life exactly as stored (SR-5.3, SR-5.5,
// SR-10.3, SR-10.4): every spawns column except claude_instance_id,
// row_version and launch_started_at, each with its stored value and SQLite
// storage class, NULL distinct from an empty value. Its fields are unexported:
// only ReadForReuse fills it, and only RestoreAfterFailedReuse reads it. The
// zero RawLife was not produced by ReadForReuse, and the restore refuses it.
type RawLife struct {
	state                   rawValue
	lifeNumber              rawValue
	noPreTrust              rawValue
	claudeSessionID         rawValue
	jsonlPath               rawValue
	pid                     rawValue
	procStarttime           rawValue
	startedAt               rawValue
	lastSeenAt              rawValue
	livenessUnverifiedSince rawValue
	livenessNote            rawValue
	endedAt                 rawValue
	cwd                     rawValue
	tmuxSessionName         rawValue
	claudeArgs              rawValue
	relayMode               rawValue
	labels                  rawValue
	extraEnv                rawValue
	parentID                rawValue
	launchToken             rawValue
	tmuxSocket              rawValue
	tmuxServerPID           rawValue
	tmuxServerStarted       rawValue
	tmuxServerStarttime     rawValue
	paneID                  rawValue
	panePID                 rawValue
	paneStarttime           rawValue
}

// rawValue is one column's value exactly as stored: class is the column's
// typeof() ("null", "integer", "real", "text" or "blob"; "" only in a zero
// RawLife) and v the driver's value for that class (nil, int64, float64,
// string or []byte). The column is read through unary + so its declared type
// is not reported and the driver never converts TIMESTAMP text to a
// time.Time.
type rawValue struct {
	class string
	v     any
}

// arg returns the value to bind so the column stores it again with the same
// storage class and bytes. The driver reads a zero-length blob as nil, which
// would bind as NULL, so a blob is always bound as a non-nil []byte. A value
// read from a column and written back to it keeps its storage class under
// the column's affinity, since affinity already applied when it was stored.
func (r rawValue) arg() any {
	if r.class == "blob" {
		b, _ := r.v.([]byte)
		if b == nil {
			b = []byte{}
		}
		return b
	}
	return r.v
}

// rawLifeColumn names one RawLife column and its field.
type rawLifeColumn struct {
	name  string
	field func(*RawLife) *rawValue
}

// rawLifeColumns lists every RawLife column, in the order ReadForReuse
// selects them.
var rawLifeColumns = []rawLifeColumn{
	{"state", func(l *RawLife) *rawValue { return &l.state }},
	{"life_number", func(l *RawLife) *rawValue { return &l.lifeNumber }},
	{"no_pre_trust", func(l *RawLife) *rawValue { return &l.noPreTrust }},
	{"claude_session_id", func(l *RawLife) *rawValue { return &l.claudeSessionID }},
	{"jsonl_path", func(l *RawLife) *rawValue { return &l.jsonlPath }},
	{"pid", func(l *RawLife) *rawValue { return &l.pid }},
	{"proc_starttime", func(l *RawLife) *rawValue { return &l.procStarttime }},
	{"started_at", func(l *RawLife) *rawValue { return &l.startedAt }},
	{"last_seen_at", func(l *RawLife) *rawValue { return &l.lastSeenAt }},
	{"liveness_unverified_since", func(l *RawLife) *rawValue { return &l.livenessUnverifiedSince }},
	{"liveness_note", func(l *RawLife) *rawValue { return &l.livenessNote }},
	{"ended_at", func(l *RawLife) *rawValue { return &l.endedAt }},
	{"cwd", func(l *RawLife) *rawValue { return &l.cwd }},
	{"tmux_session_name", func(l *RawLife) *rawValue { return &l.tmuxSessionName }},
	{"claude_args", func(l *RawLife) *rawValue { return &l.claudeArgs }},
	{"relay_mode", func(l *RawLife) *rawValue { return &l.relayMode }},
	{"labels", func(l *RawLife) *rawValue { return &l.labels }},
	{"extra_env", func(l *RawLife) *rawValue { return &l.extraEnv }},
	{"parent_id", func(l *RawLife) *rawValue { return &l.parentID }},
	{"launch_token", func(l *RawLife) *rawValue { return &l.launchToken }},
	{"tmux_socket", func(l *RawLife) *rawValue { return &l.tmuxSocket }},
	{"tmux_server_pid", func(l *RawLife) *rawValue { return &l.tmuxServerPID }},
	{"tmux_server_started", func(l *RawLife) *rawValue { return &l.tmuxServerStarted }},
	{"tmux_server_starttime", func(l *RawLife) *rawValue { return &l.tmuxServerStarttime }},
	{"pane_id", func(l *RawLife) *rawValue { return &l.paneID }},
	{"pane_pid", func(l *RawLife) *rawValue { return &l.panePID }},
	{"pane_starttime", func(l *RawLife) *rawValue { return &l.paneStarttime }},
}

// rawLifeSelect is the column fragment that reads rawLifeColumns, each as
// typeof(col), +col, in rawLifeDest's order.
var rawLifeSelect = func() string {
	parts := make([]string, len(rawLifeColumns))
	for i, c := range rawLifeColumns {
		parts[i] = "typeof(" + c.name + "), +" + c.name
	}
	return strings.Join(parts, ",\n        ")
}()

// rawLifeDest returns the Scan destinations for rawLifeSelect, in its order.
func rawLifeDest(l *RawLife) []any {
	dest := make([]any, 0, 2*len(rawLifeColumns))
	for _, c := range rawLifeColumns {
		f := c.field(l)
		dest = append(dest, &f.class, &f.v)
	}
	return dest
}

// readForReuseSQL is ReadForReuse's one statement: the state, ended_at as
// the driver parses it and as stored text, the shared snapshot and identity
// fragment, and the raw life, by primary key.
var readForReuseSQL = `SELECT state, ended_at, CAST(ended_at AS TEXT),` + lifeColumns + `,
        ` + rawLifeSelect + `
   FROM spawns
  WHERE claude_instance_id = ?`

// ReadForReuse is reuse's pre-check read (SR-10.2, SR-10.3, SR-5.5; Appendix
// F.4): one SELECT of the row with the given id by primary key. found is
// false, with no error, when no row has the id. The snapshot and launch
// identity are read through lifeColumns, the fragment GetSpawn selects, so
// they equal GetSpawn's for the same row. Life captures every column the
// restore writes back exactly as stored (RawLife). labels, claude_args and
// extra_env are never decoded, and no stored started_at, ended_at,
// launch_started_at, launch_token or no_pre_trust value fails the read
// (SR-5.5). A driver error is returned wrapped. It writes nothing and emits no
// trail event.
func (s *Store) ReadForReuse(instanceID string) (row ReuseRow, found bool, err error) {
	var (
		endedAt     any
		endedAtText sql.NullString
		life        lifeScan
	)
	dest := append([]any{&row.State, &endedAt, &endedAtText}, life.dest()...)
	dest = append(dest, rawLifeDest(&row.Life)...)
	err = s.db.QueryRow(readForReuseSQL, instanceID).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return ReuseRow{}, false, nil
	}
	if err != nil {
		return ReuseRow{}, false, fmt.Errorf("store: read for reuse: %w", err)
	}
	if t, ok := endedAt.(time.Time); ok {
		row.EndedAt = &t
	}
	row.EndedAtText = endedAtText.String
	row.Snapshot, row.Identity = life.result()
	return row, true, nil
}

// resetForReuseReadSQL is ResetForReuse's read inside its transaction: the
// session to archive, the pre-reset life, and whether the row is finished
// with the examined snapshot, judged by the shared finished guard and
// snapshot-match condition. Its placeholders take finishedStateGuardArgs,
// snapshotMatchArgs and the id, in that order.
const resetForReuseReadSQL = `SELECT COALESCE(claude_session_id, ''), COALESCE(jsonl_path, ''), life_number,
        CASE WHEN ` + finishedStateGuardSQL + ` AND ` + snapshotMatchSQL + ` THEN 1 ELSE 0 END
   FROM spawns
  WHERE claude_instance_id = ?`

// resetForReuseSQL is ResetForReuse's reset: the finished row set to pending
// in a new life, guarded by the finished state and the examined snapshot
// (SR-10.3, SR-5.3).
const resetForReuseSQL = `UPDATE spawns
    SET state                     = ?,
        claude_session_id         = NULL,
        jsonl_path                = NULL,
        pid                       = NULL,
        proc_starttime            = NULL,
        liveness_unverified_since = NULL,
        liveness_note             = NULL,
        ended_at                  = NULL,
        started_at                = ?,
        last_seen_at              = ?,
        launch_started_at         = ?,
        cwd                       = ?,
        tmux_session_name         = ?,
        claude_args               = ?,
        relay_mode                = ?,
        labels                    = ?,
        extra_env                 = ?,
        parent_id                 = ?,
        no_pre_trust              = ?,
        launch_token              = ?,
        tmux_socket               = ?,
        tmux_server_pid           = NULL,
        tmux_server_started       = NULL,
        tmux_server_starttime     = NULL,
        pane_id                   = NULL,
        pane_pid                  = NULL,
        pane_starttime            = NULL,
        life_number               = life_number + 1,
        ` + rowVersionAdvance + `
  WHERE claude_instance_id = ? AND ` + finishedStateGuardSQL + ` AND ` + snapshotMatchSQL

// resetForReuseDeleteRequestsSQL is ResetForReuse's deletion of every
// permission request of the id, decided or not (SR-10.3).
const resetForReuseDeleteRequestsSQL = `DELETE FROM permission_requests WHERE claude_instance_id = ?`

// ResetForReuse is reuse's change of a finished row to a new life, the write
// that begins its launch (SR-10.3, SR-5.3, SR-5.6, SR-5.8, SR-5.9; Appendix
// F.4). Its archive, reset and permission-request deletion commit as one
// transaction or not at all. The transaction is begun with BEGIN IMMEDIATE on
// the store's one connection, so it holds the database write lock before it
// reads the row and never fails because another process committed after its
// read began; under contention it waits up to the store's busy timeout. No
// other store transaction's locking changes.
//
// Inside it, it reads the row's session id, transcript path and life_number
// (never labels, claude_args or extra_env, so a malformed row can be reset).
// No row: CondAbsent. A row that is not ended or missing, or whose snapshot
// differs from examined (the shared finished guard and snapshot-match
// condition, compared on the values exactly as stored): CondChanged. Both
// write nothing and return archivedSessionID "" and resetVersion 0.
//
// Otherwise, in order:
//   - Archive: when the row has a session id, it is archived in the row's
//     pre-reset life through the one session-history upsert
//     (upsertSessionHistoryEntry): a new entry takes the current path; an
//     entry of the same life keeps its path unless the current one is
//     non-empty; an entry of another life moves to the ending life and takes
//     the current path exactly, NULL included. Entries stay keyed on (instance
//     id, session id) and are never duplicated.
//   - Reset, one UPDATE still guarded on the finished state and examined, and
//     required to change exactly one row: state pending; claude_session_id,
//     jsonl_path, pid, proc_starttime, liveness_unverified_since,
//     liveness_note, ended_at and the six server and pane identity columns
//     NULL, whatever fresh.Identity carries; started_at and last_seen_at
//     fresh.StartedAt in the store's CURRENT_TIMESTAMP layout;
//     launch_started_at fresh.LaunchStartedAtMillis; cwd, tmux_session_name,
//     claude_args, relay_mode, labels and extra_env from fresh, encoded before
//     the transaction begins; parent_id fresh.ParentID ("" = NULL);
//     no_pre_trust this call's fresh.NoPreTrust; launch_token and tmux_socket
//     from fresh.Identity (zero = NULL); life_number and row_version each
//     advanced by one. The store reads no clock: the caller supplies
//     fresh.StartedAt and fresh.LaunchStartedAtMillis.
//   - Permission requests: every request of the id, decided or not, is
//     deleted. Children keep their parent_id; no other row changes.
//
// Applied, it returns CondApplied, the archived session id ("" when the row
// had none) and resetVersion = examined.RowVersion + 1, the version the reset
// produced, which the restore's condition takes.
//
// Any failure rolls everything back, leaves the row, its history and its
// permission requests as they were, and returns a wrapped error with a zero
// CondResult. An archive failure's error holds ErrReuseArchive (errors.Is),
// so the caller tells it apart; every other failure (an encoding failure, a
// parentID naming no row, the reset or deletion, a busy store past its
// timeout, the commit) does not. Neither wraps a catalogued sentinel (SR-1.5).
// It emits no trail event.
func (s *Store) ResetForReuse(instanceID string, examined RowSnapshot, fresh Spawn) (res CondResult, archivedSessionID string, resetVersion int64, err error) {
	const errPrefix = "store: reset for reuse"
	argsJSON, err := encodeArgs(fresh.ClaudeArgs)
	if err != nil {
		return 0, "", 0, fmt.Errorf("%s: encode claude_args: %w", errPrefix, err)
	}
	labelsJSON, err := encodeLabels(fresh.Labels)
	if err != nil {
		return 0, "", 0, fmt.Errorf("%s: encode labels: %w", errPrefix, err)
	}
	extraEnvJSON, err := encodeExtraEnv(fresh.ExtraEnv)
	if err != nil {
		return 0, "", 0, fmt.Errorf("%s: encode extra_env: %w", errPrefix, err)
	}
	noPreTrust := 0
	if fresh.NoPreTrust {
		noPreTrust = 1
	}

	// The pool has one connection: everything below runs on conn, never
	// through s.db (condNotApplied, GetSpawn), which would wait for it.
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, "", 0, fmt.Errorf("%s: connection: %w", errPrefix, err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, "", 0, fmt.Errorf("%s: begin: %w", errPrefix, err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackConn(ctx, conn)
		}
	}()

	var (
		sessionID, jsonlPath string
		life                 int64
		matches              int
	)
	readArgs := append(finishedStateGuardArgs(), snapshotMatchArgs(examined)...)
	readArgs = append(readArgs, instanceID)
	err = conn.QueryRowContext(ctx, resetForReuseReadSQL, readArgs...).Scan(&sessionID, &jsonlPath, &life, &matches)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return CondAbsent, "", 0, nil
	case err != nil:
		return 0, "", 0, fmt.Errorf("%s: read: %w", errPrefix, err)
	case matches == 0:
		return CondChanged, "", 0, nil
	}

	q := connQuerier{ctx: ctx, conn: conn}
	if sessionID != "" {
		if err := upsertSessionHistoryEntry(q, instanceID, sessionID, jsonlPath, life); err != nil {
			return 0, "", 0, fmt.Errorf("%s: %w: %w", errPrefix, ErrReuseArchive, err)
		}
	}

	startedAt := storeTimestamp(fresh.StartedAt)
	resetArgs := []any{
		StatePending,
		startedAt, startedAt,
		positiveInt64Arg(fresh.LaunchStartedAtMillis),
		fresh.CWD, fresh.TmuxSessionName,
		argsJSON, fresh.RelayMode, labelsJSON, extraEnvJSON,
		nullableStringArg(fresh.ParentID),
		noPreTrust,
		nullableStringArg(fresh.Identity.Token), nullableStringArg(fresh.Identity.Socket),
		instanceID,
	}
	resetArgs = append(resetArgs, finishedStateGuardArgs()...)
	resetArgs = append(resetArgs, snapshotMatchArgs(examined)...)
	r, err := q.Exec(resetForReuseSQL, resetArgs...)
	if err != nil {
		return 0, "", 0, fmt.Errorf("%s: reset: %w", errPrefix, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, "", 0, fmt.Errorf("%s: reset rows affected: %w", errPrefix, err)
	}
	if n != 1 {
		return 0, "", 0, fmt.Errorf("%s: reset changed %d rows, want 1", errPrefix, n)
	}

	if _, err := q.Exec(resetForReuseDeleteRequestsSQL, instanceID); err != nil {
		return 0, "", 0, fmt.Errorf("%s: delete permission requests: %w", errPrefix, err)
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, "", 0, fmt.Errorf("%s: commit: %w", errPrefix, err)
	}
	committed = true
	return CondApplied, sessionID, examined.RowVersion + 1, nil
}

// restoredVerbatim reports whether the restore writes a RawLife column back
// exactly as stored; ended_at and parent_id have their own rules.
func restoredVerbatim(name string) bool {
	return name != "ended_at" && name != "parent_id"
}

// restoreAfterFailedReuseSQL is RestoreAfterFailedReuse's one statement
// (SR-10.4, SR-5.6): the pre-reuse life written back, ended_at and parent_id
// by their rules, the launch start cleared and the version advanced, guarded
// by pending and the reset's version. The verbatim columns' placeholders come
// first, in rawLifeColumns' order.
var restoreAfterFailedReuseSQL = func() string {
	var set []string
	for _, c := range rawLifeColumns {
		if restoredVerbatim(c.name) {
			set = append(set, c.name+" = ?")
		}
	}
	return `UPDATE spawns
    SET ` + strings.Join(set, ",\n        ") + `,
        ended_at  = ?,
        parent_id = (SELECT claude_instance_id FROM spawns WHERE claude_instance_id = ?),
        ` + launchStartClear + `,
        ` + rowVersionAdvance + `
  WHERE claude_instance_id = ? AND state = ? AND row_version = ?`
}()

// RestoreAfterFailedReuse undoes reuse's change after a launch that failed
// other than by a timeout (SR-10.4, SR-5.2, SR-5.3, SR-5.9; Appendix F.4). It
// is one conditional statement (SR-5.6) that applies only while the row is
// pending with row_version equal to resetVersion, the version ResetForReuse
// produced. In that statement it writes back every column of prior, the
// pre-reuse life ReadForReuse captured, with its stored value and storage
// class, never parsed or re-formatted (state, life_number, no_pre_trust,
// claude_session_id, jsonl_path, pid, proc_starttime, started_at,
// last_seen_at, liveness_unverified_since, liveness_note, cwd,
// tmux_session_name, claude_args, relay_mode, labels, extra_env,
// launch_token, tmux_socket and the six server and pane identity columns), so
// the row returns to its pre-reuse life and that life's history is visible
// again (SR-5.9). ended_at is prior's as stored, or failedAt in the store's
// CURRENT_TIMESTAMP layout when it was NULL. parent_id is prior's only while a
// row with that id still exists, otherwise NULL, decided in the same
// statement, so the restore never fails on the foreign key.
// launch_started_at is set to NULL and row_version advanced by one. It never
// touches session_history or permission_requests: the reuse's archived entry
// stays, and the deleted requests stay deleted. It emits no trail event.
//
// A prior not produced by ReadForReuse (the zero RawLife), or whose state is
// not ended or missing, is a caller error: an error is returned and nothing
// is written. Otherwise it returns CondApplied when the write applied;
// CondChanged when the row exists but is no longer pending at resetVersion
// (another agent-director write came first; no hook can while the row records
// no pane, and the reset cleared it, SR-22.9); CondAbsent when no row has the
// id. A store failure returns a wrapped error with a zero CondResult, and the
// row stays as the reset left it (SR-5.8).
func (s *Store) RestoreAfterFailedReuse(instanceID string, resetVersion int64, prior RawLife, failedAt time.Time) (CondResult, error) {
	const errPrefix = "store: restore after failed reuse"
	if prior.state.class == "" {
		return 0, fmt.Errorf("%s: prior life was not read by ReadForReuse", errPrefix)
	}
	if st, _ := prior.state.v.(string); st != StateEnded && st != StateMissing {
		return 0, fmt.Errorf("%s: prior state %v is not %s or %s", errPrefix, prior.state.v, StateEnded, StateMissing)
	}
	args := make([]any, 0, len(rawLifeColumns)+3)
	for _, c := range rawLifeColumns {
		if restoredVerbatim(c.name) {
			args = append(args, c.field(&prior).arg())
		}
	}
	endedAt := prior.endedAt.arg()
	if prior.endedAt.class == "null" {
		endedAt = storeTimestamp(failedAt)
	}
	args = append(args, endedAt, prior.parentID.arg(), instanceID, StatePending, resetVersion)
	applied, err := s.execGuarded(restoreAfterFailedReuseSQL, args, errPrefix)
	if err != nil {
		return 0, err
	}
	if applied {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, errPrefix)
}

// rollbackConn rolls back the transaction begun by hand on conn. If the
// ROLLBACK fails, the connection may still hold the transaction (or SQLite
// already ended it), so the connection is discarded rather than returned to
// the pool, and the pool dials a fresh one.
func rollbackConn(ctx context.Context, conn *sql.Conn) {
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}
