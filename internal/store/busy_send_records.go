package store

// The busy-send permanent core's stored codes and record types (SR-2.1,
// SR-7.2). Every code below is a permanent value of a column in
// busySendCoreDDL (busy_send_schema.go): later versions keep each meaning and
// never reuse a code for another, because both old and new processes read and
// write the same core (SR-7.2). Codes are strings, never Go ordinals. Where a
// column has a CHECK list in busySendCoreDDL, the constants here are exactly
// that list; reason and wait-reason columns have no CHECK, so the catalogue
// can grow with a later release without converting the database, and a reader
// passes an unknown code through unchanged.

// BusySendMode is a resolved Send Mode (SR-1.4): busy_send_messages.send_mode
// and busy_conversations.default_send_mode.
type BusySendMode string

const (
	BusySendWaitTurn  BusySendMode = "wait_turn"
	BusySendSteerTurn BusySendMode = "steer_turn"
)

// BusySendPublication is a conversation's publication state
// (busy_conversations.publication; SR-6.3). published is the instance's one
// current conversation, which admissions bind to. candidate is a fresh
// reuse's new conversation while its creation is pending or uncertain: it is
// kept apart from the published one, which keeps admitting until the reuse
// succeeds. retired is a published conversation that a fresh reuse replaced
// or an administrator deletion retired. abandoned is a candidate that was
// never published because its creation failed.
type BusySendPublication string

const (
	BusySendCandidate BusySendPublication = "candidate"
	BusySendPublished BusySendPublication = "published"
	BusySendRetired   BusySendPublication = "retired"
	BusySendAbandoned BusySendPublication = "abandoned"
)

// BusySendRetiredReason is why a conversation was retired
// (busy_conversations.retired_reason); it is also the never-emitted custody's
// not_started Result reason (SR-6.3).
type BusySendRetiredReason string

const (
	BusySendConversationReplaced BusySendRetiredReason = "conversation_replaced"
	BusySendRecordDeleted        BusySendRetiredReason = "record_deleted"
)

// BusySendLaunchPhase is a Launch record's publication phase
// (busy_send_launches.phase). candidate is a spawn, reuse or resume Launch
// whose creation is in progress; uncertain is one whose external creation
// outcome is unknown; failed is one whose creation failed and that was never
// published. current is the instance's published current Launch, and
// superseded one that a later publication replaced, an unpublished candidate
// replaced before a later guarded Launch start, or one whose instance row was
// deleted. Launch End is recorded separately (ended_at_ms, end_cause), because
// a current Launch's process can end before another Launch replaces it.
type BusySendLaunchPhase string

const (
	BusySendLaunchCandidate  BusySendLaunchPhase = "candidate"
	BusySendLaunchUncertain  BusySendLaunchPhase = "uncertain"
	BusySendLaunchFailed     BusySendLaunchPhase = "failed"
	BusySendLaunchCurrent    BusySendLaunchPhase = "current"
	BusySendLaunchSuperseded BusySendLaunchPhase = "superseded"
)

// BusySendLaunchKind is the operation that began a Launch record
// (busy_send_launches.kind). migrated marks the record the v20→v21 backfill
// made for a Launch that existed before the core: it carries no route, wake
// or process binding, because none was recorded then.
type BusySendLaunchKind string

const (
	BusySendLaunchSpawn    BusySendLaunchKind = "spawn"
	BusySendLaunchReuse    BusySendLaunchKind = "reuse"
	BusySendLaunchResume   BusySendLaunchKind = "resume"
	BusySendLaunchMigrated BusySendLaunchKind = "migrated"
)

// BusySendLaunchEnd is the evidence on which a Launch End was recorded
// (busy_send_launches.end_cause; SR-6.2): AD observed the current Launch's
// agent end (process_exited: the verified agent's own end report, a terminal
// mark on its proven exit, kill's or pause's verified exit, or the exit
// evidence a lifecycle command established), marked it missing without
// proven agent exit (agent_missing), a resume or reuse published a
// new Launch, supervised creation failed, its instance row was deleted, or a
// later guarded Launch start replaced a never-published candidate/uncertain
// Launch. No cause is exit evidence (busy_send_lifecycle.go): process_exited
// says the agent ended, not that every owned issuer did, and
// candidate_replaced and record_deleted prove neither physical exit nor
// old-issuer exclusion; no caller may use them as either (SR-6.2, SR-6.3).
// agent_missing is likewise only a logical end, even if the same agent
// later reports in again: it asserts neither observed agent end nor exit
// evidence. It is internal bookkeeping, never a public Result reason.
// creation_failed does not assert process exit either: an unlabelled session
// can survive supervised failure.
type BusySendLaunchEnd string

