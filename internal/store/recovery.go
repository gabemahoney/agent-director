package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// LiveSpawnIdentity is find-missing's read of one live row (SR-11.7,
// Appendix F.4): what the sweep needs to judge the row's liveness by its
// agent's process, to consult tmux when that process cannot be checked, and
// to guard its write on the life it examined. Zero values mean NULL,
// following the COALESCE scan convention (spawns.go). Its fields grow
// additively (SR-16.1).
type LiveSpawnIdentity struct {
	InstanceID string
	// State is the row's stored state: one of the live states, pending
	// included.
	State string
	// PID and ProcStarttime are the SessionStart identity, the agent
	// process the sweep reads first (SR-11.1); 0 and "" = none recorded.
	PID           int
	ProcStarttime string
	// LaunchStartedAtMillis is launch_started_at in milliseconds since the
	// epoch, the start the pending grace period is measured from (SR-11.2);
	// 0 = absent: NULL, a stored value that is not an integer, or an integer
	// outside the years 0 to 9999 UTC (decodeLaunchStartedAt, SR-5.5).
	LaunchStartedAtMillis int64
	// TmuxSessionName is the recorded session name, the name the tmux path
	// checks and reports when another session holds it (SR-11.1).
	TmuxSessionName string
	// LivenessNote is the row's current liveness_note; "" = NULL. The sweep
	// writes a note only when it differs from this one and clears one only
	// when there is one (SR-11.4).
	LivenessNote string
	// Snapshot is the row snapshot the sweep examined: every mark, note
	// write and clear applies only while the row still holds it (SR-11.6).
	// It equals GetSpawn's Snapshot for the same row.
	Snapshot RowSnapshot
	// Identity is the row's launch identity (SR-3.6, SR-11.1): the pane
	// identity for agent-process selection, and the token, socket and server
	// identity for the tmux path. Identity.Token is "" unless the stored
	// token is well formed (SR-5.5). It equals GetSpawn's Identity for the
	// same row.
	Identity LaunchIdentity
	// LaunchOwner is the process that began the row's current launch, while
	// that launch holds the row (b.kdf): the sweep does not judge a pending
	// row whose owner is provably alive. The zero value (PID 0) records no
	// owner. It equals GetSpawn's LaunchOwner for the same row.
	LaunchOwner LaunchOwner
	// Agent is the row's agent column as stored (TEXT NOT NULL DEFAULT ''),
	// read as is (SR-3.1); only "codex" takes the Codex end of find-missing and
	// pause.
	Agent string
	// EndReportedAtMillis is end_reported_at as Spawn.EndReportedAtMillis
	// reads it: milliseconds since the epoch of a Codex row's end report; 0 =
	// absent (decodeEndReportedAt). It equals GetSpawn's for the same row.
	EndReportedAtMillis int64
}

