package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// statusRowColumns is SpawnStatusRow's partial projection, in scanSpawnStatus's
// order. It excludes unrelated structured columns and parsed timestamps, and
// shares lifeColumns (the exact snapshot observation revalidation compares)
// and paneColumns with the full-row spawnColumns.
const statusRowColumns = `instance_id, state, launch_started_at, family_state,
 relay_mode, COALESCE(life_number, 0), agent, jsonl_path, end_reported_at,
 codex_startup, claude_capacity, permission_epoch, observation_version,
 COALESCE(display_observation, ''), ` + lifeColumns + `, ` + paneColumns + `,
 state_changed_ms, ` + evidenceColumns

// SpawnStatusRow is the partial Spawn reader: it reads only status, context
// and display-observation evidence in one snapshot. It fills InstanceID,
// State, ReportedState and FamilyState, LaunchStartedAtMillis, RelayMode,
// LifeNumber, Agent, JSONLPath, EndReportedAtMillis, CodexStartup and
// ClaudeCapacity; Snapshot and Identity exactly as lifeColumns reads them
// (Snapshot.StartedAt as stored text), with RowVersion, SessionID, PID,
// ProcStarttime and TmuxSessionName taken from Snapshot; PermissionEpoch,
// ObservationVersion and DisplayObservation; and the idle_screen timeout
// override and committed pane baseline (paneColumns); and StateChangedMillis
// with the main-field evidence (evidenceColumns), which StateConfirmation
// applies to the reported state.
//
// All other fields stay unpopulated; their zero values do not establish stored
// absence. These include LaunchOwner, IdleSince,
// SubagentTracking, NoPreTrust, EndedAtText, the parsed StartedAt, LastSeenAt
// and EndedAt, Labels, AgentArgs, ExtraEnv and the other unselected metadata.
// Because labels, arguments and environment are never read, malformed values
// there cannot fail it.
//
// API status consumes its status/context/display evidence through
// permissionStatusReader; CursorMissingReads delegates to this path. Display
// revalidation (lockedObservationRow, observationLanded) reuses its projection
// and scanner. Reuse this reader for this evidence; use GetSpawn,
// GetSpawnSettled or ListSpawns when omitted fields are needed.
func (s *Store) SpawnStatusRow(instanceID string, r SettleRead) (Spawn, error) {
	return s.spawnStatusRow(instanceID, r, nil)
}

func (s *Store) spawnStatusRow(instanceID string, r SettleRead, proofs []CursorMissingProof) (Spawn, error) {
	raw, args, err := cursorMissingState("state", nil, proofs)
	if err != nil {
		return Spawn{}, err
	}
	cols := strings.Replace(statusRowColumns, ", state,", ", "+raw+",", 1)
	reported, reportedArgs := r.stateExpr()
	reported, reportedArgs, err = cursorMissingState(reported, reportedArgs, proofs)
	if err != nil {
		return Spawn{}, err
	}
	family, familyArgs := r.familyExpr()
	args = append(args, reportedArgs...)
	args = append(args, familyArgs...)
	q := `SELECT ` + cols + `, ` + reported + `, ` + family + ` FROM spawns WHERE instance_id=?`
	row, err := scanSpawnStatus(s.db.QueryRowContext(s.readContext(), q, append(args, instanceID)...), true)
	if errors.Is(err, sql.ErrNoRows) {
		return Spawn{}, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return Spawn{}, err
	}
	if err := validateCursorMissingRead(row, proofs); err != nil {
		return Spawn{}, err
	}
	return row, nil
}

// scanSpawnStatus scans one row selected with statusRowColumns into the
// partial Spawn described above. When reported is set, two appended settled
// columns supply ReportedState and FamilyState; otherwise ReportedState is State.
func scanSpawnStatus(sc rowScanner, reported bool) (Spawn, error) {
	return scanSpawnStatusExtra(sc, reported, nil)
}

func scanSpawnStatusExtra(sc rowScanner, reported bool, extra []any) (Spawn, error) {
	var row Spawn
	var life lifeScan
	var pane paneScan
	var evidence evidenceScan
	var launch, agent, path, endReport, startup, capacity any
	dest := []any{&row.InstanceID, &row.State, &launch, &row.FamilyState,
		&row.RelayMode, &row.LifeNumber, &agent, &path, &endReport,
		&startup, &capacity, &row.PermissionEpoch, &row.ObservationVersion, &row.DisplayObservation}
	dest = append(dest, life.dest()...)
	dest = append(dest, pane.dest()...)
	dest = append(dest, &row.StateChangedMillis)
	dest = append(dest, evidence.dest()...)
	dest = append(dest, extra...)
	if reported {
		dest = append(dest, &row.ReportedState, &row.FamilyState)
	}
	if err := sc.Scan(dest...); err != nil {
		return Spawn{}, fmt.Errorf("store: spawn status: %w", err)
	}
	if !reported {
		row.ReportedState = row.State
	}
	row.Snapshot, row.Identity = life.result()
	row.RowVersion = row.Snapshot.RowVersion
	row.SessionID, row.PID, row.ProcStarttime = row.Snapshot.SessionID, row.Snapshot.PID, row.Snapshot.ProcStarttime
	row.TmuxSessionName = row.Snapshot.TmuxSessionName
	row.Agent, _ = agent.(string)
	row.JSONLPath, _ = path.(string)
	row.LaunchStartedAtMillis = decodeLaunchStartedAt(launch)
	row.EndReportedAtMillis = decodeEndReportedAt(endReport)
	row.CodexStartup = decodeCodexStartup(startup)
	row.ClaudeCapacity = decodeClaudeCapacity(capacity)
	pane.apply(&row)
	evidence.apply(&row)
	return row, nil
}

// RecoveryRows takes one consistent stored selection, including finished rows.
// It uses the status projection so unrelated malformed metadata cannot hide
// other agents. It also carries the exact stored CWD required by native input
// proof identity. A nil IDs slice means all rows; an empty slice means none.
func (s *Store) RecoveryRows(ids []string, settle SettleRead) ([]Spawn, error) {
	return s.recoveryRows(ids, settle, nil)
}

func (s *Store) recoveryRows(ids []string, settle SettleRead, proofs []CursorMissingProof) ([]Spawn, error) {
	raw, args, err := cursorMissingState("state", nil, proofs)
	if err != nil {
		return nil, err
	}
	cols := strings.Replace(statusRowColumns, ", state,", ", "+raw+",", 1)
	reported, ra := settle.stateExpr()
	family, fa := settle.familyExpr()
	reported, ra, err = cursorMissingState(reported, ra, proofs)
	if err != nil {
		return nil, err
	}
	q := "SELECT " + cols + ", " + launchOwnerColumns + ", COALESCE(liveness_note, ''), cwd, " + reported + ", " + family + " FROM spawns"
	args = append(args, ra...)
	args = append(args, fa...)
	if ids != nil {
		q += " WHERE instance_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	out := []Spawn{}
	stale := &CursorMissingReadError{}
	err = s.readWithin(0, "store: recovery selection", func(ctx context.Context, conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var owner LaunchOwner
			var note, cwd string
			extra := append(launchOwnerDest(&owner), &note, &cwd)
			row, err := scanSpawnStatusExtra(rows, true, extra)
			if err != nil {
				return err
			}
			if err := validateCursorMissingRead(row, proofs); err != nil {
				stale.InstanceIDs = append(stale.InstanceIDs, row.InstanceID)
				continue
			}
			row.LaunchOwner, row.LivenessNote, row.CWD = owner, note, cwd
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(stale.InstanceIDs) > 0 {
		return out, stale
	}
	return out, nil
}
