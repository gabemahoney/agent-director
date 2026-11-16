package api_test

// busy_send_force_test.go: Task t2.xis.p8.y2's force calls on the public API
// (SR-3.1, SR-3.2, SR-4.1 to SR-4.3): each outcome through the real store and
// the route's real force prompt driver on virtual time, the call's fixed
// budget and its route notifications, a send with force, and a claim that
// fails or loses its COMMIT acknowledgement. The release of a call that never
// recorded its end is TestAdviceFollow_E35_ForceEndFailedFinishByCommand's.
// The store's force matrix is internal/store's busy_send_force_test.go;
// reads' projections are busy_send_lookup_test.go's and
// busy_send_event_only_test.go's.

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/busysend"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// forceText is a 5,000-byte Message: two of them need two of Claude's
// 9,600-byte prompts.
var forceText = strings.Repeat("f", 5000)

// forceComplete is a route's complete output of a prompt with its order
// secured at Claude's unproven boundary.
var forceComplete = busysend.Disposition{Output: store.BusySendOutputComplete, Order: store.BusySendOrderEmissionComplete,
	UnprovenBoundary: true}

// forceAgent is a live covered Claude agent on e with a recorded route and
// wake file, and its Client on e's clock. Each pause a force call takes runs
// e.sleep: it records the pause and whether the call touched the wake since
// the last one, runs drive (the agent's route and hooks; nil for none), then
// lets the pause and lag (the store reads between two pauses) pass.
type forceAgent struct {
	e       *killEnv
	c       *api.Client
	id      string
	session string
	wake    string
	touched func() bool
	route   *busyRoute
	paused  []time.Duration
	woke    []int
	drive   func(pause int, woke bool)
	lag     time.Duration
}

func newForceAgent(t *testing.T, e *killEnv) *forceAgent {
	t.Helper()
	f := &forceAgent{e: e, session: uuid.NewString()}
	f.id = e.seedRow(t, killRowSpec{SessionID: f.session}).ID
	seedBusyCovered(t, e, f.id)
	f.wake, f.touched = busyWake(t, e, f.id)
	f.route = &busyRoute{dest: busyDestination(t, e, f.id, f.session), out: forceComplete}
	f.c, _ = e.client(t)
	e.sleep = func(d time.Duration) {
		woke := f.touched()
		if woke {
			f.woke = append(f.woke, len(f.paused))
		}
		f.paused = append(f.paused, d)
		if f.drive != nil {
			f.drive(len(f.paused)-1, woke)
		}
		e.clock.Advance(d + f.lag)
	}
	return f
}

// send admits text, confirmed under busyCaller's pair for request unless
// request is "", and takes back the admission's wake touch.
func (f *forceAgent) send(t *testing.T, request, text string) {
	t.Helper()
	p := api.SendKeysParams{InstanceID: f.id, Text: text}
	if request != "" {
		p.ConfirmedDelivery, p.CallerUUID, p.DeliveryRequestID = ptr(true), ptr(busyCaller), request
	}
	busyAdmit(t, f.c, p)
	f.touched()
}

// force runs force-delivery on the agent, recording its pauses afresh, and
// returns its force_result and the virtual time it took.
func (f *forceAgent) force(t *testing.T) (api.ForceResult, time.Duration) {
	t.Helper()
	f.paused, f.woke = nil, nil
	start := f.e.clock.Now()
	res, err := f.c.ForceDelivery(api.ForceDeliveryParams{InstanceID: f.id})
	if err != nil {
		t.Fatalf("ForceDelivery(%s): %v", f.id, err)
	}
	return res.ForceResult, f.e.clock.Now().Sub(start)
}

// admit records the agent's own sighting of the route's issued prompt k, its
// admission (Task t2.xis.p8.zp binds Claude's hook payload to this step).
func (f *forceAgent) admit(t *testing.T, k int) {
	t.Helper()
	p := f.route.issued[k]
	if _, err := f.e.st.RecordBusySendEvidence(context.Background(), store.BusySendPromptEvidence{HandoverID: p.HandoverID,
		PromptIndex: p.PromptIndex, LaunchID: p.Destination.LaunchID, ConnectionGeneration: p.Destination.ConnectionGeneration,
		Admission: store.BusySendAdmissionPromptSighting, At: f.e.clock.Now()}); err != nil {
		t.Fatalf("admission of prompt %d: %v", k, err)
	}
}

