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
}

// FindMissingResult is the typed return shape. Count is the number of
// rows transitioned to `missing` on this sweep; IDs is the sorted
// list (sorted so the JSON envelope is deterministic across runs).
type FindMissingResult struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// FindMissingLogger is the narrow log surface FindMissing uses for
// degraded-mode warnings. *log.Logger satisfies it; tests pass a fake
// to inspect the message without scraping stderr.
type FindMissingLogger interface {
	Printf(format string, v ...any)
}

// FindMissing reconciles the DB's live-state rows against the prober
// (SRD §4.4 + §5.2). Behavior:
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
func FindMissing(ctx context.Context, s FindMissingStore, p probe.Prober, lg FindMissingLogger) (FindMissingResult, error) {
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
		missing = append(missing, id)
	}

	// Stable order in the result envelope.
	sort.Strings(missing)
	return FindMissingResult{Count: len(missing), IDs: missing}, nil
}
