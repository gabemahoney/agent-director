package api

import (
	"context"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// CheckPermissionRepairStore is the store surface of find-missing's repair of
// stale check_permission rows (b.146 rule 9): the live-row read, the read of
// a row's permission requests, and the guarded repair write. *store.Store
// satisfies it.
type CheckPermissionRepairStore interface {
	ListLiveSpawnIdentities() ([]LiveSpawnIdentity, error)
	PermissionRequestsForSpawn(instanceID string) ([]PermissionRow, error)
	RepairCheckPermissionIfSameLife(instanceID string, examined RowSnapshot) (newState string, res CondResult, err error)
}

var _ CheckPermissionRepairStore = (*store.Store)(nil)

// reasonStaleCheckPermission is the reconciliation_reason of the
// ad.find_missing.tick a repair of a stale check_permission row writes.
const reasonStaleCheckPermission = "stale_check_permission"

// RepairCheckPermission retains the exported, permission-only capability seam.
// Full reads and find-missing use the same per-row repair below after their
// fixed selection. This helper adds no process, tmux, or delivery capability.
// It has no operation context, so its bounded recovery ticks derive from
// Background.
func RepairCheckPermission(s CheckPermissionRepairStore, v RelayView, lg FindMissingLogger) {
	var rows []Spawn
	if selected, ok := s.(interface {
		RecoveryRows([]string, store.SettleRead) ([]Spawn, error)
	}); ok {
		var err error
		rows, err = selected.RecoveryRows(nil, store.SettleRead{})
		if err != nil {
			logFindMissing(lg, "RecoveryRows", "(repair)", err)
			return
		}
	} else {
		selected, err := s.ListLiveSpawnIdentities()
		if err != nil {
			logFindMissing(lg, "ListLiveSpawnIdentities", "(repair)", err)
			return
		}
		for _, row := range selected {
			rows = append(rows, Spawn{InstanceID: row.InstanceID, State: row.State, Snapshot: row.Snapshot})
		}
	}
	read := s.PermissionRequestsForSpawn
	if bounded, ok := s.(interface {
		PermissionRequestsForSpawnWithin(string, time.Duration) ([]PermissionRow, error)
	}); ok {
		read = func(id string) ([]PermissionRow, error) { return bounded.PermissionRequestsForSpawnWithin(id, 0) }
	}
	j := newRelayJudge(v)
	for _, row := range rows {
		if row.State != store.StateCheckPermission {
			continue
		}
		judged, err := readJudgedRequests(read, j, row.InstanceID)
		if err != nil {
			logFindMissing(lg, "PermissionRequestsForSpawn", row.InstanceID, err)
			continue
		}
		_, _, err = repairCheckPermissionRow(recoveryTrail(context.Background()), s, row, judged, j)
		if err != nil {
			logFindMissing(lg, "RepairCheckPermission", row.InstanceID, err)
		}
	}
}

// repairCheckPermissionRow writes its repair tick through emit.
func repairCheckPermissionRow(emit trailEmit, s any, row Spawn, judged *judgedRequestSet, j relayJudge) (string, CondResult, error) {
	if row.State != store.StateCheckPermission || judged == nil || mayHaveJudgedHook(j, judged) {
		return "", CondChanged, nil
	}
	var state string
	var result CondResult
	var err error
	if guarded, ok := s.(interface {
		RepairCheckPermissionSample(Spawn, []PermissionRow) (string, CondResult, error)
	}); ok {
		state, result, err = guarded.RepairCheckPermissionSample(row, judged.rows)
	} else if legacy, ok := s.(CheckPermissionRepairStore); ok {
		state, result, err = legacy.RepairCheckPermissionIfSameLife(row.InstanceID, row.Snapshot)
	} else {
		return "", CondChanged, nil
	}
	if err == nil && result == CondApplied {
		emitMarkTick(emit, row.InstanceID, row.State, state, reasonStaleCheckPermission, nil)
	}
	return state, result, err
}

func mayHaveJudgedHook(j relayJudge, judged *judgedRequestSet) bool {
	now := j.now()
	for _, pr := range judged.rows {
		if !pr.Closed() && now.Before(j.confirmBy(pr)) && judged.verdicts[pr.RequestID] != hookGone {
			return true
		}
	}
	return false
}