// assertRecorded fails unless got is what get-force reads by its force_id and
// what get and status show as delivery.force (returned-equals-recorded).
func (f *forceAgent) assertRecorded(t *testing.T, got api.ForceResult) {
	t.Helper()
	if found, err := f.c.GetForce(api.GetForceParams{ForceID: got.ForceID}); err != nil || found.Lookup != "found" ||
		found.ForceResult == nil || !reflect.DeepEqual(*found.ForceResult, got) {
		t.Errorf("get-force = %+v, %v; want found %+v", found, err, got)
	}
	row, err := f.c.Get(f.id)
	if err != nil || row.Delivery == nil || !reflect.DeepEqual(row.Delivery.Force, &got) {
		t.Errorf("get delivery = %+v, %v; want force %+v", row.Delivery, err, got)
	}
	status, err := f.c.Status(f.id)
	if err != nil || status.Delivery == nil || !reflect.DeepEqual(status.Delivery.Force, &got) {
		t.Errorf("status delivery = %+v, %v; want force %+v", status.Delivery, err, got)
	}
}

// forceTurns is the agent's route during a force call: it issues the prompt
// each notification asks for (losing every one after the first with lose),
// and the agent sights the prompt admitLag pauses after its issue (never when
// admitLag is negative).
func forceTurns(t *testing.T, f *forceAgent, admitLag int, lose bool) func(int, bool) {
	issuedAt := -1
	return func(pause int, woke bool) {
		if woke && (!lose || issuedAt < 0) {
			forceWake(t, f.e, f.id, f.route)
			issuedAt = pause
		}
		if admitLag >= 0 && issuedAt >= 0 && pause == issuedAt+admitLag {
			f.admit(t, len(f.route.issued)-1)
		}
	}
}

// paneInputs is how many texts, Enters and keys reached e's panes.
func paneInputs(e *killEnv) int {
	return len(e.rec.SocketCallsOf(tmux.CallSendText)) + len(e.rec.SocketCallsOf(tmux.CallSendEnter)) +
		len(e.rec.SocketCallsOf(tmux.CallSendKey))
}

