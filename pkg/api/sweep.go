package api

import (
	"context"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// SweepResult is the common result of Sweep and its FindMissing alias.
// Counters describe only guarded liveness writes, independently of Checks.
type SweepResult = FindMissingResult

// SweepCheck is one member of the fixed selection, including finished and
// unattempted rows. Its values and evidence come from the shared checker.
type SweepCheck struct {
	InstanceID             string        `json:"instance_id"`
	State                  string        `json:"state"`
	DisplayedState         string        `json:"displayed_state,omitempty"`
	FamilyState            string        `json:"family_state"`
	IdleScreenAvailability string        `json:"idle_screen_availability"`
	ProcessLiveness        string        `json:"process_liveness"`
	Confirmations          Confirmations `json:"confirmations"`
	// Completion is complete when every eligible adapter was attempted or
	// found inapplicable. It does not mean the fields are confirmed.
	Completion string `json:"completion"`
	// Reason is null for complete; otherwise deadline, busy, unreadable,
	// stale_observation, no_evidence, commit_unconfirmed, unsupported or
	// unattempted. Unattempted work creates no confirmation timestamps.
	Reason *string `json:"reason"`
}

// Sweep performs one bounded all-agent pass through the same active checks
// used by state-reporting reads. FindMissing is its compatibility alias;
// its existing narrow store and provider interfaces remain accepted. A
// narrow store can expose only its live identities and available adapters,
// so unsupported coverage is explicitly incomplete. Production selects all
// agents from one consistent snapshot, retaining every selected ID even
// when it changes, disappears or cannot be checked. Only selection errors
// fail the call; interrupted work after selection remains in Checks.
// Missing is a check's judgement on available evidence, not proof that the
// agent has exited. Run as the agents' user in their tmux environment.
//
// Parameters and safety bounds are those of FindMissing. No input, answer,
// signal, restart or retry is authorized by these observational results.
func Sweep(ctx context.Context, s FindMissingStore, t FindMissingTmux, pc ProcChecker, pendingGrace, sweepBudget time.Duration, now func() time.Time, lg FindMissingLogger) (SweepResult, error) {
	view := RelayView{Procs: pc, Now: now, active: &activeCheckProviders{tmux: t, pendingGrace: pendingGrace, budget: sweepBudget, logger: lg}}
	return findMissingChecked(ctx, s, view, store.SettleRead{})
}

// Sweep checks a fixed all-agent snapshot. Complete describes coverage,
// never freshness or command authority. FindMissing delegates to this method.
// A closed client or failed initial selection returns an error with empty
// non-null arrays; per-agent failures preserve the other selected results.
//
// CLI: agent-director sweep
//
// Errors:
//   - ErrProbeUnsupported: retained for compatibility; no longer returned.
//   - [ErrStoreBusy]: the zero-wait store read selecting the agents did not
//     finish within its bound: another call of this process held the store's
//     operation slot or connection (an earlier call's operation keeps them
//     until it is disposed), another connection held a lock the read needed,
//     the read ran out of time, or ctx ended first. Nothing was checked.
//
// Nondeterminism: none.
func (c *Client) Sweep(ctx context.Context) (SweepResult, error) {
	if err := c.checkClosed(); err != nil {
		return emptySweepResult(), err
	}
	return findMissingChecked(ctx, c.st.WithContext(ctx), c.relayView(), c.settleRead())
}

func emptySweepResult() SweepResult {
	return SweepResult{IDs: []string{}, UnverifiedIDs: []string{}, Checks: []SweepCheck{}}
}

// sweepCompletion consumes adapter outcomes, without sampling or checking
// evidence a second time. An unsuccessful attempted adapter is covered;
// an early return leaving other eligible work unattempted is not.
func sweepCompletion(outcomes []activeCheckOutcome) (string, *string) {
	latest := map[string]activeCheckOutcome{}
	reason := ""
	for _, out := range outcomes {
		latest[out.Adapter] = out
		why := outcomeReason(out.Result)
		if why == "" && out.Err != nil {
			why = outcomeReason(activeOutcome(out.Adapter, false, out.Err).Result)
		}
		if why != "" {
			reason = why
		}
	}
	incomplete, unsupported := false, false
	for _, out := range latest {
		switch out.Result {
		case "cancelled":
			return "incomplete", nullableField(store.ReasonDeadline)
		case "unattempted":
			incomplete = true
		case "unavailable":
			incomplete, unsupported = true, true
		}
	}
	if !incomplete {
		return "complete", nil
	}
	if reason == "" {
		reason = "unattempted"
		if unsupported {
			reason = store.ReasonUnsupported
		}
	}
	return "incomplete", nullableField(reason)
}

func (r *activeReadStore) sweepChecks() []SweepCheck {
	checks := make([]SweepCheck, 0, len(r.rows))
	for _, checked := range r.rows {
		row := checked.row
		state := row.State
		if r.settle.SettleMs > 0 {
			state = row.ReportedState
		}
		process, confirmations := mainFields(r, row, r.settle)
		completion, reason := sweepCompletion(checked.outcomes)
		display := displayedState(checked.display)
		if display == StateNeedsIntelligence {
			display = ""
		}
		checks = append(checks, SweepCheck{InstanceID: row.InstanceID, State: state,
			DisplayedState: display, FamilyState: row.FamilyState,
			IdleScreenAvailability: idleScreenAvailability(row.Agent), ProcessLiveness: process,
			Confirmations: confirmations, Completion: completion, Reason: reason})
	}
	return checks
}
