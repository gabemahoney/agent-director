package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
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
	StuckInfo
	// TmuxSessionName is present when the displayed row is stuck.
	TmuxSessionName string `json:"tmux_session_name,omitempty"`

	// Context is an experimental on-demand reading; unavailable values are null.
	Context ContextReading `json:"context"`
	// State is the current lifecycle state of the Spawn. One of:
	// pending, waiting, working, ask_user, check_permission, ended, missing, stuck.
	// pending means a launch (spawn, reuse or resume) is in progress and the
	// agent has not reported in yet (its SessionStart hook); it may be
	// loading or waiting at a startup prompt. A resumed pending row keeps its
	// session id and history; a caller tells it from a fresh one by its
	// non-empty session_id, shown by [Client.Get].
	State string `json:"state"`
	// FamilyState reports working or waiting for the complete stored family.
	// Both directions settle independently using [state] settle_ms.
	FamilyState string `json:"family_state"`
	// NoScreenChangeAvailability is whether no_screen_change can apply to the
	// row's agent: available for Claude and Codex, unavailable for Cursor
	// (NoScreenChangeAvailable, NoScreenChangeUnavailable). Present on every
	// result; omitted only from the empty result of a failed call.
	NoScreenChangeAvailability string `json:"no_screen_change_availability,omitempty"`
	// LaunchStartedAt is the start of the launch in progress, RFC3339 UTC
	// with millisecond precision. Present only on a pending row; nil (and
	// omitted from JSON) otherwise (SR-22.2).
	LaunchStartedAt *time.Time `json:"launch_started_at,omitempty"`
}

// statusReader is the optional narrow read Status uses when the store it is
// given provides it: the state and the launch start in one read (SR-16.1).
// *store.Store satisfies it; a StatusStore without it yields no launch start.
type statusReader interface {
	SpawnStatus(instanceID string) (state string, launchStartedAtMillis int64, err error)
}

// settledStatusReader is the optional read status uses with a settle time
// (b.3ie): the reported state, the raw state and the launch start in one
// read. *store.Store satisfies it; a store without it reports the raw state.
type settledStatusReader interface {
	SpawnStatusSettled(instanceID string, r store.SettleRead) (reported, raw string, launchStartedAtMillis int64, err error)
}

type contextStatusReader interface {
	SpawnStatusContext(instanceID string, r store.SettleRead) (reported, raw string, launchStartedAtMillis int64, evidence store.SpawnContext, err error)
}

// permissionStatusReader retains only the evidence needed for the shared
// permission report, including its exact row and launch ownership snapshot.
type permissionStatusReader interface {
	SpawnStatusRow(instanceID string, r store.SettleRead) (Spawn, error)
}

// familyStatusReader keeps family and own-state reports in the same read.
type familyStatusReader interface {
	SpawnStatusWithFamily(instanceID string, r store.SettleRead) (reported, raw, family string, launchStartedAtMillis int64, err error)
}

// Status returns the current state column for the given instance_id,
// with launch_started_at on a pending row when s provides the narrow read.
// Missing rows surface store.ErrSpawnNotFound; the CLI translates that to
// the canonical err_name envelope. It reads with no settle time and samples
// no pane, so a working Claude or Codex row keeps its pane's current
// no_screen_change finding as committed; the Client applies its [state]
// settle_ms and samples panes.
func Status(s StatusStore, instanceID string) (StatusResult, error) {
	return statusSettled(s, store.SettleRead{}, instanceID)
}

// statusSettled is Status with the reader's settle time and clock settle
// (b.3ie). With a settle time, and a store that provides the settled read,
// the state is the settled state (store.SettleRead's derivation, in the
// read's own statement); launch_started_at still keys on the raw state. With
// no settle time it is Status exactly as before b.3ie. It never waits.
func statusSettled(s StatusStore, settle store.SettleRead, instanceID string) (StatusResult, error) {
	return statusSettledWithRelay(s, RelayView{Window: config.DefaultRelayTimeoutSeconds * time.Second}, settle, instanceID)
}

