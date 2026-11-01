package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// Confirmation is one main field's last-confirmed evidence (SR-4.5, SR-4.6),
// the same five fields on every status, get and list agent result. Every
// field is always present; an unknown value is null, never omitted.
type Confirmation struct {
	// Status is confirmed (the returned value is supported at ConfirmedAt and
	// no later attempt for the field failed), not_confirmed (a later attempt
	// could not confirm it; ConfirmedAt keeps the older applicable
	// confirmation, if any), unknown (no applicable evidence establishes the
	// value) or pending (a known, still-owned operation or pending-launch
	// protection prevented the attempt; no retry is scheduled). Confirmed
	// never means continuously true since ConfirmedAt.
	Status string `json:"status"`
	// ConfirmedAt is when the last evidence confirming this exact returned
	// value and owner was taken, RFC3339 UTC with millisecond precision; null
	// when none is known. A response clock, a settle projection or a pure
	// read never supplies it.
	ConfirmedAt *string `json:"confirmed_at"`
	// Source is that evidence's kind: agent_event, caller_command, process,
	// tmux, pane, transcript or store (a durable agent-director record or
	// intent, never provider receipt); null exactly when ConfirmedAt is.
	Source *string `json:"source"`
	// CheckedAt is the latest attempt to confirm this field, an unsuccessful
	// one included, in the same form; null when none is known. Reading a
	// stored value does not advance it.
	CheckedAt *string `json:"checked_at"`
	// Reason is null for a successful confirmation; otherwise no_evidence,
	// unreadable, busy, stale_observation, deadline, operation_pending,
	// pending_grace, unsupported or commit_unconfirmed. Detailed errors keep
	// their specific causes.
	Reason *string `json:"reason"`
}

// Confirmations holds exactly the three main-field records (SR-4.5): the
// lifecycle state, process_liveness and displayed_state. family_state has
// none: it is the stored family projection, whose freshness is
// unconfirmed. No nested permission request, subagent, receipt or error
// carries one.
type Confirmations struct {
	// State confirms the returned lifecycle state (after settling) for the
	// row's current life and launch.
	State Confirmation `json:"state"`
	// ProcessLiveness confirms process_liveness: the recorded agent process
	// at its recorded start identity, never a Helper, tmux or a descendant.
	ProcessLiveness Confirmation `json:"process_liveness"`
	// DisplayedState is present whether or not a Displayed State shows. This
	// release keeps no display provenance, so it is unknown with null
	// evidence: no_evidence, or unsupported where idle_screen is unavailable
	// (Cursor).
	DisplayedState Confirmation `json:"displayed_state"`
}

// confirmationTime formats a record time, nil for none.
func confirmationTime(ms int64) *string {
	if ms <= 0 {
		return nil
	}
	s := time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
	return &s
}

func nullableField(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// publicConfirmation is c as the public record. A source never shows without
// its time.
func publicConfirmation(c store.Confirmation) Confirmation {
	out := Confirmation{Status: c.Status, ConfirmedAt: confirmationTime(c.ConfirmedAtMillis),
		CheckedAt: confirmationTime(c.CheckedAtMillis), Reason: nullableField(c.Reason)}
	if out.ConfirmedAt != nil {
		out.Source = nullableField(c.Source)
	}
	return out
}

// withAttempt applies this invocation's unsuccessful attempt a to the stored
// record c: the attempt is the latest, so CheckedAt and Reason are its own,
// and only an older applicable confirmation survives it. It never makes a
// field confirmed. It applies only while row still holds the value and owner
// the attempt examined (fieldAttempt.describes) and the attempt is no older
// than the record's confirmation and latest check, so a value or record a
// concurrent writer committed after the attempt keeps its own record and
// CheckedAt never precedes ConfirmedAt.
func withAttempt(c store.Confirmation, a *fieldAttempt, row Spawn) store.Confirmation {
	if a == nil || !a.describes(row) || a.atMillis < max(c.ConfirmedAtMillis, c.CheckedAtMillis) {
		return c
	}
	c.CheckedAtMillis, c.Reason = a.atMillis, a.reason
	switch {
	case a.reason == store.ReasonOperationPending || a.reason == store.ReasonPendingGrace:
		c.Status = store.ConfirmationPending
	case c.ConfirmedAtMillis == 0 && a.reason == store.ReasonNoEvidence:
		c.Status = store.ConfirmationUnknown
	default:
		c.Status = store.ConfirmationNotConfirmed
	}
	return c
}

// checkedRowOf is the shared checker's row for id when s is that checker's
// projection.
func checkedRowOf(s any, id string) *activeCheckedRow {
	if r, ok := s.(*activeReadStore); ok {
		return r.byID[id]
	}
	return nil
}
