package store

import "fmt"

// ResumePrior holds what resume's move to pending clears, exactly as stored,
// so a failed launch's restore writes it back byte for byte (SR-8.5, SR-5.3).
// Zero values mean NULL. Every field is on the Spawn GetSpawn returns, so the
// caller builds it from the row it examined before the move.
type ResumePrior struct {
	State                   string // ended or missing
	EndedAtText             string // ended_at as stored text; "" = NULL
	PID                     int    // 0 = NULL
	ProcStarttime           string // "" = NULL
	LivenessUnverifiedSince string // "" = NULL
	LivenessNote            string // "" = NULL
	// Identity is the pre-move launch_token, tmux_socket and server and pane
	// identity, as stored, so the restore writes the previous token back and
	// the label of the row's earlier launch stays current (amendment 1; LFR H3).
	Identity LaunchIdentity
}

// moveToPendingSQL is MoveToPending's one statement (SR-8.3, SR-5.6): the
// move to pending with its cleared and set columns and the version advance,
// guarded by a finished state and the examined row snapshot (SR-5.3).
const moveToPendingSQL = `UPDATE spawns
    SET state                     = ?,
        pid                       = NULL,
        proc_starttime            = NULL,
        ended_at                  = NULL,
        liveness_unverified_since = NULL,
        liveness_note             = NULL,
        tmux_server_pid           = NULL,
        tmux_server_started       = NULL,
        tmux_server_starttime     = NULL,
        pane_id                   = NULL,
        pane_pid                  = NULL,
        pane_starttime            = NULL,
        launch_started_at         = ?,
        launch_token              = ?,
        tmux_socket               = ?,
        parent_id                 = ?,
        ` + rowVersionAdvance + `
  WHERE claude_instance_id = ? AND state IN (?, ?) AND ` + snapshotMatchSQL

// MoveToPending is resume's move of a finished row to pending, the write that
// begins its launch (SR-8.3). It is one conditional statement (SR-5.6) that
// applies only while the row is ended or missing and its row snapshot equals
// examined, compared on the values exactly as stored (SR-5.3). In that
// statement it sets state pending; clears pid, proc_starttime, ended_at,
// liveness_unverified_since, liveness_note and the six server and pane
// identity columns; sets launch_started_at to launchStartedAtMillis (the
// caller's clock; the store reads none), launch_token to token, tmux_socket to
// socket and parent_id to parentID, each zero value written as NULL; and
// advances row_version by one. Every other column (claude_session_id,
// jsonl_path, life_number, no_pre_trust, started_at, last_seen_at and the
// request fields) is unchanged, session_history and permission_requests are
// not touched, and no trail event is emitted.
//
// It returns CondApplied with movedVersion, the version the write produced
// (examined.RowVersion + 1), which the restore's condition takes; CondChanged
// when the row exists but is live, pending or differs from examined; CondAbsent
// when no row has the id. Neither writes anything and both return movedVersion
// 0. A store failure, a foreign-key failure on a parentID that names no row
// included, returns a wrapped error with a zero CondResult and writes nothing
// (SR-5.8).
func (s *Store) MoveToPending(instanceID string, examined RowSnapshot, launchStartedAtMillis int64, token, socket, parentID string) (res CondResult, movedVersion int64, err error) {
	const errPrefix = "store: move to pending"
	args := []any{
		StatePending,
		positiveInt64Arg(launchStartedAtMillis),
		nullableStringArg(token), nullableStringArg(socket),
		nullableStringArg(parentID),
		instanceID, StateEnded, StateMissing,
	}
	args = append(args, snapshotMatchArgs(examined)...)
	r, err := s.db.Exec(moveToPendingSQL, args...)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("%s rows affected: %w", errPrefix, err)
	}
	if n > 0 {
		// The condition pinned row_version to examined.RowVersion and the
		// statement advanced it by exactly one.
		return CondApplied, examined.RowVersion + 1, nil
	}
	res, err = s.condNotApplied(instanceID, errPrefix)
	return res, 0, err
}