// TestForceDeliveryOutcomes (SR-4.1, SR-4.3, SR-3.1, SR-3.2, SR-3.4; M1 G3
// ForceFencedAtCallEnd and NoOrphanForceClaim at the call's end, and
// returned-equals-recorded at its response): force-delivery on a live agent
// holding a, an unconfirmed u and b (two prompts: a and u, then b) succeeds
// with each outcome, through the real store and the route's real force
// prompt driver on virtual time. Only the call notifies the route: after its
// claim, for prompt 1 only once prompt 0 is admitted with its order secured,
// never twice for one prompt, and at its end only when it returned carried
// Messages to Held. It waits in 100 ms pauses clipped to its 12 s active
// phase, ends early once its outcome is decided, and returns what get-force,
// get and status read: carried_pairs a and b, and their current Results,
// forced once their prompt issued. Nothing reaches the pane. After a lost
// wake no worker remains, and the end's notification alone brings ordinary
// delivery of the returned remainder, whose new Result get-force then reads.
func TestForceDeliveryOutcomes(t *testing.T) {
	t.Parallel()
	whole := slices.Repeat([]time.Duration{100 * time.Millisecond}, 120)
	for _, tc := range []struct {
		name            string
		drive           func(t *testing.T, f *forceAgent) func(int, bool)
		lag             time.Duration
		outcome, reason string
		paused          []time.Duration
		woke            []int
		issued          int      // prompts the route issued
		held            []string // carried Messages Held after the call, in accepted order
		then            func(t *testing.T, f *forceAgent, got api.ForceResult)
	}{
		{name: "interim: the route takes no wake", lag: 30 * time.Millisecond, outcome: "refused", reason: "not_issued",
			paused: append(slices.Repeat([]time.Duration{100 * time.Millisecond}, 92), 40*time.Millisecond), woke: []int{0},
			held: []string{"a", "u", "b"}},
		{name: "each prompt issued and admitted in turn", outcome: "handed_over", reason: "admitted", paused: whole[:6], woke: []int{0, 3}, issued: 2,
			drive: func(t *testing.T, f *forceAgent) func(int, bool) { return forceTurns(t, f, 2, false) }},
		{name: "the second wake lost", outcome: "handed_over", reason: "admitted", paused: whole, woke: []int{0, 3}, issued: 1, held: []string{"b"},
			drive: func(t *testing.T, f *forceAgent) func(int, bool) { return forceTurns(t, f, 2, true) },
			then: func(t *testing.T, f *forceAgent, got api.ForceResult) {
				facts := busyFacts(t, f.e.dbPath, f.id)
				f.e.clock.Advance(time.Hour)
				if f.touched() || !reflect.DeepEqual(busyFacts(t, f.e.dbPath, f.id), facts) {
					t.Fatal("something touched the wake or custody an hour after the call returned; want no worker left")
				}
				if out := deliverBusy(t, f.e, f.id, f.route); out.Kind != busysend.OutcomeIssued || len(f.route.issued) != 2 ||
					string(f.route.issued[1].Body) != "[agent-director: message 1 of 1]\n"+forceText {
					t.Fatalf("the eligible event after the end's notification = %+v; want b issued alone", out)
				}
				b, err := f.c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "b"})
				if err != nil || b.Result == nil || !b.Result.Final || b.Result.Forced {
					t.Fatalf("b after ordinary delivery = %+v, %v; want Final, not forced", b, err)
				}
				want := got
				want.CarriedResults = []api.DeliveryResult{got.CarriedResults[0], *b.Result}
				if found, err := f.c.GetForce(api.GetForceParams{ForceID: got.ForceID}); err != nil || found.ForceResult == nil ||
					!reflect.DeepEqual(*found.ForceResult, want) {
					t.Errorf("get-force after b's delivery = %+v, %v; want the force unchanged with b's current Result %+v", found, err, want)
				}
			}},
		{name: "prompt 0 unadmitted", outcome: "pending", reason: "awaiting_admission", paused: whole, woke: []int{0}, issued: 1, held: []string{"b"},
			drive: func(t *testing.T, f *forceAgent) func(int, bool) {
				turns := forceTurns(t, f, -1, false)
				return func(pause int, woke bool) {
					turns(pause, woke)
					if pause != 1 {
						return
					}
					// MI decision 10: while the call runs, a read shows running,
					// never pending, though an issued prompt awaits admission.
					row, err := f.c.Get(f.id)
					if err != nil || row.Delivery == nil || row.Delivery.Force == nil || row.Delivery.Force.Outcome != "running" ||
						row.Delivery.Force.Reason != "" || row.Delivery.Force.EndedAt != nil {
						t.Errorf("delivery.force during the call = %+v, %v; want running, reason \"\", no end", row.Delivery, err)
					}
				}
			}},
		{name: "a definitive negative", outcome: "failed", reason: "admission_unknown", paused: whole[:1], woke: []int{0}, issued: 1, held: []string{"b"},
			drive: func(t *testing.T, f *forceAgent) func(int, bool) {
				// A synthetic route's negative: Claude supplies none (Task t2.xis.p8.zp).
				return func(_ int, woke bool) {
					if !woke {
						return
					}
					forceWake(t, f.e, f.id, f.route)
					p := f.route.issued[0]
					if res, err := f.e.st.RecordBusySendNegative(context.Background(), store.BusySendPromptNegative{HandoverID: p.HandoverID,
						PromptIndex: 0, LaunchID: p.Destination.LaunchID, ConnectionGeneration: p.Destination.ConnectionGeneration,
						At: f.e.clock.Now()}); err != nil || !res.Recorded {
						t.Fatalf("negative for prompt 0 = %+v, %v; want it recorded", res, err)
					}
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			f := newForceAgent(t, e)
			f.send(t, "a", forceText)
			f.send(t, "", "unconfirmed")
			f.send(t, "b", forceText)
			if tc.drive != nil {
				f.drive = tc.drive(t, f)
			}
			f.lag = tc.lag
			start := e.clock.Now()

			got, took := f.force(t)

			f.drive, f.lag = nil, 0
			var want time.Duration
			for _, d := range tc.paused {
				want += d + tc.lag
			}
			if took != want || !slices.Equal(f.paused, tc.paused) || !slices.Equal(f.woke, tc.woke) || len(f.route.issued) != tc.issued {
				t.Errorf("the call took %v in pauses %v, notifying the route by pauses %v, with %d prompts issued; want %v in %v, by %v, %d",
					took, f.paused, f.woke, len(f.route.issued), want, tc.paused, tc.woke, tc.issued)
			}
			if id, err := uuid.Parse(got.ForceID); err != nil || id.String() != got.ForceID {
				t.Errorf("force_id %q is not an AD-made canonical UUID (%v)", got.ForceID, err)
			}
			if got.Outcome != tc.outcome || got.Reason != tc.reason || !got.StartedAt.Equal(start) ||
				(got.EndedAt == nil) != (tc.outcome == "pending") {
				t.Errorf("force_result = %+v; want %s %s started at %v, ended unless pending", got, tc.outcome, tc.reason, start)
			}
			pairs := []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: "a"}, {CallerUUID: busyCaller, RequestID: "b"}}
			if !reflect.DeepEqual(got.CarriedPairs, pairs) || len(got.CarriedResults) != len(pairs) {
				t.Fatalf("carried %+v with Results %+v; want pairs %+v and their two Results", got.CarriedPairs, got.CarriedResults, pairs)
			}
			for i, r := range got.CarriedResults {
				now, err := f.c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: pairs[i].RequestID})
				if err != nil || now.Result == nil || !reflect.DeepEqual(r, *now.Result) || r.Forced != (i < tc.issued) {
					t.Errorf("carried Result %d = %+v; want %s's current Result %+v, forced %t", i, r, pairs[i].RequestID, now.Result, i < tc.issued)
				}
			}
			var held []string
			for i, m := range busyFacts(t, e.dbPath, f.id).Messages {
				if m.Place == "held" {
					held = append(held, []string{"a", "u", "b"}[i])
				}
			}
			if !slices.Equal(held, tc.held) {
				t.Errorf("Held after the call = %v; want %v", held, tc.held)
			}
			if woke := f.touched(); woke != (len(tc.held) > 0) {
				t.Errorf("the call's end notified the route: %t; want %t", woke, len(tc.held) > 0)
			}
			f.assertRecorded(t, got)
			if n := paneInputs(e); n != 0 {
				t.Errorf("%d pane inputs; want none", n)
			}
			if tc.then != nil {
				tc.then(t, f, got)
			}
		})
	}
}

