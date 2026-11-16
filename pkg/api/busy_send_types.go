package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// DeliveryResult is a Message's Result (SR-5.1) as a Covered Agent's send, a
// Lookup or a conflict refusal reports it: its class, whether it is Final, its reason,
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
	// Outcome is the compatibility outcome: submitted, not_started,
	// submission_unconfirmed or partial_input_unconfirmed.
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

// A force_result's outcome (SR-4.3; MI decision 10). A force call
// (force-delivery, send-keys with force) returns one of the first four. A read
// of a stored attempt (get-force, and delivery.force on get and status) may
// also show ForceOutcomeRunning while the attempt's call has not returned,
// with reason "" and ended_at null; it means nothing about admission, unlike
// pending, which has an issued prompt whose admission can still arrive.
const (
	ForceOutcomeHandedOver = "handed_over"
	ForceOutcomePending    = "pending"
	ForceOutcomeRefused    = "refused"
	ForceOutcomeFailed     = "failed"
	ForceOutcomeRunning    = "running"
)

// ForceResult is one force attempt's outcome (SR-4.1, SR-4.3): its AD-made
// force_id, outcome (ForceOutcomeHandedOver, ForceOutcomePending,
// ForceOutcomeRefused or ForceOutcomeFailed; a read may show
// ForceOutcomeRunning), reason ("" while running), start and end times (end
// null while pending or running), its carried Messages' confirmed pairs, in
// carried order ([] when none), and the current Results of those of them
// whose Result is still retained, in the same order ([] when none). A carried
// Message whose Result has expired keeps its pair and has no Result, so
// carried_results can be shorter; an unconfirmed carried Message is in
// neither list.
type ForceResult struct {
	ForceID        string           `json:"force_id"`
	Outcome        string           `json:"outcome"`
	Reason         string           `json:"reason"`
	StartedAt      time.Time        `json:"started_at"`
	EndedAt        *time.Time       `json:"ended_at"`
	CarriedPairs   []DeliveryPair   `json:"carried_pairs"`
	CarriedResults []DeliveryResult `json:"carried_results"`
}

// AgentDefaults are a Covered Agent's Agent Defaults, the Send Mode and
// Confirmed Delivery its Messages take when the send names none, and its
// experimental confirm_from_transcripts setting (SR-1.4, SR-4.5), as get,
// get-many, status and list show them (SR-5.6). An uncovered row has none.
type AgentDefaults struct {
	SendMode               string `json:"send_mode"`
	ConfirmedDelivery      bool   `json:"confirmed_delivery"`
	ConfirmFromTranscripts bool   `json:"confirm_from_transcripts"`
}

// DeliveryFacts is a Covered Agent's delivery block on get, get-many and
// status (SR-5.6), read from durable records only: reading it delivers, wakes
// and changes nothing. An uncovered row has none.
type DeliveryFacts struct {
	// HeldCount and HeldBytes are the Messages Held now and their text
	// bytes. The held limits count more: custody that can return to Held.
	HeldCount int64 `json:"held_count"`
	HeldBytes int64 `json:"held_bytes"`
	// FirstWait is what the first Held Message waits on; null with none Held.
	FirstWait *DeliveryWait `json:"first_wait"`
	// HandoverCount is the non-Final hand-overs: hand-overs, each of one or
	// more Messages, with a member whose Result is not yet Final. It counts
	// hand-overs, not Messages.
	HandoverCount int64 `json:"handover_count"`
	// StartupWait is waiting startup work; null for Claude Code, whose
	// startup prompt is part of its Launch.
	StartupWait *DeliveryWait `json:"startup_wait"`
	// Force is the running or pending force, else the latest force that
	// began a hand-over; null when there is neither. Its outcome is
	// ForceOutcomeRunning while that force's call has not returned.
	Force *ForceResult `json:"force"`
	// AwaitingTurnStart is the current Launch's completed hand-overs whose
	// input has no evidence yet that the agent took it (SR-3.3).
	AwaitingTurnStart int64 `json:"awaiting_turn_start"`
}

// DeliveryCapabilities is what busy send offers a Covered Agent, on get and
// get-many (SR-5.6): its Send Modes with their limits and its held limits,
// all in UTF-8 bytes, its force capability, and what Confirm From Transcripts
// can do for it, from the one published capability table. An uncovered row
// has none.
type DeliveryCapabilities struct {
	Modes []DeliveryModeLimits `json:"modes"`
	// HeldCount and HeldBytes cap the agent's Held Messages and their bytes,
	// counting custody that can return to Held.
	HeldCount int64 `json:"held_count"`
	HeldBytes int64 `json:"held_bytes"`
	// Force is the agent's force capability (SR-4.4); nil when this version
	// publishes none for its agent.
	Force *DeliveryForceCapability `json:"force"`
	// ConfirmFromTranscriptsCapability is what the experimental Confirm From
	// Transcripts setting can add to a force's evidence, whatever the setting
	// (SR-4.5): ConfirmFromTranscriptsNotNeeded on Claude Code.
	// ConfirmFromTranscriptsReason is why, a stable code ("" for none
	// recorded): hook_sighting_first on Claude Code, since AD's own
	// UserPromptSubmit sighting preceded the native input record in every
	// measured state.
	ConfirmFromTranscriptsCapability string `json:"confirm_from_transcripts_capability"`
	ConfirmFromTranscriptsReason     string `json:"confirm_from_transcripts_reason"`
}

