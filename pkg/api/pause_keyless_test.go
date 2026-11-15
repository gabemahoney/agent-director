package api_test

// pause_keyless_test.go: keyless pause of a Covered Agent (SR-6.4; Task
// t2.xis.oo.sf): an identity-checked SIGTERM to the recorded agent process,
// never keys, with the agent's exit and its Launch's exclusion established
// within the pause budget, else ErrSpawnNotPausable with NotPausableDetails;
// Held Messages stay Held. The shared exit judgement's causes are the
// lifecycle tests'; uncovered pause is pause_test.go's. Every test here
// replaces keyless pause's process-wide signal hook, so all are serial.

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kpSignal is one identity-checked SIGTERM keyless pause asked for.
type kpSignal struct {
	pid   int
	start string
}

// kpEnv is a covered row r (a session id, a route with a wake file) on a
// killEnv, holding two Messages admitted through c, the first confirmed
// (first is its Result). Keyless pause's signals are recorded in sent and
// answered by answer (nil: sent, and the agent exits); atSleep runs at the
// pause's first poll sleep. Keyless pause's clock (api.SetPauseNowForTest) is
// real time plus skew: each poll sleep adds its 100 ms to skew instead of
// sleeping, and advance adds more, so a deadline is crossed without waiting.
type kpEnv struct {
	*killEnv
	r       killRow
	c       *api.Client
	first   api.DeliveryResult
	woken   func() bool
	sent    []kpSignal
	answer  func(kpSignal) error
	atSleep func()
	skew    atomic.Int64
}

// advance moves keyless pause's clock d ahead.
func (e *kpEnv) advance(d time.Duration) { e.skew.Add(int64(d)) }

func newKeylessEnv(t *testing.T, spec killRowSpec) *kpEnv {
	t.Helper()
	e := &kpEnv{killEnv: newKillEnv(t)}
	spec.SessionID = uuid.NewString()
	e.r = e.seedRow(t, spec)
	seedBusyCovered(t, e.killEnv, e.r.ID)
	_, e.woken = busyWake(t, e.killEnv, e.r.ID)
	e.c, _ = e.client(t)
	e.first = busyAdmit(t, e.c, api.SendKeysParams{InstanceID: e.r.ID, Text: "first\n", ConfirmedDelivery: ptr(true),
		CallerUUID: ptr(busyCaller), DeliveryRequestID: "kp-1"})
	busyAdmit(t, e.c, api.SendKeysParams{InstanceID: e.r.ID, Text: "  second ✓"})
	e.woken()
	t.Cleanup(api.SetPauseTerminateForTest(func(pid int, start string) error {
		s := kpSignal{pid, start}
		e.sent = append(e.sent, s)
		if e.answer != nil {
			return e.answer(s)
		}
		e.pc.Set(pid, procfix.Gone())
		return nil
	}))
	t.Cleanup(api.SetPauseNowForTest(func() time.Time { return time.Now().Add(time.Duration(e.skew.Load())) }))
	const poll = 100 * time.Millisecond
	pauSleeps(t, poll, false, func(n int) {
		e.advance(poll)
		if n == 1 && e.atSleep != nil {
			e.atSleep()
		}
	})
	return e
}

// pause runs api.Pause on r with ctx and timeoutSeconds over st (e.st when
// nil), e's process fake and clock. Every keyless store read and write
// shares the pause budget (repair sf2o-2 CR2), so a zero budget would refuse
// before the first of them: 0 asks for the smallest budget, 1 s, as
// production's pause.timeout_seconds never gives less.
func (e *kpEnv) pause(ctx context.Context, st api.PauseStore, timeoutSeconds int) error {
	if st == nil {
		st = e.st
	}
	timeoutSeconds = max(timeoutSeconds, 1)
	_, err := api.Pause(ctx, st, e.rec, api.PauseEnv{Relay: api.RelayView{Procs: e.pc, Now: e.clock.Now, Window: sendKeysWindow()}},
		timeoutSeconds, api.PauseParams{InstanceID: e.r.ID})
	return err
}

// held is r's Held Messages as stored.
func (e *kpEnv) held(t *testing.T) []apitest.BusyMessageFact {
	t.Helper()
	var out []apitest.BusyMessageFact
	for _, m := range busyFacts(t, e.dbPath, e.r.ID).Messages {
		if m.Place == "held" {
			out = append(out, m)
		}
	}
	return out
}

// assertKept fails unless every Message Held before pause is Held, unchanged
// (bytes, order, options), and pause made no tmux call (no keys, /exit or
// pane read), no input journal attempt and no wake.
func (e *kpEnv) assertKept(t *testing.T, before []apitest.BusyMessageFact) {
	t.Helper()
	after := e.held(t)
	if len(before) == 0 {
		t.Fatal("no Held Message before pause")
	}
	for _, m := range before {
		if i := slices.IndexFunc(after, func(a apitest.BusyMessageFact) bool { return a.ID == m.ID }); i < 0 || !reflect.DeepEqual(after[i], m) {
			t.Errorf("Held Message %+v after pause; want it Held unchanged among %+v", m, after)
		}
	}
	if n := storefix.ReadBusySendCounts(t, e.dbPath).InputAttempts; n != 0 {
		t.Errorf("%d input journal attempts; want none", n)
	}
	if e.woken() {
		t.Error("pause touched the route's wake file")
	}
	e.assertNoTmuxCall(t)
}

