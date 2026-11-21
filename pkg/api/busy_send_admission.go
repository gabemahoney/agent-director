package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
)

// busySendAdmitter is the store's one admission path for Covered Agents
// (store.AdmitBusySend). It is not part of SendKeysStore; send-keys finds it
// by a type assertion, as resume finds resumeIdentityWriter. A store without
// it refuses a covered send with nothing stored, typed or sent.
type busySendAdmitter interface {
	AdmitBusySend(req store.BusySendAdmissionRequest) (store.BusySendAdmission, error)
}

var _ busySendAdmitter = (*store.Store)(nil)

// sendKeysRawKeys are send-keys' raw-key parameters, which a Covered Agent's
// send takes none of (SR-1.5), in manifest order.
var sendKeysRawKeys = []string{"no_enter", "key", "expect_pane_sha256", "n_lines", "request_token", "as"}

// rawKeysNamed lists the raw-key parameters p names: those its surface saw
// supplied (RawKeysSupplied, zero values included) and those set to a
// non-zero value, in sendKeysRawKeys order.
func (p SendKeysParams) rawKeysNamed() []string {
	set := map[string]bool{
		"no_enter": p.NoEnter, "key": p.Key != "", "expect_pane_sha256": p.ExpectPaneSHA256 != "",
		"n_lines": p.NLines != 0, "request_token": p.RequestToken != "", "as": p.As != "",
	}
	for _, name := range p.RawKeysSupplied {
		set[name] = true
	}
	var named []string
	for _, name := range sendKeysRawKeys {
		if set[name] {
			named = append(named, name)
		}
	}
	return named
}

// coveredSendMalformed is ErrInvalidFlags for a send to a Covered Agent:
// nothing was stored, sent or typed.
func coveredSendMalformed(format string, args ...any) error {
	return fmt.Errorf("%w: send-keys: "+format+"; nothing was stored, sent or typed", append([]any{ErrInvalidFlags}, args...)...)
}

// coveredAdmissionRequest validates a send to a Covered Agent's shape from the
// call's original parameters, never planSendKeys' normalized text (SR-1.2,
// SR-1.3, SR-1.5): no raw-key parameter, even at its zero value; nonempty
// valid UTF-8 text, kept byte for byte, and never a value a surface already
// replaced (MalformedUTF8); a delivery_request_id, when supplied, nonempty
// valid UTF-8 and only with a caller_uuid; and no caller identity with
// confirmed_delivery false, or confirmed_delivery true without a caller_uuid.
// Options the Agent Defaults resolve are judged by the store. A confirmed send
// whose delivery_request_id is omitted gets a canonical UUID made here, so an
// uncertain commit can still name its pair. allow_pending is accepted and
// does nothing.
func coveredAdmissionRequest(p SendKeysParams, callerUUID *string) (store.BusySendAdmissionRequest, error) {
	req := store.BusySendAdmissionRequest{InstanceID: p.InstanceID, Text: []byte(p.Text), DeliveryRequestID: p.DeliveryRequestID, Confirmed: p.ConfirmedDelivery}
	if named := p.rawKeysNamed(); len(named) > 0 {
		return req, coveredSendMalformed("a Covered Agent's send takes no %s; send the text alone, and use tmux with record-pane-answer for keys on the pane", strings.Join(named, ", "))
	}
	idSupplied := p.DeliveryRequestIDSupplied || p.DeliveryRequestID != ""
	switch {
	case slices.Contains(p.MalformedUTF8, "text"), !utf8.ValidString(p.Text):
		return req, coveredSendMalformed("text must be valid UTF-8")
	case p.Text == "":
		return req, coveredSendMalformed("a Covered Agent's send needs nonempty text")
	case slices.Contains(p.MalformedUTF8, "delivery_request_id"), !utf8.ValidString(p.DeliveryRequestID):
		return req, coveredSendMalformed("delivery_request_id must be valid UTF-8")
	case idSupplied && p.DeliveryRequestID == "":
		return req, coveredSendMalformed("delivery_request_id, when given, must be nonempty; omit it to have one made")
	case callerUUID == nil && idSupplied:
		return req, coveredSendMalformed("delivery_request_id needs caller_uuid: it names a confirmed send's Message, and an unconfirmed send carries none")
	case callerUUID != nil && p.ConfirmedDelivery != nil && !*p.ConfirmedDelivery:
		return req, coveredSendMalformed("an unconfirmed send (confirmed_delivery false) carries no caller_uuid or delivery_request_id")
	case callerUUID == nil && p.ConfirmedDelivery != nil && *p.ConfirmedDelivery:
		return req, coveredSendMalformed("a confirmed send needs caller_uuid")
	}
	if p.SendMode != nil {
		mode := store.BusySendMode(*p.SendMode)
		req.SendMode = &mode
	}
	if callerUUID != nil {
		req.CallerUUID = *callerUUID
		if req.DeliveryRequestID == "" {
			id, err := uuid.NewRandom()
			if err != nil {
				return req, fmt.Errorf("send-keys: make a delivery_request_id: %w; nothing was stored, sent or typed", err)
			}
			req.DeliveryRequestID = id.String()
		}
	}
	return req, nil
}

