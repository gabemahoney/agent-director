package api

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/store"
)

// GetForceParams is the typed parameter shape for the get-force verb.
type GetForceParams struct {
	// ForceID is a force_result's force_id: a hyphenated hexadecimal UUID.
	ForceID string `json:"force_id"`
}

// GetForceResult is get-force's answer (SR-4.1): Lookup is found, with the
// attempt's current ForceResult, or unknown or retention_expired, with
// ForceResult null.
type GetForceResult struct {
	Lookup      string       `json:"lookup"`
	ForceResult *ForceResult `json:"force_result"`
}

// forceReader is get-force's store surface. *store.Store satisfies it.
type forceReader interface {
	ReadBusySendForce(forceID string) (store.BusySendForceLookup, error)
}

var _ forceReader = (*store.Store)(nil)

// getForce validates the force_id (ErrInvalidFlags) and projects the store's
// durable read. It reads only durable facts and starts no work.
func getForce(s forceReader, params GetForceParams) (GetForceResult, error) {
	id, ok := canonicalUUID(params.ForceID)
	if !ok {
		return GetForceResult{}, fmt.Errorf("%w: get-force: force_id %q must be a hyphenated hexadecimal UUID (8-4-4-4-12); nothing was read", ErrInvalidFlags, params.ForceID)
	}
	found, err := s.ReadBusySendForce(id)
	if err != nil {
		return GetForceResult{}, err
	}
	out := GetForceResult{Lookup: found.Lookup}
	if found.ForceResult != nil {
		f := forceResultOf(*found.ForceResult)
		out.ForceResult = &f
	}
	return out, nil
}

// GetForce reads one force attempt by the force_id its force_result carried
// (SR-4.1): found with its current force_result, unknown when the store holds
// no such attempt, or retention_expired once it has expired. Its outcome is
// ForceOutcomeRunning, with reason "", while the attempt's call has not
// returned (MI decision 10). It reads durable
// facts only: no Active Check, provider, tmux or process probe, and it starts
// no work. It works after restarts, Launch End and the agent's deletion.
// unknown and retention_expired are answers, not permission to resend.
//
// CLI: agent-director get-force
//
// Errors:
//   - [ErrInvalidFlags]: ForceID is not a hyphenated hexadecimal UUID;
//     nothing was read.
//   - [ErrStoreBusy]: the store's read did not finish within its bound.
//
// Any other store failure is an unnamed error (ErrInternal), never unknown.
//
// Nondeterminism: none beyond the stored attempt.
func (c *Client) GetForce(params GetForceParams) (GetForceResult, error) {
	if err := c.checkClosed(); err != nil {
		return GetForceResult{}, err
	}
	return getForce(c.st, params)
}