const (
	busySendAgentMissing            BusySendLaunchEnd = "agent_missing"
	BusySendProcessExited           BusySendLaunchEnd = "process_exited"
	BusySendLaunchPublished         BusySendLaunchEnd = "launch_published"
	BusySendCreationFailed          BusySendLaunchEnd = "creation_failed"
	BusySendLaunchRecordDeleted     BusySendLaunchEnd = "record_deleted"
	BusySendLaunchCandidateReplaced BusySendLaunchEnd = "candidate_replaced"
)

// BusySendRouteKind is a Launch's provider route (busy_send_launches.route_kind,
// busy_send_handovers.route_kind): Claude's hook route A (SR-3.4), and the
// Codex app-server and Cursor ACP routes of the later releases (SR-3.6, SR-3.8).
type BusySendRouteKind string

const (
	BusySendRouteClaudeHooks    BusySendRouteKind = "claude_hooks"
	BusySendRouteCodexAppServer BusySendRouteKind = "codex_app_server"
	BusySendRouteCursorACP      BusySendRouteKind = "cursor_acp"
)

// BusySendProcessRole is the role of a process a Launch binds
// (busy_send_launch_processes.role): the agent itself, and the later
// releases' Codex app-server, remote TUI and connection helper and Cursor
// gateway (SR-3.6, SR-3.8).
type BusySendProcessRole string

const (
	BusySendProcessAgent     BusySendProcessRole = "agent"
	BusySendProcessAppServer BusySendProcessRole = "app_server"
	BusySendProcessTUI       BusySendProcessRole = "tui"
	BusySendProcessHelper    BusySendProcessRole = "helper"
	BusySendProcessGateway   BusySendProcessRole = "gateway"
)

// BusySendPlace is a Message's current physical custody
// (busy_send_messages.place; SR-2.1, M1's place). held is in the inbox with
// no hand-over membership; claimed is frozen into a hand-over whose prompt
// has no persisted emission intent; issued has a persisted emission intent,
// so it may have reached the provider; closed no longer counts as custody.
// Custody is independent of the Message's Result: a closed Message's Result
// may still be non-Final, and a Final Result never moves custody.
type BusySendPlace string

const (
	BusySendHeld    BusySendPlace = "held"
	BusySendClaimed BusySendPlace = "claimed"
	BusySendIssued  BusySendPlace = "issued"
	BusySendClosed  BusySendPlace = "closed"
)

// BusySendResultClass is a Result's class (busy_send_messages.result_class;
// SR-5.1, SR-5.2). The refusal-only class cant_send is never stored, because
// a refused send stores no Message.
type BusySendResultClass string

const (
	BusySendClassHeld               BusySendResultClass = "held"
	BusySendClassQueued             BusySendResultClass = "queued"
	BusySendClassDeliveredConfirmed BusySendResultClass = "delivered_confirmed"
	BusySendClassSentUnconfirmed    BusySendResultClass = "sent_unconfirmed"
	BusySendClassNotStarted         BusySendResultClass = "not_started"
)

// BusySendWaitReason is why a non-Final Message waits
// (busy_send_messages.wait_reason; SR-2.6).
type BusySendWaitReason string

const (
	BusySendWaitTurnRunning        BusySendWaitReason = "turn_running"
	BusySendWaitDialog             BusySendWaitReason = "dialog"
	BusySendWaitCompaction         BusySendWaitReason = "compaction"
	BusySendWaitEarlierMessage     BusySendWaitReason = "earlier_message"
	BusySendWaitHandoverInProgress BusySendWaitReason = "handover_in_progress"
	BusySendWaitAgentStarting      BusySendWaitReason = "agent_starting"
	BusySendWaitLaunchEnded        BusySendWaitReason = "launch_ended"
	BusySendWaitRouteUnavailable   BusySendWaitReason = "route_unavailable"
	BusySendWaitReadinessUnknown   BusySendWaitReason = "readiness_unknown"
	BusySendWaitInputUnresolved    BusySendWaitReason = "input_unresolved"
	BusySendWaitAwaitingProof      BusySendWaitReason = "awaiting_proof"
)

