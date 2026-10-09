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
	// LaunchOwner is the process that began the row's current launch, while
	// that launch holds the row (b.kdf): the sweep does not judge a pending
	// row whose owner is provably alive. The zero value (PID 0) records no
	// owner. It equals GetSpawn's LaunchOwner for the same row.
	LaunchOwner LaunchOwner
}

// ListLiveSpawnIdentities returns a LiveSpawnIdentity for every row in a
// live (non-terminal) state: find-missing's live-row read (SR-11.7).
// `pending` is included, since a launch that never reported in is still
// live in the store; each row's state and launch start let the sweep leave
// a `pending` row inside its pending grace period untouched (SR-11.2).
//
// The snapshot and launch identity are read through lifeColumns, and the
// launch owner through launchOwnerColumns, the fragments every read returning
// a Spawn selects, so Snapshot.StartedAt is the stored text the
// snapshot-match condition compares. launch_started_at is decoded by
// decodeLaunchStartedAt and launch_token by decodeLaunchToken, and the
// nullable columns read NULL as their zero value through COALESCE, so no
// stored launch start, token, identity or owner value fails the read; the
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
		", " + launchOwnerColumns +
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
		dest = append(dest, launchOwnerDest(&it.LaunchOwner)...)
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

// emitOrphanCloseoutTick emits the one ad.find_missing.tick of a permission
// request find-missing closed with its row's mark (SR-A-2.5): reconciliation
// reason permission_orphan_closeout, carrying the request's token. A
// trail-emit failure must not fail the store call (SR-A-3.2).
func emitOrphanCloseoutTick(instanceID, requestToken string) {
	_ = trail.Emit(context.Background(), "ad.find_missing.tick", map[string]any{
		"claude_instance_id":    instanceID,
		"request_token":         requestToken,
		"prior_state":           nil,
		"new_state":             nil,
		"reconciliation_reason": "permission_orphan_closeout",
		"source":                "ad_find_missing",
	})
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