func statusSettledWithRelay(s StatusStore, v RelayView, settle store.SettleRead, instanceID string) (StatusResult, error) {
	if r, ok := s.(permissionStatusReader); ok {
		row, err := r.SpawnStatusRow(instanceID, settle)
		if err != nil {
			return StatusResult{}, err
		}
		display := stuckDisplay(s, v, settle, row)
		out := StatusResult{State: display.State, StuckInfo: stuckInfo(display), FamilyState: row.FamilyState,
			NoScreenChangeAvailability: noScreenChangeAvailability(row.Agent),
			LaunchStartedAt:            launchStartedAt(row.State, row.LaunchStartedAtMillis), Context: contextOf(row.ContextEvidence())}
		if display.State == store.StateStuck {
			out.TmuxSessionName = row.TmuxSessionName
		}
		return out, nil
	}

	// The narrower reads below, which only stores without SpawnStatusRow
	// reach, carry no agent but the context read's; the others report
	// no_screen_change available, as for a Claude row.
	if sr, ok := s.(contextStatusReader); ok {
		reported, raw, launchMillis, evidence, err := sr.SpawnStatusContext(instanceID, settle)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: reported, FamilyState: evidence.FamilyState, LaunchStartedAt: launchStartedAt(raw, launchMillis),
			NoScreenChangeAvailability: noScreenChangeAvailability(evidence.Agent), Context: contextOf(evidence)}, nil
	}
	if sr, ok := s.(familyStatusReader); ok {
		reported, raw, family, launchMillis, err := sr.SpawnStatusWithFamily(instanceID, settle)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: reported, FamilyState: family, LaunchStartedAt: launchStartedAt(raw, launchMillis),
			NoScreenChangeAvailability: NoScreenChangeAvailable}, nil
	}

	if sr, ok := s.(settledStatusReader); ok && settle.SettleMs > 0 {
		reported, raw, launchMillis, err := sr.SpawnStatusSettled(instanceID, settle)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: reported, LaunchStartedAt: launchStartedAt(raw, launchMillis), NoScreenChangeAvailability: NoScreenChangeAvailable}, nil
	}
	if r, ok := s.(statusReader); ok {
		state, launchMillis, err := r.SpawnStatus(instanceID)
		if err != nil {
			return StatusResult{}, err
		}
		return StatusResult{State: state, LaunchStartedAt: launchStartedAt(state, launchMillis), NoScreenChangeAvailability: NoScreenChangeAvailable}, nil
	}
	state, err := s.GetSpawnState(instanceID)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{State: state, NoScreenChangeAvailability: NoScreenChangeAvailable}, nil
}

// Status returns the current state column (pending/waiting/working/ask_user/
// check_permission/ended/missing/stuck) for the identified Spawn. On a pending row
// the result also carries launch_started_at, when the agent's launch began.
// With [state] settle_ms set, the state is the settled state: a hook-written
// change shows only once it has held that long, the last settled state until
// then; terminal states and command-driven changes show at once. The read
// never waits.
//
// A working Claude or Codex row's own pane is captured on each read and its
// fingerprint compared with the previous read's: once identical samples have
// spanned the row's no_screen_change timeout, the row reads stuck with
// stuck_reason no_screen_change. Nothing observes panes between reads.
// no_screen_change_availability says whether that can apply to the row's
// agent (unavailable for Cursor).
//
// CLI: agent-director status
//
// Errors:
//   - [ErrSpawnNotFound]: no row exists for instanceID.
//
// Nondeterminism: none with no settle time; with one, the state depends on
// the clock. A stuck state depends on the clock and on the pane samples.
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

// Compile-time assertion that *store.Store provides the optional narrow read
// Status uses, so the production wiring keeps showing the launch start, and
// the settled read the Client's status uses with a settle time.
var (
	_ familyStatusReader     = (*store.Store)(nil)
	_ statusReader           = (*store.Store)(nil)
	_ settledStatusReader    = (*store.Store)(nil)
	_ contextStatusReader    = (*store.Store)(nil)
	_ contextStatusReader    = (*store.CursorMissingReads)(nil)
	_ permissionStatusReader = (*store.Store)(nil)
	_ permissionStatusReader = (*store.CursorMissingReads)(nil)
)