// assertSignals fails unless pause asked to signal exactly the row's agent
// identity n times (0 or 1).
func (e *kpEnv) assertSignals(t *testing.T, n int) {
	t.Helper()
	var want []kpSignal
	if n == 1 {
		want = []kpSignal{{e.r.AgentPID, e.r.AgentStart}}
	}
	if !reflect.DeepEqual(e.sent, want) {
		t.Errorf("signals = %+v; want %+v", e.sent, want)
	}
}

// assertEnded checks b.nqq's postcondition before any read can reconcile it.
func (e *kpEnv) assertEnded(t *testing.T, sessionID ...string) {
	t.Helper()
	wantSession := e.r.Spawn.SessionID
	if len(sessionID) == 1 {
		wantSession = sessionID[0]
	}
	row, err := e.st.GetSpawn(e.r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != store.StateEnded || row.SessionID != wantSession || row.EndedAt == nil {
		t.Errorf("row after pause: state=%s session=%s ended_at=%v; want ended with session %s and end time", row.State, row.SessionID, row.EndedAt, wantSession)
	}
	if value, evidence := row.ProcessConfirmation(); value != store.ProcessExited || evidence.Source != store.EvidenceProcess {
		t.Errorf("process after pause = %s, %+v; want confirmed exited from process", value, evidence)
	}
	got, err := e.c.Get(e.r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateEnded || got.SessionID != wantSession {
		t.Errorf("get after pause: state=%s session=%s; want ended with session %s", got.State, got.SessionID, wantSession)
	}
	if c := got.Confirmations.State; c.Status != store.ConfirmationConfirmed || c.Source == nil || *c.Source != store.EvidenceProcess {
		t.Errorf("state confirmation = %+v; want confirmed from process", c)
	}
}

// launchEnd is the current Launch's recorded end cause ("" for none) and
// whether it still has an open hand-over: its intent-bearing custody fenced.
func (e *kpEnv) launchEnd(t *testing.T) (cause string, open bool) {
	t.Helper()
	scope, err := e.st.ReadBusySendExitScope(e.r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range scope.Launches {
		open = open || (l.Current && len(l.Routes) > 0)
	}
	return currentLaunch(t, e.dbPath, e.r.ID).EndCause.String, open
}

// kpHelper is the issuing hook helper keyless pause must see exit.
var kpHelper = store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: 7001, StartTime: "4242"}

// issueHelper claims r's Held Messages at turn end and issues the first
// prompt as helper, alive in the fake, then admits one more Message, which
// stays Held: an owned issuer that may still emit for the Launch.
func (e *kpEnv) issueHelper(t *testing.T, helper store.BusySendIssuer) {
	t.Helper()
	e.busyIssue(t, e.r, &helper)
	e.pc.Set(helper.PID, procfix.Alive(helper.StartTime))
	busyAdmit(t, e.c, api.SendKeysParams{InstanceID: e.r.ID, Text: "third"})
	e.woken()
}

// TestPauseKeylessAnyLiveState (SR-6.4): through Client.Pause, a Covered
// Agent busy, waiting, or held by an open Dialog with relay on is paused by
// one SIGTERM to its recorded identity, never keys, and its Launch End is
// recorded; a finished row is the no-op success, signalling nothing and
// inventing no Launch End. Held Messages and the confirmed pair's Held Result
// are kept.
func TestPauseKeylessAnyLiveState(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	for _, tc := range []struct {
		name, state string
		dialog      bool
	}{
		{"waiting", store.StateWaiting, false},
		{"working", store.StateWorking, false},
		{"open Dialog, relay on", store.StateCheckPermission, true},
		{"ended", store.StateEnded, false},
		{"missing", store.StateMissing, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{State: tc.state, RelayOn: tc.dialog})
			if tc.dialog {
				storefix.SeedOpenPermissionRequests(t, e.st, e.r.ID, []string{"kp-dialog"})
			}
			live := store.IsLiveState(tc.state)
			held, launches := e.held(t), busyFacts(t, e.dbPath, e.r.ID).Launches

			if _, err := e.c.Pause(context.Background(), api.PauseParams{InstanceID: e.r.ID}); err != nil {
				t.Fatalf("Pause: %v; want success", err)
			}
			if live {
				// b.nqq: verified keyless shutdown must publish its deliberate end.
				e.assertEnded(t)
			}

			e.assertKept(t, held)
			got, err := e.c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "kp-1"})
			if err != nil || got.Result == nil || got.Result.Class != "held" || got.Result.RetryWith != "none" ||
				got.Result.InstanceID != e.r.ID || got.Result.RequestID != e.first.RequestID {
				t.Errorf("GetDelivery after pause = %+v, %v; want the pair still Held", got, err)
			}
			if !live {
				e.assertSignals(t, 0)
				if after := busyFacts(t, e.dbPath, e.r.ID).Launches; !reflect.DeepEqual(after, launches) {
					t.Errorf("Launches after a finished row's pause = %+v; want unchanged %+v", after, launches)
				}
				return
			}
			e.assertSignals(t, 1)
			if cause, open := e.launchEnd(t); cause != "process_exited" || open {
				t.Errorf("Launch End = %q (open hand-over %t); want process_exited, exited", cause, open)
			}
		})
	}
}