// restoreAfterFailedResumeSQL is RestoreAfterFailedResume's one statement
// (SR-8.5, SR-5.6): what the move cleared or overwrote, written back, the
// launch start cleared and the version advanced, guarded by pending and the
// move's version.
const restoreAfterFailedResumeSQL = `UPDATE spawns
    SET state                     = ?,
        ended_at                  = ?,
        pid                       = ?,
        proc_starttime            = ?,
        liveness_unverified_since = ?,
        liveness_note             = ?,
        launch_token              = ?,
        tmux_socket               = ?,
        tmux_server_pid           = ?,
        tmux_server_started       = ?,
        tmux_server_starttime     = ?,
        pane_id                   = ?,
        pane_pid                  = ?,
        pane_starttime            = ?,
        ` + launchStartClear + `,
        ` + rowVersionAdvance + `
  WHERE claude_instance_id = ? AND state = ? AND row_version = ?`

// RestoreAfterFailedResume undoes resume's move after a launch that failed
// other than by a timeout (SR-8.5). It is one conditional statement (SR-5.6)
// that applies only while the row is pending with row_version equal to
// movedVersion, the version MoveToPending produced. In that statement it sets
// state to prior.State; writes ended_at, pid, proc_starttime,
// liveness_unverified_since and liveness_note from prior, and launch_token,
// tmux_socket and the six server and pane identity columns from
// prior.Identity, each zero value as NULL; sets launch_started_at to NULL; and
// advances row_version by one. The texts are bound as the stored text they
// were read as, never parsed or re-formatted (SR-5.3), so ended_at and
// liveness_unverified_since read back byte for byte. Every other column is
// unchanged: parent_id keeps the move's value (RR5b T-h), and
// claude_session_id, jsonl_path, life_number, no_pre_trust, started_at,
// last_seen_at and the request fields are untouched. It never touches
// session_history or permission_requests and emits no trail event.
//
// A stored launch_token that was not well formed was read as "" (SR-5.5), so
// the restore writes NULL for it: the row then has no current label, which
// SR-5.5 accepts for a value only a hand edit can store.
//
// A prior.State other than ended or missing is a caller error: an error is
// returned and nothing is written. Otherwise it returns CondApplied when the
// write applied; CondChanged when the row exists but is no longer pending at
// movedVersion (another write came first; no hook can while the row records
// no pane, and the move cleared it, SR-22.9); CondAbsent when no row
// has the id. A store failure returns a wrapped error with a zero CondResult,
// and the row stays as the move left it (SR-5.8).
func (s *Store) RestoreAfterFailedResume(instanceID string, movedVersion int64, prior ResumePrior) (CondResult, error) {
	const errPrefix = "store: restore after failed resume"
	if prior.State != StateEnded && prior.State != StateMissing {
		return 0, fmt.Errorf("%s: prior state %q is not %s or %s", errPrefix, prior.State, StateEnded, StateMissing)
	}
	id := prior.Identity
	r, err := s.db.Exec(restoreAfterFailedResumeSQL,
		prior.State,
		nullableStringArg(prior.EndedAtText),
		positiveIntArg(prior.PID),
		nullableStringArg(prior.ProcStarttime),
		nullableStringArg(prior.LivenessUnverifiedSince),
		nullableStringArg(prior.LivenessNote),
		nullableStringArg(id.Token), nullableStringArg(id.Socket),
		positiveIntArg(id.ServerPID), positiveInt64Arg(id.ServerStart),
		nullableStringArg(id.ServerStarttime), nullableStringArg(id.PaneID),
		positiveIntArg(id.PanePID), nullableStringArg(id.PaneStarttime),
		instanceID, StatePending, movedVersion,
	)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("%s rows affected: %w", errPrefix, err)
	}
	if n > 0 {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, errPrefix)
}
