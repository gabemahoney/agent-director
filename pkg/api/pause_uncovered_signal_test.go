package api_test

// b.y7g A1: busy uncovered Codex pause uses an identity-checked SIGTERM;
// waiting retains /exit and the relay guard. No SessionEnd is needed.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Reuse kpEnv's signal assertions, pause calls and virtual budget, seeding
// an uncovered Launch without newKeylessEnv's covered-only Held Messages.
func newUncoveredPauseEnv(t *testing.T, spec killRowSpec) *kpEnv {
	t.Helper()
	e := &kpEnv{killEnv: newKillEnv(t)}
	e.rowAgent = store.AgentCodex
	if spec.State == "" {
		spec.State = store.StateWorking
	}
	spec.SessionID = uuid.NewString()
	e.r = e.seedRow(t, spec)
	if err := apitest.SeedBusySendCoverage(e.dbPath, e.r.ID, store.BusySendNotCovered, store.BusySendNotCovered); err != nil {
		t.Fatal(err)
	}
	e.c, _ = e.client(t)
	api.SetSelfPIDNSForTest(e.c, func() (string, bool) { return relayNS, true })
	e.step = kpStep
	e.installPauseHooks(t)
	return e
}

// seedUncoveredPauseRelay keeps a real open request and a live relay hook,
// so bypassing the relay guard cannot pass merely because no hold exists.
func seedUncoveredPauseRelay(t *testing.T, e *kpEnv) string {
	t.Helper()
	token := uuid.NewString()
	e.pc.Set(relayEnvHook.PID, procfix.Alive(relayEnvHook.Starttime))
	req := store.RelayRequest{
		RequestToken: token, ToolName: "Bash", ToolInput: `{}`, Hook: relayEnvHook,
		SettledAt: e.clock.Now().Add(time.Hour),
	}
	if e.r.Spawn.State == store.StateWaiting {
		// A child request survives the main turn's Stop, leaving waiting held.
		req.AgentID = paSubagentID
	}
	storefix.SeedRelayRequest(t, e.st, e.r.ID, req)
	if e.r.Spawn.State == store.StateWaiting {
		if a := apitest.ApplyAgentHook(t, e.dbPath, e.r.ID, "Stop", e.r.Spawn.SessionID, apitest.HookAgent(store.AgentCodex)); !a.Applied {
			t.Fatalf("main turn Stop: %s", a.Reason)
		}
		pauAssertState(t, e.killEnv, e.r.ID, store.StateWaiting)
	}
	return token
}

func assertUncoveredPauseNoInput(t *testing.T, e *kpEnv) {
	t.Helper()
	e.assertNoTmuxCall(t)
	if n := storefix.ReadBusySendCounts(t, e.dbPath).InputAttempts; n != 0 {
		t.Errorf("input journal attempts = %d; want none", n)
	}
}

// TestPauseUncoveredCodexBusy (b.y7g) also covers Client.Pause dispatch and
// the end writer closing an open request without a Codex end report.
func TestPauseUncoveredCodexBusy(t *testing.T) {
	// Serial: replaces the process-wide pause signal, clock and poll hooks.
	for _, tc := range []struct {
		state  string
		relay  bool
		client bool
	}{
		{store.StateWorking, false, false},
		{store.StateCheckPermission, true, true},
		{store.StateAskUser, false, false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			e := newUncoveredPauseEnv(t, killRowSpec{State: tc.state, RelayOn: tc.relay,
				Opts: []apitest.SpawnOption{apitest.WithPID(apitest.TestPanePID + 100)}})
			var token string
			if tc.relay {
				token = seedUncoveredPauseRelay(t, e)
			}
			var err error
			if tc.client {
				_, err = e.c.Pause(context.Background(), api.PauseParams{InstanceID: e.r.ID})
			} else {
				err = e.pause(context.Background(), nil, kpBudget)
			}
			if err != nil {
				t.Errorf("Pause = %v; want verified SIGTERM shutdown", err)
			}
			e.assertSignals(t, 1)
			// Check the stored row before a read surface can recover it.
			e.assertEnded(t)
			row, readErr := e.st.GetSpawn(e.r.ID)
			if readErr != nil || row.EndReportedAtMillis != 0 {
				t.Errorf("end report = %d, %v; want none", row.EndReportedAtMillis, readErr)
			}
			if token != "" {
				pr, readErr := e.st.GetPermissionRequest(e.r.ID, token)
				if readErr != nil || pr.Decision != "deny" || pr.DecisionReason != store.DecisionReasonEnded || pr.ProvenGoneAt.IsZero() {
					t.Errorf("request after pause = %+v, %v; want denied ended and proven gone", pr, readErr)
				}
			}
			assertUncoveredPauseNoInput(t, e)
		})
	}
}