// TestPauseKeylessEndsRowAfterSweepMissing (b.nqq): recovery can observe the
// agent's exit before pause records its deliberate end. That same-Launch
// process-based missing mark must not prevent pause from leaving ended.
func TestPauseKeylessEndsRowAfterSweepMissing(t *testing.T) {
	e := newKeylessEnv(t, killRowSpec{})
	held := e.held(t)
	var mark int
	e.answer = func(s kpSignal) error {
		e.pc.Set(s.pid, procfix.Gone())
		if _, err := e.c.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		row, err := e.st.GetSpawn(e.r.ID)
		if err != nil || row.State != store.StateMissing {
			t.Fatalf("sweep before pause completion: state=%s, err=%v; want missing", row.State, err)
		}
		mark = trailMark(t)
		return nil
	}
	if err := e.pause(context.Background(), nil, 1); err != nil {
		t.Fatalf("Pause: %v; want success after concurrent sweep", err)
	}
	e.assertEnded(t)
	e.assertSignals(t, 1)
	e.assertKept(t, held)
	assertMoveRecords(t, mark, e.r.ID, moveWant{source: "ad_recovery", prior: store.StateMissing, now: store.StateEnded,
		reason: "proc_absent", denyReason: store.DecisionReasonEnded})
}

// TestPauseKeylessOutcomes (SR-6.4): the signal goes only to the recorded
// identity, at most once, and pause succeeds only once the agent's exit and
// every owned issuer's exit are established within the budget. A reused pid
// or a target already gone is not signalled; an unknown identity, an
// unverifiable or unsupported signal, a failed signal, an agent still running
// or an owned helper still running or uncheckable at the deadline, and a
// cancelled context, each refuse with their structured cause and whether the
// signal was sent, never inventing exit: a Launch End is recorded only after
// the agent's verified exit, and a surviving helper keeps its hand-over
// fenced. Held Messages stay Held in every case.
func TestPauseKeylessOutcomes(t *testing.T) {
	// Serial: it replaces keyless pause's signal, the exit judgement's pid namespace and the pause knobs (process-wide).
	foreignNS := store.BusySendIssuer{Role: kpHelper.Role, PID: kpHelper.PID, StartTime: kpHelper.StartTime, PIDNamespace: "pid:[4026531836]"}
	keepsRunning := func(kpSignal) error { return nil }
	answer := func(err error) func(kpSignal) error { return func(kpSignal) error { return err } }
	cases := []struct {
		name    string
		spec    killRowSpec
		answer  func(kpSignal) error
		helper  *store.BusySendIssuer
		timeout int
		// atSleep, given e and the call's cancel, runs at the first poll sleep.
		atSleep func(e *kpEnv, cancel context.CancelFunc)
		setup   func(e *kpEnv)
		want    string // the err_details JSON; "" success; "canceled" context.Canceled
		signals int
		end     string // the current Launch's end cause after pause
		open    bool   // a hand-over still open (custody fenced) after pause
	}{
		{name: "pid reused by another process", setup: func(e *kpEnv) { e.pc.Set(e.r.AgentPID, procfix.Alive("999999")) },
			end: "process_exited"},
		{name: "agent gone at the signal", setup: func(e *kpEnv) {
			e.answer = func(s kpSignal) error { e.pc.Set(s.pid, procfix.Gone()); return probe.ErrSignalTargetGone }
		}, signals: 1, end: "process_exited"},
		{name: "start time unreadable", spec: killRowSpec{Agent: agentUnreadable},
			want: `{"cause":"agent_process_unverifiable","signal_sent":false}`},
		{name: "no agent identity recorded", spec: killRowSpec{Agent: agentNotRecorded},
			want: `{"cause":"agent_process_unverifiable","signal_sent":false}`},
		{name: "unverifiable at the signal", answer: answer(probe.ErrSignalTargetUnverifiable), signals: 1,
			want: `{"cause":"agent_process_unverifiable","signal_sent":false}`},
		{name: "signalling unsupported", answer: answer(probe.ErrProbeUnsupported), signals: 1,
			want: `{"cause":"signal_unsupported","signal_sent":false}`},
		{name: "signal call failed", answer: answer(errors.New("operation not permitted")), signals: 1,
			want: `{"cause":"signal_uncertain"}`},
		{name: "agent ignores SIGTERM", answer: keepsRunning, signals: 1,
			want: `{"cause":"exit_uncertain","signal_sent":true}`},
		{name: "helper still runs at the deadline", helper: &kpHelper, signals: 1, end: "process_exited", open: true,
			want: `{"cause":"owned_process_running","signal_sent":true}`},
		{name: "helper exits within the budget", helper: &kpHelper, timeout: 1, signals: 1, end: "process_exited",
			atSleep: func(e *kpEnv, _ context.CancelFunc) { e.pc.Set(kpHelper.PID, procfix.Gone()) }},
		{name: "helper in another pid namespace", helper: &foreignNS, signals: 1, end: "process_exited", open: true,
			want: `{"cause":"owned_process_unverifiable","signal_sent":true}`},
		{name: "cancelled waiting for the agent", answer: keepsRunning, timeout: 1, signals: 1, want: "canceled",
			atSleep: func(_ *kpEnv, cancel context.CancelFunc) { cancel() }},
		{name: "cancelled waiting for a helper", helper: &kpHelper, timeout: 1, signals: 1, end: "process_exited", open: true,
			want: "canceled", atSleep: func(_ *kpEnv, cancel context.CancelFunc) { cancel() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, tc.spec)
			t.Cleanup(api.SetBusySendExitNamespaceForTest(func() (string, bool) { return "pid:[4026531999]", true }))
			if tc.helper != nil {
				e.issueHelper(t, *tc.helper)
			}
			e.answer = tc.answer
			if tc.setup != nil {
				tc.setup(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.atSleep != nil {
				e.atSleep = func() { tc.atSleep(e, cancel) }
			}
			held := e.held(t)

			err := e.pause(ctx, nil, tc.timeout)

			switch tc.want {
			case "":
				if err != nil {
					t.Fatalf("Pause: %v; want success", err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) || api.ErrDetails(err) != nil {
					t.Fatalf("Pause = %v (details %+v); want the bare context error", err, api.ErrDetails(err))
				}
			default:
				assertOneName(t, err, "ErrSpawnNotPausable")
				if got := jsonOf(t, api.ErrDetails(err)); got != tc.want {
					t.Errorf("err_details = %s; want %s", got, tc.want)
				}
			}
			e.assertSignals(t, tc.signals)
			e.assertKept(t, held)
			if cause, open := e.launchEnd(t); cause != tc.end || open != tc.open {
				t.Errorf("Launch End = %q, open hand-over %t; want %q, %t", cause, open, tc.end, tc.open)
			}
		})
	}
}

// kpHookedStore is a store that runs first just before its first row read
// (pause's bounded first read) and once just before its second: keyless
// pause's read again just before the signal, or the first read's fallback.
type kpHookedStore struct {
	*store.Store
	reads       int
	first, once func()
}

// kpExitHookStore schedules a race after physical exit evidence commits,
// outside the store connection, before pause completes its row transition.
type kpExitHookStore struct {
	*store.Store
	afterExit func()
	afterEnd  func()
}

func (s *kpExitHookStore) EstablishBusySendExitWithin(ctx context.Context, ev store.BusySendExitEvidence, wait time.Duration) (bool, error) {
	applied, err := s.Store.EstablishBusySendExitWithin(ctx, ev, wait)
	if err == nil && applied && s.afterExit != nil {
		f := s.afterExit
		s.afterExit = nil
		f()
	}
	return applied, err
}

func (s *kpExitHookStore) MarkKeylessPauseEndedWithin(ctx context.Context, row api.Spawn, launch store.BusySendPublishedLaunch, exit store.ProcessExit, wait time.Duration) (string, store.CondResult, error) {
	prior, res, err := s.Store.MarkKeylessPauseEndedWithin(ctx, row, launch, exit, wait)
	if err == nil && res == store.CondApplied && s.afterEnd != nil {
		s.afterEnd()
	}
	return prior, res, err
}

// read counts a row read and runs its hook.
func (s *kpHookedStore) read() {
	s.reads++
	switch {
	case s.reads == 1 && s.first != nil:
		s.first()
	case s.reads == 2 && s.once != nil:
		s.once()
	}
}

func (s *kpHookedStore) GetSpawn(id string) (api.Spawn, error) {
	s.read()
	return s.Store.GetSpawn(id)
}

// GetSpawnContext is keyless pause's bounded read again before the signal
// (repair sf2o-2 CR2), counted with GetSpawn.
func (s *kpHookedStore) GetSpawnContext(ctx context.Context, id string, wait time.Duration) (api.Spawn, error) {
	s.read()
	return s.Store.GetSpawnContext(ctx, id, wait)
}

// GetSpawnConnBounded is pause's first row read (repair sf2o-5 R4-1), counted
// with GetSpawn.
func (s *kpHookedStore) GetSpawnConnBounded(ctx context.Context, id string) (api.Spawn, bool, error) {
	s.read()
	return s.Store.GetSpawnConnBounded(ctx, id)
}

// TestPauseKeylessSessionStartDuringPause (b.nqq, code-review round 1):
// SessionStart may promote the captured pane process to the SessionStart
// columns and record a fresh session without replacing the Launch or agent.
func TestPauseKeylessSessionStartDuringPause(t *testing.T) {
	for _, when := range []string{"before signal", "after exit evidence"} {
		t.Run(when, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithNoPID()}})
			held := e.held(t)
			latestSession := uuid.NewString()
			start := func() {
				if a := storefix.ApplyAgentHook(t, e.st, e.r.ID, "SessionStart", latestSession); !a.Applied {
					t.Fatalf("same-Launch SessionStart not applied: %+v", a)
				}
				row, err := e.st.GetSpawn(e.r.ID)
				if err != nil || row.SessionID != latestSession || row.PID != e.r.AgentPID || row.ProcStarttime != e.r.AgentStart || row.Identity.Token != e.r.Token {
					t.Fatalf("SessionStart row = %+v, %v; want the same Launch and agent with fresh session metadata", row, err)
				}
			}
			var st api.PauseStore = &kpHookedStore{Store: e.st, once: start}
			if when == "after exit evidence" {
				st = &kpExitHookStore{Store: e.st, afterExit: start}
			}
			if err := e.pause(context.Background(), st, 1); err != nil {
				t.Fatalf("Pause after same-Launch SessionStart: %v; want ended with fresh session %s", err, latestSession)
			}
			e.assertEnded(t, latestSession)
			e.assertSignals(t, 1)
			e.assertKept(t, held)
		})
	}
}

