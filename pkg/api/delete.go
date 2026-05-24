package api

import (
	"errors"

	"github.com/gabemahoney/agent-director/internal/store"
)

// DeleteStore is the narrow store surface Delete needs.
type DeleteStore interface {
	DeleteSpawn(instanceID string) error
}

// DeleteResult is the typed return shape. Results maps each input id
// to either "ok" (deleted) or the canonical err_name from the CLI's
// errCatalog. Always non-nil so JSON encodes deterministically.
type DeleteResult struct {
	// Results maps each requested id to its outcome: "ok" on success,
	// or an err_name string (e.g. "ErrSpawnNotFound") on failure.
	// The map is always non-nil and contains one entry per input id.
	Results map[string]string `json:"results"`
}

// Delete removes one or more rows by id (SRD §12). Behavior:
//
//   - Each id is processed independently. A miss on one id does NOT
//     abort the batch — the result map records ErrSpawnNotFound for
//     the offending id and continues.
//   - The verb does NOT touch tmux sessions or JSONL transcripts.
//     A delete on a live-state row removes the DB row and leaves the
//     orphan tmux session running; the caller is expected to have
//     killed it first (or accepted the orphan).
//   - Bypasses all state-precondition guards by design (admin verb).
//
// Returns nil error unconditionally; the per-row map is the canonical
// reporting surface. A future infrastructure failure that prevents
// any row from being attempted (e.g. DB unreachable) would surface
// as an error from DeleteSpawn on the FIRST id; that error is
// recorded in the map per the same convention.
func Delete(s DeleteStore, ids []string) (DeleteResult, error) {
	results := make(map[string]string, len(ids))
	for _, id := range ids {
		err := s.DeleteSpawn(id)
		switch {
		case err == nil:
			results[id] = "ok"
		case errors.Is(err, store.ErrSpawnNotFound):
			results[id] = "ErrSpawnNotFound"
		default:
			// Anything else is an infrastructure failure — record the
			// canonical "internal" string so callers see SOMETHING per
			// id even when the DB has thrown a surprise.
			results[id] = "ErrInternal"
		}
	}
	return DeleteResult{Results: results}, nil
}

// Delete removes one or more Spawn rows by id, bypassing all state guards.
// Each id is processed independently; a missing id records ErrSpawnNotFound in
// the result map rather than aborting the batch. Does not touch tmux sessions
// or JSONL transcripts. Per-row outcomes are in DeleteResult.Results.
//
// CLI: agent-director delete
//
// Errors: none at the verb level; per-row outcomes are in DeleteResult.Results.
//
// Nondeterminism: none.
func (c *Client) Delete(claudeInstanceIDs []string) (DeleteResult, error) {
	if err := c.checkClosed(); err != nil {
		return DeleteResult{}, err
	}
	return Delete(c.st, claudeInstanceIDs)
}
