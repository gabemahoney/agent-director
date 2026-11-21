package api

import (
	"fmt"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/store"
)

// GetDeliveryParams is the typed parameter shape for the get-delivery verb:
// a confirmed Message's pair.
type GetDeliveryParams struct {
	// CallerUUID is the send's Caller UUID: a hyphenated hexadecimal UUID,
	// either hex case.
	CallerUUID string `json:"caller_uuid"`
	// DeliveryRequestID is the send's Message ID: nonempty UTF-8.
	DeliveryRequestID string `json:"delivery_request_id"`
	// MalformedUTF8 records a surface's refusal of a lossy JSON string decode.
	MalformedUTF8 bool `json:"-"`
}

// GetDeliveryResult is get-delivery's answer (SR-5.4): Lookup is found, with
// the Message's current Result, or unknown or retention_expired, with Result
// null.
type GetDeliveryResult struct {
	Lookup string          `json:"lookup"`
	Result *DeliveryResult `json:"result"`
}

// deliveryReader is get-delivery's store surface. *store.Store satisfies it.
type deliveryReader interface {
	ReadBusySendResult(callerUUID, deliveryRequestID string) (store.BusySendResultLookup, error)
}

var _ deliveryReader = (*store.Store)(nil)

// getDelivery validates the pair (SR-1.3; ErrInvalidFlags) and projects the
// store's durable read of its canonical form. It reads only durable facts and
// starts no work.
func getDelivery(s deliveryReader, params GetDeliveryParams) (GetDeliveryResult, error) {
	caller, ok := canonicalUUID(params.CallerUUID)
	if !ok {
		return GetDeliveryResult{}, fmt.Errorf("%w: get-delivery: caller_uuid %q must be a hyphenated hexadecimal UUID (8-4-4-4-12); nothing was read", ErrInvalidFlags, params.CallerUUID)
	}
	if params.MalformedUTF8 || params.DeliveryRequestID == "" || !utf8.ValidString(params.DeliveryRequestID) {
		return GetDeliveryResult{}, fmt.Errorf("%w: get-delivery: delivery_request_id must be nonempty UTF-8; nothing was read", ErrInvalidFlags)
	}
	found, err := s.ReadBusySendResult(caller, params.DeliveryRequestID)
	if err != nil {
		return GetDeliveryResult{}, err
	}
	out := GetDeliveryResult{Lookup: found.Lookup}
	if found.Result != nil {
		r := deliveryResultOf(*found.Result)
		out.Result = &r
	}
	return out, nil
}

// GetDelivery reads a confirmed Message's current Result by its pair, its
// Caller UUID and Message ID (SR-5.4): found with the Result, unknown when no
// Message holds the pair, or retention_expired once its Result has expired.
// Any caller with store access can read any pair. It reads durable facts
// only: no Active Check, provider, tmux or process probe, and it changes no
// Result and starts no work. It works after restarts, upgrades, Launch End
// and the agent's deletion. Only a found not_started Result reads as safe to
// resend; unknown and retention_expired never do.
//
// CLI: agent-director get-delivery
//
// Errors:
//   - [ErrInvalidFlags]: CallerUUID is not a hyphenated hexadecimal UUID, or
//     DeliveryRequestID is empty or not UTF-8; nothing was read.
//   - [ErrStoreBusy]: the store's read did not finish within its bound.
//
// Any other store failure is an unnamed error (ErrInternal), never unknown.
//
// Nondeterminism: none beyond the stored Result.
func (c *Client) GetDelivery(params GetDeliveryParams) (GetDeliveryResult, error) {
	if err := c.checkClosed(); err != nil {
		return GetDeliveryResult{}, err
	}
	return getDelivery(c.st, params)
}