// TestPauseKeylessAgentAdoptedDuringPause (b.nqq, round 1 sweep): recovery
// may fill the pane identity from the same recorded SessionStart process.
func TestPauseKeylessAgentAdoptedDuringPause(t *testing.T) {
	e := newKeylessEnv(t, killRowSpec{NoPane: true, NoSession: true, Opts: []apitest.SpawnOption{
		apitest.WithPID(apitest.TestPanePID), apitest.WithProcStarttime(apitest.LinuxProcStarttime),
	}})
	if e.r.AgentPID != e.r.Spawn.PID || e.r.AgentStart != e.r.Spawn.ProcStarttime {
		t.Fatal("adoption fixture must retain the recorded SessionStart process")
	}
	held := e.held(t)
	st := &kpExitHookStore{Store: e.st, afterExit: func() {
		row, err := e.st.GetSpawn(e.r.ID)
		if err != nil {
			t.Fatal(err)
		}
		identity := row.Identity
		identity.PaneID, identity.PanePID, identity.PaneStarttime = apitest.TestPaneID, e.r.AgentPID, e.r.AgentStart
		if res, _, err := e.st.AdoptIdentityIfSameLife(e.r.ID, row.Snapshot, identity); err != nil || res != store.CondApplied {
			t.Fatalf("same-agent adoption = %v, %v; want applied", res, err)
		}
	}}
	if err := e.pause(context.Background(), st, 1); err != nil {
		t.Fatalf("Pause after same-agent adoption: %v; want ended", err)
	}
	e.assertEnded(t)
	e.assertSignals(t, 1)
	e.assertKept(t, held)
}