// TestPauseUncoveredCodexRefusals (b.y7g): every refusal has the keyless
// details type and exact signal fact, never the keys path's ErrPauseTimeout.
func TestPauseUncoveredCodexRefusals(t *testing.T) {
	// Serial: replaces the process-wide pause signal, clock and poll hooks.
	for _, tc := range []struct {
		name    string
		agent   agentState
		answer  func(kpSignal) error
		want    string
		signals int
	}{
		{name: "unreadable identity", agent: agentUnreadable,
			want: `{"cause":"agent_process_unverifiable","signal_sent":false}`},
		{name: "missing identity", agent: agentNotRecorded,
			want: `{"cause":"agent_process_unverifiable","signal_sent":false}`},
		{name: "unverifiable at signal", answer: func(kpSignal) error { return probe.ErrSignalTargetUnverifiable }, signals: 1,
			want: `{"cause":"agent_process_unverifiable","signal_sent":false}`},
		{name: "unsupported signal", answer: func(kpSignal) error { return probe.ErrProbeUnsupported }, signals: 1,
			want: `{"cause":"signal_unsupported","signal_sent":false}`},
		{name: "signal fails", answer: func(kpSignal) error { return errors.New("operation not permitted") }, signals: 1,
			want: `{"cause":"signal_uncertain"}`},
		{name: "alive at deadline", answer: func(kpSignal) error { return nil }, signals: 1,
			want: `{"cause":"exit_uncertain","signal_sent":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newUncoveredPauseEnv(t, killRowSpec{Agent: tc.agent})
			e.answer = tc.answer
			before := e.columns(t, e.r.ID)

			err := e.pause(context.Background(), nil, kpBudget)

			assertOneName(t, err, "ErrSpawnNotPausable")
			if _, ok := api.ErrDetails(err).(api.NotPausableDetails); !ok {
				t.Errorf("err_details type = %T; want NotPausableDetails", api.ErrDetails(err))
			}
			if got := jsonOf(t, api.ErrDetails(err)); got != tc.want {
				t.Errorf("err_details = %s; want %s", got, tc.want)
			}
			e.assertSignals(t, tc.signals)
			assertUncoveredPauseNoInput(t, e)
			e.assertRowUnchanged(t, e.r.ID, before)
		})
	}
}

// TestPauseUncoveredCodexChangedBeforeSignal (b.y7g): revalidate both the
// Launch token and the selected agent identity after capturing the row.
func TestPauseUncoveredCodexChangedBeforeSignal(t *testing.T) {
	// Serial: replaces the process-wide pause signal, clock and poll hooks.
	for _, change := range []string{"launch", "identity"} {
		t.Run(change, func(t *testing.T) {
			// With no SessionStart pid, the recorded pane is the selected agent.
			e := newUncoveredPauseEnv(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithPID(0), apitest.WithProcStarttime("")}})
			changed := false
			var afterChange apitest.SpawnColumns
			st := &kpHookedStore{Store: e.st, once: func() {
				changed = true
				if change == "launch" {
					pcSetToken(t, e.dbPath, e.r.ID, newToken())
				} else {
					row, err := e.st.GetSpawn(e.r.ID)
					if err != nil {
						t.Fatal(err)
					}
					identity := row.Identity
					identity.PanePID++
					e.pc.Set(identity.PanePID, procfix.Alive(identity.PaneStarttime))
					if res, _, err := e.st.AdoptIdentityIfSameLife(e.r.ID, row.Snapshot, identity); err != nil || res != store.CondApplied {
						t.Fatalf("identity change = %v, %v; want applied", res, err)
					}
				}
				afterChange = e.columns(t, e.r.ID)
			}}

			err := e.pause(context.Background(), st, kpBudget)

			assertOneName(t, err, "ErrSpawnNotPausable")
			if got := jsonOf(t, api.ErrDetails(err)); got != `{"cause":"launch_changed","signal_sent":false}` {
				t.Errorf("err_details = %s; want launch_changed with nothing signalled", got)
			}
			if !changed {
				t.Error("pause never revalidated the captured row")
			} else {
				e.assertRowUnchanged(t, e.r.ID, afterChange)
			}
			e.assertSignals(t, 0)
			assertUncoveredPauseNoInput(t, e)
		})
	}
}

// TestPauseUncoveredCodexConcurrentMissing (b.y7g; b.nqq parity): a same-Launch
// process-based recovery mark after the signal must not prevent deliberate ended.
func TestPauseUncoveredCodexConcurrentMissing(t *testing.T) {
	// Serial: replaces process-wide pause hooks and clock, and reads the shared trail.
	e := newUncoveredPauseEnv(t, killRowSpec{})
	e.answer = func(kpSignal) error { return nil }
	marked := false
	var mark int
	e.atSleep = func() {
		e.pc.Set(e.r.AgentPID, procfix.Gone())
		if _, err := e.c.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		row, err := e.st.GetSpawn(e.r.ID)
		if err != nil || row.State != store.StateMissing {
			t.Fatalf("sweep before pause completion: state=%s, err=%v; want missing", row.State, err)
		}
		marked = true
		mark = trailMark(t)
	}

	if err := e.pause(context.Background(), nil, kpBudget); err != nil {
		t.Errorf("Pause = %v; want success after concurrent missing", err)
	}
	if !marked {
		t.Error("pause never waited for the signalled agent to exit")
	}
	e.assertEnded(t)
	e.assertSignals(t, 1)
	assertUncoveredPauseNoInput(t, e)
	if marked {
		assertMoveRecords(t, mark, e.r.ID, moveWant{source: "ad_recovery", prior: store.StateMissing, now: store.StateEnded,
			reason: "proc_absent", denyReason: store.DecisionReasonEnded})
	}
}

// TestPauseUncoveredCodexGuards (b.y7g): A1 leaves the existing waiting,
// pending, finished, conditional and other-provider rules intact.
func TestPauseUncoveredCodexGuards(t *testing.T) {
	// Serial: replaces the process-wide pause signal, clock and poll hooks.
	for _, tc := range []struct {
		name, agent, state string
		relay, conditional bool
		want               string
	}{
		{name: "waiting types exit", state: store.StateWaiting},
		{name: "waiting held by relay", state: store.StateWaiting, relay: true, want: "ErrSendKeysWhileRelayed"},
		{name: "pending", state: store.StatePending, want: "ErrSpawnNotPausable"},
		{name: "ended", state: store.StateEnded},
		{name: "missing", state: store.StateMissing},
		{name: "only if waiting", state: store.StateWorking, conditional: true, want: "ErrSpawnNotPausable"},
		{name: "uncovered Claude", agent: store.AgentClaude, state: store.StateWorking, want: "ErrSpawnNotPausable"},
		{name: "uncovered Cursor", agent: store.AgentCursor, state: store.StateWorking, want: "ErrSpawnNotPausable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := killRowSpec{State: tc.state, RelayOn: tc.relay}
			if tc.agent != "" {
				spec.Opts = []apitest.SpawnOption{apitest.WithAgent(tc.agent)}
			}
			e := newUncoveredPauseEnv(t, spec)
			if tc.relay {
				seedUncoveredPauseRelay(t, e)
			}
			before := e.columns(t, e.r.ID)
			typesExit := tc.state == store.StateWaiting && !tc.relay
			if typesExit {
				e.codexExitsAfterEnter(t, e.r, nil)
			}

			var err error
			if tc.relay {
				_, err = e.c.Pause(context.Background(), api.PauseParams{InstanceID: e.r.ID})
			} else {
				err = e.pauseOpt(context.Background(), nil, kpBudget, tc.conditional)
			}

			if tc.want != "" {
				assertOneName(t, err, tc.want)
			} else if err != nil {
				t.Errorf("Pause = %v; want success", err)
			}
			if tc.conditional {
				if got := jsonOf(t, api.ErrDetails(err)); got != `{"cause":"agent_not_covered"}` {
					t.Errorf("err_details = %s; want agent_not_covered", got)
				}
			}
			e.assertSignals(t, 0)
			if typesExit {
				e.assertExitDeliveredTimes(t, e.r.Socket, e.r.Spawn.Identity.PaneID, codexClearPresses)
				pauAssertState(t, e.killEnv, e.r.ID, store.StateEnded)
			} else {
				assertUncoveredPauseNoInput(t, e)
				e.assertRowUnchanged(t, e.r.ID, before)
			}
		})
	}
}
