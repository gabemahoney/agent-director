package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestPauseSignalBudget (b.y7g) keeps the original budget through revalidation,
// the signal and the end write, preserving whether the signal was sent.
func TestPauseSignalBudget(t *testing.T) {
	// Serial: uses the shared pause signal and injected clock.
	for _, phase := range []string{"late read with end report", "before signal", "after signal", "at end write"} {
		t.Run(phase, func(t *testing.T) {
			spec := killRowSpec{}
			if phase == "late read with end report" {
				spec.Agent = agentGone
				spec.Opts = []apitest.SpawnOption{apitest.WithEndReportedAt(codexEndReport)}
			}
			e := newUncoveredPauseEnv(t, spec)
			var st api.PauseStore = e.st
			signals := 1
			if phase == "late read with end report" {
				st = &kpHookedStore{Store: e.st, first: func() { e.advance(2 * kpBudget * time.Second) }}
				signals = 0
			} else if phase == "before signal" {
				st = &kpHookedStore{Store: e.st, once: func() { e.advance(2 * kpBudget * time.Second) }}
				signals = 0
			} else if phase == "after signal" {
				e.answer = func(s kpSignal) error {
					e.pc.Set(s.pid, procfix.Gone())
					e.advance(2 * kpBudget * time.Second)
					return nil
				}
			} else {
				st = &signalEndHookStore{Store: e.st, after: func() { e.advance(2 * kpBudget * time.Second) }}
			}
			err := e.pause(context.Background(), st, kpBudget)
			assertOneName(t, err, "ErrSpawnNotPausable")
			want := `{"cause":"budget_exhausted","signal_sent":true}`
			if signals == 0 {
				want = `{"cause":"budget_exhausted","signal_sent":false}`
			}
			if got := jsonOf(t, api.ErrDetails(err)); got != want {
				t.Errorf("err_details = %s; want %s", got, want)
			}
			e.assertSignals(t, signals)
			assertUncoveredPauseNoInput(t, e)
			if phase == "at end write" {
				e.assertEnded(t)
			}
		})
	}
}

type signalEndHookStore struct {
	*store.Store
	after func()
}

func (s *signalEndHookStore) MarkSignalPauseEndedWithin(ctx context.Context, row api.Spawn, exit store.ProcessExit, wait time.Duration) (string, store.CondResult, error) {
	prior, res, err := s.Store.MarkSignalPauseEndedWithin(ctx, row, exit, wait)
	if s.after != nil {
		s.after()
	}
	return prior, res, err
}

// TestPauseSignalReplacementAfterSignal (b.y7g) proves the end transaction
// cannot adopt a fresh row snapshot belonging to another launch.
func TestPauseSignalReplacementAfterSignal(t *testing.T) {
	// Serial: replaces the shared pause signal and clock.
	e := newUncoveredPauseEnv(t, killRowSpec{})
	var replacement apitest.SpawnColumns
	e.answer = func(kpSignal) error { return nil }
	e.atSleep = func() {
		e.pc.Set(e.r.AgentPID, procfix.Gone())
		pcSetToken(t, e.dbPath, e.r.ID, newToken())
		replacement = e.columns(t, e.r.ID)
	}
	err := e.pause(context.Background(), nil, kpBudget)
	assertOneName(t, err, "ErrSpawnNotPausable")
	if got := jsonOf(t, api.ErrDetails(err)); got != `{"cause":"launch_changed","signal_sent":true}` {
		t.Errorf("err_details = %s; want launch_changed after signal", got)
	}
	e.assertSignals(t, 1)
	e.assertRowUnchanged(t, e.r.ID, replacement)
	assertUncoveredPauseNoInput(t, e)
}

// TestPauseSignalAgentGoneAtSignal (b.y7g; TW-C race) covers another caller
// ending the process at the signal boundary, with and without a new Launch.
func TestPauseSignalAgentGoneAtSignal(t *testing.T) {
	// Serial: replaces the shared pause signal and clock.
	for _, replaced := range []bool{false, true} {
		name := "same launch"
		if replaced {
			name = "replacement launch"
		}
		t.Run(name, func(t *testing.T) {
			e := newUncoveredPauseEnv(t, killRowSpec{})
			var replacement apitest.SpawnColumns
			e.answer = func(s kpSignal) error {
				e.pc.Set(s.pid, procfix.Gone())
				if replaced {
					pcSetToken(t, e.dbPath, e.r.ID, newToken())
					replacement = e.columns(t, e.r.ID)
				}
				return probe.ErrSignalTargetGone
			}
			err := e.pause(context.Background(), nil, kpBudget)
			if replaced {
				assertOneName(t, err, "ErrSpawnNotPausable")
				if got := jsonOf(t, api.ErrDetails(err)); got != `{"cause":"launch_changed","signal_sent":false}` {
					t.Errorf("err_details = %s; want launch_changed without a signal", got)
				}
				e.assertRowUnchanged(t, e.r.ID, replacement)
			} else {
				if err != nil {
					t.Fatalf("Pause: %v; want ended despite the concurrent exit", err)
				}
				e.assertEnded(t)
			}
			e.assertSignals(t, 1)
			assertUncoveredPauseNoInput(t, e)
		})
	}
}
