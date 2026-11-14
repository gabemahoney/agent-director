package api

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
)

// ForceDeliveryParams is the typed parameter shape for the force-delivery
// verb.
type ForceDeliveryParams struct {
	// InstanceID identifies the agent whose Held Messages to force.
	InstanceID string `json:"instance_id"`
}

// ForceDeliveryResult is force-delivery's result: the attempt's force_result,
// which get-force reads again by its force_id.
type ForceDeliveryResult struct {
	ForceResult ForceResult `json:"force_result"`
}

// forceDeliveryStore is the store surface force-delivery needs: the read of
// the published current Launch, its identity and coverage together, and the
// force attempt's insert, which binds the attempt to that same Launch.
// *store.Store satisfies it.
type forceDeliveryStore interface {
	GetBusySendPublishedLaunch(instanceID string) (store.BusySendPublishedLaunch, error)
	InsertBusySendForce(attempt store.BusySendForce) error
}

var _ forceDeliveryStore = (*store.Store)(nil)

// forceRebindAttempts bounds how often force-delivery reads the published
// Launch again when a resume or reuse published another Launch between its
// read and its insert (store.ErrBusySendLaunchChanged). Each attempt decides
// afresh from the Launch it read, so an attempt is never bound to a Launch it
// did not examine.
const forceRebindAttempts = 3

// forceDelivery is force-delivery's flow (SR-4.1, SR-9.2): the instance id
// is required (ErrInvalidFlags), an unknown id is ErrSpawnNotFound, and the
// published current Launch's recorded coverage, read with its identity,
// decides; a resume or reuse candidate staged meanwhile is never that Launch.
// For an agent busy send does not cover, the attempt is stored as refused with
// reason agent_not_covered under a new force_id, bound to that Launch, ending
// in the same instant it began and carrying no Message; the call succeeds only
// once that is committed, and every call stores its own attempt. Nothing is
// delivered, typed, woken or changed. A Covered Agent gets busySendGate's
// unnamed error, since this version builds no force. A store failure is an
// error, never a refusal: ErrStoreBusy when the read or the insert could not
// take the store within its wait, otherwise unnamed (a missing binding
// included).
func forceDelivery(s forceDeliveryStore, now func() time.Time, params ForceDeliveryParams) (ForceDeliveryResult, error) {
	if params.InstanceID == "" {
		return ForceDeliveryResult{}, fmt.Errorf("%w: force-delivery: instance_id is required; nothing was forced", ErrInvalidFlags)
	}
	for attempt := 1; ; attempt++ {
		launch, err := s.GetBusySendPublishedLaunch(params.InstanceID)
		if err != nil {
			return ForceDeliveryResult{}, err
		}
		if launch.Coverage != store.BusySendNotCovered {
			return ForceDeliveryResult{}, busySendGate("force-delivery "+params.InstanceID, "force", launch.Coverage)
		}
		force, err := refusedForce(params.InstanceID, launch, clockNow(now), store.BusySendForceAgentNotCovered)
		if err != nil {
			return ForceDeliveryResult{}, err
		}
		err = s.InsertBusySendForce(force)
		if errors.Is(err, store.ErrBusySendLaunchChanged) && attempt < forceRebindAttempts {
			continue
		}
		if err != nil {
			return ForceDeliveryResult{}, err
		}
		return ForceDeliveryResult{ForceResult: forceResultOf(force)}, nil
	}
}

// refusedForce is a force attempt refused before any hand-over: its own
// force_id and owning call, bound to the examined published Launch (its
// conversation, Launch id, own launch token and provider), starting, reaching
// its call deadline and ending at once.
func refusedForce(instanceID string, launch store.BusySendPublishedLaunch, at time.Time, reason store.BusySendForceReason) (store.BusySendForce, error) {
	forceID, err := uuid.NewRandom()
	if err != nil {
		return store.BusySendForce{}, fmt.Errorf("force-delivery %s: no force_id could be made (%v); nothing was forced", instanceID, err)
	}
	callID, err := uuid.NewRandom()
	if err != nil {
		return store.BusySendForce{}, fmt.Errorf("force-delivery %s: no call id could be made (%v); nothing was forced", instanceID, err)
	}
	ms := at.UnixMilli()
	return store.BusySendForce{
		ForceID: forceID.String(), InstanceID: instanceID,
		ConversationID: launch.ConversationID, LaunchID: launch.LaunchID,
		LaunchToken: launch.LaunchToken, Provider: launch.Provider,
		OwnerCallID: callID.String(), Revision: 1,
		CallDeadlineMillis: ms, CallEndedAtMillis: ms,
		Outcome: store.BusySendForceRefused, Reason: reason,
		StartedAtMillis: ms, EndedAtMillis: ms,
		Retention: store.BusySendRetained,
	}, nil
}

// ForceDelivery hands the agent's Held Messages over now, skipping only the
// ordinary readiness waits (SR-4.1, SR-4.2). It decides from the coverage the
// row's published current Launch recorded, never from this version's covered
// set. For a Launch recorded not_covered the call succeeds with force_result
// outcome refused, reason agent_not_covered (SR-9.2): the attempt is stored
// under a new AD-made force_id before the call returns, each call its own
// attempt, and get-force reads it again. A retained Launch recorded covered
// gets this version's unnamed busy-send-unavailable error (ErrInternal), with
// no attempt stored, since this version builds no force. Nothing is
// delivered, typed or changed.
//
// CLI: agent-director force-delivery
//
// Errors:
//   - [ErrInvalidFlags]: InstanceID is empty; nothing was read or stored.
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrStoreBusy]: the store's read or the attempt's write could not take
//     the store within its wait; nothing was stored.
//
// Any other store failure is an unnamed error (ErrInternal), never a refused
// force.
//
// Nondeterminism: .force_result.force_id and its times.
func (c *Client) ForceDelivery(params ForceDeliveryParams) (ForceDeliveryResult, error) {
	if err := c.checkClosed(); err != nil {
		return ForceDeliveryResult{}, err
	}
	return forceDelivery(c.st, c.now, params)
}