// A delivery_capabilities' confirm_from_transcripts_capability (SR-4.5).
const (
	ConfirmFromTranscriptsCanConfirm    = "can_confirm"
	ConfirmFromTranscriptsNotNeeded     = "not_needed"
	ConfirmFromTranscriptsCannotConfirm = "cannot_confirm"
)

// DeliveryForceCapability is a Covered Agent's force capability (SR-3.5,
// SR-4.4): Admission, the route's qualified admission event (prompt_sighting
// on Claude Code); Limits, that evidence's named limits in order
// (duplicate_text, unrecoverable_escaping, user_hook_after_sighting); and
// States, the force-state table in order. It describes measured behaviour
// only: it grants no issue or admission, promises no time, and a force's own
// outcome and reason are its force_result's. A force whose sighting arrives
// after its call returns reports pending, even in a state listed handed_over.
type DeliveryForceCapability struct {
	Admission string               `json:"admission"`
	Limits    []string             `json:"limits"`
	States    []DeliveryForceState `json:"states"`
}

// DeliveryForceState is one row of the force-state table: the agent's State
// when a force is made (idle, tool_running, between_tools,
// permission_dialog, plan_dialog, compacting, not_started or ended), the
// measured Outcome (handed_over at once; pending first, then handed_over on
// the later sighting; or refused, its Messages Held) and Reason, an existing
// force reason: the outcome's own, or for pending its wait cause
// (turn_running, dialog or compaction).
type DeliveryForceState struct {
	State   string `json:"state"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// DeliveryModeLimits is one offered Send Mode's limits: a Message's own text
// bytes, a complete prompt's bytes with AD's separators and the route's
// wrapper (a Message at message_bytes always fits alone), and the text it
// accepts (utf8_tab_lf: valid UTF-8 with no C0 control characters other than
// TAB and LF).
type DeliveryModeLimits struct {
	SendMode     string `json:"send_mode"`
	MessageBytes int64  `json:"message_bytes"`
	PromptBytes  int64  `json:"prompt_bytes"`
	TextKind     string `json:"text_kind"`
}

// agentDefaultsOf projects published Agent Defaults.
func agentDefaultsOf(d store.BusySendAgentDefaults) *AgentDefaults {
	return &AgentDefaults{SendMode: string(d.SendMode), ConfirmedDelivery: d.Confirmed, ConfirmFromTranscripts: d.ConfirmFromTranscripts}
}

// busySendDeliveryOf projects a delivery observation.
func busySendDeliveryOf(o *store.BusySendObservation) *DeliveryFacts {
	out := &DeliveryFacts{HeldCount: o.HeldCount, HeldBytes: o.HeldBytes, FirstWait: deliveryWaitOf(o.FirstWait),
		HandoverCount: o.HandoverCount, StartupWait: deliveryWaitOf(o.StartupWait), AwaitingTurnStart: o.AwaitingTurnStart}
	if o.Force != nil {
		force := forceResultOf(*o.Force)
		out.Force = &force
	}
	return out
}

// deliveryCapabilitiesOf projects provider's row of the capability table.
func deliveryCapabilitiesOf(provider string) *DeliveryCapabilities {
	caps := store.BusySendCapabilitiesFor(provider)
	out := &DeliveryCapabilities{Modes: []DeliveryModeLimits{}, HeldCount: caps.HeldCount, HeldBytes: caps.HeldBytes}
	for _, m := range caps.Modes {
		out.Modes = append(out.Modes, DeliveryModeLimits{SendMode: string(m.Mode), MessageBytes: m.MessageBytes,
			PromptBytes: m.PromptBytes, TextKind: string(m.TextKind)})
	}
	out.ConfirmFromTranscriptsCapability = string(caps.ConfirmFromTranscripts)
	out.ConfirmFromTranscriptsReason = string(caps.ConfirmFromTranscriptsReason)
	if caps.ForceAdmission == "" {
		return out
	}
	out.Force = &DeliveryForceCapability{Admission: string(caps.ForceAdmission), Limits: []string{}, States: []DeliveryForceState{}}
	for _, l := range caps.ForceLimits {
		out.Force.Limits = append(out.Force.Limits, string(l))
	}
	for _, s := range caps.ForceStates {
		out.Force.States = append(out.Force.States, DeliveryForceState{State: string(s.State), Outcome: string(s.Outcome),
			Reason: string(s.Reason)})
	}
	return out
}

// busySendObserver is the delivery read get and status make for a covered row
// (store.ReadBusySendObservation). It is not part of GetStore or StatusStore;
// they find it by a type assertion, through an Active Check run's source. A
// store without it shows no delivery facts.
type busySendObserver interface {
	ReadBusySendObservation(instanceID string, maxWait time.Duration) (*store.BusySendObservation, error)
}

var _ busySendObserver = (*store.Store)(nil)

// busySendObservationOf is row's delivery facts when its current Launch is
// covered: nil for an uncovered row, a store without the read, or a Launch no
// longer covered when read. An Active Check run's projection shows the facts
// its row read took in that read's own read transaction
// (store.BusySendDeliveries), so a covered row answers wherever an uncovered
// one does, with facts of the very row state read. Only a row read
// that took none (a store without that read, or a row since read without it)
// reads them again here, without waiting, as the reading verbs' other reads
// (readerLockWait). A read failure is the caller's error: the reading verb
// then fails (ErrStoreBusy while the connection is held), never showing a
// covered row without its delivery facts.
func busySendObservationOf(s any, row Spawn) (*store.BusySendObservation, error) {
	if row.BusySend != store.BusySendCovered {
		return nil, nil
	}
	if run, ok := s.(*activeReadStore); ok {
		if observation, found, err := run.delivery.Lookup(row); found {
			return observation, err
		}
		s = run.source
	}
	observer, ok := s.(busySendObserver)
	if !ok {
		return nil, nil
	}
	return observer.ReadBusySendObservation(row.InstanceID, readerLockWait)
}

// deliveryWaitOf projects a stored wait; nil stays nil.
func deliveryWaitOf(w *store.BusySendWait) *DeliveryWait {
	if w == nil {
		return nil
	}
	out := &DeliveryWait{Reason: string(w.Reason), Since: time.UnixMilli(w.SinceMillis).UTC()}
	if e := w.Earlier; e != nil {
		out.Earlier = &DeliveryPair{CallerUUID: e.CallerUUID, RequestID: e.DeliveryRequestID}
	}
	return out
}

// The busy-send codes the public projections and refusals use.
const (
	cantSendClass         = "cant_send"
	reasonAgentNotCovered = "agent_not_covered"
	retryWithNewSend      = "new_send"
	retryWithNewID        = "new_id"
	retryWithNone         = "none"
)

// deliveryResultOf projects a stored Result (SR-5.1, SR-5.3), a send's
// response, a conflict's owner Result and a Lookup alike. An unconfirmed
// Message's has no pair, so caller_uuid and request_id are omitted.
func deliveryResultOf(r store.BusySendResult) DeliveryResult {
	out := DeliveryResult{
		CallerUUID: r.Pair.CallerUUID, RequestID: r.Pair.DeliveryRequestID,
		Class: string(r.Class), Final: r.Final, Reason: r.Reason, RetryWith: retryWithNone,
		Forced: r.Forced, InstanceID: r.InstanceID, LaunchToken: r.LaunchToken,
		SessionID: r.SessionID, RecordID: r.RecordID, RecordSHA256: r.RecordSHA256,
		Outcome: compatibilityOutcome(r.Class, r.PartialTransport),
	}
	if r.Class == store.BusySendClassNotStarted {
		out.ResendSafe, out.RetryWith = true, retryWithNewID
	}
	if r.EvidenceKind != nil {
		kind := string(*r.EvidenceKind)
		out.EvidenceKind = &kind
	}
	if !r.Final {
		out.Wait = deliveryWaitOf(r.Wait)
	}
	return out
}

// compatibilityOutcome is SR-5.3's outcome for a Result class: submitted for
// delivered_confirmed; for queued and sent_unconfirmed,
// partial_input_unconfirmed when the store knows its latest prompt was only
// partly written (store.BusySendResult.PartialTransport) and
// submission_unconfirmed for a whole prompt; not_started otherwise.
func compatibilityOutcome(class store.BusySendResultClass, partial bool) string {
	switch class {
	case store.BusySendClassDeliveredConfirmed:
		return "submitted"
	case store.BusySendClassQueued, store.BusySendClassSentUnconfirmed:
		if partial {
			return "partial_input_unconfirmed"
		}
		return "submission_unconfirmed"
	}
	return "not_started"
}

// forceResultOf projects a stored force attempt (SR-4.1).
func forceResultOf(f store.BusySendForce) ForceResult {
	out := ForceResult{ForceID: f.ForceID, Outcome: string(f.Outcome), Reason: string(f.Reason),
		StartedAt: time.UnixMilli(f.StartedAtMillis).UTC(), CarriedPairs: []DeliveryPair{}, CarriedResults: []DeliveryResult{}}
	if f.EndedAtMillis != 0 {
		ended := time.UnixMilli(f.EndedAtMillis).UTC()
		out.EndedAt = &ended
	}
	for _, p := range f.CarriedPairs {
		out.CarriedPairs = append(out.CarriedPairs, DeliveryPair{CallerUUID: p.CallerUUID, RequestID: p.DeliveryRequestID})
	}
	for _, r := range f.CarriedResults {
		out.CarriedResults = append(out.CarriedResults, deliveryResultOf(r))
	}
	return out
}
