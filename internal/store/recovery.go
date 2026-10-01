package store

import (
	"context"
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
	ClaudeInstanceID string
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
}

// ListLiveSpawnIdentities returns a LiveSpawnIdentity for every row in a
// live (non-terminal) state: find-missing's live-row read (SR-11.7).
// `pending` is included, since a launch that never reported in is still
// live in the store; each row's state and launch start let the sweep leave
// a `pending` row inside its pending grace period untouched (SR-11.2).
//
// The snapshot and launch identity are read through lifeColumns, the
// fragment every read returning a Spawn selects, so Snapshot.StartedAt is
// the stored text the snapshot-match condition compares. launch_started_at
// is decoded by decodeLaunchStartedAt and launch_token by decodeLaunchToken,
// and the nullable columns read NULL as their zero value through COALESCE,
// so no stored launch start, token or identity value fails the read; the
// structured columns (labels, claude_args, extra_env) are never read
// (SR-5.5).
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
	q := "SELECT claude_instance_id, state, launch_started_at, COALESCE(liveness_note, ''), " + lifeColumns +
		" FROM spawns WHERE state IN (" + strings.Join(placeholders, ",") + ")"
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
			life            lifeScan
		)
		dest := append([]any{&it.ClaudeInstanceID, &it.State, &launchStartedAt, &it.LivenessNote}, life.dest()...)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("store: list live identities scan: %w", err)
		}
		it.LaunchStartedAtMillis = decodeLaunchStartedAt(launchStartedAt)
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

// CloseOrphanedPermissionRequests denies all open permission_requests rows for
// a Spawn that has been marked missing. Each open row receives its own
// per-row UPDATE via DecidePermissionRequest with DecisionReasonFindMissing so
// the relay polling loop observes a fail-closed deny rather than spinning to
// its own internal timeout. No-op if the Spawn has no open rows.
//
// find-missing calls this immediately after each mark that applied
// (MarkMissingIfSameLife). Per-row errors are surfaced to the caller; the
// caller decides whether to log-and-continue or abort the sweep.
func (s *Store) CloseOrphanedPermissionRequests(instanceID string) error {
	rows, err := s.OpenPermissionRequestsForSpawn(instanceID)
	if err != nil {
		return fmt.Errorf("store: close orphaned permission requests: %w", err)
	}
	for _, row := range rows {
		ok, err := s.DecidePermissionRequest(instanceID, row.RequestToken, "deny", DecisionReasonFindMissing, WriterProcessFindMissing)
		if err != nil {
			return fmt.Errorf("store: close orphaned permission request (token=%s): %w", row.RequestToken, err)
		}
		// Emit one ad.find_missing.tick per successfully closed row (SR-A-2.5).
		// The Epic 3 ad.row_mutation.committed event fires in DecidePermissionRequest
		// for the same write; this event adds the reconciliation_reason layer.
		// A trail-emit failure must not fail the store call (SR-A-3.2).
		if ok {
			_ = trail.Emit(context.Background(), "ad.find_missing.tick", map[string]any{
				"claude_instance_id":    instanceID,
				"request_token":         row.RequestToken,
				"prior_state":           nil,
				"new_state":             nil,
				"reconciliation_reason": "permission_orphan_closeout",
				"source":                "ad_find_missing",
			})
		}
	}
	return nil
}

// DeleteSpawn removes a row by id. Returns ErrSpawnNotFound when the
// id matches no row. Per SRD §12 the `delete` verb (admin path)
// composes this primitive per id. Does NOT touch tmux sessions or
// JSONL transcripts; the caller is expected to have already torn
// those down or accept the orphan.
func (s *Store) DeleteSpawn(instanceID string) error {
	const q = `DELETE FROM spawns WHERE claude_instance_id = ?`
	res, err := s.db.Exec(q, instanceID)
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