// replace replaces r's Launch as a covered resume would: the agent exits and
// reports its end, resume moves the row on its exit evidence to a new launch
// token and publishes that Launch, covered, with no agent identity recorded
// (its create lost its reply) or, with pane, that pane's identity. It
// returns the new Launch.
func (e *kpEnv) replace(t *testing.T, pane ...store.LaunchIdentity) apitest.BusyLaunchFact {
	t.Helper()
	e.pc.Set(e.r.AgentPID, procfix.Gone())
	if a := apitest.ApplyAgentHook(t, e.dbPath, e.r.ID, "SessionEnd", e.r.Spawn.SessionID); !a.Applied {
		t.Fatalf("SessionEnd not applied: %s", a.Reason)
	}
	row, err := e.st.GetSpawn(e.r.ID)
	if err != nil {
		t.Fatal(err)
	}
	token := newToken()
	res, version, err := e.st.MoveToPendingWithExit(e.r.ID, row.Snapshot, e.clock.Now().UnixMilli(), token, e.r.Socket, "",
		store.LaunchOwner{}, store.BusySendDefaultsChoice{}, exitEvidenceFor(t, e.st, e.r.ID))
	if err != nil || res != store.CondApplied {
		t.Fatalf("resume move = %v, %v; want applied", res, err)
	}
	publish := func() (store.CondResult, error) { return e.st.PublishBusySendLaunch(e.r.ID, version, token) }
	if len(pane) == 1 {
		li := pane[0]
		li.Token = token
		publish = func() (store.CondResult, error) { return e.st.RecordLaunchIdentity(e.r.ID, version, token, li) }
	}
	if res, err := publish(); err != nil || res != store.CondApplied {
		t.Fatalf("publish = %v, %v; want applied", res, err)
	}
	if err := apitest.SeedBusySendCoverage(e.dbPath, e.r.ID, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	return currentLaunch(t, e.dbPath, e.r.ID)
}

// TestPauseKeylessLaunchReplaced (SR-6.4; M1 G4): a Launch replaced after
// pause captured it is never signalled or changed by that pause. Replaced
// before the signal, the row is read again and pause refuses launch_changed,
// signalling nothing. Replaced after the signal, pause's late completion
// (the old agent's exit) refuses launch_changed with the signal sent, and
// never records the replacement's Launch End or otherwise changes it, even
// one with no agent identity yet.
func TestPauseKeylessLaunchReplaced(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	t.Run("before the signal", func(t *testing.T) {
		e := newKeylessEnv(t, killRowSpec{})
		var replacement apitest.BusyLaunchFact
		st := &kpHookedStore{Store: e.st, once: func() { replacement = e.replace(t) }}

		err := e.pause(context.Background(), st, 1)

		assertOneName(t, err, "ErrSpawnNotPausable")
		if got, want := jsonOf(t, api.ErrDetails(err)), `{"cause":"launch_changed","signal_sent":false}`; got != want {
			t.Errorf("err_details = %s; want %s", got, want)
		}
		e.assertSignals(t, 0)
		if now := currentLaunch(t, e.dbPath, e.r.ID); !reflect.DeepEqual(now, replacement) {
			t.Errorf("replacement Launch = %+v; want unchanged %+v", now, replacement)
		}
	})
	t.Run("after the signal", func(t *testing.T) {
		e := newKeylessEnv(t, killRowSpec{})
		var replacement apitest.BusyLaunchFact
		e.answer = func(kpSignal) error { replacement = e.replace(t); return nil }

		err := e.pause(context.Background(), nil, 0)

		assertOneName(t, err, "ErrSpawnNotPausable")
		if got, want := jsonOf(t, api.ErrDetails(err)), `{"cause":"launch_changed","signal_sent":true}`; got != want {
			t.Errorf("err_details = %s; want %s", got, want)
		}
		e.assertSignals(t, 1)
		if now := currentLaunch(t, e.dbPath, e.r.ID); !reflect.DeepEqual(now, replacement) {
			t.Errorf("replacement Launch after pause (err %v) = %+v; want unchanged %+v", err, now, replacement)
		}
	})
	t.Run("after exit evidence commits", func(t *testing.T) {
		// b.nqq: a late row write must not finish a concurrently resumed Launch.
		e := newKeylessEnv(t, killRowSpec{})
		var replacement apitest.BusyLaunchFact
		var before apitest.SpawnColumns
		st := &kpExitHookStore{Store: e.st, afterExit: func() {
			replacement = e.replace(t)
			before = e.columns(t, e.r.ID)
		}}
		err := e.pause(context.Background(), st, 1)
		assertOneName(t, err, "ErrSpawnNotPausable")
		if got := jsonOf(t, api.ErrDetails(err)); got != `{"cause":"launch_changed","signal_sent":true}` {
			t.Errorf("err_details = %s; want launch_changed with signal sent", got)
		}
		e.assertRowUnchanged(t, e.r.ID, before)
		if now := currentLaunch(t, e.dbPath, e.r.ID); !reflect.DeepEqual(now, replacement) {
			t.Errorf("replacement Launch = %+v; want unchanged %+v", now, replacement)
		}
	})
}

// pauseWatched runs e.pause on its own goroutine. If it has not returned
// within 30 s, it calls free (clearing the held store condition), joins the
// call and fails, so a regression to an unbounded store wait fails, not hangs.
func (e *kpEnv) pauseWatched(t *testing.T, ctx context.Context, st api.PauseStore, timeoutSeconds int, free func()) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.pause(ctx, st, timeoutSeconds) }()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		free()
		err := joinWithin(t, done)
		t.Errorf("pause did not return within 30 s while the store was held (then: %v)", err)
		return err
	}
}