// forceCallStep is one store step of a force call, with the context it ran on.
type forceCallStep struct {
	name string
	ctx  context.Context
}

// forceProbe is a force's store over a real one. Each Launch read and claim
// first lets lag pass on clock, as a slow store step would; with stale, the
// first Launch read hands back its Launch under another token, so the claim
// finds the examined Launch replaced (store.ErrBusySendLaunchChanged, nothing
// written) and the call reads and claims again. It records every step with
// its context, the wall time of the first, each claim's request and the
// end's time.
type forceProbe struct {
	api.ForceDeliveryStore
	clock  *tmuxfix.Clock
	lag    time.Duration
	stale  bool
	steps  []forceCallStep
	first  time.Time
	claims []store.BusySendForceClaimRequest
	endAt  time.Time
}

func (p *forceProbe) step(ctx context.Context, name string, lag time.Duration) {
	if len(p.steps) == 0 {
		p.first = time.Now()
	}
	p.steps = append(p.steps, forceCallStep{name, ctx})
	p.clock.Advance(lag)
}

func (p *forceProbe) GetBusySendPublishedLaunchWithin(ctx context.Context, id string, wait time.Duration) (store.BusySendPublishedLaunch, error) {
	p.step(ctx, "launch", p.lag)
	l, err := p.ForceDeliveryStore.GetBusySendPublishedLaunchWithin(ctx, id, wait)
	if p.stale {
		p.stale, l.LaunchToken = false, "replaced-"+l.LaunchToken
	}
	return l, err
}

func (p *forceProbe) ClaimBusySendForce(ctx context.Context, req store.BusySendForceClaimRequest) (store.BusySendForceClaim, error) {
	p.step(ctx, "claim", p.lag)
	p.claims = append(p.claims, req)
	return p.ForceDeliveryStore.ClaimBusySendForce(ctx, req)
}

func (p *forceProbe) ReadBusySendForceProgress(ctx context.Context, forceID string) (store.BusySendForceProgress, error) {
	p.step(ctx, "progress", 0)
	return p.ForceDeliveryStore.ReadBusySendForceProgress(ctx, forceID)
}

func (p *forceProbe) EndBusySendForceCall(ctx context.Context, ref store.BusySendForceRef, at time.Time) (store.BusySendForceEnd, error) {
	p.step(ctx, "end", 0)
	p.endAt = at
	return p.ForceDeliveryStore.EndBusySendForceCall(ctx, ref, at)
}

