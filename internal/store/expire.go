package store

import (
	"fmt"
	"time"
)

// finishedStateGuardSQL is the WHERE fragment that confines an expire read or
// write to a finished row (SR-12.1, SR-12.3): state ended or missing, whose
// arguments finishedStateGuardArgs returns in order.
const finishedStateGuardSQL = `state IN (?, ?)`

// finishedStateGuardArgs returns the bound arguments for
// finishedStateGuardSQL.
func finishedStateGuardArgs() []any {
	return []any{StateEnded, StateMissing}
}

// ExpireCandidate is expire's read of one finished row older than the cutoff
// (SR-12.1, SR-12.2, Appendix F.4): what expire needs to judge whether the
// row's agent, own session or a leftover may still run, and to guard its
// delete on the life it examined. It is read without decoding labels,
// claude_args or extra_env, and without parsing started_at or ended_at, so a
// row a hand edit made malformed is judged like any other (SR-5.5). Zero
// values mean NULL.
type ExpireCandidate struct {
	ClaudeInstanceID string
	// TmuxSessionName is the recorded session name.
	TmuxSessionName string
	// PID and ProcStarttime are the SessionStart identity, the agent process
	// the process_alive check reads (SR-12.2); 0 and "" = none recorded.
	PID           int
	ProcStarttime string
	// Identity is the row's launch identity (SR-3.6). Identity.Token is ""
	// unless the stored token is well formed (SR-5.5). It equals GetSpawn's
	// Identity for the same row.
	Identity LaunchIdentity
	// Snapshot is the row snapshot expire examined: its delete applies only
	// while the row still holds it (SR-12.3, SR-5.3). It equals GetSpawn's
	// Snapshot for the same row.
	Snapshot RowSnapshot
}

// listExpireCandidatesSQL is ListExpireCandidates' one statement: the
// finished rows with an ended_at older than the cutoff, compared as stored
// text, in instance-id order.
const listExpireCandidatesSQL = `SELECT claude_instance_id, ` + lifeColumns + `
   FROM spawns
  WHERE ` + finishedStateGuardSQL + `
    AND ended_at IS NOT NULL
    AND ended_at < ?
  ORDER BY claude_instance_id`

// ListExpireCandidates returns an ExpireCandidate for every finished row
// (state ended or missing) whose ended_at is set and older than cutoff:
// expire's candidate read (SR-12.1, SR-12.2; Appendix F.4). ended_at is
// compared as stored text against cutoff in the store's CURRENT_TIMESTAMP
// layout (storeTimestamp: UTC, whole seconds) (SR-5.3). Live rows, pending
// included, and rows with a NULL ended_at are never returned. The store never
// reads the clock: the caller passes the cutoff, and to select every finished
// row with an ended_at it passes a cutoff later than any stored ended_at.
//
// The snapshot and launch identity are read through lifeColumns, the
// fragment GetSpawn selects, so Snapshot.StartedAt is the stored text the
// snapshot-match condition compares, and PID, ProcStarttime and
// TmuxSessionName are copied from the snapshot. launch_token is decoded by
// decodeLaunchToken and the nullable columns read NULL as their zero value,
// so no stored value of those columns fails the read; labels, claude_args and
// extra_env are never read (SR-5.5).
//
// Candidates are returned in instance-id order, so a run's per-socket stop
// and budget cut-off are reproducible. A failure is returned as a wrapped
// error and fails the verb (SR-12.1). It emits no trail event.
func (s *Store) ListExpireCandidates(cutoff time.Time) ([]ExpireCandidate, error) {
	args := append(finishedStateGuardArgs(), storeTimestamp(cutoff))
	rows, err := s.db.Query(listExpireCandidatesSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list expire candidates: %w", err)
	}
	defer rows.Close()
	var cands []ExpireCandidate
	for rows.Next() {
		var (
			c    ExpireCandidate
			life lifeScan
		)
		dest := append([]any{&c.ClaudeInstanceID}, life.dest()...)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("store: list expire candidates scan: %w", err)
		}
		c.Snapshot, c.Identity = life.result()
		c.PID = c.Snapshot.PID
		c.ProcStarttime = c.Snapshot.ProcStarttime
		c.TmuxSessionName = c.Snapshot.TmuxSessionName
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list expire candidates iterate: %w", err)
	}
	return cands, nil
}

// deleteFinishedIfSameLifeSQL is DeleteFinishedIfSameLife's one statement:
// the row removed, guarded by a finished state and the examined row snapshot
// (SR-5.3, SR-12.3).
const deleteFinishedIfSameLifeSQL = `DELETE FROM spawns
  WHERE claude_instance_id = ? AND ` + finishedStateGuardSQL + ` AND ` + snapshotMatchSQL

// DeleteFinishedIfSameLife is expire's delete of a row whose lookup was Gone
// (SR-12.3, SR-5.3, SR-5.6, SR-5.8; Appendix F.4). It is one conditional
// statement that applies only while the row is finished (ended or missing)
// and its row snapshot equals examined, the snapshot ListExpireCandidates
// read, compared on the values exactly as stored through the store's one
// snapshot-match condition (SR-5.3). resume's move to pending, a restore, a
// reuse's reset and every hook write advance row_version (SR-5.2), so any of
// them landing after expire read the row makes the delete apply nothing. The
// row's dependent rows go by the schema's foreign-key actions, as DeleteSpawn
// relies on; it touches nothing else, emits no trail event, and never touches
// tmux or transcripts.
//
// It returns CondApplied when the row was deleted; CondChanged, having
// deleted nothing, when the row exists but is live or pending now or its
// snapshot differs; CondAbsent when no row has the id. A store failure is
// returned as a wrapped error with a zero CondResult, never as a CondResult
// value (SR-5.8).
func (s *Store) DeleteFinishedIfSameLife(instanceID string, examined RowSnapshot) (CondResult, error) {
	const errPrefix = "store: delete finished if same life"
	args := append([]any{instanceID}, finishedStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(examined)...)
	applied, err := s.execGuarded(deleteFinishedIfSameLifeSQL, args, errPrefix)
	if err != nil {
		return 0, err
	}
	if applied {
		return CondApplied, nil
	}
	return s.condNotApplied(instanceID, errPrefix)
}
