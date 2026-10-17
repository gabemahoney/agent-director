package store

import (
	"context"
	"database/sql"
	"fmt"
)

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
	// EndReportedAtMillis is end_reported_at as stored, milliseconds since the
	// epoch; 0 = NULL (no end report). The move clears it and a failed
	// resume's restore writes it back.
	EndReportedAtMillis int64
	// SubagentTracking is subagent_tracking as the Spawn read it (SR-9): the
	// move sets the flag to 1 for the launch it begins, and a failed resume's
	// restore writes this pre-move value back (1, or 0 for a row from before
	// the v9 migration), since the earlier launch becomes current again. A
	// stored value other than 1 was read as false and is written back as 0.
	SubagentTracking bool
	// Identity is the pre-move launch_token, tmux_socket and server and pane
	// identity, as stored, so the restore writes the previous token back and
	// the label of the row's earlier launch stays current (amendment 1; LFR H3).
	Identity LaunchIdentity
}

// moveToPendingSQL is MoveToPending's one statement (SR-8.3, SR-5.6): the
// move to pending with its cleared and set columns and the version advance,
// guarded by a finished state and the examined row snapshot (SR-5.3). pending
// is an immediate write (immediateStateSet, b.3ie): it reports at once.
const moveToPendingSQL = `UPDATE spawns
    SET ` + immediateStateSet + `,
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
        end_reported_at           = NULL,
        subagent_tracking         = ` + subagentTrackedColumnSQL + `,
        launch_started_at         = ?,
        launch_token              = ?,
        tmux_socket               = ?,
        parent_id                 = ?,
        launch_owner_pid          = ?,
        launch_owner_starttime    = ?,
        launch_owner_pidns        = ?,
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND ` + finishedStateGuardSQL + ` AND ` + snapshotMatchSQL

// MoveToPending is resume's move of a finished row to pending, the write that
// begins its launch (SR-8.3), with a backstop close of the row's open
// permission requests (b.146 rule 12) and the close of the row's running subagent
// entries and claimable launches (SR-8.1) in the same transaction, all or
// nothing. The transaction is begun with BEGIN IMMEDIATE (inImmediateTx,
// waiting the store's busy timeout for the write lock).
//
// The move is one conditional statement (SR-5.6) that applies only while the
// row is ended or missing and its row snapshot equals examined, compared on
// the values exactly as stored (SR-5.3). In that statement it sets state
// pending as an immediate write (settled_state pending, state_changed_ms the
// statement's clock; b.3ie: a caller that has just resumed never reads the
// row ended for the settle time); clears pid, proc_starttime, ended_at, liveness_unverified_since,
// liveness_note, end_reported_at and the six server and pane identity columns;
// sets launch_started_at to launchStartedAtMillis (the caller's clock; the store
// reads none), launch_token to token, tmux_socket to socket, parent_id to
// parentID and the three launch-owner columns to owner (the resume records
// itself as the owner of the launch it begins, b.kdf), each zero value
// written as NULL; and advances row_version by one. Every other column
// (session_id, jsonl_path, life_number, no_pre_trust, started_at,
// last_seen_at, agent and the request fields) is unchanged, and session_history
// is not touched. It also sets subagent_tracking, the flag of the launch it
// begins (SR-9; subagentTrackedColumnSQL, no parameter): from the row's own
// agent, which the move keeps, 1 for claude and 0 for codex or cursor, over whatever the
// row held. The statement itself touches no subagent entry; the close below does.
//
// The close (closeOrphanedRequests, as the ended transition's): every request
// of the row that still awaits an answer gets closed_at, an undecided one
// also denied with decision_reason ended, a decided one whose relay hook has
// not acked keeps its verdict. The ended transition and find-missing's mark
// already close a finished row's requests, so this finds one only on a row
// that finished before they did (an earlier release, or any path that missed
// them): no request of the row's earlier launch awaits an answer in the life
// the resume begins. Every request of the row not yet proven gone is proven
// gone, agent_gone (b.146 step 2c), so none of the earlier launch's holds
// plain send-keys in the new one. After the commit it emits one ad.row_mutation.committed
// (writer resume, decision_reason ended) per request it denied, in
// request-id order, fail-open; nothing else.
//
// The subagent close (closeSubagentsAgentGone, SR-8.1), only when the move
// applied: every running subagent entry of the row, of any launch, turns lost
// with close_reason agent_gone and every claimable launch of the row is deleted,
// whatever the row's subagent_tracking flag, so no record of the earlier launch
// reads running in the launch the move begins (a SubagentStart reopens one in
// the new launch). Its instant is launchStartedAtMillis, the new launch's
// launch_started_at the move writes: lost_at and activity_at equal it and the
// store reads no clock for the close (SR-3.5). The request close above keeps its
// own store-clock closed_at, unchanged. A non-positive launchStartedAtMillis is
// refused by the close, so a move that applied with it fails whole. A move that
// did not apply closes nothing.
//
// It returns CondApplied with movedVersion, the version the write produced
// (examined.RowVersion + 1), which the restore's condition takes; CondChanged
// when the row exists but is live, pending or differs from examined; CondAbsent
// when no row has the id. Neither writes anything and both return movedVersion
// 0. A store failure, a foreign-key failure on a parentID that names no row
// included, returns a wrapped error with a zero CondResult and writes nothing,
// the row, its requests, its entries and its claimable launches as they were
// (SR-5.8).
func (s *Store) MoveToPending(instanceID string, examined RowSnapshot, launchStartedAtMillis int64, token, socket, parentID string, owner LaunchOwner) (res CondResult, movedVersion int64, err error) {
	const errPrefix = "store: move to pending"
	args := append(immediateStateArgs(StatePending),
		positiveInt64Arg(launchStartedAtMillis),
		nullableStringArg(token), nullableStringArg(socket),
		nullableStringArg(parentID),
	)
	args = append(args, launchOwnerArgs(owner)...)
	args = append(args, instanceID)
	args = append(args, finishedStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(examined)...)
	var (
		moved  bool
		closed []closedRequest
	)
	err = s.inImmediateTx(func(ctx context.Context, conn *sql.Conn) error {
		r, err := conn.ExecContext(ctx, moveToPendingSQL, args...)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected: %w", err)
		}
		if n == 0 {
			return nil
		}
		moved = true
		closed, err = closeOrphanedRequests(ctx, conn, instanceID, DecisionReasonEnded, storeNowMillis())
		if err != nil {
			return fmt.Errorf("close permission requests: %w", err)
		}
		return closeSubagentsAgentGone(connQuerier{ctx: ctx, conn: conn}, instanceID, launchStartedAtMillis)
	})
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	if !moved {
		// Nothing was written: the guarded statement matched no row.
		res, err = s.condNotApplied(instanceID, errPrefix)
		return res, 0, err
	}
	for _, c := range closed {
		c.emitDeny(instanceID, DecisionReasonEnded, WriterProcessResume)
	}
	// The condition pinned row_version to examined.RowVersion and the
	// statement advanced it by exactly one.
	return CondApplied, examined.RowVersion + 1, nil
}