// TestForceCallBudget (SR-3.2, SR-4.3; E5 Main Agent decision 1; code review
// F2): a force call's budget is fixed at its entry and never renewed, even
// when its Launch reads and claims take time and the claim finds its examined
// Launch replaced, so the call reads and claims again. Every step but the end
// runs on the one active-phase context made before the first read, ending
// 12 s after the call began, and the end on the whole call's, ending 15 s
// after; neither is renewed by the retry or by the end coming late. Both
// claims record the entry, 12 s and 3 s, so the force's recorded call
// deadline is the entry plus 15 s, and the 4 s the reads and claims took
// shorten the active wait to 8 s of 100 ms pauses, ending at the entry plus
// 12 s. The call notifies the route's wake path only after its claim and at
// its end.
func TestForceCallBudget(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	f := newForceAgent(t, e)
	f.send(t, "a", "hello")
	p := &forceProbe{ForceDeliveryStore: e.st, clock: e.clock, lag: time.Second, stale: true}
	var paused []time.Duration
	var touches []string
	entry, began := e.clock.Now(), time.Now()

	res, err := api.ForceDeliveryForTest(p, context.Background(), e.clock.Now,
		func(d time.Duration) { paused = append(paused, d); e.clock.Advance(d) },
		func(path string) error { touches = append(touches, path); return nil },
		api.ForceDeliveryParams{InstanceID: f.id})

	if err != nil || res.ForceResult.Outcome != "refused" || res.ForceResult.Reason != "not_issued" || !res.ForceResult.StartedAt.Equal(entry) {
		t.Fatalf("force = %+v, %v; want refused not_issued (no forced wake yet), started at the entry %v", res, err, entry)
	}
	var names []string
	for _, s := range p.steps {
		if len(names) == 0 || names[len(names)-1] != s.name {
			names = append(names, s.name)
		}
	}
	if want := []string{"launch", "claim", "launch", "claim", "progress", "end"}; !slices.Equal(names, want) {
		t.Fatalf("steps %v; want %v, the claim retried once on the replaced Launch", names, want)
	}
	active, call := p.steps[0].ctx, p.steps[len(p.steps)-1].ctx
	for i, s := range p.steps[:len(p.steps)-1] {
		if s.ctx != active {
			t.Errorf("step %d (%s) ran on another context; want the active phase's from before the first read", i, s.name)
		}
	}
	for _, c := range []struct {
		name  string
		ctx   context.Context
		after time.Duration
	}{{"active phase", active, 12 * time.Second}, {"call", call, 15 * time.Second}} {
		if end, ok := c.ctx.Deadline(); !ok || end.Before(began.Add(c.after)) || end.After(p.first.Add(c.after)) {
			t.Errorf("the %s's context ends at %v (%t); want %v after the call began, in [%v, %v]", c.name, end, ok, c.after,
				began.Add(c.after), p.first.Add(c.after))
		}
	}
	for i, c := range p.claims {
		if !c.At.Equal(entry) || c.ActiveBudget != 12*time.Second || c.CleanupBudget != 3*time.Second {
			t.Errorf("claim %d at %v with phases %v + %v; want the entry %v, 12s + 3s", i, c.At, c.ActiveBudget, c.CleanupBudget, entry)
		}
	}
	if want := slices.Repeat([]time.Duration{100 * time.Millisecond}, 80); !slices.Equal(paused, want) || !p.endAt.Equal(entry.Add(12*time.Second)) {
		t.Errorf("%d pauses %v, end recorded at %v; want 80 of 100ms after 4s of reads and claims, the end at %v", len(paused), paused, p.endAt,
			entry.Add(12*time.Second))
	}
	stored, err := e.st.ReadBusySendForce(res.ForceResult.ForceID)
	if err != nil || stored.ForceResult == nil || stored.ForceResult.CallDeadlineMillis != entry.Add(15*time.Second).UnixMilli() ||
		stored.ForceResult.CallEndedAtMillis != p.endAt.UnixMilli() {
		t.Errorf("stored force = %+v, %v; want call deadline %v and its end at %v", stored.ForceResult, err, entry.Add(15*time.Second), p.endAt)
	}
	if !slices.Equal(touches, []string{f.wake, f.wake}) {
		t.Errorf("wake notifications = %q; want %q after the claim and at the end", touches, f.wake)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath).Forces; got != 1 {
		t.Errorf("%d force attempts; want 1: the replaced Launch's claim wrote nothing", got)
	}
}

// assertReleased runs explicit, a command for the agent past the recorded
// deadline of its force forceID, which holds a and u and never recorded its
// end, and checks the store's one release path ran: the force ended refused
// not_issued, a and u are Held in accepted order, a unforced, and the route
// was notified.
func (f *forceAgent) assertReleased(t *testing.T, forceID string, explicit func(t *testing.T, f *forceAgent)) {
	t.Helper()
	f.touched()
	explicit(t, f)
	if got, err := f.c.GetForce(api.GetForceParams{ForceID: forceID}); err != nil || got.ForceResult == nil ||
		got.ForceResult.Outcome != "refused" || got.ForceResult.Reason != "not_issued" || got.ForceResult.EndedAt == nil {
		t.Errorf("abandoned force after the command = %+v, %v; want refused not_issued", got.ForceResult, err)
	}
	for i, m := range busyFacts(t, f.e.dbPath, f.id).Messages[:2] {
		if m.Place != "held" || m.Seq != int64(i+1) {
			t.Errorf("Message %d after the release = %+v; want Held in accepted order", i+1, m)
		}
	}
	if a, err := f.c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "a"}); err != nil || a.Result == nil || a.Result.Forced {
		t.Errorf("a after the release = %+v, %v; want not forced", a, err)
	}
	if !f.touched() {
		t.Error("the release did not notify the route")
	}
}

