package api_test

// advice_follow_pause_claim_test.go: literal-follow tests for the advice of
// pause's claim answers (SR-6.5) and of a plain signal, sent under the store's
// write lock, whose result is not known (SR-6.4), on pause_keyless_test.go's
// covered-row fixture and pause_claim_test.go's helpers. They replace keyless
// pause's process-wide signal hook, so all are serial.

import (
	"context"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// pcClaimStands is how every standing claim's answer says the claim ends.
const pcClaimStands = "its claim on this Launch and that claim's fence on input stay until the Launch ends with evidence that its agent exited, or a resume or reuse publishes a new Launch (kill establishes that evidence)"

// TestAdviceFollow_E29_PauseSignalResultUnknownInspectRetry: E29 "inspect with
// get before retrying" after a plain signal begun under the store's lock whose
// result was not returned: get reads the row, and the retried pause signals
// once and succeeds.
func TestAdviceFollow_E29_PauseSignalResultUnknownInspectRetry(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	e := newKeylessEnv(t, killRowSpec{})
	st := &pcStore{Store: e.st, plain: func(int, func() error, func(func() error) (store.BusySendPlainPauseSignalResult, error)) (store.BusySendPlainPauseSignalResult, error) {
		return store.BusySendPlainPauseSignalResult{Invoked: true}, nil
	}}
	err := e.pause(context.Background(), st, kpBudget)
	adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "so whether the agent got it is not known; inspect with get before retrying")

	if _, err := e.c.Get(e.r.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := e.pause(context.Background(), nil, kpBudget); err != nil {
		t.Fatalf("retried pause: %v; want success", err)
	}
	e.assertSignals(t, 1)
	e.assertEnded(t)
}

// TestAdviceFollow_E36_PauseNotWaitingRetryOrPlain: E36 "retry pause once it
// is waiting, or pause without only_if_waiting" after a conditional pause of
// a working agent: retried while it works, the refusal comes back unchanged;
// once the agent's Stop has it waiting, the retry pauses it; a pause without
// only_if_waiting pauses it at once. One signal either way.
func TestAdviceFollow_E36_PauseNotWaitingRetryOrPlain(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	for _, follow := range []string{"retry once waiting", "pause without only_if_waiting"} {
		t.Run(follow, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			if a := apitest.ApplyAgentHook(t, e.dbPath, e.r.ID, "UserPromptSubmit", e.r.Spawn.SessionID); !a.Applied {
				t.Fatalf("UserPromptSubmit not applied: %s", a.Reason)
			}
			err := e.pauseOpt(context.Background(), nil, kpBudget, true)
			adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "nothing was signalled or changed; retry pause once it is waiting, or pause without only_if_waiting")

			if follow == "pause without only_if_waiting" {
				pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, false), "")
				e.assertClaims(t)
			} else {
				if again := e.pauseOpt(context.Background(), nil, kpBudget, true); errText(again) != err.Error() {
					t.Errorf("retry while working = %v; want the same refusal %q", again, err)
				}
				if a := apitest.ApplyAgentHook(t, e.dbPath, e.r.ID, "Stop", e.r.Spawn.SessionID); !a.Applied {
					t.Fatalf("Stop not applied: %s", a.Reason)
				}
				pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, true), "")
				e.assertClaims(t, "ended launch_end paused r4 intent")
			}
			e.assertSignals(t, 1)
			e.assertEnded(t)
		})
	}
}

// TestAdviceFollow_E37_PauseTurnPendingRetryAfterTurn: E37 "retry pause after
// that turn" after a conditional pause while issued input has not started:
// retried meanwhile, the refusal comes back unchanged; once the agent took
// that turn (its output written and its admission recorded), the retry
// pauses it with one signal.
func TestAdviceFollow_E37_PauseTurnPendingRetryAfterTurn(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	e := newKeylessEnv(t, killRowSpec{})
	ref := e.issue(t)
	err := e.pauseOpt(context.Background(), nil, kpBudget, true)
	adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "nothing was signalled or changed; retry pause after that turn")
	if again := e.pauseOpt(context.Background(), nil, kpBudget, true); errText(again) != err.Error() {
		t.Errorf("retry before the turn = %v; want the same refusal %q", again, err)
	}

	e.takeTurn(t, ref)
	pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, true), "")
	e.assertSignals(t, 1)
	e.assertClaims(t, "ended launch_end paused r4 intent")
	e.assertEnded(t)
}

