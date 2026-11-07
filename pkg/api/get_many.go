package api

import (
	"errors"
	"fmt"
)

// GetManyResult holds the full get rows and the ids without a record.
type GetManyResult struct {
	// Spawns contains exactly get's result for each found id, in input order.
	// Always an array, empty rather than null when no ids are found.
	Spawns []SpawnRow `json:"spawns"`
	// NotFound contains the ids without a record, in input order.
	// Always an array, empty rather than null when every id is found.
	NotFound []string `json:"not_found"`
}

func emptyGetManyResult() GetManyResult {
	return GetManyResult{Spawns: []SpawnRow{}, NotFound: []string{}}
}

// GetMany returns get's full row for each given id, in order, and lists ids
// without a record in NotFound. It validates the whole list before reading
// any agent; the Id Cap comes from the Client's loaded config. Every failure
// returns both lists non-nil and empty. A read failure other than not found
// stops the call, leaving earlier reads' writes in place.
//
// GetMany never waits for the store's write lock or for a state to settle.
// Agents are read one after another, each through get's own read, so they
// are not one snapshot: family_state can disagree within one result.
//
// CLI: agent-director get-many
//
// Errors:
//   - [ErrInvalidFlags]: no ids, an empty id or a repeated id.
//   - [ErrTooManyIds]: more ids than the configured Id Cap; err_details is
//     [TooManyIdsDetails], carrying that cap.
//   - [ErrStoreBusy]: a zero-wait store read selecting the row, or projecting
//     its checked result, did not finish within its bound: another operation
//     of this process held the store's operation slot or connection (another
//     call's, or an earlier one of this call; an operation keeps them until it
//     is disposed), another connection held a lock the read needed, or the
//     read ran out of time.
//
// Nondeterminism: the delivery facts depend on the clock and on whether each
// request's relay hook process runs; with a settle time, so do the states.
func (c *Client) GetMany(instanceIDs []string) (GetManyResult, error) {
	if err := c.checkClosed(); err != nil {
		return emptyGetManyResult(), err
	}
	idCap := c.cfg.GetMany.EffectiveIdCap()
	if len(instanceIDs) == 0 {
		return emptyGetManyResult(), fmt.Errorf("%w: get-many: no ids given", ErrInvalidFlags)
	}
	if len(instanceIDs) > idCap {
		return emptyGetManyResult(), &DetailedError{
			Err: fmt.Errorf("%w: get-many: %d ids given, exceeding the Id Cap of %d; split the ids across calls of at most %d",
				ErrTooManyIds, len(instanceIDs), idCap, idCap),
			Details: TooManyIdsDetails{IdCap: idCap},
		}
	}
	positions := make(map[string]int, len(instanceIDs))
	for i, id := range instanceIDs {
		if id == "" {
			return emptyGetManyResult(), fmt.Errorf("%w: get-many: id at position %d is empty", ErrInvalidFlags, i+1)
		}
		if earlier, exists := positions[id]; exists {
			return emptyGetManyResult(), fmt.Errorf("%w: get-many: id at position %d repeats position %d", ErrInvalidFlags, i+1, earlier)
		}
		positions[id] = i + 1
	}
	out := emptyGetManyResult()
	for _, id := range instanceIDs {
		row, err := getSettled(c.st, c.relayView(), c.settleRead(), id)
		if errors.Is(err, ErrSpawnNotFound) {
			out.NotFound = append(out.NotFound, id)
			continue
		}
		if err != nil {
			return emptyGetManyResult(), err
		}
		out.Spawns = append(out.Spawns, row)
	}
	return out, nil
}