// joinWithin is a pause call's result from done once its store condition is
// cleared, failing the test if the call has not returned 30 s later.
func joinWithin(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("pause did not return within 30 s of its store condition clearing")
		return nil
	}
}

// followBudgetAdvice follows a budget refusal's advice literally (b.fji), the
// held store condition already cleared: err carries phrase word for word; get
// reads the row when the advice says to inspect, and a retry, when advised,
// succeeds. The agent was signalled once in all, nothing was typed, the exit
// is recorded and the Held Messages are kept.
func (e *kpEnv) followBudgetAdvice(t *testing.T, err error, phrase string, inspect, retry bool, held []apitest.BusyMessageFact) {
	t.Helper()
	adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, phrase)
	if inspect {
		if _, err := e.c.Get(e.r.ID); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	if retry {
		if err := e.pause(context.Background(), nil, 1); err != nil {
			t.Fatalf("retried pause: %v; want success", err)
		}
	}
	e.assertSignals(t, 1)
	e.assertKept(t, held)
	if cause, open := e.launchEnd(t); cause != "process_exited" || open {
		t.Errorf("Launch End after following the advice = %q (open hand-over %t); want process_exited, exited", cause, open)
	}
}

// TestPauseKeylessBudgetBoundsStore (SR-6.4; repair sf2o-2 CR2): keyless
// pause's store reads and writes share its one call budget. With the store's
// connection, or SQLite's write lock, held from the signal on and the budget's
// clock moved past its deadline, the Launch End write waits only what is left
// of it, never the store's own waits: pause refuses budget_exhausted with the
// signal sent and records no Launch End. Its advice, followed once the store
// is free (get, then retry pause), ends in success with no second signal. A
// cancellation at the signal is the bare context error, nothing recorded
// after it. Held Messages stay Held in every case.
func TestPauseKeylessBudgetBoundsStore(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and the pause knobs (process-wide).
	for _, tc := range []string{"store connection held", "write lock held", "cancelled at the signal"} {
		t.Run(tc, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			frees := make(chan func(), 1)
			free := func() {
				select {
				case f := <-frees:
					f()
				default:
				}
			}
			defer free()
			e.answer = func(s kpSignal) error {
				e.pc.Set(s.pid, procfix.Gone())
				switch tc {
				case "store connection held":
					take, _, release := holdReadConnection(t, e.st, e.r.ID)
					if err := take(); err != nil {
						t.Error(err)
					}
					frees <- release
				case "write lock held":
					frees <- apitest.HoldWriteLock(t, e.dbPath, time.Minute)
				default:
					cancel()
					return nil
				}
				e.advance(time.Hour)
				return nil
			}
			held := e.held(t)
			start := time.Now()

			err := e.pauseWatched(t, ctx, nil, 1, free)

			elapsed := time.Since(start)
			free()
			if cause, _ := e.launchEnd(t); cause != "" {
				t.Errorf("Launch End = %q; want none recorded once the budget or the call ended", cause)
			}
			if tc == "cancelled at the signal" {
				if !errors.Is(err, context.Canceled) || api.ErrDetails(err) != nil {
					t.Fatalf("Pause = %v (details %+v); want the bare context error", err, api.ErrDetails(err))
				}
				e.assertSignals(t, 1)
				e.assertKept(t, held)
				return
			}
			assertOneName(t, err, "ErrSpawnNotPausable")
			if got, want := jsonOf(t, api.ErrDetails(err)), `{"cause":"budget_exhausted","signal_sent":true}`; got != want {
				t.Errorf("err_details = %s; want %s", got, want)
			}
			if elapsed > 3*time.Second {
				t.Errorf("pause took %v past its budget; want the store's waits bounded by what was left of it", elapsed)
			}
			e.followBudgetAdvice(t, err, "inspect with get before more input, and retry pause", true, true, held)
		})
	}
}

