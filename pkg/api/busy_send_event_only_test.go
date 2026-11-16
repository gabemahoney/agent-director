package api_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/busysend"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// busyRoute is a route eligible at dest that records each issued prompt,
// runs before (when set) as its own earlier callback and observes out.
type busyRoute struct {
	dest   store.BusySendDestination
	out    busysend.Disposition
	before func(busysend.Prepared)
	issued []busysend.Prepared
}

func (r *busyRoute) Eligibility(context.Context, busysend.Event) (busysend.Eligibility, error) {
	return busysend.Eligibility{Destination: r.dest}, nil
}

func (r *busyRoute) Measure(body []byte) int64 { return int64(len(body)) }

func (r *busyRoute) Authorize(context.Context, busysend.Prepared) (busysend.Authority, error) {
	return busysend.Authority{}, nil
}

func (r *busyRoute) Issue(_ context.Context, p busysend.Prepared) busysend.Disposition {
	r.issued = append(r.issued, p)
	if r.before != nil {
		r.before(p)
	}
	return r.out
}

// busyDestination is the destination of id's published current Launch, with
// its recorded Claude route and session.
func busyDestination(t *testing.T, e *killEnv, id, session string) store.BusySendDestination {
	t.Helper()
	l, err := e.st.GetBusySendPublishedLaunch(id)
	if err != nil {
		t.Fatal(err)
	}
	return store.BusySendDestination{LaunchID: l.LaunchID, LaunchToken: l.LaunchToken, Provider: l.Provider, SessionID: session,
		RouteKind: store.BusySendRouteClaudeHooks}
}

// deliverBusy runs one eligible turn-end event for id on e's store and clock,
// which each issue commit also reads (store.BusySendIssueRequest.Now).
func deliverBusy(t *testing.T, e *killEnv, id string, route *busyRoute) busysend.Outcome {
	t.Helper()
	c := busysend.Coordinator{Store: e.st, Route: route, Budgets: busysend.Budgets{Issue: time.Minute, Job: time.Minute}, Now: e.clock.Now}
	out, err := c.Deliver(context.Background(), busysend.Event{InstanceID: id, Boundary: store.BusySendBoundaryTurnEnd})
	if err != nil {
		t.Fatalf("Deliver(%s): %v", id, err)
	}
	return out
}

// forceWake runs one forced wake of id's running force on e's store and clock
// (busysend.Coordinator.DeliverForcePrompt, the route's driver of the force's
// frozen next prompt), with minute-long budgets the force's own recorded
// deadlines clip.
func forceWake(t *testing.T, e *killEnv, id string, route *busyRoute) busysend.Outcome {
	t.Helper()
	c := busysend.Coordinator{Store: e.st, Route: route, Budgets: busysend.Budgets{Issue: time.Minute, Job: time.Minute}, Now: e.clock.Now}
	out, err := c.DeliverForcePrompt(context.Background(), busysend.Event{InstanceID: id, Boundary: store.BusySendBoundaryForce})
	if err != nil {
		t.Fatalf("DeliverForcePrompt(%s): %v", id, err)
	}
	return out
}

// TestBusySendEventOnly (SR-2.2, SR-3.1, SR-3.2): Held work whose wake hint
// cannot be touched stays Held through get, status, get-delivery, sweep, a
// store reopen and 400 idle days: nothing claims it, no issuer is recorded and
// nothing is typed into the live agent's pane. Only the next eligible event
// delivers it.
func TestBusySendEventOnly(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	c, _ := e.client(t)
	session := uuid.NewString()
	r := e.seedRow(t, killRowSpec{SessionID: session})
	seedBusyCovered(t, e, r.ID)
	if err := apitest.SeedBusySendRoute(e.dbPath, r.ID, "claude_hooks", filepath.Join(t.TempDir(), "gone", "wake")); err != nil {
		t.Fatal(err)
	}
	held := busyAdmit(t, c, api.SendKeysParams{InstanceID: r.ID, Text: "hello", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller),
		DeliveryRequestID: "e-1"})
	facts := busyFacts(t, e.dbPath, r.ID)
	lookup := api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "e-1"}
	observe := func(c *api.Client) {
		t.Helper()
		row, err := c.Get(r.ID)
		if err != nil || row.Delivery == nil || row.Delivery.HeldCount != 1 || row.Delivery.HandoverCount != 0 {
			t.Errorf("get = %+v, %v; want one Message Held and no hand-over", row.Delivery, err)
		}
		if _, err := c.Status(r.ID); err != nil {
			t.Error(err)
		}
		if got, err := c.GetDelivery(lookup); err != nil || got.Result == nil || !reflect.DeepEqual(*got.Result, held) {
			t.Errorf("get-delivery = %+v, %v; want the admitted Result %+v", got, err, held)
		}
		if _, err := c.Sweep(context.Background()); err != nil {
			t.Error(err)
		}
	}
	observe(c)
	e.clock.Advance(400 * 24 * time.Hour)
	reopened, _ := e.client(t)
	observe(reopened)
	if got := busyFacts(t, e.dbPath, r.ID); !reflect.DeepEqual(got, facts) {
		t.Errorf("busy-send core after reads, sweep, reopen and idle time = %+v; want unchanged %+v", got, facts)
	}
	dest := busyDestination(t, e, r.ID, session)
	if fence := storefix.ReadLaunchFence(t, e.st, dest.LaunchID); len(fence.Processes) != 0 || len(fence.Prompts) != 0 {
		t.Errorf("Launch fence = %+v; want no issuer and no issued prompt", fence)
	}
	input := func() int {
		return len(e.rec.SocketCallsOf(tmux.CallSendText)) + len(e.rec.SocketCallsOf(tmux.CallSendEnter)) + len(e.rec.SocketCallsOf(tmux.CallSendKey))
	}
	if n := input(); n != 0 {
		t.Errorf("%d pane inputs; want none", n)
	}

	route := &busyRoute{dest: dest, out: busysend.Disposition{Output: store.BusySendOutputComplete, Order: store.BusySendOrderEmissionComplete,
		UnprovenBoundary: true}}
	if out := deliverBusy(t, e, r.ID, route); out.Kind != busysend.OutcomeIssued || len(route.issued) != 1 ||
		string(route.issued[0].Body) != "[agent-director: message 1 of 1]\nhello" {
		t.Fatalf("eligible event = %+v issuing %+v; want the Held Message issued alone", out, route.issued)
	}
	got, err := reopened.GetDelivery(lookup)
	if err != nil || got.Result == nil || !got.Result.Final || got.Result.Reason != "handed_over_not_proven" || got.Result.SessionID != session {
		t.Errorf("get-delivery after the event = %+v, %v; want Final handed_over_not_proven on session %s", got, err, session)
	}
	if n := input(); n != 0 {
		t.Errorf("%d pane inputs; want none: the route, not the pane, issues", n)
	}
}