// TestAdviceFollow_E38_PauseInProgressRetry: E38 "another pause of this
// Launch is in progress; nothing was signalled; retry pause", for either
// option value: retried while its owner is within its deadline, the answer
// comes back unchanged; once that owner, abandoned before any signal intent,
// is past it, the retry releases its claim, claims and pauses with one
// signal.
func TestAdviceFollow_E38_PauseInProgressRetry(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	e := newKeylessEnv(t, killRowSpec{})
	e.claimNow(t, 15*time.Second)
	for _, only := range []bool{false, true} {
		err := e.pauseOpt(context.Background(), nil, kpBudget, only)
		adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "another pause of this Launch is in progress; nothing was signalled; retry pause")
		if again := e.pauseOpt(context.Background(), nil, kpBudget, only); errText(again) != err.Error() {
			t.Errorf("retry within the owner's deadline = %v; want the same answer %q", again, err)
		}
	}
	e.assertSignals(t, 0)

	e.advance(time.Minute)
	pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, true), "")
	e.assertSignals(t, 1)
	e.assertClaims(t, "released abandoned_before_signal r2", "ended launch_end paused r4 intent")
	e.assertEnded(t)
}

// TestAdviceFollow_E39_PauseClaimStandsInspectKill: E39 "inspect with get
// before more input" and the claim's "... (kill establishes that evidence)",
// for an uncertain pause and the replays of a recorded exit_uncertain and of a
// failure with no cause: get reads the row still live with its Messages
// Held; the answer stays unchanged after a kill whose agent did not exit (no
// evidence); once kill verifies the agent's exit, the claim ends and pause is
// the no-op, with no signal sent by any of them.
func TestAdviceFollow_E39_PauseClaimStandsInspectKill(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, e *kpEnv)
		ended string
	}{
		{"intent with no outcome", func(t *testing.T, e *kpEnv) {
			e.intentNow(t, e.claimNow(t, 15*time.Second))
			e.advance(time.Minute)
		}, "ended launch_end r3 intent"},
		{"exit_uncertain recorded", func(t *testing.T, e *kpEnv) {
			e.recordNow(t, e.intentNow(t, e.claimNow(t, 15*time.Second)), store.BusySendPauseExitUncertain, "exit_uncertain")
		}, "ended launch_end exit_uncertain:exit_uncertain r4 intent"},
		{"failure recorded with no cause", func(t *testing.T, e *kpEnv) {
			e.recordNow(t, e.intentNow(t, e.claimNow(t, 15*time.Second)), store.BusySendPauseFailed, "")
		}, "ended launch_end failed r4 intent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			tc.setup(t, e)
			held := e.held(t)
			e.answer = func(kpSignal) error { t.Error("a pause the claim answered signalled"); return nil }
			answers := make([]string, 2)
			for i, only := range []bool{false, true} {
				err := e.pauseOpt(context.Background(), nil, kpBudget, only)
				adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "inspect with get before more input")
				adviceAssertPhrase(t, err, pcClaimStands)
				answers[i] = errText(err)
			}

			row, err := e.c.Get(e.r.ID)
			if err != nil || !store.IsLiveState(row.State) || row.Delivery == nil || row.Delivery.HeldCount != int64(len(held)) {
				t.Fatalf("get = %+v, %v; want the row live with %d Held", row, err, len(held))
			}
			e.assertKept(t, held)
			if _, err := e.c.Kill(api.KillParams{InstanceID: e.r.ID}); err == nil {
				t.Fatal("kill of an agent that does not exit succeeded; want it refused")
			}
			for i, only := range []bool{false, true} {
				if got := errText(e.pauseOpt(context.Background(), nil, kpBudget, only)); got != answers[i] {
					t.Errorf("pause after a kill with no exit evidence = %q; want unchanged %q", got, answers[i])
				}
			}

			e.pc.Set(e.r.AgentPID, procfix.Gone())
			if _, err := e.c.Kill(api.KillParams{InstanceID: e.r.ID}); err != nil {
				t.Fatalf("kill once the agent exited: %v", err)
			}
			e.assertClaims(t, tc.ended)
			for _, only := range []bool{false, true} {
				pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, only), "")
			}
			e.assertSignals(t, 0)
		})
	}
}