// TestPauseKeylessBudgetStartsBeforeFirstRead (SR-6.4; repair sf2o-3 R2-1,
// sf2o-4 decision 18): a covered pause's one budget starts before its first
// row read. With the store's connection held before Pause is called, that
// read is cut short by the 1 s budget (the one real-time SQLite deadline
// proof), or by the caller's cancellation as the read starts, under a 60 s
// budget only the cancellation can beat. Coverage is then unknown, so pause
// reads the row again as before keyless pause (GetSpawn, which waits for the
// connection). Once the holder releases it, the covered row refuses
// budget_exhausted with nothing signalled, or returns the bare context error;
// nothing is recorded and Held Messages stay Held. The budget refusal's
// advice, retry pause, then succeeds with one signal.
func TestPauseKeylessBudgetStartsBeforeFirstRead(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and the pause knobs (process-wide).
	for _, tc := range []struct {
		name    string
		budget  int
		cancels bool
	}{{"budget", 1, false}, {"cancelled", 60, true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			held := e.held(t)
			take, _, release := holdReadConnection(t, e.st, e.r.ID)
			if err := take(); err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// The second row read is the fallback: the bounded read was cut short.
			fellBack := make(chan struct{})
			st := &kpHookedStore{Store: e.st, once: func() { close(fellBack) }}
			if tc.cancels {
				st.first = cancel
			}
			done := make(chan error, 1)

			go func() { done <- e.pause(ctx, st, tc.budget) }()

			select {
			case <-fellBack:
			case <-time.After(30 * time.Second):
				release()
				joinWithin(t, done)
				t.Fatalf("pause's first read was not cut short within 30 s (budget %d s)", tc.budget)
			}
			release()
			err := joinWithin(t, done)
			e.assertSignals(t, 0)
			e.assertKept(t, held)
			if cause, _ := e.launchEnd(t); cause != "" {
				t.Errorf("Launch End = %q; want none recorded", cause)
			}
			if tc.cancels {
				if !errors.Is(err, context.Canceled) || api.ErrDetails(err) != nil {
					t.Fatalf("Pause = %v (details %+v); want the bare context error", err, api.ErrDetails(err))
				}
				return
			}
			assertOneName(t, err, "ErrSpawnNotPausable")
			if got, want := jsonOf(t, api.ErrDetails(err)), `{"cause":"budget_exhausted","signal_sent":false}`; got != want {
				t.Errorf("err_details = %s; want %s", got, want)
			}
			e.followBudgetAdvice(t, err, "(the store did not answer in time); nothing was signalled; retry pause", false, true, held)
		})
	}
}