// ListLiveSpawnIdentities returns a LiveSpawnIdentity for every row in a
// live (non-terminal) state: find-missing's live-row read (SR-11.7).
// `pending` is included, since a launch that never reported in is still
// live in the store; each row's state and launch start let the sweep leave
// a `pending` row inside its pending grace period untouched (SR-11.2).
//
// The snapshot and launch identity are read through lifeColumns, which
// selects started_at through CAST, so Snapshot.StartedAt is the stored text
// the snapshot-match condition compares; the launch owner is read through
// launchOwnerColumns, as the full-row Spawn reads do. launch_started_at is
// decoded by decodeLaunchStartedAt and launch_token by decodeLaunchToken, and
// the nullable columns read NULL as their zero value through COALESCE. Stored
// launch starts, tokens and text identity or owner values do not fail the read.
// A wrong storage class (such as non-numeric text) in row_version, pid,
// tmux_server_pid, tmux_server_started, pane_pid or launch_owner_pid fails the
// whole read: these columns are scanned into Go integers. The structured
// columns (labels, agent_args, extra_env) are never read (SR-5.5).
//
// Order is unspecified. Callers that need stable ordering sort the
// result themselves.
func (s *Store) ListLiveSpawnIdentities() ([]LiveSpawnIdentity, error) {
	placeholders := make([]string, len(liveStates))
	args := make([]any, len(liveStates))
	for i, st := range liveStates {
		placeholders[i] = "?"
		args[i] = st
	}
	q := "SELECT instance_id, state, launch_started_at, COALESCE(liveness_note, ''), " + lifeColumns +
		", " + launchOwnerColumns +
		", agent, end_reported_at FROM spawns WHERE state IN (" + strings.Join(placeholders, ",") + ")"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list live identities: %w", err)
	}
	defer rows.Close()
	var ids []LiveSpawnIdentity
	for rows.Next() {
		var (
			it              LiveSpawnIdentity
			launchStartedAt any
			endReportedAt   any
			life            lifeScan
		)
		dest := append([]any{&it.InstanceID, &it.State, &launchStartedAt, &it.LivenessNote}, life.dest()...)
		dest = append(dest, launchOwnerDest(&it.LaunchOwner)...)
		dest = append(dest, &it.Agent, &endReportedAt)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("store: list live identities scan: %w", err)
		}
		it.LaunchStartedAtMillis = decodeLaunchStartedAt(launchStartedAt)
		it.EndReportedAtMillis = decodeEndReportedAt(endReportedAt)
		it.Snapshot, it.Identity = life.result()
		it.PID = it.Snapshot.PID
		it.ProcStarttime = it.Snapshot.ProcStarttime
		it.TmuxSessionName = it.Snapshot.TmuxSessionName
		ids = append(ids, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list live identities iterate: %w", err)
	}
	return ids, nil
}

// emitOrphanCloseoutTick emits the one ad.find_missing.tick of a permission
// request shared recovery closed with its row's mark (SR-A-2.5): reconciliation
// reason permission_orphan_closeout, carrying the request's token. A
// trail-emit failure must not fail the store call (SR-A-3.2).
func emitOrphanCloseoutTick(instanceID, requestToken string) {
	emitOrphanCloseoutTickContext(context.Background(), instanceID, requestToken)
}

func emitOrphanCloseoutTickContext(ctx context.Context, instanceID, requestToken string, sources ...string) {
	source := "ad_find_missing"
	if len(sources) > 0 && sources[0] == "ad_recovery" {
		source = sources[0]
	}
	_ = trail.Emit(ctx, "ad.find_missing.tick", map[string]any{
		"instance_id":           instanceID,
		"request_token":         requestToken,
		"prior_state":           nil,
		"new_state":             nil,
		"reconciliation_reason": "permission_orphan_closeout",
		"source":                source,
	})
}