// BusySendResultReason is a Result's stable reason
// (busy_send_messages.result_reason; SR-5.1). A non-Final Result's reason is
// its wait reason (BusySendWaitReason, converted). The codes below are the
// Final reasons: exact_receipt for delivered_confirmed (SR-3.7, SR-3.8);
// confirmation_off, handed_over_not_proven (SR-3.5) and the sticky
// wait_turn_raced (SR-3.3a) for sent_unconfirmed; launch_ended and
// admission_unknown for the Launch End and force terminal rules (SR-4.3,
// SR-6.2); record_deleted and conversation_replaced for never-emitted custody
// a retirement finalized (SR-6.3).
type BusySendResultReason string

const (
	BusySendReasonExactReceipt         BusySendResultReason = "exact_receipt"
	BusySendReasonConfirmationOff      BusySendResultReason = "confirmation_off"
	BusySendReasonHandedOverNotProven  BusySendResultReason = "handed_over_not_proven"
	BusySendReasonWaitTurnRaced        BusySendResultReason = "wait_turn_raced"
	BusySendReasonLaunchEnded          BusySendResultReason = "launch_ended"
	BusySendReasonAdmissionUnknown     BusySendResultReason = "admission_unknown"
	BusySendReasonRecordDeleted        BusySendResultReason = "record_deleted"
	BusySendReasonConversationReplaced BusySendResultReason = "conversation_replaced"
)

// BusySendRetention is whether a confirmed Result, a Message's content or an
// ended force is still retained (SR-5.5, SR-4.1). expired keeps only the
// permanent ownership and the non-content facts: no text, digest, Result or
// raw evidence.
type BusySendRetention string

const (
	BusySendRetained BusySendRetention = "retained"
	BusySendExpired  BusySendRetention = "expired"
)

// BusySendBoundary is the boundary a hand-over was issued at
// (busy_send_handovers.boundary): turn end (an idle or ending agent), a
// Steer Turn tool boundary inside a running turn, or an explicit force.
type BusySendBoundary string

const (
	BusySendBoundaryTurnEnd      BusySendBoundary = "turn_end"
	BusySendBoundaryToolBoundary BusySendBoundary = "tool_boundary"
	BusySendBoundaryForce        BusySendBoundary = "force"
)

// BusySendHandoverState is whether a hand-over still owns its conversation's
// order and transport (busy_send_handovers.state; SR-2.2, SR-2.5). At most
// one hand-over per conversation is open; a closed one keeps its history and
// any evidence that may still arrive for its prompts.
type BusySendHandoverState string

const (
	BusySendHandoverOpen   BusySendHandoverState = "open"
	BusySendHandoverClosed BusySendHandoverState = "closed"
)

// BusySendEmission is a prompt's emission disposition
// (busy_send_prompts.emission; SR-2.4). none: claimed, no intent persisted.
// intent: emission intent and the captured destination persisted, so a route
// call may follow and the prompt is never re-emitted. issued: its intent and
// issue revalidation committed (together, CommitBusySendIssue), so the one
// route call may have been made. released: proven never issued (its claim
// released, or fenced by a call end before revalidation), so its Messages may
// return to Held.
type BusySendEmission string

const (
	BusySendEmissionNone     BusySendEmission = "none"
	BusySendEmissionIntent   BusySendEmission = "intent"
	BusySendEmissionIssued   BusySendEmission = "issued"
	BusySendEmissionReleased BusySendEmission = "released"
)

// BusySendTransport is a prompt's observed transport disposition
// (busy_send_prompts.transport). A transport write is never a receipt or an
// admission. unwritten is proof that nothing was written.
type BusySendTransport string

const (
	BusySendTransportWritten   BusySendTransport = "written"
	BusySendTransportPartial   BusySendTransport = "partial"
	BusySendTransportUncertain BusySendTransport = "uncertain"
	BusySendTransportUnwritten BusySendTransport = "unwritten"
)

