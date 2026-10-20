package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// statusRowColumns excludes unrelated structured columns and parsed timestamps.
// lifeColumns retains the exact snapshot used by observation revalidation.
const statusRowColumns = `instance_id, state, launch_started_at, family_state,
 relay_mode, COALESCE(life_number, 0), agent, jsonl_path, end_reported_at,
 codex_startup, claude_capacity, permission_epoch, stuck_version,
 COALESCE(stuck_observation, ''), ` + lifeColumns

// SpawnStatusRow reads only status, context and permission-observation evidence
// in one snapshot. Malformed labels, arguments or environment cannot fail it.
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

func scanSpawnStatus(sc rowScanner, reported bool) (Spawn, error) {
	var row Spawn
	var life lifeScan
	var launch, agent, path, endReport, startup, capacity any
	dest := []any{&row.InstanceID, &row.State, &launch, &row.FamilyState,
		&row.RelayMode, &row.LifeNumber, &agent, &path, &endReport,
		&startup, &capacity, &row.PermissionEpoch, &row.StuckVersion, &row.StuckObservation}
	dest = append(dest, life.dest()...)
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
	return row, nil
}
