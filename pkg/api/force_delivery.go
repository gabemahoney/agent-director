package api

import (
	"context"
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

// forceDeliveryStore is the store surface a force needs, on force-delivery and
// on a send with force: the read of the published current Launch, its
// identity and coverage together; the uncovered refusal's insert, which binds
// the attempt to that same Launch; and a covered force's claim, its own
// progress read and its call's end. *store.Store satisfies it.
type forceDeliveryStore interface {
	GetBusySendPublishedLaunchWithin(ctx context.Context, instanceID string, maxWait time.Duration) (store.BusySendPublishedLaunch, error)
	InsertBusySendForce(attempt store.BusySendForce) error
	ClaimBusySendForce(ctx context.Context, req store.BusySendForceClaimRequest) (store.BusySendForceClaim, error)
	ReadBusySendForceProgress(ctx context.Context, forceID string) (store.BusySendForceProgress, error)
	EndBusySendForceCall(ctx context.Context, ref store.BusySendForceRef, at time.Time) (store.BusySendForceEnd, error)
}

var _ forceDeliveryStore = (*store.Store)(nil)

// forceRebindAttempts bounds how often a force reads the published Launch
// again when a resume or reuse published another Launch between its read and
// its write (store.ErrBusySendLaunchChanged). Each attempt decides afresh from
// the Launch it read, so an attempt is never bound to a Launch it did not
// examine.
const forceRebindAttempts = 3

// A covered force call's finite phases (SR-3.2, SR-4.3; E5 Main Agent
// decision 1), both fixed at the call's entry and never renewed: the active
// phase, in which the call notifies the route and observes its force's issue
// and admission, then the call-owned cleanup that records its end. Their sum
// is the call's recorded finite deadline, after which the agent's next
// eligible event or explicit command may release a call that never recorded
// its end. While it waits, the call reads its force every forcePollInterval,
// today's submission-confirmation interval.
const (
	forceActivePhase  = 12 * time.Second
	forceCleanupPhase = 3 * time.Second
	forcePollInterval = submissionPollInterval
)

// forceCallEnv is what a force call runs with besides its store: the caller's
// context, which cuts both phases short; the clock its phases and records are
// fixed on (nil: time.Now); the pause between two readings of its force
// (nil: a timer the context ends); and the wake notification of the agent's
// route (nil: store.TouchBusySendWake).
type forceCallEnv struct {
	ctx   context.Context
	now   func() time.Time
	sleep func(time.Duration)
	touch func(path string) error
}

func (e forceCallEnv) context() context.Context {
	if e.ctx == nil {
		return context.Background()
	}
	return e.ctx
}

func (e forceCallEnv) clock() time.Time {
	if e.now == nil {
		return time.Now()
	}
	return e.now()
}

func (e forceCallEnv) pause(d time.Duration) {
	if e.sleep != nil {
		e.sleep(d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-e.context().Done():
	}
}

// notify touches the route's wake endpoint, a content-free hint (SR-3.1). A
// lost hint grants no replay or retry: the Messages wait for the agent's next
// eligible event or explicit command.
func (e forceCallEnv) notify(path string) {
	if path == "" {
		return
	}
	if e.touch != nil {
		_ = e.touch(path)
		return
	}
	_ = store.TouchBusySendWake(path)
}

// forceAction is one explicit force action's budget (SR-3.2, SR-4.3; E5
// Main Agent decision 1): its entry on the call's clock and the contexts of
// its active phase and of the whole call (active plus cleanup), all fixed
// once, before the action's first store step, and carried through every
// preflight, Launch read, rebind, claim, observation and end. Nothing renews
// them, and the force's recorded call deadline is measured from that entry.
type forceAction struct {
	env          forceCallEnv
	entry        time.Time
	active, call context.Context
	cancel       func()
}

// begin fixes a force action's budget now; its caller defers cancel.
func (e forceCallEnv) begin() *forceAction {
	active, cancelActive := context.WithTimeout(e.context(), forceActivePhase)
	call, cancelCall := context.WithTimeout(e.context(), forceActivePhase+forceCleanupPhase)
	return &forceAction{env: e, entry: e.clock(), active: active, call: call,
		cancel: func() { cancelActive(); cancelCall() }}
}

// forceDelivery is force-delivery's flow (SR-4.1, SR-9.2): the instance id
// is required (ErrInvalidFlags), an unknown id is ErrSpawnNotFound, and the
// force runs as forceAgent decides it, within one action's budget fixed
// before its first store step. A store failure is an error, never a refusal.
func forceDelivery(s forceDeliveryStore, env forceCallEnv, params ForceDeliveryParams) (ForceDeliveryResult, error) {
	if params.InstanceID == "" {
		return ForceDeliveryResult{}, fmt.Errorf("%w: force-delivery: instance_id is required; nothing was forced", ErrInvalidFlags)
	}
	a := env.begin()
	defer a.cancel()
	force, err := forceAgent(s, a, params.InstanceID)
	if err != nil {
		return ForceDeliveryResult{}, err
	}
	return ForceDeliveryResult{ForceResult: forceResultOf(force)}, nil
}

// forceAgent makes one force attempt on instanceID, decided by the coverage
// its published current Launch recorded, read with its identity; a resume or
// reuse candidate staged meanwhile is never that Launch. For an agent busy
// send does not cover, the attempt is stored as refused with reason
// agent_not_covered under a new force_id, bound to that Launch, ending in the
// same instant it began and carrying no Message; nothing is delivered, typed,
// woken or changed. A Covered Agent's force is one finite call
// (runForceCall). Every read and rebind runs within a's active phase and
// keeps a's entry, so a retry never renews the budget. Each attempt is its
// own record under its own force_id and is returned as stored. A store
// failure is an error: ErrStoreBusy when the read or the write could not take
// the store within its wait or the active phase, so nothing was stored,
// otherwise unnamed (a missing binding included). A failure after a claim
// stored a running force is always unnamed (runForceCall), and returns that
// claimed force with the error.
func forceAgent(s forceDeliveryStore, a *forceAction, instanceID string) (store.BusySendForce, error) {
	for attempt := 1; ; attempt++ {
		launch, err := s.GetBusySendPublishedLaunchWithin(a.active, instanceID, store.DefaultLockWait)
		if err != nil {
			return store.BusySendForce{}, err
		}
		var force store.BusySendForce
		if launch.Coverage == store.BusySendNotCovered {
			if force, err = refusedForce(instanceID, launch, a.entry, store.BusySendForceAgentNotCovered); err == nil {
				err = s.InsertBusySendForce(force)
			}
		} else {
			force, err = runForceCall(s, a, instanceID, launch)
		}
		if errors.Is(err, store.ErrBusySendLaunchChanged) && attempt < forceRebindAttempts {
			continue
		}
		return force, err
	}
}

// runForceCall is a Covered Agent's force call (SR-4.1 to SR-4.3, SR-3.1,
// SR-3.2), finite and owning no worker past its return. Its two phases are
// a's, fixed at the action's entry (forceActivePhase, forceCleanupPhase) and
// cut short by the caller's context; the claim records the call's deadline
// from that entry. The claim (store.ClaimBusySendForce) stores the attempt,
// refused or running; a refusal returns at once, notifying the route when the
// claim's recovery returned Messages to Held. A running force's call owns
// every wake notification for it: it notifies the route once for each prompt
// the force may issue, the first after the claim and each later one only once
// the prompt before it is admitted with its order secured, as the store's
// progress read reports (a route's hook only drives that prompt and records
// evidence). It observes only its own force's durable facts, every
// forcePollInterval on its clock, until the active phase ends or the outcome
// is decided (the force finished, or nothing is left to issue and every
// issued prompt is admitted, or a negative is recorded); the wait infers no
// admission or not-submitted, secures no order, issues nothing and finishes
// no pending force. Then, in its cleanup phase, it records its end
// (store.EndBusySendForceCall), which fences the force and returns its
// never-issued Messages to Held in accepted order, and notifies the route
// when any carried Message is Held, so ordinary delivery follows without a
// later send. It returns the force as recorded. A cleanup that cannot commit
// returns the claimed force with an unnamed error, never ErrStoreBusy, since
// the force is stored: it stays for the agent's next eligible event or
// explicit command once its recorded deadline has passed. Errors before the
// claim committed keep their names; nothing was stored.
func runForceCall(s forceDeliveryStore, a *forceAction, instanceID string, launch store.BusySendPublishedLaunch) (store.BusySendForce, error) {
	forceID, err := uuid.NewRandom()
	if err != nil {
		return store.BusySendForce{}, fmt.Errorf("force %s: no force_id could be made (%v); nothing was forced", instanceID, err)
	}
	callID, err := uuid.NewRandom()
	if err != nil {
		return store.BusySendForce{}, fmt.Errorf("force %s: no call id could be made (%v); nothing was forced", instanceID, err)
	}
	env := a.env
	claim, err := s.ClaimBusySendForce(a.active, store.BusySendForceClaimRequest{InstanceID: instanceID, Launch: launch,
		ForceID: forceID.String(), OwnerCallID: callID.String(), ActiveBudget: forceActivePhase,
		CleanupBudget: forceCleanupPhase, At: a.entry})
	if err != nil {
		return store.BusySendForce{}, err
	}
	if claim.Handover == nil {
		if claim.Returned > 0 {
			env.notify(claim.WakePath)
		}
		return claim.Force, nil
	}
	activeEnd := a.entry.Add(forceActivePhase)
	notified := -1
	for {
		// A failed read decides nothing: the call keeps waiting within its
		// phase and its end still fences the force.
		if p, err := s.ReadBusySendForceProgress(a.active, claim.Ref.ForceID); err == nil {
			if p.Outcome != store.BusySendForceRunning || p.Decided {
				break
			}
			if p.Next > notified {
				env.notify(claim.WakePath)
				notified = p.Next
			}
		}
		left := activeEnd.Sub(env.clock())
		if left <= 0 || a.active.Err() != nil {
			break
		}
		env.pause(min(forcePollInterval, left))
	}
	end, err := s.EndBusySendForceCall(a.call, claim.Ref, env.clock())
	if err != nil {
		return claim.Force, forceEndError(instanceID, claim.Ref.ForceID, err)
	}
	if end.HeldMembers > 0 {
		env.notify(end.WakePath)
	}
	return end.Force, nil
}

// forceEndError is a force call whose end could not be recorded after its
// claim stored a running force: an unnamed error (the cause's text only, so
// no store error's name, ErrStoreBusy included, reaches the caller), naming
// the stored force and what finishes it.
func forceEndError(instanceID, forceID string, err error) error {
	return fmt.Errorf("force %s: force %s is stored and ran, but its call's end could not be recorded (%v); get-force reads it, and the agent's next eligible event or an explicit send-keys or force-delivery finishes it once its recorded deadline has passed; do not treat it as refused",
		instanceID, forceID, err)
}

// refusedForce is a force attempt refused before any hand-over: its own
// force_id and owning call, bound to the examined published Launch (its
// conversation, Launch id, own launch token and provider), starting, reaching
// its call deadline and ending at once.
func refusedForce(instanceID string, launch store.BusySendPublishedLaunch, at time.Time, reason store.BusySendForceReason) (store.BusySendForce, error) {
	forceID, err := uuid.NewRandom()
	if err != nil {
		return store.BusySendForce{}, fmt.Errorf("force %s: no force_id could be made (%v); nothing was forced", instanceID, err)
	}
	callID, err := uuid.NewRandom()
	if err != nil {
		return store.BusySendForce{}, fmt.Errorf("force %s: no call id could be made (%v); nothing was forced", instanceID, err)
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

// ForceDelivery hands a Covered Agent's Held Messages over now, skipping only
// the ordinary readiness waits for turn end, a Dialog or compaction (SR-4.1,
// SR-4.2). It decides from the coverage the row's published current Launch
// recorded, never from this version's covered set. Every call stores its own
// attempt under a new AD-made force_id before it returns, and get-force reads
// it again. For a Launch recorded not_covered the force_result is refused,
// reason agent_not_covered (SR-9.2), with nothing delivered, typed or
// changed. For a covered one the call claims every Held Message, in accepted
// order, as the force's frozen membership, notifies the agent's route, waits
// at most 12 seconds while the route issues the force's prompts one at a
// time, then up to 3 seconds to record its end. Its force_result is one of
// four outcomes, each a success (SR-4.3): handed_over (every issued prompt
// admitted), pending (an issued prompt's admission can still arrive; it
// finishes later on that admission, a definitive negative or Launch End),
// refused (no_held_messages, handover_in_progress, earlier_message,
// input_unresolved, agent_starting, launch_ended, route_unavailable,
// not_submitted or not_issued, the last when no prompt was issued by the
// call's end) or failed (admission_unknown or launch_ended). Carried Messages
// whose prompt was not issued by the call's end return to Held, in order, for
// ordinary delivery, and the route is notified. A call whose caller died is
// finished by the agent's next eligible event or the next send-keys or
// force-delivery for it, once its recorded 15-second deadline has passed.
//
// CLI: agent-director force-delivery
//
// Errors:
//   - [ErrInvalidFlags]: InstanceID is empty; nothing was read or stored.
//   - [ErrSpawnNotFound]: no row exists for the instance id.
//   - [ErrStoreBusy]: the store's read or the attempt's claim could not take
//     the store within its wait; nothing was stored.
//
// Any other store failure is an unnamed error (ErrInternal), never a refused
// force; one after the claim leaves the force for the agent's next eligible
// event or explicit command.
//
// Nondeterminism: .force_result.force_id, its times, outcome and reason
// (which depend on when the agent's route takes the prompts), and the carried
// Messages' current Results.
func (c *Client) ForceDelivery(params ForceDeliveryParams) (ForceDeliveryResult, error) {
	if err := c.checkClosed(); err != nil {
		return ForceDeliveryResult{}, err
	}
	return forceDelivery(c.st, forceCallEnv{now: c.now, sleep: c.sleep}, params)
}