// BusySendOrderKind is the evidence that secured a prompt's order
// (busy_send_prompts.order_kind; SR-2.5): the route's qualified ordered
// acceptance, an exact receipt, or Claude's complete keyless emission, which
// releases the order barrier without claiming intake.
type BusySendOrderKind string

const (
	BusySendOrderOrderedAcceptance BusySendOrderKind = "ordered_acceptance"
	BusySendOrderExactReceipt      BusySendOrderKind = "exact_receipt"
	BusySendOrderEmissionComplete  BusySendOrderKind = "emission_complete"
)

// BusySendAdmissionKind is the qualified admission event recorded for a
// prompt (busy_send_prompts.admission_kind; SR-4.3, SR-4.4, SR-4.5a):
// Claude's own UserPromptSubmit sighting of the exact prompt; Codex's
// turn/start or turn/steer RPC success; Cursor's correlated session/prompt
// response or a qualified earlier native signal; a Confirm From Transcripts
// match; or an exact receipt, which also admits.
type BusySendAdmissionKind string

const (
	BusySendAdmissionPromptSighting  BusySendAdmissionKind = "prompt_sighting"
	BusySendAdmissionRPCSuccess      BusySendAdmissionKind = "rpc_success"
	BusySendAdmissionPromptResponse  BusySendAdmissionKind = "prompt_response"
	BusySendAdmissionNativeSignal    BusySendAdmissionKind = "native_signal"
	BusySendAdmissionTranscriptMatch BusySendAdmissionKind = "transcript_match"
	BusySendAdmissionExactReceipt    BusySendAdmissionKind = "exact_receipt"
)

// BusySendEvidenceKind is a qualified receipt's kind
// (busy_send_prompts.evidence_kind, busy_send_messages.evidence_kind; SR-5.1):
// Codex's exact userMessage from item/started or from thread/items/list
// reconciliation (SR-3.7), and Cursor's correlated normal end_turn (SR-3.8).
type BusySendEvidenceKind string

const (
	BusySendEvidenceCodexItemStarted BusySendEvidenceKind = "codex_item_started"
	BusySendEvidenceCodexThreadItems BusySendEvidenceKind = "codex_thread_items"
	BusySendEvidenceCursorEndTurn    BusySendEvidenceKind = "cursor_end_turn"
)

// BusySendNegativeKind is definitive negative evidence for a prompt
// (busy_send_prompts.negative_kind; SR-2.4, SR-4.3): the provider's explicit
// not-submitted response, or a route event after which no admission
// evidence can arrive that is not a not-submitted.
type BusySendNegativeKind string

const (
	BusySendNegativeNotSubmitted BusySendNegativeKind = "not_submitted"
	BusySendNegativeNoAdmission  BusySendNegativeKind = "no_admission"
)

// BusySendForceOutcome is a force attempt's outcome
// (busy_send_forces.outcome; SR-4.3). running is stored only while the
// force's call has not ended; a call end replaces it with one of the four
// caller-facing outcomes, and only pending may change again.
type BusySendForceOutcome string

const (
	BusySendForceRunning    BusySendForceOutcome = "running"
	BusySendForcePending    BusySendForceOutcome = "pending"
	BusySendForceHandedOver BusySendForceOutcome = "handed_over"
	BusySendForceRefused    BusySendForceOutcome = "refused"
	BusySendForceFailed     BusySendForceOutcome = "failed"
)

// BusySendForceReason is a force attempt's stable reason
// (busy_send_forces.reason; SR-4.2 to SR-4.5a, SR-9.2): why it was refused,
// failed, is pending, or how its hand-over was admitted.
type BusySendForceReason string

