package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// DeliveryResult is a confirmed Message's Result (SR-5.1) as a Lookup or a
// conflict refusal reports it: its class, whether it is Final, its reason,
// whether a new send may carry it and with what, what it waits on, its
// original target and actual hand-over binding, its receipt identity and the
// compatibility outcome (SR-5.3). It never holds the Message's text, raw
// evidence or a private path.
type DeliveryResult struct {
	// CallerUUID and RequestID are the Message's confirmed pair: its Caller
	// UUID and Message ID (delivery_request_id).
	CallerUUID string `json:"caller_uuid,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	// Class is held, queued, delivered_confirmed, sent_unconfirmed or
	// not_started.
	Class string `json:"class"`
	// Final is true exactly when the Result will no longer change.
	Final bool `json:"final"`
	// Reason is the Result's stable machine-readable reason.
	Reason string `json:"reason"`
	// ResendSafe and RetryWith are true and new_id only for not_started;
	// false and none otherwise.
	ResendSafe bool   `json:"resend_safe"`
	RetryWith  string `json:"retry_with"`
	// Wait is what a non-Final Result waits on; null when Final.
	Wait *DeliveryWait `json:"wait"`
	// Forced is whether the Message's attempted hand-over was forced.
	Forced bool `json:"forced"`
	// InstanceID is the Message's original target.
	InstanceID string `json:"instance_id"`
	// LaunchToken is the actual hand-over's Launch; null until bound.
	LaunchToken *string `json:"launch_token"`
	// SessionID is the actual hand-over's session; omitted until established.
	SessionID string `json:"session_id,omitempty"`
	// EvidenceKind is the qualified receipt's kind; null without one.
	EvidenceKind *string `json:"evidence_kind"`
	// RecordID and RecordSHA256 identify the qualified receipt; omitted
	// without one.
	RecordID     string `json:"record_id,omitempty"`
	RecordSHA256 string `json:"record_sha256,omitempty"`
	// Outcome is the compatibility outcome: submitted, not_started or
	// submission_unconfirmed.
	Outcome string `json:"outcome"`
}

// DeliveryWait is what a non-Final Result waits on (SR-2.6): the reason, since
// when, and the earlier confirmed Message it waits behind, if any.
type DeliveryWait struct {
	Reason  string        `json:"reason"`
	Since   time.Time     `json:"since"`
	Earlier *DeliveryPair `json:"earlier,omitempty"`
}

// DeliveryPair is a confirmed Message's pair: its Caller UUID and Message ID.
type DeliveryPair struct {
	CallerUUID string `json:"caller_uuid"`
	RequestID  string `json:"request_id"`
}

// ForceResult is one force attempt's outcome (SR-4.1, SR-4.3): its AD-made
// force_id, outcome (handed_over, pending, refused or failed), reason, start
// and end times (end null while pending) and its carried Messages' confirmed
// pairs, in carried order ([] when none).
type ForceResult struct {
	ForceID      string         `json:"force_id"`
	Outcome      string         `json:"outcome"`
	Reason       string         `json:"reason"`
	StartedAt    time.Time      `json:"started_at"`
	EndedAt      *time.Time     `json:"ended_at"`
	CarriedPairs []DeliveryPair `json:"carried_pairs"`
}

// The busy-send codes the public projections and refusals use.
const (
	cantSendClass         = "cant_send"
	reasonAgentNotCovered = "agent_not_covered"
	retryWithNewSend      = "new_send"
	retryWithNewID        = "new_id"
	retryWithNone         = "none"
)

// deliveryResultOf projects a stored Result (SR-5.1, SR-5.3).
func deliveryResultOf(r store.BusySendResult) DeliveryResult {
	out := DeliveryResult{
		CallerUUID: r.Pair.CallerUUID, RequestID: r.Pair.DeliveryRequestID,
		Class: string(r.Class), Final: r.Final, Reason: r.Reason, RetryWith: retryWithNone,
		Forced: r.Forced, InstanceID: r.InstanceID, LaunchToken: r.LaunchToken,
		SessionID: r.SessionID, RecordID: r.RecordID, RecordSHA256: r.RecordSHA256,
		Outcome: compatibilityOutcome(r.Class),
	}
	if r.Class == store.BusySendClassNotStarted {
		out.ResendSafe, out.RetryWith = true, retryWithNewID
	}
	if r.EvidenceKind != nil {
		kind := string(*r.EvidenceKind)
		out.EvidenceKind = &kind
	}
	if !r.Final && r.Wait != nil {
		out.Wait = &DeliveryWait{Reason: string(r.Wait.Reason), Since: time.UnixMilli(r.Wait.SinceMillis).UTC()}
		if e := r.Wait.Earlier; e != nil {
			out.Wait.Earlier = &DeliveryPair{CallerUUID: e.CallerUUID, RequestID: e.DeliveryRequestID}
		}
	}
	return out
}

// compatibilityOutcome is SR-5.3's outcome for a Result class. The stored
// Result does not record partial transport, so a sent_unconfirmed Result
// maps to submission_unconfirmed.
func compatibilityOutcome(class store.BusySendResultClass) string {
	switch class {
	case store.BusySendClassDeliveredConfirmed:
		return "submitted"
	case store.BusySendClassQueued, store.BusySendClassSentUnconfirmed:
		return "submission_unconfirmed"
	}
	return "not_started"
}

// forceResultOf projects a stored force attempt (SR-4.1).
func forceResultOf(f store.BusySendForce) ForceResult {
	out := ForceResult{ForceID: f.ForceID, Outcome: string(f.Outcome), Reason: string(f.Reason),
		StartedAt: time.UnixMilli(f.StartedAtMillis).UTC(), CarriedPairs: []DeliveryPair{}}
	if f.EndedAtMillis != 0 {
		ended := time.UnixMilli(f.EndedAtMillis).UTC()
		out.EndedAt = &ended
	}
	for _, p := range f.CarriedPairs {
		out.CarriedPairs = append(out.CarriedPairs, DeliveryPair{CallerUUID: p.CallerUUID, RequestID: p.DeliveryRequestID})
	}
	return out
}