// sendCovered admits a send to a Covered Agent through the store's one
// transactional path (SR-1.1, SR-3.9): no native, pane or relay input code
// runs, before or after. It returns the stored Result; a repeat of an owned
// pair returns the owner's current Result. row is nil when the target row
// does not exist and the call names a pair, whose repeat is answered even
// after the record's deletion (SR-1.2). A refused, conflicting or uncertain
// admission forces nothing. After an accepted send or a repeat, force true
// makes its own force attempt (forceAfterSend), each repeat a new one; any
// other send first finishes the agent's force whose call never reached its
// end (SR-3.2), then touches the Launch's recorded wake file when the send
// was newly admitted or that release returned Messages to Held.
func (r *sendKeysRun) sendCovered(row *Spawn, params SendKeysParams, callerUUID *string) error {
	req, err := coveredAdmissionRequest(params, callerUUID)
	if err != nil {
		return err
	}
	admitter, ok := r.s.(busySendAdmitter)
	if !ok {
		return fmt.Errorf("send-keys %s: the store cannot admit busy-send Messages; nothing was stored, sent or typed", params.InstanceID)
	}
	adm, err := admitter.AdmitBusySend(req)
	var refusal *store.BusySendRefusal
	switch {
	case errors.As(err, &refusal):
		if refusal.Reason == store.BusySendRefuseAgentNotCovered && row != nil && params.busySendOptions() == "" && !r.coverageChanged {
			// The current Launch stopped being covered after the row was
			// read, so this option-free call is today's again.
			r.coverageChanged = true
			return r.run(params)
		}
		return cantSendError("send-keys "+params.InstanceID, refusal, params.busySendOptions())
	case errors.Is(err, store.ErrBusySendMalformed):
		return coveredSendMalformed("%s", strings.TrimPrefix(err.Error(), store.ErrBusySendMalformed.Error()+": "))
	case store.CommitUnconfirmed(err):
		return admissionUncertain(params.InstanceID, req, err)
	case err != nil:
		return fmt.Errorf("send-keys %s: %w", params.InstanceID, err)
	}
	result := deliveryResultOf(adm.Result)
	r.deliveryResult = SendKeysResult{Delivery: &result, CallerUUID: result.CallerUUID, Class: result.Class,
		RequestID: result.RequestID, Outcome: result.Outcome, SessionID: result.SessionID, RecordID: result.RecordID,
		RecordSHA256: result.RecordSHA256}
	if params.Force != nil && *params.Force {
		force, err := r.forceAfterSend(row, req, adm)
		if err != nil {
			return forceAfterSendError(params.InstanceID, result, err)
		}
		forced := forceResultOf(force)
		r.deliveryResult.Force = &forced
		return nil
	}
	wake := ""
	if adm.Outcome == store.BusySendAdmitted {
		wake = adm.WakePath
	}
	if releaser, ok := r.s.(abandonedForceReleaser); ok && row != nil {
		// The release is the agent's to finish on its next eligible event or
		// command too, so a failed one leaves the send's own outcome standing.
		if rel, err := releaser.ReleaseAbandonedBusySendForce(r.forceEnv().context(), params.InstanceID, r.forceEnv().clock()); err == nil && rel.Returned > 0 {
			wake = rel.WakePath
		}
	}
	if wake != "" {
		store.TouchBusySendWake(wake)
	}
	return nil
}

