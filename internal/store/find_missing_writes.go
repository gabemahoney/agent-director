package store

import (
	"fmt"
	"strings"
)

// liveStateGuardSQL is the WHERE fragment that confines a find-missing write
// to a row in a live state (SR-11.6): `state IN (...)` with one placeholder
// per entry of liveStates, whose arguments liveStateGuardArgs returns in
// order. With it a terminal row whose snapshot still equals the examined one
// is refused (CondChanged) rather than written.
var liveStateGuardSQL = `state IN (` + strings.TrimSuffix(strings.Repeat("?,", len(liveStates)), ",") + `)`

// liveStateGuardArgs returns the bound arguments for liveStateGuardSQL.
func liveStateGuardArgs() []any {
	args := make([]any, len(liveStates))
	for i, st := range liveStates {
		args[i] = st
	}
	return args
}

// markMissingIfSameLifeSQL is MarkMissingIfSameLife's one statement: the mark
// with the liveness-field clear and the launch-start clear folded in, and the
// version advance, guarded by a live state and the examined row snapshot
// (SR-5.2, SR-5.3, SR-11.3, SR-11.6).
var markMissingIfSameLifeSQL = `UPDATE spawns
    SET state                     = ?,
        ended_at                  = CURRENT_TIMESTAMP,
        last_seen_at              = CURRENT_TIMESTAMP,
        liveness_unverified_since = NULL,
        liveness_note             = NULL,
        ` + launchStartClear + `,
        ` + rowVersionAdvance + `
  WHERE claude_instance_id = ? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL

// setLivenessNoteIfSameLifeSQL is SetLivenessNoteIfSameLife's one statement:
// the note overwritten, the unverified time kept or set, and the version
// advance, guarded by a live state and the examined row snapshot (SR-5.3,
// SR-11.4, SR-11.6).
var setLivenessNoteIfSameLifeSQL = `UPDATE spawns
    SET liveness_note             = ?,
        liveness_unverified_since = COALESCE(liveness_unverified_since, CURRENT_TIMESTAMP),
        ` + rowVersionAdvance + `
  WHERE claude_instance_id = ? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL

// clearLivenessIfSameLifeSQL is ClearLivenessIfSameLife's one statement: both
// liveness columns NULLed and the version advance, guarded by a live state and
// the examined row snapshot (SR-5.3, SR-11.4, SR-11.6).
var clearLivenessIfSameLifeSQL = `UPDATE spawns
    SET liveness_unverified_since = NULL,
        liveness_note             = NULL,
        ` + rowVersionAdvance + `
  WHERE claude_instance_id = ? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL

// MarkMissingIfSameLife is find-missing's mark (SR-11.3, SR-11.6; Appendix
// F.4). It is one conditional statement that applies only while the row is in
// a live state and its row snapshot equals examined, the snapshot the sweep
// read (or the one its adoption produced), compared on the values exactly as
// stored through the store's one snapshot-match condition (SR-5.3). In that
// statement it sets state to missing, ended_at and last_seen_at to
// CURRENT_TIMESTAMP, NULLs liveness_unverified_since and liveness_note (the
// liveness clear is folded into the mark, because a separate clear after it
// would be refused by the version the mark just advanced, SR-11.3), NULLs
// launch_started_at, and advances row_version by exactly one (SR-5.2). It
// never writes life_number, no_pre_trust, launch_token, tmux_socket, the
// server or pane identity columns or any request column, and emits no trail
// event: the tick and the permission-request denial stay with the caller.
//
// It returns the prior state and CondApplied when the write applied;
// CondChanged, having written nothing, when the row exists but is terminal or
// its snapshot differs; CondAbsent when no row has the id. The prior state is
// returned only when applied, and is "" otherwise. A store failure is returned
// as a wrapped error with a zero CondResult, never as a CondResult value
// (SR-5.8).
//
// The prior state comes from a read of the row's state just before the
// guarded statement (UPDATE ... RETURNING yields post-update values). That
// read never decides whether the mark applies: the snapshot guard alone does.
// Every write advances row_version (SR-5.2), so a mark that applied found the
// row unchanged since the read, and the state read is the state the guarded
// statement matched.
func (s *Store) MarkMissingIfSameLife(instanceID string, examined RowSnapshot) (priorState string, res CondResult, err error) {
	const errPrefix = "store: mark missing if same life"
	prior, found, err := s.selectPriorState(instanceID)
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	if !found {
		return "", CondAbsent, nil
	}
	args := append([]any{StateMissing, instanceID}, liveStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(examined)...)
	applied, err := s.execGuarded(markMissingIfSameLifeSQL, args, errPrefix)
	if err != nil {
		return "", 0, err
	}
	if applied {
		return prior, CondApplied, nil
	}
	res, err = s.condNotApplied(instanceID, errPrefix)
	return "", res, err
}

// SetLivenessNoteIfSameLife is find-missing's note write (SR-11.4, SR-11.6;
// Appendix F.4). It is one conditional statement that applies only while the
// row is in a live state and its row snapshot equals examined, compared
// through the store's one snapshot-match condition (SR-5.3). In that
// statement it overwrites liveness_note with note, so the note shows the
// latest reason; keeps liveness_unverified_since when it is set (the time of
// the first unverified sweep) and sets it to CURRENT_TIMESTAMP when it is
// NULL; and advances row_version by exactly one (SR-5.2). It writes it even
// when note equals the stored note: skipping an equal note is the caller's
// rule (SR-11.4). launch_started_at is unchanged, and so are life_number,
// no_pre_trust, launch_token, tmux_socket, the server and pane identity
// columns and every request column. It emits no trail event: ticks stay with
// the caller.
//
// It returns CondApplied when the write applied; CondChanged, having written
// nothing, when the row exists but is terminal or its snapshot differs;
// CondAbsent when no row has the id. A store failure is returned as a wrapped
// error with a zero CondResult, never as a CondResult value (SR-5.8).
func (s *Store) SetLivenessNoteIfSameLife(instanceID string, examined RowSnapshot, note string) (CondResult, error) {
	const errPrefix = "store: set liveness note if same life"
	args := append([]any{note, instanceID}, liveStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(examined)...)
	applied, err := s.execGuarded(setLivenessNoteIfSameLifeSQL, args, errPrefix)
	if err != nil {
		return 0, err
	}
	if applied {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, errPrefix)
}

// ClearLivenessIfSameLife is find-missing's note clear, for a row whose
// process the sweep found alive (SR-11.4, SR-11.6; Appendix F.4). It is one
// conditional statement that applies only while the row is in a live state
// and its row snapshot equals examined, compared through the store's one
// snapshot-match condition (SR-5.3). In that statement it NULLs
// liveness_unverified_since and liveness_note and advances row_version by
// exactly one (SR-5.2), whether or not a note was set: not writing a row
// without a note is the caller's rule (SR-11.4). launch_started_at is
// unchanged, and so are life_number, no_pre_trust, launch_token, tmux_socket,
// the server and pane identity columns and every request column. It emits no
// trail event.
//
// It returns CondApplied when the write applied; CondChanged, having written
// nothing, when the row exists but is terminal or its snapshot differs;
// CondAbsent when no row has the id. A store failure is returned as a wrapped
// error with a zero CondResult, never as a CondResult value (SR-5.8).
func (s *Store) ClearLivenessIfSameLife(instanceID string, examined RowSnapshot) (CondResult, error) {
	const errPrefix = "store: clear liveness if same life"
	args := append([]any{instanceID}, liveStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(examined)...)
	applied, err := s.execGuarded(clearLivenessIfSameLifeSQL, args, errPrefix)
	if err != nil {
		return 0, err
	}
	if applied {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, errPrefix)
}

// adoptIdentityIfSameLifeSQL is AdoptIdentityIfSameLife's one statement: the
// shared identity-column assignment (adoptIdentitySet), guarded by a live
// state and the examined row snapshot (SR-3.6, SR-5.3, SR-11.6).
var adoptIdentityIfSameLifeSQL = `UPDATE spawns
    SET ` + adoptIdentitySet + `
  WHERE claude_instance_id = ? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL

