package apitest

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
)

// PauseSurface is one conditional pause envelope shape (reusable fixture for
// interface parity): Name selects it in SeedPauseSurface and ts-helper's
// seed-pause-surface, and Details is the exact err_details of
// ErrSpawnNotPausable that pause with only_if_waiting returns on it.
type PauseSurface struct {
	Name    string
	Details map[string]any
}

// PauseSurfaces are the shapes each interface's parity test pins (SR-6.5): a
// fresh not_waiting refusal with the raw observed_state, and a standing
// claim's replay of a recorded exit_uncertain (signal_sent true) and of a
// failure with a structured cause (signal_sent omitted).
var PauseSurfaces = []PauseSurface{
	{"not_waiting", map[string]any{"cause": "not_waiting", "observed_state": "working"}},
	{"exit_uncertain", map[string]any{"cause": "exit_uncertain", "signal_sent": true}},
	{"failed_cause", map[string]any{"cause": "signal_uncertain"}},
}

// SeedPauseSurface seeds id, a covered Claude row, in the store at dbPath
// (created when missing) in the PauseSurfaces shape name: raw working for
// not_waiting; otherwise waiting, with a pause claim on its current Launch
// that the store's own writers made, gave a signal intent and recorded the
// shape's outcome on. Nothing is signalled.
func SeedPauseSurface(dbPath, id, name string) error {
	state, outcome, cause := store.StateWaiting, store.BusySendPauseExitUncertain, ""
	switch name {
	case "not_waiting":
		state = store.StateWorking
	case "exit_uncertain":
	case "failed_cause":
		outcome, cause = store.BusySendPauseFailed, "signal_uncertain"
	default:
		return fmt.Errorf("SeedPauseSurface: unknown shape %q", name)
	}
	if _, err := SeedSpawn(dbPath, id, state, "", "off", "", true); err != nil || name == "not_waiting" {
		return err
	}
	s, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("SeedPauseSurface: open store: %w", err)
	}
	defer s.Close() //nolint:errcheck
	pub, err := s.GetBusySendPublishedLaunch(id)
	if err != nil {
		return fmt.Errorf("SeedPauseSurface: published Launch: %w", err)
	}
	ctx, wait := context.Background(), 10*time.Second
	d, err := s.ClaimBusySendPauseWithin(ctx, store.BusySendPauseRequest{InstanceID: id, LaunchID: pub.LaunchID,
		LaunchToken: pub.LaunchToken, OwnerCallID: uuid.NewString(), Deadline: time.Now().Add(time.Minute)}, wait)
	if err != nil || d.Kind != store.BusySendPauseDecisionClaimed || d.Claim == nil {
		return fmt.Errorf("SeedPauseSurface: claim = %+v, %v; want claimed", d, err)
	}
	w, err := s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: d.Claim.Ref}, wait)
	if err != nil || !w.Applied || w.NextRef == nil {
		return fmt.Errorf("SeedPauseSurface: signal intent = %+v, %v; want applied", w, err)
	}
	w, err = s.RecordBusySendPauseOutcomeWithin(ctx, store.BusySendPauseOutcomeRequest{Ref: *w.NextRef, Outcome: outcome, Cause: cause}, wait)
	if err != nil || !w.Applied {
		return fmt.Errorf("SeedPauseSurface: outcome %s = %+v, %v; want applied", outcome, w, err)
	}
	return nil
}
