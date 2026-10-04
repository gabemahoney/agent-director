package api

import (
	"errors"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/store"
)

// This file holds the operator-only delete (SRD §12), run only by
// agent-director-admin's delete through internal/adminapi (b.vqr). Nothing
// here is exported: no agent-facing surface (the CLI, MCP, the Go and
// TypeScript clients) can delete a row.

// deleteStore is the narrow store surface deleteRows needs.
type deleteStore interface {
	DeleteSpawn(instanceID string) error
}

// deleteRows removes one or more rows by id (SRD §12). Behavior:
//
//   - Each id is processed independently. A miss on one id does NOT
//     abort the batch — the result map records ErrSpawnNotFound for
//     the offending id and continues.
//   - It does NOT touch tmux sessions or JSONL transcripts: a delete
//     on a live-state row removes the DB row and leaves its session and
//     agent running, untracked.
//   - Bypasses all state-precondition guards by design.
//
// Returns nil error unconditionally; the per-row map is the canonical
// reporting surface. A future infrastructure failure that prevents
// any row from being attempted (e.g. DB unreachable) would surface
// as an error from DeleteSpawn on the FIRST id; that error is
// recorded in the map per the same convention.
func deleteRows(s deleteStore, ids []string) (adminapi.DeleteResult, error) {
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
	return adminapi.DeleteResult{Results: results}, nil
}

// deleteRows runs deleteRows on c's store: the rows claudeInstanceIDs are
// removed, bypassing all state guards, with per-row outcomes in Results.
func (c *Client) deleteRows(claudeInstanceIDs []string) (adminapi.DeleteResult, error) {
	if err := c.checkClosed(); err != nil {
		return adminapi.DeleteResult{}, err
	}
	return deleteRows(c.st, claudeInstanceIDs)
}