// TestAdviceFollow_E40_PauseBudgetSignalUnknownInspectPause: E40 "inspect
// with get before more input or another pause" after a plain signal that
// reached the agent but whose result the store did not return before the
// budget ran out: budget_exhausted with signal_sent omitted, never false, and
// nothing recorded. get reads the row, and another
// pause finds the agent gone and succeeds with no second signal. The store
// method's facts are injected at the pause claim seam and the budget crossed
// on the injected clock; the store's own unconfirmed return is
// internal/store's TestBusySendPausePlainSignalFacts.
func TestAdviceFollow_E40_PauseBudgetSignalUnknownInspectPause(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	e := newKeylessEnv(t, killRowSpec{})
	held := e.held(t)
	st := &pcStore{Store: e.st, plain: func(_ int, signal func() error, _ func(func() error) (store.BusySendPlainPauseSignalResult, error)) (store.BusySendPlainPauseSignalResult, error) {
		// The signal reaches the agent, then the budget runs out before the
		// store returns its result: invoked, not completed.
		_ = signal()
		e.advance(time.Hour)
		return store.BusySendPlainPauseSignalResult{Invoked: true}, pcInjected
	}}

	err := e.pause(context.Background(), st, kpBudget)

	adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "so whether the agent got it is not known; inspect with get before more input or another pause")
	if got := jsonOf(t, api.ErrDetails(err)); got != `{"cause":"budget_exhausted"}` {
		t.Errorf("err_details = %s; want budget_exhausted with signal_sent omitted", got)
	}
	if cause, _ := e.launchEnd(t); cause != "" {
		t.Errorf("Launch End = %q; want none recorded", cause)
	}
	e.assertClaims(t)
	e.assertKept(t, held)

	if _, err := e.c.Get(e.r.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := e.pause(context.Background(), nil, kpBudget); err != nil {
		t.Fatalf("another pause: %v; want success", err)
	}
	e.assertSignals(t, 1)
	// get's own check may already have found the agent gone (missing).
	if got, err := e.c.Get(e.r.ID); err != nil || (got.State != store.StateEnded && got.State != store.StateMissing) {
		t.Errorf("get after another pause = %+v, %v; want the row ended or missing", got, err)
	}
}

// TestAdviceFollow_E41_PauseEndedLaunchAgentRunsRetryOrKill: E41 "inspect
// with get, and retry pause once that process has exited, or kill" after a
// conditional pause of a live row whose Launch has ended (its End recorded,
// no claim) while its agent still runs, so pause sends it no signal: get
// reads the row; a retry while it runs gets the same refusal, nothing signalled; once
// the agent has exited the retry ends the row, or kill ends the agent and the
// next pause ends the row. No signal in either.
func TestAdviceFollow_E41_PauseEndedLaunchAgentRunsRetryOrKill(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	for _, follow := range []string{"retry once exited", "kill"} {
		t.Run(follow, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			e.step = kpStep // each refusal waits its budget out
			if ended, err := e.st.EndBusySendLaunchWithin(context.Background(), e.r.ID, e.r.Token, time.Minute); err != nil || !ended {
				t.Fatalf("Launch End = %t, %v; want recorded", ended, err)
			}
			err := e.pauseOpt(context.Background(), nil, kpBudget, true)
			adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "inspect with get, and retry pause once that process has exited, or kill")

			if row, err := e.c.Get(e.r.ID); err != nil || !store.IsLiveState(row.State) {
				t.Fatalf("get = %+v, %v; want the row live", row, err)
			}
			if again := e.pauseOpt(context.Background(), nil, kpBudget, true); errText(again) != err.Error() {
				t.Errorf("retry while the agent runs = %v; want the same refusal %q", again, err)
			}
			if follow == "kill" {
				e.setAfterCall(tmux.CallKillPane, procfix.Gone(), e.r.AgentPID)
				if res, err := e.c.Kill(api.KillParams{InstanceID: e.r.ID}); err != nil || !res.KillSent {
					t.Fatalf("kill = %+v, %v; want the agent's pane killed", res, err)
				}
			} else {
				e.pc.Set(e.r.AgentPID, procfix.Gone())
			}
			pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, true), "")
			e.assertSignals(t, 0)
			e.assertClaims(t)
			if row, err := e.c.Get(e.r.ID); err != nil || row.State != store.StateEnded {
				t.Errorf("get after the pause = %+v, %v; want the row ended", row, err)
			}
		})
	}
}
