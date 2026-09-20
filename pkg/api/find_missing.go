package api

import (
	"context"
	"sort"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// FindMissingStore is the narrow store surface FindMissing needs.
// *store.Store satisfies it via the recovery primitives.
type FindMissingStore interface {
	ListLiveSpawnIdentities() ([]LiveSpawnIdentity, error)
	// MarkSpawnMissing transitions a row from any live state to `missing`.
	// Returns the prior state captured before the write, or ("", nil) when
	// no write occurred (row absent or already terminal). A non-empty prior
	// state signals "write happened" and the caller should emit a trail event.
	MarkSpawnMissing(instanceID string) (string, error)
	// SetLivenessUnverified records that a live row's process could not be
	// verified (an EACCES/EPERM probe wall). It writes
	// liveness_unverified_since + liveness_note in a single guarded UPDATE,
	// but only when liveness_unverified_since is currently NULL. The returned
	// transitioned bool is the sole NULL→set signal: true on the first write,
	// false on a repeat (which preserves the original timestamp). find-missing
	// keys exactly one probe_eacces tick off that bool (SR-8.1/8.4).
	SetLivenessUnverified(instanceID, note string) (bool, error)
	// ClearLivenessUnverified NULLs both liveness columns for a row. It is
	// idempotent (a row already clear or absent is a no-op) and is called in
	// the verified-alive case and immediately after MarkSpawnMissing in the
	// marking path (SR-8.2). MarkSpawnMissing itself is NOT widened.
	ClearLivenessUnverified(instanceID string) error
	// CloseOrphanedPermissionRequests denies all open permission_requests rows
	// for a Spawn that has just been marked missing, so any relay polling loop
	// for that Spawn receives a fail-closed deny rather than spinning to its own
	// internal timeout (SR-5.4).
	CloseOrphanedPermissionRequests(instanceID string) error
}

// FindMissingResult is the typed return shape. Count is the number of
// rows transitioned to `missing` on this sweep; IDs is the sorted
// list (sorted so the JSON envelope is deterministic across runs).
type FindMissingResult struct {
	// Count is the number of rows transitioned to missing on this sweep.
	Count int `json:"count"`
	// IDs is the sorted slice of instance ids transitioned to missing.
	// Always non-nil — encodes as [] when no rows were transitioned.
	IDs []string `json:"ids"`
	// Unverified is the number of live rows this sweep left untouched because
	// the liveness checker returned an unknown verdict (a permission wall).
	Unverified int `json:"unverified"`
	// UnverifiedIDs is the sorted slice of instance ids skipped as unverified.
	// Always non-nil — encodes as [] when no rows were unverified (same
	// discipline as IDs).
	UnverifiedIDs []string `json:"unverified_ids"`
}

// FindMissingLogger is the narrow log surface FindMissing uses to report
// per-row store/checker errors. *log.Logger satisfies it; tests pass a fake
// to inspect the message without scraping stderr.
type FindMissingLogger interface {
	Printf(format string, v ...any)
}

// findMissingImpl is the unexported verb handler called by
// (c *Client).FindMissing. It takes probe.Prober and probe.LivenessChecker
// directly and is not part of the public API surface; external consumers use
// the Client method instead.
//
// Behavior (SR-7 + SR-8):
//
//  1. List live-state identities (anything not ended/missing, including
//     pending — SRD §5.2 explicitly scans pending). Each carries the
//     recorded pid + proc_starttime.
//  2. For each row with BOTH a recorded pid and proc_starttime, ask the
//     liveness checker for an evidence-based verdict:
//     - provably-dead → mark missing in the pinned order (MarkSpawnMissing →
//     ClearLivenessUnverified → proc_absent tick → CloseOrphanedPermission-
//     Requests), the clear+tick gated on a non-empty prior state.
//     - verified-alive → skip and clear any stale liveness fields.
//     - unknown → skip THIS ROW ONLY, set the liveness fields (guarded
//     setter), record the id as unverified, and emit exactly one
//     probe_eacces tick per NULL→set transition.
//  3. Rows with a NULL pid OR NULL proc_starttime (partial identity) fall
//     back to the environ probe-set diff: a live id absent from the probe
//     set is marked missing in the same pinned order.
//
// Per-row store/checker errors are logged and skipped; the sweep does not
// abort on a transient SQLite failure. Returns the sweep result. nil error
// iff the prober + list calls succeeded; a hard prober error (e.g. /proc
// unreachable) or a list error bubbles up because there's nothing useful the
// verb can do without them.
func findMissingImpl(ctx context.Context, s FindMissingStore, p probe.Prober, chk probe.LivenessChecker, lg FindMissingLogger) (FindMissingResult, error) {
	identities, err := s.ListLiveSpawnIdentities()
	if err != nil {
		return FindMissingResult{}, err
	}

	// Partition rows: those with a full recorded identity (pid+starttime) get
	// an evidence-based checker verdict; those with a partial/absent identity
	// (NULL pid OR NULL starttime) fall back to the environ probe-set diff.
	var fallbackIDs []string
	missing := make([]string, 0)
	unverified := make([]string, 0)

	for _, it := range identities {
		if it.PID <= 0 || it.ProcStarttime == "" {
			// Partial identity → SR-7.5 fallback. Defer to the probe-set diff.
			fallbackIDs = append(fallbackIDs, it.ClaudeInstanceID)
			continue
		}
		switch chk.CheckLiveness(it.PID, it.ProcStarttime, it.ClaudeInstanceID) {
		case probe.VerdictProvablyDead:
			if markMissing(s, it.ClaudeInstanceID, lg) {
				missing = append(missing, it.ClaudeInstanceID)
			}
		case probe.VerdictVerifiedAlive:
			// Alive: skip, and clear any stale liveness fields.
			if err := s.ClearLivenessUnverified(it.ClaudeInstanceID); err != nil && lg != nil {
				lg.Printf("find-missing: ClearLivenessUnverified(%s): %v (continuing)", it.ClaudeInstanceID, err)
			}
		default: // probe.VerdictUnknown
			// Genuinely unknown (permission wall): skip THIS ROW ONLY and record
			// the liveness metadata. transitioned is the sole NULL→set signal;
			// emit exactly one probe_eacces tick per transition.
			transitioned, err := s.SetLivenessUnverified(it.ClaudeInstanceID, "probe_eacces")
			if err != nil {
				if lg != nil {
					lg.Printf("find-missing: SetLivenessUnverified(%s): %v (continuing)", it.ClaudeInstanceID, err)
				}
				continue
			}
			if transitioned {
				// Fail-open: trail-emit failure does not abort the sweep (SR-7.7).
				_ = trail.Emit(context.Background(), "ad.find_missing.tick", map[string]any{
					"claude_instance_id":    it.ClaudeInstanceID,
					"prior_state":           nil,
					"new_state":             nil,
					"reconciliation_reason": "probe_eacces",
					"source":                "ad_find_missing",
				})
			}
			unverified = append(unverified, it.ClaudeInstanceID)
		}
	}

	// SR-7.5 fallback: rows with a partial/absent recorded identity are
	// reconciled against the environ probe set. This is the sole remaining
	// consumer of the environ probe. A hard prober error aborts the sweep —
	// there's nothing useful to do without it.
	if len(fallbackIDs) > 0 {
		probeSet, err := p.Probe(ctx)
		if err != nil {
			return FindMissingResult{}, err
		}
		for _, id := range fallbackIDs {
			if _, ok := probeSet[id]; ok {
				continue
			}
			if markMissing(s, id, lg) {
				missing = append(missing, id)
			}
		}
	}

	// Stable order in the result envelope.
	sort.Strings(missing)
	sort.Strings(unverified)
	return FindMissingResult{
		Count:         len(missing),
		IDs:           missing,
		Unverified:    len(unverified),
		UnverifiedIDs: unverified,
	}, nil
}

// markMissing runs the PM-pinned marking path for a provably-dead row:
// MarkSpawnMissing → ClearLivenessUnverified → proc_absent tick →
// CloseOrphanedPermissionRequests. The clear and tick fire only when a
// non-empty prior state proves the UPDATE actually wrote (absent or
// already-terminal rows get neither). Per-row store errors are logged and
// skipped. Returns true iff the row was written to missing.
func markMissing(s FindMissingStore, id string, lg FindMissingLogger) bool {
	priorState, err := s.MarkSpawnMissing(id)
	if err != nil {
		if lg != nil {
			lg.Printf("find-missing: MarkSpawnMissing(%s): %v (continuing)", id, err)
		}
		return false
	}
	// priorState is non-empty only when the UPDATE actually fired (n>0);
	// empty means the row was absent or already terminal — clear neither the
	// liveness fields nor emit a tick in that case.
	if priorState == "" {
		return false
	}
	// Clear stale liveness fields immediately after the mark so a reconciled
	// row never carries a lingering unverified_since (SR-8.2).
	if err := s.ClearLivenessUnverified(id); err != nil && lg != nil {
		lg.Printf("find-missing: ClearLivenessUnverified(%s): %v (continuing)", id, err)
	}
	// Emit one ad.find_missing.tick per successfully written row (SR-A-2.5).
	// Fail-open: trail-emit failure does not abort the sweep (SR-7.7).
	_ = trail.Emit(context.Background(), "ad.find_missing.tick", map[string]any{
		"claude_instance_id":    id,
		"prior_state":           priorState,
		"new_state":             "missing",
		"reconciliation_reason": "proc_absent",
		"source":                "ad_find_missing",
	})
	// Close any open permission_requests rows so relay polling loops observe a
	// fail-closed deny rather than spinning to their own timeout (SR-5.4).
	// CloseOrphanedPermissionRequests emits one
	// ad.find_missing.tick(permission_orphan_closeout) per row it closes.
	// Errors are logged and skipped — MarkSpawnMissing already succeeded, so
	// the Spawn is reconciled even if the row-close fails.
	if err := s.CloseOrphanedPermissionRequests(id); err != nil && lg != nil {
		lg.Printf("find-missing: CloseOrphanedPermissionRequests(%s): %v (continuing)", id, err)
	}
	return true
}

// FindMissing reconciles DB state against live OS processes. It scans all
// live-state rows (including pending) and, per row, applies an evidence-based
// liveness verdict: a provably-dead process transitions to missing; a
// permission-walled row is left untouched and flagged unverified; rows with a
// partial recorded identity fall back to an environ probe-set diff. Intended
// for periodic cron use.
//
// CLI: agent-director find-missing
//
// Errors:
//   - ErrProbeUnsupported: the current OS/platform has no probe implementation.
//
// Nondeterminism: none.
func (c *Client) FindMissing(ctx context.Context) (FindMissingResult, error) {
	if err := c.checkClosed(); err != nil {
		return FindMissingResult{}, err
	}
	return findMissingImpl(ctx, c.st, probe.New(), probe.NewChecker(), c.logger)
}
