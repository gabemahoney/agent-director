package store

import (
	"context"
	"database/sql"
	"errors"
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
// (SR-5.2, SR-5.3, SR-11.3, SR-11.6). missing is an immediate write
// (immediateStateSet, b.3ie): it reports at once.
var markMissingSetSQL = `UPDATE spawns
    SET ` + immediateStateSet + `,
        ended_at                  = CURRENT_TIMESTAMP,
        last_seen_at              = CURRENT_TIMESTAMP,
        liveness_unverified_since = NULL,
        liveness_note             = NULL,
        ` + launchStartClear + `,
        ` + rowVersionAdvance

var markMissingIfSameLifeSQL = markMissingSetSQL + `
  WHERE instance_id = ? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL

// setLivenessNoteIfSameLifeSQL is SetLivenessNoteIfSameLife's one statement:
// the note overwritten, the unverified time kept or set, and the version
// advance, guarded by a live state and the examined row snapshot (SR-5.3,
// SR-11.4, SR-11.6).
var setLivenessNoteIfSameLifeSQL = `UPDATE spawns
    SET liveness_note             = ?,
        liveness_unverified_since = COALESCE(liveness_unverified_since, CURRENT_TIMESTAMP),
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL

// clearLivenessIfSameLifeSQL is ClearLivenessIfSameLife's one statement: both
// liveness columns NULLed and the version advance, guarded by a live state and
// the examined row snapshot (SR-5.3, SR-11.4, SR-11.6).
var clearLivenessIfSameLifeSQL = `UPDATE spawns
    SET liveness_unverified_since = NULL,
        liveness_note             = NULL,
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL

// MarkMissingIfSameLife is find-missing's mark (SR-11.3, SR-11.6; Appendix
// F.4), with the close of the row's open permission requests (SR-5.4) in the
// same transaction (b.146 rule 12), and the close of the row's running subagent
// entries and claimable launches (SR-8.1): all or nothing.
//
// The transaction is begun with BEGIN IMMEDIATE on the store's one
// connection, so it holds the database write lock before it reads the row;
// under contention it waits up to the store's busy timeout. Inside it:
//
//  1. It reads the row's state, the prior state the caller's tick reports. No
//     row: CondAbsent.
//  2. The mark: one conditional statement that applies only while the row is
//     in a live state and its row snapshot equals examined, the snapshot the
//     sweep read (or the one its adoption produced), compared on the values
//     exactly as stored through the store's one snapshot-match condition
//     (SR-5.3). In that statement it sets state to missing, ended_at and
//     last_seen_at to CURRENT_TIMESTAMP, NULLs liveness_unverified_since and
//     liveness_note (the liveness clear is folded into the mark, because a
//     separate clear after it would be refused by the version the mark just
//     advanced, SR-11.3), NULLs launch_started_at, and advances row_version by
//     exactly one (SR-5.2). It never writes life_number, no_pre_trust,
//     launch_token, tmux_socket, the server or pane identity columns or the
//     launch owner. A row that exists but is terminal or holds another
//     snapshot: CondChanged.
//  3. The close (b.146 rule 12; closeOrphanedRequests, which the ended
//     transition and resume's move share): every request of the row that
//     still awaits an answer (awaitingAnswerSQL: not acked and not answered
//     at the pane, or, recorded before schema v7, undecided) is closed, its
//     closed_at set to the time of the close, so that no request of the row
//     awaits an answer after the mark, and none holds the row's moves to
//     working, shows on get and list, or blocks find-missing's
//     check_permission repair once the row is resumed. An undecided one also
//     gets decision deny, decision_reason find_missing and decided_at
//     CURRENT_TIMESTAMP, as before, so a relay polling for the row reads a
//     fail-closed deny rather than spinning to its own timeout. A decided
//     one, whose relay hook has not acked its verdict, keeps its decision,
//     decision_reason and decided_at. Two statements, the deny first; then
//     a third proves every request of the row not yet proven gone, closed
//     now or before, proven_gone_how agent_gone (b.146 step 2c).
//  4. The subagent close (closeSubagentsAgentGone, SR-8.1), only when the mark
//     applied: every running subagent entry of the row, of any launch, turns
//     lost with close_reason agent_gone and every claimable launch of the row is
//     deleted, whatever the row's subagent_tracking flag. The instant is one
//     store-clock read (storeNowMillis, millisecond precision) taken once inside
//     the transaction after the mark applied and shared with step 3's closed_at,
//     so the entries' lost_at and activity_at equal the requests' closed_at.
//     CondChanged and CondAbsent read no clock and close nothing.
//
// Any failure in the read, the mark, the close or the commit rolls the whole
// transaction back: the row keeps its state and snapshot, and its requests,
// entries and claimable launches stay as they were. The mark never applies
// without the closes, and the closes never apply without the mark.
//
// After the commit, and only then, it emits per closed request, in request-id
// order, one ad.row_mutation.committed (writer find_missing) for a request it
// denied, and one ad.find_missing.tick with reconciliation_reason
// permission_orphan_closeout for every request it closed, fail-open
// (SR-A-2.5, SR-A-3.2). It emits nothing for the mark itself: the mark's tick
// stays with the caller.
//
// It returns the prior state and CondApplied when the transaction committed;
// CondChanged or CondAbsent, having written nothing, otherwise. The prior
// state is returned only when applied, and is "" otherwise. A store failure is
// returned as a wrapped error with a zero CondResult, never as a CondResult
// value (SR-5.8).
//
// The prior state read never decides whether the mark applies: the snapshot
// guard alone does. It is taken under the write lock and every write advances
// row_version (SR-5.2), so a mark that applied found the row unchanged since
// the read.
func (s *Store) MarkMissingIfSameLife(instanceID string, examined RowSnapshot) (priorState string, res CondResult, err error) {
	args := append(immediateStateArgs(StateMissing), instanceID)
	args = append(args, liveStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(examined)...)
	return s.terminalMarkIfSameLife("store: mark missing if same life", markMissingIfSameLifeSQL, args,
		instanceID, DecisionReasonFindMissing)
}

// markEndedIfSameLifeSQL is MarkEndedIfSameLife's one statement: the mark's
// statement with state ended in place of missing, and with three more
// conditions in its WHERE: the row is a Codex row (agent codex), and it holds
// an end report, read as decodeEndReportedAt reads it (an integer above zero;
// NULL, zero, a negative and any other stored type are no report). The
// statement never writes end_reported_at: the report stays set. ended is an
// immediate write (immediateStateSet, b.3ie): it reports at once.
var markEndedIfSameLifeSQL = `UPDATE spawns
    SET ` + immediateStateSet + `,
        ended_at                  = CURRENT_TIMESTAMP,
        last_seen_at              = CURRENT_TIMESTAMP,
        liveness_unverified_since = NULL,
        liveness_note             = NULL,
        ` + launchStartClear + `,
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL + `
    AND agent = '` + AgentCodex + `' AND typeof(end_reported_at) = 'integer' AND end_reported_at > 0`

// MarkEndedIfSameLife is the guarded move of a live Codex row to ended (SR-6.5,
// MC-2, finding F1's fix): find-missing's mark with state ended in place of
// missing, and decision_reason ended in place of find_missing, made once the
// row's agent process is proven gone and the row holds an end report (the
// Codex SessionEnd of its verified process, ApplyHookEndReport). find-missing
// and pause both make this one write, through one outcome writer.
//
// It follows MarkMissingIfSameLife step for step, in one BEGIN IMMEDIATE
// transaction on the store's one connection: the prior state read (no row:
// CondAbsent); the one conditional statement (markEndedIfSameLifeSQL); then,
// only when the statement applied, one store-clock read at (storeNowMillis),
// the close of the row's permission requests (closeOrphanedRequests with
// reason ended and at: an undecided request denied with decision_reason
// ended, a decided one keeping its verdict, every request proven agent_gone;
// b.146 rule 12) and the close of the row's running subagent entries and
// claimable launches (closeSubagentsAgentGone with the same at, so the entries'
// lost_at and activity_at equal the requests' closed_at; SR-8.1, b.xjn); then
// COMMIT. It is a fifth write that closes subagent records, after the
// terminal SessionEnd's move to ended, the mark, resume's move to pending and
// reuse's reset.
//
// The statement applies only while the row is a Codex row, in a live state,
// still holds examined (the snapshot the caller read, compared through the
// store's one snapshot-match condition, SR-5.3) and still holds an end
// report. It sets state to ended with the settle columns of an immediate
// write, ended_at and last_seen_at to CURRENT_TIMESTAMP, NULLs
// liveness_unverified_since, liveness_note and launch_started_at, and advances
// row_version by exactly one. It leaves end_reported_at set and writes no other
// column. A row that exists but is a Claude row (even one whose end_reported_at
// a hand set), is terminal, has no end report (including one cleared between
// the caller's read and this write) or holds another snapshot: CondChanged,
// with nothing written, no clock read, no request closed and no trail.
//
// Any failure in the read, the move, either close or the commit rolls the whole
// transaction back. After the commit, and only then, it emits per closed
// request, in request-id order, one ad.row_mutation.committed (writer
// find_missing) for a request it denied and one ad.find_missing.tick with
// reconciliation_reason permission_orphan_closeout for every request it closed
// (fail-open). It emits no ad.spawn.state_transition and no tick for the move
// itself: the row's tick is the caller's.
//
// It returns the prior state and CondApplied when the transaction committed;
// CondChanged or CondAbsent, with the prior state "", otherwise. A store failure
// is returned as a wrapped error with a zero CondResult (SR-5.8).
func (s *Store) MarkEndedIfSameLife(instanceID string, examined RowSnapshot) (priorState string, res CondResult, err error) {
	args := append(immediateStateArgs(StateEnded), instanceID)
	args = append(args, liveStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(examined)...)
	return s.terminalMarkIfSameLife("store: mark ended if same life", markEndedIfSameLifeSQL, args,
		instanceID, DecisionReasonEnded)
}

// terminalMarkIfSameLife is the transaction MarkMissingIfSameLife and
// MarkEndedIfSameLife share: the prior state read, the one conditional
// statement stmt with args, and, when it applied, the close of the row's
// requests with reason and of its subagent records at one instant, the commit
// and the post-commit emits (writer find_missing). Their docs state each step.
func (s *Store) terminalMarkIfSameLife(errPrefix, stmt string, args []any, instanceID, reason string) (priorState string, res CondResult, err error) {
	// The pool has one connection: everything below runs on conn, never
	// through s.db, which would wait for it.
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("%s: connection: %w", errPrefix, err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", 0, fmt.Errorf("%s: begin: %w", errPrefix, err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackConn(ctx, conn)
		}
	}()

	prior, res, closed, err := terminalMarkOnConn(ctx, conn, errPrefix, stmt, args, instanceID, reason)
	if err != nil || res != CondApplied {
		return "", res, err
	}
	if err := s.refreshFamilies(connQuerier{ctx, conn}); err != nil {
		return "", 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return "", 0, fmt.Errorf("%s: commit: %w", errPrefix, err)
	}
	committed = true

	emitTerminalCloseout(ctx, closed, instanceID, reason)
	return prior, CondApplied, nil
}