// DeleteSpawn removes a row by id. Returns ErrSpawnNotFound when the
// id matches no row. Per SRD §12 the `delete` verb (admin path)
// composes this primitive per id. Does NOT touch tmux sessions or
// JSONL transcripts; the caller is expected to have already torn
// those down or accept the orphan. The row's permission requests, session
// history, subagent entries and claimable launches go with it through the
// schema's ON DELETE CASCADE foreign keys (foreign_keys=1), with no delete
// statement of their own.
func (s *Store) DeleteSpawn(instanceID string) error {
	const q = `DELETE FROM spawns WHERE instance_id = ?`
	res, err := s.execFamily(instanceID, q, instanceID)
	if err != nil {
		return fmt.Errorf("store: delete spawn: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete spawn rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	return nil
}

// RecoveryWrites reuses the lifecycle statements with nonwaiting admission.
// Command and hook callers keep their original lock policies.
type RecoveryWrites struct {
	*Store
	samples map[string]Spawn
}

func (s *Store) RecoveryWrites(rows ...Spawn) *RecoveryWrites {
	samples := make(map[string]Spawn, len(rows))
	for _, row := range rows {
		samples[row.InstanceID] = row
	}
	return &RecoveryWrites{Store: s, samples: samples}
}

func (s *RecoveryWrites) guard(id, statement string, args []any) (string, []any) {
	if row, ok := s.samples[id]; ok {
		statement += " AND observation_version=? AND permission_epoch=?"
		args = append(args, row.ObservationVersion, row.PermissionEpoch)
	}
	return statement, args
}

func (s *RecoveryWrites) MarkMissingIfSameLife(id string, snapshot RowSnapshot) (string, CondResult, error) {
	prior, res, _, err := s.MarkMissingIfSameLifeFrom(id, snapshot, "", ProcessExit{})
	return prior, res, err
}

// MarkMissingIfSameLifeFrom is the nonwaiting mark recording, in the mark's
// own statement, the state record of missing from src (EvidenceProcess for a
// gone agent process, EvidenceTmux for a session lookup; "" records none) and
// the process record: exit's exited record when the mark acted on that
// process's proven exit, otherwise NULL (terminalProcessEvidenceSet).
// recorded reports that the mark applied and stored that exited record.
func (s *RecoveryWrites) MarkMissingIfSameLifeFrom(id string, snapshot RowSnapshot, src string, exit ProcessExit) (prior string, res CondResult, recorded bool, err error) {
	args := append(markMissingArgs(src, exit), id)
	args = append(args, liveStateGuardArgs()...)
	args = append(args, snapshotMatchArgs(snapshot)...)
	statement, args := s.guard(id, markMissingIfSameLifeSQL, args)
	prior, res, err = s.terminalMarkWithoutWait("store: recovery missing", statement, args, id, DecisionReasonFindMissing, &recorded)
	if err != nil {
		return "", 0, false, err
	}
	return prior, res, recorded && res == CondApplied, nil
}

func (s *RecoveryWrites) MarkEndedIfSameLife(id string, snapshot RowSnapshot) (string, CondResult, error) {
	prior, res, _, err := s.MarkEndedIfSameLifeFrom(id, snapshot, ProcessExit{})
	return prior, res, err
}

// MarkEndedIfSameLifeFrom is the nonwaiting move to ended recording exit's
// exited record in the move's own statement (Store.MarkEndedIfSameLifeFrom);
// recorded reports that the move applied and stored it.
func (s *RecoveryWrites) MarkEndedIfSameLifeFrom(id string, snapshot RowSnapshot, exit ProcessExit) (prior string, res CondResult, recorded bool, err error) {
	statement, args := s.guard(id, markEndedIfSameLifeSQL, endedIfSameLifeArgs(id, snapshot, exit))
	prior, res, err = s.terminalMarkWithoutWait("store: recovery ended", statement, args, id, DecisionReasonEnded, &recorded)
	if err != nil {
		return "", 0, false, err
	}
	return prior, res, recorded && res == CondApplied, nil
}

// recoveryExec checks the no-match outcome under the same write boundary and
// refreshes changed family contributions atomically. No follow-up read can
// queue behind an abandoned commit.
func (s *Store) recoveryExec(id, q string, args []any) (CondResult, error) {
	result := CondChanged
	var exits []displayTransition
	ctx, cancel := s.operationContext(0)
	defer cancel()
	err := s.inWriteTxContext(ctx, 0, func(ctx context.Context, conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, q, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var exists bool
			if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM spawns WHERE instance_id=?)", id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				result = CondAbsent
			}
			return nil
		}
		result = CondApplied
		if err := s.refreshFamilies(connQuerier{ctx, conn}); err != nil {
			return err
		}
		exits, err = takeDisplayExits(ctx, conn, id)
		return err
	})
	if err != nil {
		return 0, err
	}
	emitDisplayTransitionsContext(ctx, exits)
	return result, nil
}

func (s *RecoveryWrites) SetLivenessNoteIfSameLife(id string, snapshot RowSnapshot, note string) (CondResult, error) {
	args := append([]any{note, id}, liveStateGuardArgs()...)
	statement, args := s.guard(id, setLivenessNoteIfSameLifeSQL, append(args, snapshotMatchArgs(snapshot)...))
	return s.recoveryExec(id, statement, args)
}

