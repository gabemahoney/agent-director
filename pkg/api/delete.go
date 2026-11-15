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

// coveredDeleteStore is the store surface a row with a covered busy-send
// conversation is deleted through (SR-6.3): the row read, its exit scope and
// the deletion that applies the exit evidence. *store.Store satisfies it; a
// narrow store without it deletes every row through DeleteSpawn, which
// refuses a covered one.
type coveredDeleteStore interface {
	GetSpawn(instanceID string) (Spawn, error)
	ReadBusySendExitScope(instanceID string) (store.BusySendExitScope, error)
	DeleteBusySendCovered(instanceID string, examined RowSnapshot, ev store.BusySendExitEvidence) (CondResult, error)
}

// The per-id results of deleteRows besides "ok": err_names.
const (
	deleteResultOK            = "ok"
	deleteResultNotFound      = "ErrSpawnNotFound"
	deleteResultInternalError = "ErrInternal"
)

// deleteResultNotDeletable is a covered row's refusal (ErrSpawnNotDeletable).
var deleteResultNotDeletable = ErrSpawnNotDeletable.Error()

// deleteCoveredAttempts bounds the judgements a covered deletion makes when
// the row or its owned processes keep changing between the judgement and the
// delete; after the last it refuses ErrSpawnNotDeletable.
const deleteCoveredAttempts = 3

// deleteRows removes one or more rows by id (SRD §12). Behavior:
//
//   - Each id is processed independently. A miss on one id does NOT
//     abort the batch — the result map records ErrSpawnNotFound for
//     the offending id and continues.
//   - It does NOT touch tmux sessions or JSONL transcripts: a delete
//     on a live-state row removes the DB row and leaves its session and
//     agent running, untracked.
//   - Bypasses all state-precondition guards by design.
//   - A row whose published busy-send conversation is covered is deleted
//     only with positive exit evidence for every Launch of it, which delete
//     establishes itself, with identity-checked process checks and no tmux
//     (judgeBusySendExit), so a row whose recorded session name cannot be
//     used is deleted the same way. With it, one transaction records any
//     missing Launch End and exit, retires the conversation and finalizes its
//     Held Messages not_started record_deleted (store.DeleteBusySendCovered).
//     Without it (an owned process still runs or cannot be checked) the id
//     gets ErrSpawnNotDeletable and nothing changes; delete never signals or
//     stops a process. The recovery on a live row is to end the agent (kill,
//     then sweep) and retry; on a finished row, where kill is a no-op, to
//     retry once its processes have exited (decision 16). An uncovered row
//     keeps the unconditional delete.
//
// Returns nil error unconditionally; the per-row map is the canonical
// reporting surface. A future infrastructure failure that prevents
// any row from being attempted (e.g. DB unreachable) would surface
// as an error from DeleteSpawn on the FIRST id; that error is
// recorded in the map per the same convention.
func deleteRows(s deleteStore, pc ProcChecker, ids []string) (adminapi.DeleteResult, error) {
	results := make(map[string]string, len(ids))
	for _, id := range ids {
		results[id] = deleteRow(s, pc, id)
	}
	return adminapi.DeleteResult{Results: results}, nil
}

// deleteRow deletes one id and returns its per-id result.
func deleteRow(s deleteStore, pc ProcChecker, id string) string {
	covered, ok := s.(coveredDeleteStore)
	if !ok {
		return deleteOutcome(s.DeleteSpawn(id))
	}
	for range deleteCoveredAttempts {
		if result, done := deleteCovered(s, covered, pc, id); done {
			return result
		}
	}
	return deleteResultNotDeletable
}

// deleteCovered makes one judgement and deletion attempt for id. done is
// false when the row or its owned processes changed since the judgement, so
// another attempt judges again.
func deleteCovered(s deleteStore, covered coveredDeleteStore, pc ProcChecker, id string) (result string, done bool) {
	row, err := covered.GetSpawn(id)
	if err != nil {
		return deleteOutcome(err), true
	}
	scope, err := covered.ReadBusySendExitScope(id)
	switch {
	case errors.Is(err, store.ErrSpawnNotFound):
		return deleteResultNotFound, true
	case errors.Is(err, store.ErrBusySendNoBinding), err == nil && scope.Coverage != store.BusySendCovered:
		// No consistent binding (history from before the core) or an
		// uncovered conversation holds no custody: the plain delete, which
		// refuses a conversation that became covered meanwhile.
		err := s.DeleteSpawn(id)
		return deleteOutcome(err), !errors.Is(err, store.ErrBusySendExitNotEstablished)
	case err != nil:
		// A store failure reading the exit scope is the store's, never an
		// exit refusal.
		return deleteResultInternalError, true
	}
	if pc == nil {
		return deleteResultNotDeletable, true
	}
	ev, refusal := judgeBusySendExit(pc, scope)
	if refusal != nil {
		return deleteResultNotDeletable, true
	}
	res, err := covered.DeleteBusySendCovered(id, row.Snapshot, ev)
	switch {
	case errors.Is(err, store.ErrBusySendExitNotEstablished):
		return "", false
	case err != nil:
		return deleteResultInternalError, true
	case res == CondApplied:
		return deleteResultOK, true
	case res == CondAbsent:
		return deleteResultNotFound, true
	}
	return "", false
}

// deleteOutcome maps a deletion's error to its per-id result. An exit
// refusal is ErrSpawnNotDeletable; anything uncatalogued is an
// infrastructure failure, recorded as ErrInternal so callers see SOMETHING
// per id even when the DB has thrown a surprise.
func deleteOutcome(err error) string {
	switch {
	case err == nil:
		return deleteResultOK
	case errors.Is(err, store.ErrSpawnNotFound):
		return deleteResultNotFound
	case errors.Is(err, store.ErrBusySendExitNotEstablished):
		return deleteResultNotDeletable
	}
	return deleteResultInternalError
}

// deleteRows runs deleteRows on c's store: the rows instanceIDs are
// removed, bypassing all state guards, with per-row outcomes in Results. On a
// closed Client it returns ErrClientClosed and Results as a non-nil empty map,
// as adminapi.DeleteResult documents (b.4nt).
func (c *Client) deleteRows(instanceIDs []string) (adminapi.DeleteResult, error) {
	if err := c.checkClosed(); err != nil {
		return adminapi.DeleteResult{Results: map[string]string{}}, err
	}
	return deleteRows(c.st, c.procChecker, instanceIDs)
}
