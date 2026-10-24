package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// StatusStore is the narrow store surface Status needs. *store.Store
// satisfies it; tests pass the real store.
type StatusStore interface {
	GetSpawnState(instanceID string) (string, error)
}

// StatusResult is the typed return shape of Status — the row's state value,
// plus launch_started_at on a pending row, surfaced as a JSON object on the
// CLI.
type StatusResult struct {
	StateDetail
	// TmuxSessionName is present while a Displayed State is shown
	// (DisplayedState).
	TmuxSessionName string `json:"tmux_session_name,omitempty"`

	// Context is an experimental on-demand reading; unavailable values are null.
	Context ContextReading `json:"context"`
	// State is the current lifecycle state of the Spawn. One of:
	// pending, waiting, working, ask_user, check_permission, ended, missing;
	// never a Displayed State, which DisplayedState carries beside it.
	// pending means a launch (spawn, reuse or resume) is in progress and the
	// agent has not reported in yet (its SessionStart hook); it may be
	// loading or waiting at a startup prompt. A resumed pending row keeps its
	// session id and history; a caller tells it from a fresh one by its
	// non-empty session_id, shown by [Client.Get].
	State string `json:"state"`
	// FamilyState reports working or waiting for the complete stored family.
	// Both directions settle independently using [state] settle_ms.
	FamilyState string `json:"family_state"`
	// IdleScreenAvailability is whether idle_screen can apply to the row's
	// agent: available for Claude, Codex or an absent agent, unavailable for Cursor
	// (IdleScreenAvailable, IdleScreenUnavailable). Present on every result;
	// omitted only from the empty result of a failed call.
	IdleScreenAvailability string `json:"idle_screen_availability,omitempty"`
	// LaunchStartedAt is the start of the launch in progress, RFC3339 UTC
	// with millisecond precision. Present only on a pending row; nil (and
	// omitted from JSON) otherwise (SR-22.2).
	LaunchStartedAt *time.Time `json:"launch_started_at,omitempty"`
}

// statusReader reads the state and the launch start in one read (SR-16.1).
// It is a fallback for stores lacking the richer status readers (see
// statusSettledWithRelay).
type statusReader interface {
	SpawnStatus(instanceID string) (state string, launchStartedAtMillis int64, err error)
}

// settledStatusReader reads the reported state, the raw state and the launch
// start in one read (b.3ie). It is used only with a settle time, by a store
// lacking the richer status readers (see statusSettledWithRelay).
type settledStatusReader interface {
	SpawnStatusSettled(instanceID string, r store.SettleRead) (reported, raw string, launchStartedAtMillis int64, err error)
}

type contextStatusReader interface {
	SpawnStatusContext(instanceID string, r store.SettleRead) (reported, raw string, launchStartedAtMillis int64, evidence store.SpawnContext, err error)
}

// permissionStatusReader retains only the evidence needed for the shared
// permission report, including the row's exact snapshot and launch identity.
type permissionStatusReader interface {
	SpawnStatusRow(instanceID string, r store.SettleRead) (Spawn, error)
}

// familyStatusReader keeps family and own-state reports in the same read.
type familyStatusReader interface {
	SpawnStatusWithFamily(instanceID string, r store.SettleRead) (reported, raw, family string, launchStartedAtMillis int64, err error)
}

// Status returns the current state column for the given instance_id,
// with launch_started_at on a pending row unless s offers only GetSpawnState.
// Missing rows surface store.ErrSpawnNotFound; the CLI translates that to
// the canonical err_name envelope. v judges each permission request's relay
// hook for the row's display observation, as for [Get] and [List] (see
// [RelayView]). It reads with no settle time and samples no pane, so a
// working Claude or Codex row keeps its pane's current idle_screen finding as
// committed; the Client applies its [state] settle_ms and samples panes. It
// never waits.
func Status(s StatusStore, v RelayView, instanceID string) (StatusResult, error) {
	return statusSettledWithRelay(s, v, store.SettleRead{}, instanceID)
}