// AdoptIdentityIfSameLife is find-missing's adoption write (SR-3.6, SR-11.6;
// LFR H2; Appendix F.4): when the sweep's lookup found Ours for a row that
// records no server identity or no pane (a lost create reply), the sweep
// records what it found before it judges the row. It is one conditional
// statement that applies only while the row is in a live state (pending
// included) and its row snapshot equals examined, the snapshot the sweep
// read, compared on the values exactly as stored through the store's one
// snapshot-match condition (SR-5.3). In that statement it writes the tmux
// server's identity (tmux_server_pid, tmux_server_started,
// tmux_server_starttime) and the agent's pane (pane_id, pane_pid,
// pane_starttime) from id, a zero value as NULL, and advances row_version by
// exactly one (SR-5.2), through the identity-column assignment it shares with
// AdoptIdentityIfUnchanged. It never writes id.Token, id.Socket, the state,
// the liveness columns, launch_started_at, life_number, no_pre_trust or any
// request column, and emits no trail event: the sweep records adopted on
// ad.provenance.disagree (SR-14).
//
// It returns CondApplied and now, the row's snapshot after the write (what
// GetSpawn would return), which then guards the row's verdict write (note,
// clear or mark); CondChanged, having written nothing, when the row exists
// but is terminal or its snapshot differs; CondAbsent when no row has the id.
// On anything but CondApplied, now is the zero RowSnapshot. A store failure
// is returned as a wrapped error with a zero CondResult, never as a
// CondResult value (SR-5.8).
//
// now needs no read: the snapshot holds none of the six identity columns, so
// the applied write changed only row_version, by exactly one, from the value
// the guard matched. The guarded statement alone decides whether it applied.
//
// Only find-missing calls it (SR-3.6; LFR H2); kill, send-keys and pause use
// AdoptIdentityIfUnchanged.
func (s *Store) AdoptIdentityIfSameLife(instanceID string, examined RowSnapshot, id LaunchIdentity) (res CondResult, now RowSnapshot, err error) {
	const errPrefix = "store: adopt identity if same life"
	args := append(adoptIdentityArgs(id), instanceID)
	args = append(args, liveStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(examined)...)
	applied, err := s.execGuarded(adoptIdentityIfSameLifeSQL, args, errPrefix)
	if err != nil {
		return 0, RowSnapshot{}, err
	}
	if applied {
		now = examined
		now.RowVersion++
		return CondApplied, now, nil
	}
	res, err = s.condNotApplied(instanceID, errPrefix)
	return res, RowSnapshot{}, err
}

// execGuarded runs one guarded UPDATE or DELETE and reports whether it
// matched a row.
// errPrefix names the write in a driver error.
func (s *Store) execGuarded(q string, args []any, errPrefix string) (bool, error) {
	r, err := s.db.Exec(q, args...)
	if err != nil {
		return false, fmt.Errorf("%s: %w", errPrefix, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%s rows affected: %w", errPrefix, err)
	}
	return n > 0, nil
}
