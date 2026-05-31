package api

import (
	"context"
	"sort"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// FindMissingStore is the narrow store surface FindMissing needs.
// *store.Store satisfies it via the recovery primitives.
type FindMissingStore interface {
	ListLiveSpawnIDs() ([]string, error)
	MarkSpawnMissing(instanceID string) error
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
}

// FindMissingLogger is the narrow log surface FindMissing uses for
// degraded-mode warnings. *log.Logger satisfies it; tests pass a fake
// to inspect the message without scraping stderr.
type FindMissingLogger interface {
	Printf(format string, v ...any)
}

// findMissingImpl is the unexported verb handler called by
// (c *Client).FindMissing. It takes probe.Prober directly and is not
// part of the public API surface; external consumers use the Client
// method instead.
//
// Behavior (SRD §4.4 + §5.2):
//
//  1. List live-state IDs (anything not ended/missing, including
//     pending — SRD §5.2 explicitly scans pending).
//  2. Probe the OS for live AGENT_DIRECTOR_INSTANCE_ID values.
//  3. Degraded-mode guard (SRD §14.6): if the probe returned 0 IDs
//     AND there are ≥1 live-state rows, log a warning and return
//     count=0, ids=nil with nil error. The most common cause is the
//     cron running as a different user than the one that launched
//     the Spawns; mass-marking everything missing would corrupt
//     state. The verb refuses to write in this scenario and counts
//     on the operator to notice the warning.
//  4. Otherwise: each live ID not in the probe set is transitioned
//     to `missing` via MarkSpawnMissing. Per-row errors are logged
//     and skipped; the sweep does not abort on a transient SQLite
//     failure.
//
// Returns the sweep result. nil error iff the prober + store calls
// succeeded; a hard prober error (e.g. /proc unreachable) bubbles up
// because there's nothing useful the verb can do without it.
func findMissingImpl(ctx context.Context, s FindMissingStore, p probe.Prober, lg FindMissingLogger) (FindMissingResult, error) {
	liveIDs, err := s.ListLiveSpawnIDs()
	if err != nil {
		return FindMissingResult{}, err
	}

	probeSet, err := p.Probe(ctx)
	if err != nil {
		return FindMissingResult{}, err
	}

	// Degraded-mode guard. Empty probe + non-empty DB live set is the
	// SRD §14.6 "cron is running as the wrong user" signal.
	if len(probeSet) == 0 && len(liveIDs) > 0 {
		if lg != nil {
			lg.Printf("find-missing: refusing to sweep — probe returned 0 ids but %d live row(s) exist (cron user mismatch?)", len(liveIDs))
		}
		return FindMissingResult{Count: 0, IDs: nil}, nil
	}

	missing := make([]string, 0)
	for _, id := range liveIDs {
		if _, ok := probeSet[id]; ok {
			continue
		}
		if err := s.MarkSpawnMissing(id); err != nil {
			if lg != nil {
				lg.Printf("find-missing: MarkSpawnMissing(%s): %v (continuing)", id, err)
			}
			continue
		}
		// Close any open permission_requests rows so relay polling loops
		// observe a fail-closed deny rather than spinning to their own timeout
		// (SR-5.4). Errors are logged and skipped — MarkSpawnMissing already
		// succeeded, so the Spawn is reconciled even if the row-close fails.
		if err := s.CloseOrphanedPermissionRequests(id); err != nil {
			if lg != nil {
				lg.Printf("find-missing: CloseOrphanedPermissionRequests(%s): %v (continuing)", id, err)
			}
		}
		missing = append(missing, id)
	}

	// Stable order in the result envelope.
	sort.Strings(missing)
	return FindMissingResult{Count: len(missing), IDs: missing}, nil
}

// FindMissing reconciles DB state against live OS processes. It scans all
// live-state rows (including pending), probes the OS for live
// AGENT_DIRECTOR_INSTANCE_ID env values, and transitions any row whose
// process is no longer observable to missing. Intended for periodic cron use.
//
// Degraded-mode guard: if the probe returns zero ids but live DB rows exist,
// the sweep is aborted and a warning is logged — the most common cause is
// the cron running as a different user than the Spawn owner.
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
	return findMissingImpl(ctx, c.st, probe.New(), c.logger)
}