// terminalMarkWithoutWait is the Cursor reconciliation path. One operation
// context bounds SQL, connection cleanup and post-commit audit; inWriteTxContext
// preserves confirmed commits even when cleanup reaches that deadline.
func (s *Store) terminalMarkWithoutWait(errPrefix, stmt string, args []any, instanceID, reason string) (string, CondResult, error) {
	ctx, cancel := s.operationContext(0)
	defer cancel()
	var prior string
	var res CondResult
	var closed []closedRequest
	err := s.inWriteTxContext(ctx, 0, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		prior, res, closed, err = terminalMarkOnConn(ctx, conn, errPrefix, stmt, args, instanceID, reason)
		if err == nil && res == CondApplied {
			return s.refreshFamiliesAfterStateChange(connQuerier{ctx, conn}, prior, StateMissing)
		}
		return err
	})
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	if res == CondApplied {
		emitTerminalCloseout(ctx, closed, instanceID, reason)
	}
	return prior, res, nil
}

// terminalMarkOnConn shares the guarded terminal update and atomic request /
// subagent closure. The caller owns the transaction and emits only after commit.
func terminalMarkOnConn(ctx context.Context, conn *sql.Conn, errPrefix, stmt string, args []any, instanceID, reason string) (string, CondResult, []closedRequest, error) {
	var prior string
	err := conn.QueryRowContext(ctx, `SELECT state FROM spawns WHERE instance_id = ?`, instanceID).Scan(&prior)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", CondAbsent, nil, nil
	case err != nil:
		return "", 0, nil, fmt.Errorf("%s: prior state: %w", errPrefix, err)
	}

	r, err := conn.ExecContext(ctx, stmt, args...)
	if err != nil {
		return "", 0, nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return "", 0, nil, fmt.Errorf("%s rows affected: %w", errPrefix, err)
	}
	if n == 0 {
		// The row exists (read above, under the write lock), so the guard
		// refused it.
		return "", CondChanged, nil, nil
	}

	at := storeNowMillis()
	closed, err := closeOrphanedRequests(ctx, conn, instanceID, reason, at)
	if err != nil {
		return "", 0, nil, fmt.Errorf("%s: close permission requests: %w", errPrefix, err)
	}
	if err := closeSubagentsAgentGone(connQuerier{ctx: ctx, conn: conn}, instanceID, at); err != nil {
		return "", 0, nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	return prior, CondApplied, closed, nil
}