// abandonedForceReleaser is the store's release of a force whose call never
// reached its end (store.ReleaseAbandonedBusySendForce), which an explicit
// send for the agent runs (SR-3.2). A store without it releases nothing.
type abandonedForceReleaser interface {
	ReleaseAbandonedBusySendForce(ctx context.Context, instanceID string, at time.Time) (store.BusySendForceRelease, error)
}

var _ abandonedForceReleaser = (*store.Store)(nil)

// deletedForceRefuser is the store's record_deleted refusal of a repeated
// pair's force (store.RefuseBusySendForceForDeleted).
type deletedForceRefuser interface {
	RefuseBusySendForceForDeleted(ctx context.Context, req store.BusySendForceDeletedRequest) (store.BusySendForce, bool, error)
}

var _ deletedForceRefuser = (*store.Store)(nil)

// forceEnv is the force call environment of a send with force: the call's
// context, the relay view's clock and the call's sleep.
func (r *sendKeysRun) forceEnv() forceCallEnv {
	return forceCallEnv{ctx: r.env.Context, now: r.env.Relay.Now, sleep: r.env.Sleep}
}

// forceAfterSend makes an accepted or repeated send's own force attempt
// (SR-4.1): a repeat whose pair's owner was retired by its record's deletion
// gets a stored refused attempt, reason record_deleted, bound to that
// retained ownership and never to an agent created since under the same id;
// any other forces the target as force-delivery does (forceAgent). The send
// is never accepted again and its pair, acceptance order and options never
// change. The force's budget is fixed once, after the confirmed admission and
// before the deleted-record check, and the whole force runs within it. A
// newly admitted Message's ordinary wake waits until the force has had its
// turn, so the ordinary notifier does not take the Message the caller asked to
// force; whenever the force leaves no claim of its own to notify the route
// (it was refused before a hand-over, or it failed before claiming), the send
// gives that wake as any accepted send does (SR-3.1).
func (r *sendKeysRun) forceAfterSend(row *Spawn, req store.BusySendAdmissionRequest, adm store.BusySendAdmission) (force store.BusySendForce, err error) {
	if adm.Outcome == store.BusySendAdmitted {
		defer func() {
			if force.HandoverID == "" {
				store.TouchBusySendWake(adm.WakePath)
			}
		}()
	}
	s, ok := r.s.(forceDeliveryStore)
	if !ok {
		return store.BusySendForce{}, fmt.Errorf("the store cannot force delivery")
	}
	a := r.forceEnv().begin()
	defer a.cancel()
	if pair, confirmed := req.CallerUUID, req.CallerUUID != ""; confirmed && adm.Outcome == store.BusySendReplayed {
		refuser, ok := r.s.(deletedForceRefuser)
		if !ok {
			return store.BusySendForce{}, fmt.Errorf("the store cannot refuse a deleted record's force")
		}
		forceID, err := uuid.NewRandom()
		if err != nil {
			return store.BusySendForce{}, fmt.Errorf("no force_id could be made (%v); nothing was forced", err)
		}
		callID, err := uuid.NewRandom()
		if err != nil {
			return store.BusySendForce{}, fmt.Errorf("no call id could be made (%v); nothing was forced", err)
		}
		force, deleted, err := refuser.RefuseBusySendForceForDeleted(a.active, store.BusySendForceDeletedRequest{
			ForceID: forceID.String(), OwnerCallID: callID.String(),
			Pair: store.BusySendPair{CallerUUID: pair, DeliveryRequestID: req.DeliveryRequestID}, At: a.entry})
		if err != nil || deleted {
			return force, err
		}
	}
	if row == nil {
		return store.BusySendForce{}, fmt.Errorf("%w: %s", store.ErrSpawnNotFound, req.InstanceID)
	}
	return forceAgent(s, a, req.InstanceID)
}