// TestPauseKeylessSuccessBoundary (SR-6.4; repair sf2o-3 R2-1): pause checks
// the caller's cancellation and its own budget after its last store write,
// before reporting success. Just after the exit evidence commits, through
// Client.Pause, a cancelled caller gets the context error, wrapped with no
// err_details; a budget whose clock has moved past its deadline (the caller
// not cancelled) gets budget_exhausted with the signal sent, whose advice,
// inspect with get, is followed. Each says the evidence stays recorded, and
// the recorded exit is kept, not rolled back.
func TestPauseKeylessSuccessBoundary(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and the pause knobs (process-wide).
	for _, tc := range []string{"cancelled", "budget"} {
		t.Run(tc, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			held := e.held(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var exitWrite atomic.Bool
			stop, err := api.ObserveClientSQLiteForTest(e.c, func(d driver.Driver) driver.Driver {
				return sqliteobserve.Wrap(d, sqliteobserve.Hooks{
					Before: func(_ context.Context, q string) error {
						// Only the exit evidence's write keeps the agent identity row.
						if strings.HasPrefix(q, "INSERT INTO busy_send_launch_processes") {
							exitWrite.Store(true)
						}
						return nil
					},
					AfterCommit: func(context.Context) error {
						if !exitWrite.Load() {
							return nil
						}
						if tc == "cancelled" {
							cancel()
						} else {
							e.advance(time.Hour)
						}
						return nil
					},
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := stop(context.Background()); err != nil {
					t.Error(err)
				}
			}()

			_, err = e.c.Pause(ctx, api.PauseParams{InstanceID: e.r.ID})

			if !exitWrite.Load() {
				t.Fatalf("Pause = %v with no exit evidence written; want the boundary after it", err)
			}
			if tc == "cancelled" {
				if !errors.Is(err, context.Canceled) || api.ErrDetails(err) != nil || !strings.Contains(err.Error(), "stays recorded") {
					t.Fatalf("Pause = %v (details %+v); want the context error after the committed exit, saying it stays recorded",
						err, api.ErrDetails(err))
				}
				e.assertSignals(t, 1)
				e.assertKept(t, held)
				if cause, open := e.launchEnd(t); cause != "process_exited" || open {
					t.Errorf("Launch End = %q (open hand-over %t); want the committed exit kept: process_exited, exited", cause, open)
				}
				return
			}
			assertOneName(t, err, "ErrSpawnNotPausable")
			if got, want := jsonOf(t, api.ErrDetails(err)), `{"cause":"budget_exhausted","signal_sent":true}`; got != want {
				t.Errorf("err_details = %s; want %s", got, want)
			}
			e.followBudgetAdvice(t, err, "its exit evidence was recorded and stays recorded; inspect with get", true, false, held)
		})
	}
}

// TestPauseKeylessRowCompletionBudget (b.nqq): the new terminal write shares
// the original budget and cancellation; it never bypasses a blocked store.
func TestPauseKeylessRowCompletionBudget(t *testing.T) {
	for _, tc := range []string{"connection held", "write lock held", "cancelled"} {
		t.Run(tc, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			held := e.held(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var release func()
			free := func() {
				if release != nil {
					release()
					release = nil
				}
			}
			defer free()
			st := &kpExitHookStore{Store: e.st, afterExit: func() {
				switch tc {
				case "cancelled":
					cancel()
				case "connection held":
					take, _, unlock := holdReadConnection(t, e.st, e.r.ID)
					release = unlock
					if err := take(); err != nil {
						t.Error(err)
					}
				case "write lock held":
					release = apitest.HoldWriteLock(t, e.dbPath, time.Minute)
				}
				e.advance(900 * time.Millisecond)
			}}
			err := e.pauseWatched(t, ctx, st, 1, free)
			free()
			if tc == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Pause = %v; want cancellation", err)
				}
			} else if got := jsonOf(t, api.ErrDetails(err)); got != `{"cause":"budget_exhausted","signal_sent":true}` {
				t.Fatalf("Pause = %v, details %s; want budget_exhausted with signal sent", err, got)
			}
			row, readErr := e.st.GetSpawn(e.r.ID)
			if readErr != nil || !store.IsLiveState(row.State) {
				t.Errorf("row after uncommitted end = %s, %v; want still live", row.State, readErr)
			}
			e.assertSignals(t, 1)
			e.assertKept(t, held)
		})
	}
}

// TestPauseKeylessRowCompletionSuccessBoundary (b.nqq): cancellation or budget
// expiry after the row commit refuses success while retaining the committed end.
func TestPauseKeylessRowCompletionSuccessBoundary(t *testing.T) {
	for _, tc := range []string{"cancelled", "budget"} {
		t.Run(tc, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := &kpExitHookStore{Store: e.st, afterEnd: func() {
				if tc == "cancelled" {
					cancel()
				} else {
					e.advance(time.Hour)
				}
			}}
			err := e.pause(ctx, st, 1)
			if tc == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Pause = %v; want cancellation", err)
				}
			} else if got := jsonOf(t, api.ErrDetails(err)); got != `{"cause":"budget_exhausted","signal_sent":true}` {
				t.Fatalf("Pause = %v, details %s; want budget_exhausted", err, got)
			}
			e.assertEnded(t)
		})
	}
}