func emitTerminalCloseout(ctx context.Context, closed []closedRequest, instanceID, reason string) {
	for _, c := range closed {
		c.emitDenyContext(ctx, instanceID, reason, WriterProcessFindMissing)
		emitOrphanCloseoutTickContext(ctx, instanceID, c.token)
	}
}

// The two liveness notes the store itself names (b.kdf, b.146 rule 10).
// find-missing writes every note; the store compares these two in
// NoteUnreportedIfSameLife.
const (
	// LivenessNoteUnreported is the note find-missing writes on a pending row
	// whose agent is alive but has not reported through any hook since its
	// launch (NoteUnreportedIfSameLife). The agent may sit at a Claude Code
	// startup screen or idle at its prompt, which only something that reads
	// its pane can tell apart. The row stays pending; the agent's next
	// applied hook clears the note, as every applied hook clears the
	// liveness columns.
	LivenessNoteUnreported = "unreported"
	// LivenessNoteProvenanceConflict is the note of a row whose lookup found
	// more than one session carrying its launch's label, so which session is
	// the row's own is in doubt. It takes precedence over
	// LivenessNoteUnreported: unreported tells a caller to read the row's
	// pane and type, which is no help while the row's session is in doubt.
	LivenessNoteProvenanceConflict = "provenance_conflict"
)