// forceClaimFault makes c's next force claim fail: before its COMMIT, at the
// insert of its carried membership, or, with lost, after its COMMIT landed,
// by losing the acknowledgement. Its stop fails the test unless it did.
func forceClaimFault(t *testing.T, c *api.Client, lost bool) (stop func()) {
	t.Helper()
	var claiming, failed atomic.Bool
	end, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
		return sqliteobserve.Wrap(d, sqliteobserve.Hooks{
			Before: func(_ context.Context, q string) error {
				switch {
				case !strings.Contains(q, "INSERT INTO busy_send_force_members"):
				case lost:
					claiming.Store(true)
				case !failed.Swap(true):
					return errors.New("injected claim write failure")
				}
				return nil
			},
			AfterCommit: func(context.Context) error {
				if claiming.Swap(false) && !failed.Swap(true) {
					return errors.New("lost COMMIT acknowledgement")
				}
				return nil
			},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := end(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !failed.Load() {
			t.Fatal("no force claim failed")
		}
	}
}

// TestForceDeliveryClaimFaults (SR-4.1, SR-3.2; M1 G3 NoOrphanForceClaim;
// test review F3): force-delivery on a live covered agent holding a and u,
// whose claim fails before its COMMIT, is an error naming its cause (a failed
// write unnamed, a write lock held past the store's busy timeout
// ErrStoreBusy) that stores no force and changes no custody. One whose claim
// COMMIT landed but lost its acknowledgement is an unnamed error with no
// force_result: the call notifies no route and nothing is issued or typed,
// and the landed force, running in get and get-force, holds a and u until an
// explicit send after its recorded deadline releases it.
func TestForceDeliveryClaimFaults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		call    func(t *testing.T, f *forceAgent) (api.ForceDeliveryResult, error)
		errName string
		landed  bool
	}{
		{"a failed write before the claim's COMMIT", func(t *testing.T, f *forceAgent) (api.ForceDeliveryResult, error) {
			defer forceClaimFault(t, f.c, false)()
			return f.c.ForceDelivery(api.ForceDeliveryParams{InstanceID: f.id})
		}, "ErrInternal", false},
		{"the write lock held", func(t *testing.T, f *forceAgent) (api.ForceDeliveryResult, error) {
			cfg := filepath.Join(t.TempDir(), "config.toml")
			apitest.WriteKeysConfig(t, cfg, apitest.ConfigKeys{StoreBusyTimeoutMs: 100})
			busy, _, err := f.e.clientFor(t, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer apitest.HoldWriteLock(t, f.e.dbPath, time.Minute)()
			return busy.ForceDelivery(api.ForceDeliveryParams{InstanceID: f.id})
		}, "ErrStoreBusy", false},
		{"the claim's COMMIT acknowledgement lost", func(t *testing.T, f *forceAgent) (api.ForceDeliveryResult, error) {
			defer forceClaimFault(t, f.c, true)()
			return f.c.ForceDelivery(api.ForceDeliveryParams{InstanceID: f.id})
		}, "ErrInternal", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			f := newForceAgent(t, e)
			f.send(t, "a", "hello")
			f.send(t, "", "unconfirmed")
			facts, entry := busyFacts(t, e.dbPath, f.id), e.clock.Now()

			res, err := tc.call(t, f)

			assertOneName(t, err, tc.errName)
			if api.ErrDetails(err) != nil || !reflect.DeepEqual(res, api.ForceDeliveryResult{}) {
				t.Errorf("failed claim = %+v, details %+v; want a bare error and no force_result", res, api.ErrDetails(err))
			}
			if f.touched() || paneInputs(e) != 0 || len(f.paused) != 0 {
				t.Errorf("after the failed claim: wake touched, %d pane inputs, %d pauses; want no notification, input or wait", paneInputs(e), len(f.paused))
			}
			forces := storefix.ReadBusySendCounts(t, e.dbPath).Forces
			if !tc.landed {
				if got := busyFacts(t, e.dbPath, f.id); forces != 0 || !reflect.DeepEqual(got, facts) {
					t.Errorf("%d forces stored and custody %+v; want none and unchanged %+v", forces, got, facts)
				}
				return
			}
			row, rerr := f.c.Get(f.id)
			if rerr != nil || row.Delivery == nil || row.Delivery.Force == nil || forces != 1 {
				t.Fatalf("get after the lost acknowledgement = %+v, %v with %d forces; want the landed force shown", row.Delivery, rerr, forces)
			}
			landed := *row.Delivery.Force
			if found, err := f.c.GetForce(api.GetForceParams{ForceID: landed.ForceID}); err != nil || found.ForceResult == nil ||
				!reflect.DeepEqual(*found.ForceResult, landed) || landed.Outcome != "running" || !landed.StartedAt.Equal(entry) ||
				!reflect.DeepEqual(landed.CarriedPairs, []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: "a"}}) {
				t.Errorf("landed force = %+v, get-force %+v, %v; want running from the entry, carrying a", landed, found, err)
			}
			for _, m := range busyFacts(t, e.dbPath, f.id).Messages {
				if m.Place != "claimed" {
					t.Errorf("Message %d = %+v; want claimed by the landed force", m.Seq, m)
				}
			}
			e.clock.Advance(entry.Add(15*time.Second + time.Millisecond).Sub(e.clock.Now()))
			f.assertReleased(t, landed.ForceID, func(t *testing.T, f *forceAgent) {
				busyAdmit(t, f.c, api.SendKeysParams{InstanceID: f.id, Text: "next"})
			})
		})
	}
}