func (s *RecoveryWrites) ClearLivenessIfSameLife(id string, snapshot RowSnapshot) (CondResult, error) {
	args := append([]any{id}, liveStateGuardArgs()...)
	statement, args := s.guard(id, clearLivenessIfSameLifeSQL, append(args, snapshotMatchArgs(snapshot)...))
	return s.recoveryExec(id, statement, args)
}

func (s *RecoveryWrites) NoteUnreportedIfSameLife(id string, snapshot RowSnapshot) (CondResult, error) {
	args := append([]any{LivenessNoteUnreported, id, StatePending, LivenessNoteProvenanceConflict}, snapshotMatchArgs(snapshot)...)
	statement, args := s.guard(id, noteUnreportedIfSameLifeSQL, args)
	return s.recoveryExec(id, statement, args)
}

func (s *RecoveryWrites) AdoptIdentityIfSameLife(id string, snapshot RowSnapshot, identity LaunchIdentity) (CondResult, RowSnapshot, error) {
	args := append(adoptIdentityArgs(identity), id)
	args = append(args, liveStateGuardArgs()...)
	statement, args := s.guard(id, adoptIdentityIfSameLifeSQL, append(args, snapshotMatchArgs(snapshot)...))
	result, err := s.recoveryExec(id, statement, args)
	if result != CondApplied || err != nil {
		return result, RowSnapshot{}, err
	}
	// The adoption invalidated pane evidence in its own statement. Carry
	// that known advance into the verdict's observation guard, without a
	// re-read that could accept an intervening observer's version.
	if row, ok := s.samples[id]; ok {
		row.ObservationVersion++
		s.samples[id] = row
	}
	snapshot.RowVersion++
	return result, snapshot, nil
}

// RepairCheckPermissionSample revalidates every judged request, not just the
// spawn's row_version: delivery/proof facts can change without a row write.
func (s *Store) RepairCheckPermissionSample(row Spawn, sampled []PermissionRow) (string, CondResult, error) {
	var state string
	result := CondChanged
	var exits []displayTransition
	ctx, cancel := s.operationContext(0)
	defer cancel()
	err := s.inWriteTxContext(ctx, 0, func(ctx context.Context, conn *sql.Conn) error {
		if _, err := lockedObservationRow(ctx, conn, row); err != nil {
			return err
		}
		current, err := queryPermissionRows(func(q string, args ...any) (*sql.Rows, error) { return conn.QueryContext(ctx, q, args...) },
			"SELECT "+permissionColumns+" FROM permission_requests pr WHERE instance_id=? ORDER BY request_id", "store: recovery requests", row.InstanceID)
		if err != nil {
			return err
		}
		if len(current) != len(sampled) {
			return errDisplaySampleChanged
		}
		byID := make(map[int64]PermissionRow, len(sampled))
		for _, sample := range sampled {
			byID[sample.RequestID] = sample
		}
		for _, request := range current {
			if sample, ok := byID[request.RequestID]; !ok || sample != request {
				return errDisplaySampleChanged
			}
		}
		var args []any
		for range 4 {
			args = append(args, StateWaiting, StateWorking)
		}
		args = append(args, row.InstanceID, StateCheckPermission)
		args = append(args, snapshotMatchArgs(row.Snapshot)...)
		if err := conn.QueryRowContext(ctx, repairCheckPermissionSQL, args...).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		result = CondApplied
		if err := s.refreshFamilies(connQuerier{ctx, conn}); err != nil {
			return err
		}
		exits, err = takeDisplayExits(ctx, conn, row.InstanceID)
		return err
	})
	if err != nil {
		return "", 0, err
	}
	emitDisplayTransitionsContext(ctx, exits)
	return state, result, nil
}

// WithRecoveryTickSource returns an immutable per-call audit view. This changes
// no write policy, evidence, permission reason or event name. Legacy callers
// retain ad_find_missing; reads and pause explicitly select ad_recovery.
// Recovery paths must reuse this view for orphan-closeout tick attribution
// and pair it with the API recoveryTickTrail wrapper for the same source.
func (s *Store) WithRecoveryTickSource(source string) *Store {
	scoped := *s
	scoped.recoveryTickSource = source
	return &scoped
}
