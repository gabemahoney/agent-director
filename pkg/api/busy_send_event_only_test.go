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
	if fence, err := e.st.ReadBusySendLaunchFence(context.Background(), dest.LaunchID); err != nil || len(fence.Processes) != 0 || len(fence.Prompts) != 0 {
		t.Errorf("Launch fence = %+v, %v; want no issuer and no issued prompt", fence, err)
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