// restoreAfterFailedResumeSQL is RestoreAfterFailedResume's one statement
// (SR-8.5, SR-5.6): what the move cleared or overwrote, written back, the
// launch start cleared and the version advanced, guarded by pending and the
// move's version. The prior state is an immediate write (immediateStateSet,
// b.3ie): it reports at once, never the move's pending for the settle time.
const restoreAfterFailedResumeSQL = `UPDATE spawns
    SET ` + immediateStateSet + `,
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
        end_reported_at           = ?,
        subagent_tracking         = ?,
        ` + launchStartClear + `,
        ` + rowVersionAdvance + `
  WHERE instance_id = ? AND state = ? AND row_version = ?`

// RestoreAfterFailedResume undoes resume's move after a launch that failed
// other than by a timeout (SR-8.5). It is one conditional statement (SR-5.6)
// that applies only while the row is pending with row_version equal to
// movedVersion, the version MoveToPending produced. In that statement it sets
// state to prior.State as an immediate write (settled_state prior.State,
// state_changed_ms the statement's clock; b.3ie); writes ended_at, pid, proc_starttime,
// liveness_unverified_since, liveness_note, end_reported_at and
// subagent_tracking (1 for prior.SubagentTracking true, else 0; the earlier
// launch's flag, SR-9) from prior, and
// launch_token, tmux_socket and the six server and pane identity columns from
// prior.Identity, each zero value as NULL; sets launch_started_at to NULL; and
// advances row_version by one. The texts are bound as the stored text they
// were read as, never parsed or re-formatted (SR-5.3), so ended_at and
// liveness_unverified_since read back byte for byte. Every other column is
// unchanged: parent_id keeps the move's value (RR5b T-h), and
// session_id, jsonl_path, life_number, no_pre_trust, started_at,
// last_seen_at and the request fields are untouched. It never touches
// session_history or permission_requests and emits no trail event. It closes
// no subagent entry and deletes no claimable launch (SR-8.1): the move already
// closed the earlier launch's running entries, a failed launch's own hooks never
// reached a row with no pane, and the earlier launch becoming current again
// does not reopen a closed entry (only a SubagentStart does).
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
	args := append(immediateStateArgs(prior.State),
		nullableStringArg(prior.EndedAtText),
		positiveIntArg(prior.PID),
		nullableStringArg(prior.ProcStarttime),
		nullableStringArg(prior.LivenessUnverifiedSince),
		nullableStringArg(prior.LivenessNote),
		nullableStringArg(id.Token), nullableStringArg(id.Socket),
		positiveIntArg(id.ServerPID), positiveInt64Arg(id.ServerStart),
		nullableStringArg(id.ServerStarttime), nullableStringArg(id.PaneID),
		positiveIntArg(id.PanePID), nullableStringArg(id.PaneStarttime),
		positiveInt64Arg(prior.EndReportedAtMillis),
		boolInt(prior.SubagentTracking),
		instanceID, StatePending, movedVersion,
	)
	r, err := s.db.Exec(restoreAfterFailedResumeSQL, args...)
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