// TestForceSendKeys (SR-4.1, SR-1.1, SR-3.9; M1 G3 returned-equals-recorded):
// send-keys with force true on a live agent in a Dialog admits the Message,
// then forces: its result is the Message's own Result with a separate
// force_result beside its fields, and the Message's ordinary wake never runs
// before the force's claim. The interim force (no forced wake yet) claims it,
// waits out its 12 s and returns it Held, refused not_issued, as get-force
// reads it. A repeat of its pair forces again under a new force_id and
// accepts nothing; force false is an ordinary send. A refused, conflicting or
// uncertain send forces nothing. After the record's deletion a repeat returns
// the owner's Result and a stored refused force, record_deleted. No call opens
// native input, types, sends a key or answers the Dialog.
func TestForceSendKeys(t *testing.T) {
	t.Parallel()
	ne := newNativeSubmissionEnv(t)
	e := ne.killEnv
	c, _ := e.client(t)
	r := e.seedRow(t, killRowSpec{SessionID: uuid.NewString(), State: store.StateCheckPermission, RelayOn: true})
	e.seedRequest(t, r.ID)
	seedBusyCovered(t, e, r.ID)
	_, touched := busyWake(t, e, r.ID)
	cols := e.columns(t, r.ID)
	requests, err := e.st.PermissionRequestsForSpawn(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var woke []int
	pauses := 0
	env := ne.env
	env.Sleep = func(d time.Duration) {
		if touched() {
			woke = append(woke, pauses)
		}
		pauses++
		ne.env.Sleep(d)
	}
	// claimWoke records, at each force attempt's insert, whether the wake had
	// been touched since the last look.
	var claimWoke []bool
	stop, err := store.ObserveSQLiteForTest(e.st, func(d driver.Driver) driver.Driver {
		return sqliteobserve.Wrap(d, sqliteobserve.Hooks{Before: func(_ context.Context, q string) error {
			if strings.Contains(q, "INSERT INTO busy_send_forces") {
				claimWoke = append(claimWoke, touched())
			}
			return nil
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()
	send := func(p api.SendKeysParams) (api.SendKeysResult, error) {
		t.Helper()
		pauses, woke, claimWoke = 0, nil, nil
		return api.SendKeys(e.st, e.rec, env, p)
	}
	owner := func() api.DeliveryResult {
		t.Helper()
		got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "m-1"})
		if err != nil || got.Result == nil {
			t.Fatalf("get-delivery m-1 = %+v, %v", got, err)
		}
		return *got.Result
	}
	recorded := func(f api.ForceResult) {
		t.Helper()
		if got, err := c.GetForce(api.GetForceParams{ForceID: f.ForceID}); err != nil || got.ForceResult == nil || !reflect.DeepEqual(*got.ForceResult, f) {
			t.Errorf("get-force = %+v, %v; want found %+v", got, err, f)
		}
	}
	m1 := api.SendKeysParams{InstanceID: r.ID, Text: "forced", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller),
		DeliveryRequestID: "m-1", Force: ptr(true)}

	res, err := send(m1)
	if err != nil || res.Delivery == nil || res.Force == nil {
		t.Fatalf("send with force = %+v, %v; want its Result and force_result", res, err)
	}
	if res.Delivery.Class != "held" || res.Delivery.Wait == nil || res.Delivery.Wait.Reason != "dialog" {
		t.Errorf("Result = %+v; want Held behind the Dialog", *res.Delivery)
	}
	first := *res.Force
	want := api.ForceResult{ForceID: first.ForceID, Outcome: "refused", Reason: "not_issued", StartedAt: first.StartedAt,
		EndedAt: first.EndedAt, CarriedPairs: []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: "m-1"}},
		CarriedResults: []api.DeliveryResult{owner()}}
	if !reflect.DeepEqual(first, want) || first.EndedAt == nil || first.EndedAt.Sub(first.StartedAt) != 12*time.Second {
		t.Errorf("force_result = %+v; want %+v ended 12s after it started", first, want)
	}
	if pauses != 120 || !slices.Equal(woke, []int{0}) || !slices.Equal(claimWoke, []bool{false}) || !touched() {
		t.Errorf("%d pauses, route notified by pauses %v, wake touched before the claim %v; want 120, [0], [false], and at the end",
			pauses, woke, claimWoke)
	}
	recorded(first)
	var shape, result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonOf(t, res)), &shape); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(jsonOf(t, *res.Delivery)), &result); err != nil {
		t.Fatal(err)
	}
	forceJSON := shape["force_result"]
	delete(shape, "force_result")
	if string(forceJSON) != jsonOf(t, first) || !reflect.DeepEqual(shape, result) {
		t.Errorf("send-keys JSON = %s; want the Result's fields with force_result beside them", jsonOf(t, res))
	}
	var back api.SendKeysResult
	if err := json.Unmarshal([]byte(jsonOf(t, res)), &back); err != nil || back.Delivery == nil || back.Force == nil ||
		jsonOf(t, back) != jsonOf(t, res) {
		t.Errorf("send-keys JSON read back = %+v, %v; want the same Result and force_result", back, err)
	}

	counts, current := storefix.ReadBusySendCounts(t, e.dbPath), owner()
	res, err = send(m1)
	if err != nil || res.Delivery == nil || res.Force == nil || !reflect.DeepEqual(*res.Delivery, current) ||
		res.Force.ForceID == first.ForceID || res.Force.Outcome != "refused" || res.Force.Reason != "not_issued" {
		t.Errorf("repeat with force = %+v, %v; want the owner's Result and a new force", res, err)
	}
	if got := storefix.ReadBusySendCounts(t, e.dbPath); got.Messages != 1 || got.Pairs != 1 || got.Forces != counts.Forces+1 ||
		busyFacts(t, e.dbPath, r.ID).Conversations[0].AcceptedSeq != 1 {
		t.Errorf("records after the repeat = %+v; want one Message accepted once and one more force", got)
	}

	res, err = send(api.SendKeysParams{InstanceID: r.ID, Text: "plain", Force: ptr(false)})
	if err != nil || res.Delivery == nil || res.Force != nil || jsonOf(t, res) != jsonOf(t, *res.Delivery) || !touched() || pauses != 0 {
		t.Errorf("send with force false = %+v, %v; want an ordinary send: the Result alone, its wake touched", res, err)
	}
	counts = storefix.ReadBusySendCounts(t, e.dbPath)
	for _, p := range []api.SendKeysParams{
		{InstanceID: r.ID, Text: "bad\rtext", Force: ptr(true)},
		{InstanceID: r.ID, Text: "other", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller), DeliveryRequestID: "m-1", Force: ptr(true)},
	} {
		_, err := send(p)
		assertOneName(t, err, "ErrCantSend")
	}
	busyUncertain(t, c, api.SendKeysParams{InstanceID: r.ID, Text: "uncertain", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller),
		DeliveryRequestID: "m-3", Force: ptr(true)}, touched)
	if got := storefix.ReadBusySendCounts(t, e.dbPath).Forces; got != counts.Forces {
		t.Errorf("%d force attempts after refused, conflicting and uncertain sends; want %d", got, counts.Forces)
	}
	e.assertRowUnchanged(t, r.ID, cols)
	if after, err := e.st.PermissionRequestsForSpawn(r.ID); err != nil || !reflect.DeepEqual(after, requests) {
		t.Errorf("permission requests = %+v, %v; want unchanged %+v", after, err, requests)
	}

	deleteCoveredRow(t, e.st, r.ID)
	kept := owner()
	res, err = send(m1)
	if err != nil || res.Delivery == nil || !reflect.DeepEqual(*res.Delivery, kept) || res.Force == nil {
		t.Fatalf("repeat after deletion = %+v, %v; want the owner's Result %+v and a force", res, err, kept)
	}
	deleted := *res.Force
	if deleted.Outcome != "refused" || deleted.Reason != "record_deleted" || deleted.EndedAt == nil || !deleted.EndedAt.Equal(deleted.StartedAt) ||
		len(deleted.CarriedPairs) != 0 || len(deleted.CarriedResults) != 0 || pauses != 0 {
		t.Errorf("force after deletion = %+v after %d pauses; want refused record_deleted at once, carrying nothing", deleted, pauses)
	}
	recorded(deleted)
	assertNoInputPath(t, ne)
}