const (
	BusySendForceAdmitted           BusySendForceReason = "admitted"
	BusySendForceTranscriptMatch    BusySendForceReason = "transcript_match"
	BusySendForceAwaitingAdmission  BusySendForceReason = "awaiting_admission"
	BusySendForceNoHeldMessages     BusySendForceReason = "no_held_messages"
	BusySendForceHandoverInProgress BusySendForceReason = "handover_in_progress"
	BusySendForceEarlierMessage     BusySendForceReason = "earlier_message"
	BusySendForceInputUnresolved    BusySendForceReason = "input_unresolved"
	BusySendForceAgentStarting      BusySendForceReason = "agent_starting"
	BusySendForceLaunchEnded        BusySendForceReason = "launch_ended"
	BusySendForceRouteUnavailable   BusySendForceReason = "route_unavailable"
	BusySendForceNotSubmitted       BusySendForceReason = "not_submitted"
	BusySendForceNotIssued          BusySendForceReason = "not_issued"
	BusySendForceAdmissionUnknown   BusySendForceReason = "admission_unknown"
	BusySendForceAgentNotCovered    BusySendForceReason = "agent_not_covered"
	BusySendForceRecordDeleted      BusySendForceReason = "record_deleted"
	BusySendForceCompaction         BusySendForceReason = "compaction"
	BusySendForceTurnRunning        BusySendForceReason = "turn_running"
	BusySendForceDialog             BusySendForceReason = "dialog"
)

// BusySendPauseClaimState is the lifetime of a pause claim's fence
// (busy_send_pause_claims.state; SR-6.5), independent of what its owning call
// returned. A standing claim fences its Launch; released ends one before any
// signal (BusySendPauseRefusedBeforeSignal or
// BusySendPauseAbandonedBeforeSignal); ended ends one at Launch End on exit
// evidence or a new Launch's publication, SR-6.5's "outcome ended".
type BusySendPauseClaimState string

const (
	BusySendPauseClaimStanding BusySendPauseClaimState = "standing"
	BusySendPauseClaimReleased BusySendPauseClaimState = "released"
	BusySendPauseClaimEnded    BusySendPauseClaimState = "ended"
)

// BusySendPauseCallOutcome is what a claim's owning pause call returned, once
// known (busy_send_pause_claims.call_outcome; SR-6.4, SR-6.5): paused (the
// recorded process's exit was verified within the budget), refused (keyless
// shutdown refused before signalling; the same transaction releases the
// claim), exit_uncertain (the signal was sent and exit was not verified
// within the budget, which is uncertain exit, not success) or failed (the
// call ended with another error after its signal intent). call_cause holds
// the structured cause. Recording an outcome never ends the fence: after a
// signal the claim stands until exit evidence or a new Launch, and a later
// pause on it returns the recorded outcome. A signal intent with no recorded
// outcome is a call that never recorded its end (pause_uncertain).
type BusySendPauseCallOutcome string

const (
	BusySendPausePaused        BusySendPauseCallOutcome = "paused"
	BusySendPauseRefused       BusySendPauseCallOutcome = "refused"
	BusySendPauseExitUncertain BusySendPauseCallOutcome = "exit_uncertain"
	BusySendPauseFailed        BusySendPauseCallOutcome = "failed"
)

// BusySendPauseClosure is the one transition that closed a pause claim's
// fence (busy_send_pause_claims.closed_by; SR-6.5): released before any
// signal, by the owning call's refusal or by a later pause after the owner's
// deadline passed with neither a signal intent nor an outcome; or ended by
// Launch End recorded on exit evidence or by a new Launch's publication. A
// kill or resume that fails or refuses, a kill without exit evidence, a read,
// a timer or elapsed time closes nothing.
type BusySendPauseClosure string

const (
	BusySendPauseRefusedBeforeSignal   BusySendPauseClosure = "refused_before_signal"
	BusySendPauseAbandonedBeforeSignal BusySendPauseClosure = "abandoned_before_signal"
	BusySendPauseLaunchEnd             BusySendPauseClosure = "launch_end"
	BusySendPauseLaunchPublished       BusySendPauseClosure = "launch_published"
)

// BusySendPauseSignal is the keyless shutdown a pause claim intends
// (busy_send_pause_claims.signal_kind; SR-6.4): Claude's SIGTERM, and the
// later releases' Codex server shutdown and Cursor gateway exit.
type BusySendPauseSignal string

const (
	BusySendSignalSIGTERM        BusySendPauseSignal = "sigterm"
	BusySendSignalServerShutdown BusySendPauseSignal = "server_shutdown"
	BusySendSignalGatewayExit    BusySendPauseSignal = "gateway_exit"
)