// statusSettledWithRelay is Status with the reader's settle time and clock
// settle (b.3ie). With a settle time, and a store that provides the settled
// read, the state is the settled state (store.SettleRead's derivation, in the
// read's own statement); launch_started_at still keys on the raw state. The
// row, and each Codex descendant its family_state counts, whose rollout
// records its turn's end is first moved to waiting (endCodexTurns). It never
// waits.
func statusSettledWithRelay(s StatusStore, v RelayView, settle store.SettleRead, instanceID string) (StatusResult, error) {
	endCodexTurns(s, instanceID)
	if r, ok := s.(permissionStatusReader); ok {
		row, err := r.SpawnStatusRow(instanceID, settle)
		if err != nil {
			return StatusResult{}, err
		}
		display, family := displayed(s, v, settle, row)
		out := StatusResult{State: row.ReportedState, StateDetail: stateDetail(display), FamilyState: family,
			IdleScreenAvailability: idleScreenAvailability(row.Agent),
			LaunchStartedAt:        launchStartedAt(row.State, row.LaunchStartedAtMillis), Context: contextOf(row.ContextEvidence())}
		if out.DisplayedState != "" {
			out.TmuxSessionName = row.TmuxSessionName
		}
		return out, nil
	}

	// The narrower reads below, which only stores without SpawnStatusRow
	// reach, carry no agent but the context read's; the others use the shared
	// empty-agent compatibility projection without authorizing pane sampling.
	if sr, ok := s.(contextStatusReader); ok {
		reported, raw, launchMillis, evidence, err := sr.SpawnStatusContext(instanceID, settle)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: reported, FamilyState: evidence.FamilyState, LaunchStartedAt: launchStartedAt(raw, launchMillis),
			IdleScreenAvailability: idleScreenAvailability(evidence.Agent), Context: contextOf(evidence)}, nil
	}
	if sr, ok := s.(familyStatusReader); ok {
		reported, raw, family, launchMillis, err := sr.SpawnStatusWithFamily(instanceID, settle)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: reported, FamilyState: family, LaunchStartedAt: launchStartedAt(raw, launchMillis),
			IdleScreenAvailability: idleScreenAvailability("")}, nil
	}

	if sr, ok := s.(settledStatusReader); ok && settle.SettleMs > 0 {
		reported, raw, launchMillis, err := sr.SpawnStatusSettled(instanceID, settle)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: reported, LaunchStartedAt: launchStartedAt(raw, launchMillis), IdleScreenAvailability: idleScreenAvailability("")}, nil
	}
	if r, ok := s.(statusReader); ok {
		state, launchMillis, err := r.SpawnStatus(instanceID)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: state, LaunchStartedAt: launchStartedAt(state, launchMillis), IdleScreenAvailability: idleScreenAvailability("")}, nil
	}
	state, err := s.GetSpawnState(instanceID)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{State: state, IdleScreenAvailability: idleScreenAvailability("")}, nil
}

// Status returns the current state (pending/waiting/working/ask_user/
// check_permission/ended/missing) for the identified Spawn, and the
// Displayed State it shows, if any, in displayed_state. On a pending row the
// result also carries launch_started_at, when the agent's launch began.
// With [state] settle_ms set, the state is the settled state: a hook-written
// change shows only once it has held that long, the last settled state until
// then. Entering, leaving or moving between Displayed States instead times
// settle_ms from the read that commits its candidate and shows from a later
// successful read that commits it; until then displayed_state, since and
// detail stay the previous display's. Terminal states and command-driven
// changes show at once. family_state settles independently. The read never
// waits.
//
// A working Claude or Codex row's own pane is captured on each read and its
// fingerprint compared with the previous read's: once identical samples have
// spanned the row's idle_screen timeout, the row reads state working with
// displayed_state idle_screen. Nothing observes panes between reads.
// idle_screen_availability says whether that can apply to the row's agent
// (unavailable for Cursor). A permission request never yields a Displayed
// State: its row reads check_permission however long it waits.
//
// CLI: agent-director status
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for instanceID.
//
// Nondeterminism: none with no settle time; with one, the state depends on
// the clock. A Displayed State depends on the clock and on the pane samples.
func (c *Client) Status(instanceID string) (StatusResult, error) {
	if err := c.checkClosed(); err != nil {
		return StatusResult{}, err
	}
	proof, err := c.reconcileCursorGateway(instanceID)
	if err != nil {
		return StatusResult{}, err
	}
	var proofs []store.CursorMissingProof
	if proof != nil {
		proofs = append(proofs, *proof)
	}
	out, err := statusSettledWithRelay(c.st.WithCursorMissingReads(proofs), c.relayView(), c.settleRead(), instanceID)
	if err != nil {
		return StatusResult{}, err
	}
	if err := c.persistCursorMissing(proofs); err != nil {
		return StatusResult{}, err
	}
	return out, nil
}

// Compile-time assertion that *store.Store provides every optional status
// reader, and *store.CursorMissingReads the context and row reads; production
// uses SpawnStatusRow (through permissionStatusReader).
var (
	_ familyStatusReader     = (*store.Store)(nil)
	_ statusReader           = (*store.Store)(nil)
	_ settledStatusReader    = (*store.Store)(nil)
	_ contextStatusReader    = (*store.Store)(nil)
	_ contextStatusReader    = (*store.CursorMissingReads)(nil)
	_ permissionStatusReader = (*store.Store)(nil)
	_ permissionStatusReader = (*store.CursorMissingReads)(nil)
)