// noteUnreportedIfSameLifeSQL is NoteUnreportedIfSameLife's one statement:
// the note set, the time first flagged kept or set, and the version advance,
// guarded by pending, a note other than provenance_conflict and the examined
// row snapshot (b.146 rule 10; SR-5.3).
var noteUnreportedIfSameLifeSQL = `UPDATE spawns
    SET liveness_note             = ?,
        liveness_unverified_since = COALESCE(liveness_unverified_since, CURRENT_TIMESTAMP),
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND state = ? AND COALESCE(liveness_note, '') <> ? AND ` + snapshotMatchSQL

// NoteUnreportedIfSameLife is find-missing's report of a live pending row
// that no hook has reported (b.kdf, b.146 rule 10): a report, not a state
// change. The sweep calls it only for a pending row past its grace period,
// not held by a live launch owner, whose pane is recorded (or was just
// adopted) and whose agent process is alive; the decision is the caller's,
// and this write checks none of it beyond pending, the note precedence and
// the snapshot.
//
// It is one conditional statement that applies only while the row is
// pending, its liveness note is not LivenessNoteProvenanceConflict (which
// unreported never overwrites), and its row snapshot equals examined, the
// snapshot the sweep read (or the one its adoption produced), compared
// through the store's one snapshot-match condition (SR-5.3), so a hook that
// writes first wins. In that statement it sets liveness_note to
// LivenessNoteUnreported; keeps liveness_unverified_since when it is set and
// sets it to CURRENT_TIMESTAMP when it is NULL, as SetLivenessNoteIfSameLife
// does, so it holds the time a note first flagged the row and a change of
// note (unreported to another note and back) keeps it (every launch,
// InsertPending, MoveToPending or ResetForReuse, starts it NULL, and every
// applied hook, the clear and the mark NULL it); and advances row_version by
// exactly one (SR-5.2). The state stays pending, and
// launch_started_at (which send-keys' allow_pending needs), last_seen_at, the
// session id, the transcript path, pid, proc_starttime, life_number,
// no_pre_trust, launch_token, tmux_socket, the server and pane identity, the
// launch owner and every request column are unchanged. It writes even when
// the stored note is already unreported: skipping that write, so repeated
// sweeps do not advance row_version, is the caller's rule. It emits no trail
// event: ticks stay with the caller.
//
// The agent's next applied hook clears the note: SessionStart moves the row
// to waiting and records its identity, and a UserPromptSubmit moves it to
// working and records its session id when the row has none.
//
// It returns CondApplied when the write applied; CondChanged, having written
// nothing, when the row exists but is not pending, carries note
// provenance_conflict or its snapshot differs; CondAbsent when no row has
// the id. A store failure is returned as a wrapped error with a zero
// CondResult, never as a CondResult value (SR-5.8).
func (s *Store) NoteUnreportedIfSameLife(instanceID string, examined RowSnapshot) (CondResult, error) {
	const errPrefix = "store: note unreported if same life"
	args := append([]any{
		LivenessNoteUnreported,
		instanceID, StatePending, LivenessNoteProvenanceConflict,
	}, snapshotMatchArgs(examined)...)
	applied, err := s.execGuarded(noteUnreportedIfSameLifeSQL, args, errPrefix)
	if err != nil {
		return 0, err
	}
	if applied {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, errPrefix)
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
  WHERE instance_id = ? AND ` + liveStateGuardSQL + ` AND ` + snapshotMatchSQL

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

// repairCheckPermissionSQL is RepairCheckPermissionIfSameLife's one
// statement: the move out of check_permission (on a Claude row waiting when
// idle_since is set, working otherwise; on a Codex row, whose agent sends no
// idle-prompt Notification, always working: SR-8.3), the launch-start clear
// and the version advance,
// guarded by check_permission, relay_mode on, the examined row snapshot and
// no permission request of the row holding it (holdsRowSQL: still awaiting an
// answer, b.146 rule 9, and not proven gone, b.omt).
// The move is an immediate write (b.3ie): settled_state takes the same value
// as state, and state_changed_ms the statement's clock (the WHERE pins
// check_permission, so the value always changes).
var repairCheckPermissionSQL = `UPDATE spawns
    SET state = CASE WHEN idle_since IS NOT NULL AND agent <> '` + AgentCodex + `' THEN ? ELSE ? END,
        settled_state = CASE WHEN idle_since IS NOT NULL AND agent <> '` + AgentCodex + `' THEN ? ELSE ? END,
        state_changed_ms = ` + nowMsSQL + `,
        ` + launchStartClear + `,
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND state = ? AND relay_mode = 'on' AND ` + snapshotMatchSQL + `
    AND NOT EXISTS (SELECT 1 FROM permission_requests pr
                     WHERE pr.instance_id = spawns.instance_id AND ` + holdsRowSQL + `)
  RETURNING state`

// RepairCheckPermissionIfSameLife is find-missing's repair of a stale
// check_permission row (b.146 rule 9, problem 3): one conditional statement
// that moves the row out of check_permission, to waiting when idle_since is
// set (the main agent's idle-prompt Notification landed and no hook since)
// and to working otherwise; a Codex row (the row's own agent column, read in
// the same statement) always goes to working whatever idle_since reads, since
// Codex sends no idle-prompt Notification and a hand-set or stale idle_since
// says nothing of its turn (SR-8.3). It writes only while the row is in check_permission with
// relay_mode on (a row with the relay off is in check_permission while
// Claude Code's own dialog waits, with no request on record), still holds
// examined (the snapshot the sweep read; SR-5.3) and none of its permission
// requests holds it (holdsRowSQL: one that still awaits an answer, not closed
// by a close of the row's requests, and not acked and not answered at the
// pane, or, recorded before schema v7, undecided, and that Claude Code has
// not proven gone; b.omt). A request proven gone does not hold the repair off
// even while it awaits an answer in agent-director's records (one a person
// answered at the pane): its dialog is gone (b.146 step 2c).
// In the same statement it NULLs launch_started_at, advances row_version by
// one (SR-5.2) and writes the settle columns of an immediate write (b.3ie:
// the new state reports at once). It writes no other own-state column:
// idle_since is kept until the agent's next hook clears it, and last_seen_at
// is not bumped, since no hook reported. The guarded repair and family-history
// refresh commit together.
//
// Whether a request's relay hook may still be alive is the caller's to check
// before calling: a process check is no statement. A request recorded after
// that check advances the row's snapshot (its relay hook's transaction moves
// the row to check_permission), so the snapshot guard refuses the repair.
//
// It returns the state written and CondApplied when the write applied;
// CondChanged, having written nothing, when the row exists but is not in
// check_permission, has the relay off, holds another snapshot or has a
// request that holds it; CondAbsent when no row has the id. A
// store failure is
// returned as a wrapped error with a zero CondResult (SR-5.8). It emits no
// trail event: the tick is the caller's.
func (s *Store) RepairCheckPermissionIfSameLife(instanceID string, examined RowSnapshot) (newState string, res CondResult, err error) {
	const errPrefix = "store: repair check_permission if same life"
	args := append([]any{StateWaiting, StateWorking, StateWaiting, StateWorking, instanceID, StateCheckPermission},
		snapshotMatchArgs(examined)...)
	err = s.inImmediateTx(func(ctx context.Context, conn *sql.Conn) error {
		return conn.QueryRowContext(ctx, repairCheckPermissionSQL, args...).Scan(&newState)
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err = s.condNotApplied(instanceID, errPrefix)
		return "", res, err
	case err != nil:
		return "", 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	return newState, CondApplied, nil
}

// execGuarded runs one guarded UPDATE or DELETE and reports whether it
// matched a row.
// errPrefix names the write in a driver error.
func (s *Store) execGuarded(q string, args []any, errPrefix string) (bool, error) {
	r, err := s.execFamily(q, args...)
	if err != nil {
		return false, fmt.Errorf("%s: %w", errPrefix, err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%s rows affected: %w", errPrefix, err)
	}
	return n > 0, nil
}