// The durable lookups' answers (SR-4.1, SR-5.4): found, no durable record
// (unknown), or ownership kept past retention (retention_expired). A store
// failure is an error, never one of these.
const (
	BusySendLookupFound            = "found"
	BusySendLookupUnknown          = "unknown"
	BusySendLookupRetentionExpired = "retention_expired"
)

// BusySendPair is a confirmed Message's caller-owned identity: its Caller UUID
// and Message ID (SR-1.2, SR-1.3), unique across the store
// (busy_send_results' primary key).
type BusySendPair struct {
	CallerUUID        string
	DeliveryRequestID string
}

// BusySendForce is one force attempt as the store keeps it
// (busy_send_forces with its ordered busy_send_force_members; SR-4.1, SR-4.3).
// It names its captured target and needs no spawns row, so it survives agent
// deletion. Times are milliseconds since the epoch, 0 for NULL; "" is NULL for
// the captured strings. A refused attempt that never began a hand-over has no
// HandoverID and no carried pairs.
type BusySendForce struct {
	ForceID        string
	InstanceID     string
	ConversationID string
	LaunchID       string
	LaunchToken    string
	Provider       string
	// OwnerCallID and Revision are the force's owner and entity revision:
	// every mutation compares both and advances Revision (SR-7.2).
	OwnerCallID string
	Revision    int64
	// HandoverID is the hand-over this attempt began; its prompts carry the
	// force's remaining prompt cursor and admission evidence.
	HandoverID         string
	CallDeadlineMillis int64
	CallEndedAtMillis  int64
	Outcome            BusySendForceOutcome
	Reason             BusySendForceReason
	StartedAtMillis    int64
	EndedAtMillis      int64
	Retention          BusySendRetention
	ExpiredAtMillis    int64
	// CarriedPairs are the confirmed pairs of the carried Messages, in carried
	// order; unconfirmed carried Messages have none to show.
	CarriedPairs []BusySendPair
}

// BusySendForceLookup is a read of one force attempt by force_id: Lookup is
// BusySendLookupFound with ForceResult set, or BusySendLookupUnknown or
// BusySendLookupRetentionExpired with ForceResult nil.
type BusySendForceLookup struct {
	Lookup      string
	ForceResult *BusySendForce
}

// BusySendWait is a non-Final Result's wait (SR-2.6, SR-5.1): its reason,
// since when, and the earlier confirmed Message it waits behind, if any.
type BusySendWait struct {
	Reason      BusySendWaitReason
	SinceMillis int64
	Earlier     *BusySendPair
}

// BusySendResult is a Message's stored Result (SR-5.1, SR-5.2) as a send's
// response or a durable lookup reads it: its structured fields, its actual
// Launch binding and receipt identity (nil or "" until established) and its
// observed transition times (milliseconds since the epoch, 0 = not observed).
// Pair is zero for an unconfirmed Message. It holds no text, raw evidence or
// private path. The caller-facing projection (resend_safe, retry_with,
// outcome and the JSON shape) is derived from it outside the store.
type BusySendResult struct {
	Pair         BusySendPair
	InstanceID   string
	SendMode     BusySendMode
	Class        BusySendResultClass
	Final        bool
	Reason       string
	Forced       bool
	Wait         *BusySendWait
	LaunchToken  *string
	SessionID    string
	EvidenceKind *BusySendEvidenceKind
	RecordID     string
	RecordSHA256 string
	// PartialTransport is whether the prompt of the Message's latest hand-over
	// attempt was known to be only partly written (busy_send_prompts.transport
	// partial), which the compatibility outcome reports (SR-5.3). For a Final
	// Result it is what was known by its Final time, so it never changes.
	PartialTransport bool

	AcceptedAtMillis   int64
	QueuedAtMillis     int64
	HandedOverAtMillis int64
	DeliveredAtMillis  int64
	FinalAtMillis      int64
}

// BusySendResultLookup is a read of one confirmed pair: Lookup is
// BusySendLookupFound with Result set, or BusySendLookupUnknown or
// BusySendLookupRetentionExpired with Result nil.
type BusySendResultLookup struct {
	Lookup string
	Result *BusySendResult
}