// forceAfterSendError is a send's force that failed after its Message was
// accepted or repeated. It carries that exact accepted Result, even when a
// real force was stored before the failure; its recovery remains intact.
// The cause stays diagnostic text so its name cannot hide known acceptance.
func forceAfterSendError(id string, result DeliveryResult, err error) error {
	var description error
	if result.CallerUUID != "" {
		description = fmt.Errorf("%w: send-keys %s: the Message was accepted (caller_uuid %s, delivery_request_id %q), but its force failed: %v; do not send it as new: repeat it with the same pair to force again, or read its Result with get-delivery",
			ErrSendAcceptedForceFailed, id, result.CallerUUID, result.RequestID, err)
	} else {
		description = fmt.Errorf("%w: send-keys %s: the Message was accepted, but its force failed: %v; do not send it again: call force-delivery to force the agent's Held Messages", ErrSendAcceptedForceFailed, id, err)
	}
	return &DetailedError{Err: description, Details: SendAcceptedForceFailedDetails{Result: result}}
}

// admissionUncertain is a covered send whose admission COMMIT outcome is
// unknown (SR-1.1, SR-7.4a): an ordinary store error, never
// ErrSubmissionUnconfirmed, a rollback, a free pair or delivery authority.
// A confirmed send learns its outcome by repeating with the same pair.
func admissionUncertain(id string, req store.BusySendAdmissionRequest, err error) error {
	if req.CallerUUID != "" {
		return fmt.Errorf("send-keys %s: whether the Message was accepted is unknown: %w; send it again with caller_uuid %s and delivery_request_id %q, or call get-delivery with them, to read its Result; a repeat never accepts it twice",
			id, err, req.CallerUUID, req.DeliveryRequestID)
	}
	return fmt.Errorf("send-keys %s: whether the Message was accepted is unknown: %w; an unconfirmed send has no identity, so nothing can resolve this one (get's delivery counts are aggregates) and it must not be resent automatically; use confirmed delivery with your own caller_uuid and delivery_request_id where a send must be recoverable", id, err)
}

// cantSendError maps a store refusal to ErrCantSend with CantSendDetails
// (SR-9.4): resend_safe and retry_with true and new_send for a retryable
// refusal (SR-5.1): one the same send can pass later or changed
// (agent_not_covered, steer_unsupported, held_count, held_bytes) and
// id_conflict, whose Message was never stored and may go as a new send under
// another Message ID; false and none otherwise. new_id stays reserved for a
// confirmed not_started Result. A conflict carries its owner's Result,
// unchanged, and a limit its actual and allowed values.
func cantSendError(target string, refusal *store.BusySendRefusal, options string) error {
	if refusal.Reason == store.BusySendRefuseAgentNotCovered {
		if options == "" {
			options = "busy send"
		}
		return agentNotCoveredError(target, options)
	}
	d := CantSendDetails{Class: cantSendClass, Reason: string(refusal.Reason), RetryWith: retryWithNone}
	next := "do not send it again unchanged"
	switch refusal.Reason {
	case store.BusySendRefuseSteerUnsupported:
		d.ResendSafe, d.RetryWith, next = true, retryWithNewSend, "send it with send_mode wait_turn"
	case store.BusySendRefuseHeldCount, store.BusySendRefuseHeldBytes:
		d.ResendSafe, d.RetryWith, next = true, retryWithNewSend, "send it again once Held Messages drain"
	case store.BusySendRefuseIDConflict:
		d.ResendSafe, d.RetryWith, next = true, retryWithNewSend, "the pair's owner Result is in err_details; send this Message only as a new send under a new delivery_request_id"
	case store.BusySendRefuseRetentionExpired:
		next = "the pair stays owned; never treat it as free"
	}
	if refusal.Limit != nil {
		d.Limit = &CantSendLimit{Actual: refusal.Limit.Actual, Allowed: refusal.Limit.Allowed}
	}
	if refusal.Owner != nil {
		owner := deliveryResultOf(*refusal.Owner)
		d.Result = &owner
	}
	return &DetailedError{
		Err: fmt.Errorf("%w: %s: %s (%s); nothing was stored, sent or typed; %s",
			ErrCantSend, target, strings.TrimPrefix(refusal.Error(), "store: busy send refused: "), refusal.Reason, next),
		Details: d,
	}
}