// TestBusySendForceEventOnly (SR-3.1, SR-3.2, SR-4.3, SR-5.6; M2 P-C4.4; MI
// decision 10): on two live agents, a pending force of an unconfirmed Message
// and a force whose call passed its 15 s deadline without recording its end
// keep their facts through get, status, get-many, list, get-force,
// get-delivery, sweep, a reopen and 400 idle days. get and status show them
// as delivery.force: pending (awaiting_admission, carrying no pair or Result)
// and running (reason "", no end); nothing settles or releases them, issues
// or types. A force refused meanwhile, handover_in_progress, never replaces
// the pending force there; once the agent's own sighting settles it
// handed_over, it stays delivery.force over a later refusal
// (no_held_messages).
func TestBusySendForceEventOnly(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	c, _ := e.client(t)
	pendingSession := uuid.NewString()
	pending, overdue := e.seedRow(t, killRowSpec{SessionID: pendingSession}), e.seedRow(t, killRowSpec{SessionID: uuid.NewString()})
	ids := []string{pending.ID, overdue.ID}
	seedBusyCovered(t, e, ids...)
	for _, id := range ids {
		busyWake(t, e, id)
	}
	busyAdmit(t, c, api.SendKeysParams{InstanceID: pending.ID, Text: "unconfirmed"})
	busyAdmit(t, c, api.SendKeysParams{InstanceID: overdue.ID, Text: "overdue", ConfirmedDelivery: ptr(true), CallerUUID: ptr(busyCaller),
		DeliveryRequestID: "o-1"})
	claim := func(id string) store.BusySendForceRef {
		t.Helper()
		pub, err := e.st.GetBusySendPublishedLaunch(id)
		if err != nil {
			t.Fatal(err)
		}
		cl, err := e.st.ClaimBusySendForce(context.Background(), store.BusySendForceClaimRequest{InstanceID: id, Launch: pub,
			ForceID: uuid.NewString(), OwnerCallID: uuid.NewString(), ActiveBudget: 12 * time.Second, CleanupBudget: 3 * time.Second, At: e.clock.Now()})
		if err != nil || cl.Handover == nil {
			t.Fatalf("force claim on %s = %+v, %v; want running", id, cl, err)
		}
		return cl.Ref
	}
	// The pending force's call issued its one prompt and ended before the
	// prompt's admission; the other call never reached its end.
	route := &busyRoute{dest: busyDestination(t, e, pending.ID, pendingSession), out: forceComplete}
	ref := claim(pending.ID)
	if out := forceWake(t, e, pending.ID, route); out.Kind != busysend.OutcomeIssued {
		t.Fatalf("forced wake = %+v; want its prompt issued", out)
	}
	if end, err := e.st.EndBusySendForceCall(context.Background(), ref, e.clock.Now()); err != nil || end.Force.Outcome != store.BusySendForcePending {
		t.Fatalf("call end = %+v, %v; want pending", end, err)
	}
	claim(overdue.ID)
	e.clock.Advance(15*time.Second + time.Millisecond)

	shown := func(c *api.Client) map[string]api.ForceResult {
		t.Helper()
		many, err := c.GetMany(ids)
		if err != nil || len(many.Spawns) != len(ids) {
			t.Fatalf("get-many = %+v, %v", many, err)
		}
		out := map[string]api.ForceResult{}
		for i, id := range ids {
			row, err := c.Get(id)
			if err != nil || row.Delivery == nil || row.Delivery.Force == nil {
				t.Fatalf("get %s delivery = %+v, %v; want a force", id, row.Delivery, err)
			}
			f := *row.Delivery.Force
			status, err := c.Status(id)
			if err != nil || status.Delivery == nil || !reflect.DeepEqual(status.Delivery.Force, &f) ||
				many.Spawns[i].Delivery == nil || !reflect.DeepEqual(many.Spawns[i].Delivery.Force, &f) {
				t.Errorf("status %+v, %v and get-many %+v delivery.force; want get's %+v", status.Delivery, err, many.Spawns[i].Delivery, f)
			}
			if got, err := c.GetForce(api.GetForceParams{ForceID: f.ForceID}); err != nil || got.ForceResult == nil || !reflect.DeepEqual(*got.ForceResult, f) {
				t.Errorf("get-force = %+v, %v; want found %+v", got, err, f)
			}
			out[id] = f
		}
		if _, err := c.List(api.ListParams{}); err != nil {
			t.Error(err)
		}
		if got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "o-1"}); err != nil || got.Lookup != "found" {
			t.Errorf("get-delivery o-1 = %+v, %v; want found", got, err)
		}
		if _, err := c.Sweep(context.Background()); err != nil {
			t.Error(err)
		}
		return out
	}
	first := shown(c)
	p, o := first[pending.ID], first[overdue.ID]
	if p.Outcome != "pending" || p.Reason != "awaiting_admission" || p.EndedAt != nil || len(p.CarriedPairs) != 0 || len(p.CarriedResults) != 0 {
		t.Errorf("pending delivery.force = %+v; want pending awaiting_admission, no end, carrying no pair or Result", p)
	}
	if o.Outcome != "running" || o.Reason != "" || o.EndedAt != nil || len(o.CarriedResults) != 1 ||
		!reflect.DeepEqual(o.CarriedPairs, []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: "o-1"}}) {
		t.Errorf("overdue delivery.force = %+v; want running, reason \"\", no end, carrying o-1", o)
	}
	facts := map[string]apitest.BusySendFacts{}
	for _, id := range ids {
		facts[id] = busyFacts(t, e.dbPath, id)
	}
	fence := storefix.ReadLaunchFence(t, e.st, route.dest.LaunchID)
	e.clock.Advance(400 * 24 * time.Hour)
	reopened, _ := e.client(t)
	if got := shown(reopened); !reflect.DeepEqual(got, first) {
		t.Errorf("forces after reads, sweep, a reopen and 400 days = %+v; want unchanged %+v", got, first)
	}
	for _, id := range ids {
		if got := busyFacts(t, e.dbPath, id); !reflect.DeepEqual(got, facts[id]) {
			t.Errorf("%s's busy-send core = %+v; want unchanged %+v", id, got, facts[id])
		}
	}
	if got := storefix.ReadLaunchFence(t, e.st, route.dest.LaunchID); !reflect.DeepEqual(got, fence) || len(route.issued) != 1 {
		t.Errorf("Launch fence = %+v after %d prompts; want unchanged %+v after the one", got, len(route.issued), fence)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallSendText)) + len(e.rec.SocketCallsOf(tmux.CallSendEnter)) + len(e.rec.SocketCallsOf(tmux.CallSendKey)); n != 0 {
		t.Errorf("%d pane inputs; want none", n)
	}

	refuse := func(reason string) {
		t.Helper()
		res, err := reopened.ForceDelivery(api.ForceDeliveryParams{InstanceID: pending.ID})
		if err != nil || res.ForceResult.Outcome != "refused" || res.ForceResult.Reason != reason {
			t.Errorf("force-delivery = %+v, %v; want refused %s", res, err, reason)
		}
	}
	refuse("handover_in_progress")
	if got := shown(reopened)[pending.ID]; !reflect.DeepEqual(got, p) {
		t.Errorf("delivery.force after a refused force = %+v; want the pending force %+v", got, p)
	}
	issued := route.issued[0]
	if _, err := e.st.RecordBusySendEvidence(context.Background(), store.BusySendPromptEvidence{HandoverID: issued.HandoverID,
		LaunchID: issued.Destination.LaunchID, ConnectionGeneration: issued.Destination.ConnectionGeneration,
		Admission: store.BusySendAdmissionPromptSighting, At: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	settled := shown(reopened)[pending.ID]
	if settled.ForceID != p.ForceID || settled.Outcome != "handed_over" || settled.Reason != "admitted" || settled.EndedAt == nil {
		t.Errorf("delivery.force after the agent's sighting = %+v; want %s handed_over", settled, p.ForceID)
	}
	refuse("no_held_messages")
	if got := shown(reopened)[pending.ID]; !reflect.DeepEqual(got, settled) {
		t.Errorf("delivery.force after a later refusal = %+v; want the force that began a hand-over %+v", got, settled)
	}
}
