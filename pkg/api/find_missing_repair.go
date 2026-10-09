package api

import (
	"context"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
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

// RepairCheckPermission is find-missing's repair of stale check_permission
// rows (b.146 rule 9, problem 3); Client.FindMissing runs it after the sweep
// (FindMissing), on a fresh read of the live rows. A row in check_permission
// with relay_mode on is moved out of it when none of its permission requests
// still awaits an answer (not acked and not answered at the pane, or,
// recorded before schema v7, undecided) and none has a relay hook that may
// still run: to waiting when the main agent's idle-prompt Notification is the
// last hook it recorded (idle_since set), to working otherwise. Such a row's
// agent moved on from its last permission dialog and the hook that would have
// moved the row was lost (a lost Stop after a delivered timeout deny, for
// example).
//
// For each such row it reads every request of the row and judges, through v,
// each request find-missing's mark has not closed (closed_at NULL; b.146
// rule 12) whose confirm_by has not passed (rule 14's check: a relay hook
// past its settle instant has been killed or has ended on its own): one
// judged alive or can't tell may still run, and the row is left as it is for
// a later run. A closed request is not judged (mayHaveLiveHook says why), so
// after the mark and a resume a closed request whose hook cannot be checked
// does not hold the repair off until its confirm_by. Otherwise one guarded
// statement (RepairCheckPermissionIfSameLife) writes the repair, only while
// the row is in check_permission with relay_mode on, still holds the snapshot
// read here and has no request that still awaits an answer, so a request
// recorded or a hook that wrote after this read (either advances the row's
// snapshot) wins. The hooks are judged before that statement reads the
// requests (the check-before-read rule).
//
// An applied repair writes one ad.find_missing.tick (prior_state
// check_permission, new_state the state written, reconciliation_reason
// stale_check_permission), fail-open. Per-row store errors, and a failed
// live-row read, are logged on lg (a nil lg writes nothing) and never fail
// find-missing.
func RepairCheckPermission(s CheckPermissionRepairStore, v RelayView, lg FindMissingLogger) {
	rows, err := s.ListLiveSpawnIdentities()
	if err != nil {
		logFindMissing(lg, "ListLiveSpawnIdentities", "(repair)", err)
		return
	}
	j := newRelayJudge(v)
	for _, it := range rows {
		if it.State != store.StateCheckPermission {
			continue
		}
		reqs, err := s.PermissionRequestsForSpawn(it.ClaudeInstanceID)
		if err != nil {
			logFindMissing(lg, "PermissionRequestsForSpawn", it.ClaudeInstanceID, err)
			continue
		}
		if mayHaveLiveHook(j, reqs) {
			continue
		}
		newState, res, err := s.RepairCheckPermissionIfSameLife(it.ClaudeInstanceID, it.Snapshot)
		if err != nil {
			logFindMissing(lg, "RepairCheckPermissionIfSameLife", it.ClaudeInstanceID, err)
			continue
		}
		if res != CondApplied {
			continue
		}
		_ = trail.Emit(context.Background(), findMissingTickEvent, map[string]any{
			"claude_instance_id":    it.ClaudeInstanceID,
			"prior_state":           store.StateCheckPermission,
			"new_state":             newState,
			"reconciliation_reason": reasonStaleCheckPermission,
			"source":                findMissingTickSource,
		})
	}
}

// mayHaveLiveHook reports whether a request of reqs has a relay hook that may
// still run and matter to the row: one find-missing's mark has not closed,
// whose confirm_by has not passed and whose hook j does not judge gone
// (alive, or can't tell).
//
// A closed request (closed_at set, b.146 rule 12) is skipped unjudged. The
// mark closed it when it judged the row's agent gone and moved the row to
// missing, so the row is in check_permission again only through a later hook
// (after a resume, a later life's), and the closed request is not what holds
// it there. Its leftover relay hook cannot change the row: one whose parent
// is gone fails its parent check and acks nothing, and even one whose parent
// lives (the mark judged wrong) writes only its own request's delivered_at,
// never the row, and reads a verdict at once (the mark leaves no closed
// request undecided), so it ends within one poll. The closed request no
// longer awaits an answer, and the repair statement ignores it already
// (awaitingAnswerSQL). Judging it would let a hook that cannot be checked
// (another or unreadable pid namespace, unreadable /proc) hold the row in
// check_permission until its confirm_by, up to a relay window later.
func mayHaveLiveHook(j relayJudge, reqs []PermissionRow) bool {
	now := j.now()
	for _, pr := range reqs {
		if pr.Closed() || !now.Before(j.confirmBy(pr)) {
			continue
		}
		if j.hook(pr) != hookGone {
			return true
		}
	}
	return false
}
