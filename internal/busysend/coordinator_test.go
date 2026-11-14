package busysend_test

// The provider-neutral coordinator (Task t2.xis.oo.xe) driven by one fake
// route over real SQLite custody: the invocation-order part of M2 P-C4.3 and
// the coordinator side of M1's order-barrier release and Held-to-Final
// progress, as bounded, conditional event traces. Store guards, partition
// arithmetic, transaction atomicity and evidence repeats are internal/store's
// (t3.xis.oo.xe.58); Claude's actual hook output is Task 3's.

import (
	"context"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/busysend"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// coordCaller is the Caller UUID of every Message these tests admit.
const coordCaller = "6a1f3c2e-9b7d-4e5a-8c1b-2d3e4f5a6b7c"

// helper is the issuer the fake route names for every prompt.
var helper = &store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: 4242, StartTime: "4242"}

// delivered is complete output with Claude's qualified order evidence, from a
// route that can issue the group's next prompt in the same invocation.
var delivered = busysend.Disposition{Output: store.BusySendOutputComplete, Order: store.BusySendOrderEmissionComplete,
	UnprovenBoundary: true, More: true}

// fakeRoute is the one Route here: it records every issued prompt, and its
// optional hooks run outside its lock (issue's nil default is delivered).
type fakeRoute struct {
	elig        busysend.Eligibility
	eligibility func()
	authorize   func(context.Context, busysend.Prepared) busysend.Authority
	issue       func(context.Context, busysend.Prepared) busysend.Disposition
	observe     func(string, context.Context)

	mu     sync.Mutex
	issued []busysend.Prepared
}

func (r *fakeRoute) Eligibility(ctx context.Context, _ busysend.Event) (busysend.Eligibility, error) {
	if r.observe != nil {
		r.observe("eligibility", ctx)
	}
	if r.eligibility != nil {
		r.eligibility()
	}
	return r.elig, nil
}

func (r *fakeRoute) Measure(body []byte) int64 {
	limits, _ := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendWaitTurn)
	return int64(len(body)) + limits.WrapperBytes
}

func (r *fakeRoute) Authorize(ctx context.Context, p busysend.Prepared) (busysend.Authority, error) {
	if r.observe != nil {
		r.observe("authorize", ctx)
	}
	if r.authorize != nil {
		return r.authorize(ctx, p), nil
	}
	return busysend.Authority{Issuer: helper}, nil
}

func (r *fakeRoute) Issue(ctx context.Context, p busysend.Prepared) busysend.Disposition {
	if r.observe != nil {
		r.observe("output", ctx)
	}
	r.mu.Lock()
	r.issued = append(r.issued, p)
	r.mu.Unlock()
	if r.issue != nil {
		return r.issue(ctx, p)
	}
	return delivered
}

// prepared is every prompt Issue was handed, in call order.
func (r *fakeRoute) prepared() []busysend.Prepared {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.issued)
}

// storeSeam is the real Store, recording each committed issue and, when
// observe is set, the context of each call it overrides.
type storeSeam struct {
	*store.Store
	observe func(string, context.Context)

	mu     sync.Mutex
	issues []store.BusySendIssue
}

func (s *storeSeam) see(name string, ctx context.Context) {
	if s.observe != nil {
		s.observe(name, ctx)
	}
}

func (s *storeSeam) ClaimBusySendHandover(ctx context.Context, req store.BusySendClaimRequest) (store.BusySendClaim, error) {
	s.see("claim", ctx)
	return s.Store.ClaimBusySendHandover(ctx, req)
}

func (s *storeSeam) RecordBusySendCompletion(ctx context.Context, c store.BusySendCompletion) (store.BusySendCompletionResult, error) {
	s.see("completion", ctx)
	return s.Store.RecordBusySendCompletion(ctx, c)
}

func (s *storeSeam) ReleaseBusySendClaims(ctx context.Context, r store.BusySendReleaseRequest) (store.BusySendRelease, error) {
	s.see("release", ctx)
	return s.Store.ReleaseBusySendClaims(ctx, r)
}

func (s *storeSeam) RecordBusySendWait(ctx context.Context, w store.BusySendWaitRequest) (bool, error) {
	s.see("wait", ctx)
	return s.Store.RecordBusySendWait(ctx, w)
}

func (s *storeSeam) CommitBusySendIssue(ctx context.Context, req store.BusySendIssueRequest) (store.BusySendIssue, error) {
	s.see("issue commit", ctx)
	out, err := s.Store.CommitBusySendIssue(ctx, req)
	if err == nil {
		s.mu.Lock()
		s.issues = append(s.issues, out)
		s.mu.Unlock()
	}
	return out, err
}

func (s *storeSeam) committed() []store.BusySendIssue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.issues)
}

// coordEnv is a covered, waiting Claude agent with a recorded route and Turn
// Start counter 3, the fake route eligible at its destination, and a clock.
type coordEnv struct {
	path, id string
	st       *store.Store
	seam     *storeSeam
	dest     store.BusySendDestination
	route    *fakeRoute
	clock    *tmuxfix.Clock
	start    time.Time
}

func newCoordEnv(t *testing.T) *coordEnv {
	t.Helper()
	st, path := storefix.OpenTempStore(t)
	session := uuid.NewString()
	id, err := apitest.SeedSpawn(path, "", store.StateWaiting, "", "off", session, false, apitest.WithTurnStarts(3))
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendCoverage(path, id, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendRoute(path, id, string(store.BusySendRouteClaudeHooks), filepath.Join(t.TempDir(), "wake")); err != nil {
		t.Fatal(err)
	}
	l, err := st.GetBusySendPublishedLaunch(id)
	if err != nil {
		t.Fatal(err)
	}
	dest := store.BusySendDestination{LaunchID: l.LaunchID, LaunchToken: l.LaunchToken, Provider: l.Provider, SessionID: session,
		RouteKind: store.BusySendRouteClaudeHooks}
	start := time.Now().UTC().Truncate(time.Millisecond)
	return &coordEnv{path: path, id: id, st: st, seam: &storeSeam{Store: st}, dest: dest,
		route: &fakeRoute{elig: busysend.Eligibility{Destination: dest}}, clock: tmuxfix.NewClock(start), start: start}
}

// deliver runs one eligible turn-end event (1 min issue phase, 2 min job) on
// e.clock, which each issue commit also reads (store.BusySendIssueRequest.Now).
func (e *coordEnv) deliver(ctx context.Context) (busysend.Outcome, error) {
	c := busysend.Coordinator{Store: e.seam, Route: e.route, Budgets: busysend.Budgets{Issue: time.Minute, Job: 2 * time.Minute},
		Now: e.clock.Now}
	return c.Deliver(ctx, busysend.Event{InstanceID: e.id, Boundary: store.BusySendBoundaryTurnEnd})
}

// admit admits text as a confirmed Message with Message ID request.
func (e *coordEnv) admit(t *testing.T, request, text string) {
	t.Helper()
	confirmed := true
	if _, err := e.st.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: e.id, Text: []byte(text), CallerUUID: coordCaller,
		DeliveryRequestID: request, Confirmed: &confirmed}); err != nil {
		t.Fatalf("admit %s: %v", request, err)
	}
}

// sight records the agent's own sighting of a hand-over's first prompts, the
// evidence it took them (SR-3.3), so a later turn end may claim again.
func (e *coordEnv) sight(t *testing.T, handover string, prompts int) {
	t.Helper()
	for k := range prompts {
		if _, err := e.st.RecordBusySendEvidence(context.Background(), store.BusySendPromptEvidence{HandoverID: handover, PromptIndex: k,
			LaunchID: e.dest.LaunchID, ConnectionGeneration: e.dest.ConnectionGeneration,
			Admission: store.BusySendAdmissionPromptSighting}); err != nil {
			t.Fatalf("sighting of prompt %d: %v", k, err)
		}
	}
}

// result is the stored Result of Message ID request.
func (e *coordEnv) result(t *testing.T, request string) store.BusySendResult {
	t.Helper()
	l, err := e.st.ReadBusySendResult(coordCaller, request)
	if err != nil || l.Result == nil {
		t.Fatalf("ReadBusySendResult(%s) = %+v, %v", request, l, err)
	}
	return *l.Result
}

// places is each Message's custody, in accepted order.
func (e *coordEnv) places(t *testing.T) []string {
	t.Helper()
	facts, err := apitest.ReadBusySendFacts(e.path, e.id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range facts.Messages {
		out = append(out, m.Place)
	}
	return out
}

// reopen closes the store and opens it again, as a later process would.
func (e *coordEnv) reopen(t *testing.T) {
	t.Helper()
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.st, e.seam = st, &storeSeam{Store: st}
}

// TestCoordinatorDeliversFrozenPrompts (SR-2.2 to SR-2.5; M2 P-C4.3 output
// order; M1 Held-to-Final progress): an eligible event claims the Held prefix
// at its cutoff and issues each frozen prompt once, in order, with its exact
// bytes, to the captured destination, each only once its issue is durably
// committed. An eligible callback meanwhile owns and issues nothing; a send
// admitted meanwhile waits outside the cutoff and the next event delivers it;
// an event with nothing Held issues nothing.
func TestCoordinatorDeliversFrozenPrompts(t *testing.T) {
	t.Parallel()
	e := newCoordEnv(t)
	b, c := strings.Repeat("b", 4000), strings.Repeat("c", 4000)
	e.admit(t, "m1", "first")
	e.admit(t, "m2", b)
	e.admit(t, "m3", c)
	var callback busysend.Outcome
	e.route.issue = func(ctx context.Context, p busysend.Prepared) busysend.Disposition {
		// M2 P-C4.3: at its first possible output the prompt's issue is
		// durable. Separate reads find it issued with nothing observed yet,
		// its captured destination, its owned-input obligation and issuer;
		// the commit captured the Turn Start counter and no running turn.
		h, err := e.st.ReadBusySendHandover(ctx, p.HandoverID)
		if err != nil {
			t.Errorf("ReadBusySendHandover at output: %v", err)
			return delivered
		}
		if got := h.Prompts[p.PromptIndex]; got.Emission != store.BusySendEmissionIssued || got.Transport != "" ||
			h.Destination != e.dest || h.ExpectedTurnStarts != 3 {
			t.Errorf("at prompt %d's output its record is %+v, hand-over destination %+v, Turn Starts %d; want issued with nothing observed, %+v, 3",
				p.PromptIndex, got, h.Destination, h.ExpectedTurnStarts, e.dest)
		}
		fence, err := e.st.ReadBusySendLaunchFence(ctx, e.dest.LaunchID)
		want := []store.BusySendFencedPrompt{{HandoverID: p.HandoverID, PromptIndex: p.PromptIndex, Destination: e.dest,
			Emission: store.BusySendEmissionIssued}}
		issuer := store.BusySendLaunchProcess{Role: helper.Role, PID: helper.PID, StartTime: helper.StartTime}
		if err != nil || !reflect.DeepEqual(fence.Prompts, want) || !slices.Contains(fence.Processes, issuer) {
			t.Errorf("Launch fence at prompt %d's output = %+v, %v; want only its own obligation %+v and the issuer %+v",
				p.PromptIndex, fence, err, want, issuer)
		}
		if issues := e.seam.committed(); len(issues) != len(e.route.prepared()) || issues[len(issues)-1].TurnStarts != 3 ||
			issues[len(issues)-1].RunningTurn {
			t.Errorf("issue commits before output %d = %+v; want one per output, at Turn Start 3 and no running turn",
				len(e.route.prepared()), issues)
		}
		if len(e.route.prepared()) == 1 {
			e.admit(t, "late", "late")
			done := make(chan busysend.Outcome, 1)
			go func() {
				out, err := e.deliver(context.Background())
				if err != nil {
					t.Errorf("concurrent callback: %v", err)
				}
				done <- out
			}()
			select {
			case callback = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the concurrent callback did not return within 10 s")
			}
		}
		return delivered
	}
	ctx := context.Background()
	out, err := e.deliver(ctx)
	issued := e.route.prepared()
	if err != nil || len(issued) != 2 {
		t.Fatalf("Deliver = %+v, %v with %d prompts issued; want the frozen group's 2", out, err, len(issued))
	}
	if want := (busysend.Outcome{Kind: busysend.OutcomeIssued, HandoverID: issued[0].HandoverID,
		Issued: []busysend.Disposition{delivered, delivered}}); !reflect.DeepEqual(out, want) {
		t.Errorf("Deliver = %+v; want %+v", out, want)
	}
	if callback.Kind != busysend.OutcomeNoIssue || callback.Wait != store.BusySendWaitHandoverInProgress {
		t.Errorf("concurrent callback = %+v; want no issue, waiting handover_in_progress", callback)
	}
	if r := e.result(t, "late"); r.Class != store.BusySendClassHeld || r.Wait == nil || r.Wait.Reason != store.BusySendWaitReadinessUnknown {
		t.Errorf("the send past the cutoff = %+v; want Held, waiting readiness_unknown", r)
	}

	e.sight(t, issued[0].HandoverID, 2)
	if out, err := e.deliver(ctx); err != nil || out.Kind != busysend.OutcomeIssued {
		t.Fatalf("next event = %+v, %v; want the late send issued", out, err)
	}
	if out, err := e.deliver(ctx); err != nil || out.Kind != busysend.OutcomeNoIssue {
		t.Errorf("event with nothing Held = %+v, %v; want no issue", out, err)
	}
	issued = e.route.prepared()
	if len(issued) != 3 {
		t.Fatalf("%d prompts issued; want 3", len(issued))
	}
	deadline := e.start.Add(time.Minute)
	prompt := func(handover string, index, count int, body string) busysend.Prepared {
		return busysend.Prepared{HandoverID: handover, PromptIndex: index, PromptCount: count, Destination: e.dest,
			Body: []byte(body), RenderedBytes: e.route.Measure([]byte(body)), IssueDeadline: deadline}
	}
	want := []busysend.Prepared{
		prompt(issued[0].HandoverID, 0, 2, "[agent-director: message 1 of 2]\nfirst\n\n[agent-director: message 2 of 2]\n"+b),
		prompt(issued[0].HandoverID, 1, 2, "[agent-director: message 1 of 1]\n"+c),
		prompt(issued[2].HandoverID, 0, 1, "[agent-director: message 1 of 1]\nlate"),
	}
	if !reflect.DeepEqual(issued, want) || issued[2].HandoverID == issued[0].HandoverID {
		for i := range issued {
			t.Errorf("issued prompt %d: %s %d/%d to %+v, %d bytes (%d measured), deadline %v", i, issued[i].HandoverID,
				issued[i].PromptIndex, issued[i].PromptCount, issued[i].Destination, len(issued[i].Body), issued[i].RenderedBytes,
				issued[i].IssueDeadline)
		}
		t.Errorf("want the frozen group's two prompts in order, then the late send alone in a new hand-over, each once with its exact bytes")
	}
	for _, request := range []string{"m1", "m2", "m3", "late"} {
		r := e.result(t, request)
		if r.Class != store.BusySendClassSentUnconfirmed || !r.Final || r.Reason != "handed_over_not_proven" ||
			r.LaunchToken == nil || *r.LaunchToken != e.dest.LaunchToken || r.SessionID != e.dest.SessionID {
			t.Errorf("%s Result = %+v; want Final sent_unconfirmed handed_over_not_proven on the captured Launch and session", request, r)
		}
	}
}

// TestCoordinatorKeepsOrderBarrier (SR-2.4, SR-2.5; M1 RecNs and the order
// barrier): only qualified order evidence lets an invocation go on to its next
// prompt. A partial write, or complete output whose completion is lost with
// its worker, keeps the issued prompt's barrier: no later eligible event,
// after a reopen and past every deadline, outputs anything. Proof that nothing
// was submitted returns both Messages to Held for the next event to deliver.
func TestCoordinatorKeepsOrderBarrier(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		first    func(cancel context.CancelFunc) busysend.Disposition
		kind     busysend.OutcomeKind
		released bool
	}{
		{"partial write", func(context.CancelFunc) busysend.Disposition {
			return busysend.Disposition{Output: store.BusySendOutputPartial, More: true}
		}, busysend.OutcomeIssued, false},
		{"completion lost with its worker", func(cancel context.CancelFunc) busysend.Disposition {
			cancel()
			return delivered
		}, busysend.OutcomeUncertain, false},
		{"proven not submitted", func(context.CancelFunc) busysend.Disposition {
			return busysend.Disposition{Output: store.BusySendOutputNotSubmitted, More: true}
		}, busysend.OutcomeIssued, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newCoordEnv(t)
			e.admit(t, "a", strings.Repeat("a", 4000))
			e.admit(t, "b", strings.Repeat("b", 4000))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.route.issue = func(context.Context, busysend.Prepared) busysend.Disposition { return tc.first(cancel) }
			out, err := e.deliver(ctx)
			if out.Kind != tc.kind || (err != nil && tc.kind != busysend.OutcomeUncertain) || len(e.route.prepared()) != 1 {
				t.Fatalf("first event = %+v, %v with %d prompts issued; want %s after prompt 0 alone", out, err, len(e.route.prepared()), tc.kind)
			}
			wantA := store.BusySendClassSentUnconfirmed
			if tc.released {
				wantA = store.BusySendClassHeld
			}
			if a, b := e.result(t, "a"), e.result(t, "b"); a.Class != wantA || a.Final || b.Class != store.BusySendClassHeld {
				t.Errorf("Results after the first event: a %+v, b %+v; want a non-Final %s, b held", a, b, wantA)
			}

			e.reopen(t)
			e.clock.Advance(time.Hour)
			e.route.issue = nil
			later, err := e.deliver(context.Background())
			issued := e.route.prepared()
			if err != nil {
				t.Fatalf("later event: %v", err)
			}
			if tc.released {
				if later.Kind != busysend.OutcomeIssued || len(issued) != 3 || issued[1].HandoverID == issued[0].HandoverID ||
					!reflect.DeepEqual(issued[1].Body, issued[0].Body) || issued[2].PromptIndex != 1 {
					t.Errorf("later event = %+v with %d prompts issued; want both Messages delivered again in a new hand-over", later, len(issued))
				}
				for _, request := range []string{"a", "b"} {
					if r := e.result(t, request); !r.Final || r.Class != store.BusySendClassSentUnconfirmed {
						t.Errorf("%s Result = %+v; want Final sent_unconfirmed", request, r)
					}
				}
				return
			}
			if later.Kind != busysend.OutcomeNoIssue || later.Wait != store.BusySendWaitHandoverInProgress || len(issued) != 1 {
				t.Errorf("later event = %+v with %d prompts issued in all; want no issue behind the barrier", later, len(issued))
			}
			fence, err := e.st.ReadBusySendLaunchFence(context.Background(), e.dest.LaunchID)
			if err != nil || len(fence.Prompts) != 1 || fence.Prompts[0].HandoverID != issued[0].HandoverID || fence.Prompts[0].PromptIndex != 0 {
				t.Errorf("Launch fence = %+v, %v; want prompt 0's unresolved issue alone", fence, err)
			}
			if got := e.places(t); !slices.Equal(got, []string{"issued", "held"}) {
				t.Errorf("custody %v; want a issued and b Held", got)
			}
		})
	}
}

// TestCoordinatorRevalidatesAtIssue (SR-2.4; M1 Revalidate): the agent's
// session rotates after the route prepared a prompt and before its issue
// commit. The commit refuses, so nothing is output to the captured session and
// the claim returns to Held; the old session's next event is stale, and only
// an event from the current session delivers, there.
func TestCoordinatorRevalidatesAtIssue(t *testing.T) {
	t.Parallel()
	e := newCoordEnv(t)
	e.admit(t, "m1", "hello")
	rotated := e.dest
	rotated.SessionID = uuid.NewString()
	e.route.authorize = func(_ context.Context, p busysend.Prepared) busysend.Authority {
		if p.Destination == e.dest {
			if applied := apitest.ApplyAgentHook(t, e.path, e.id, "SessionStart", rotated.SessionID); !applied.Applied {
				t.Errorf("SessionStart %s = %+v; want applied", rotated.SessionID, applied)
			}
		}
		return busysend.Authority{Issuer: helper}
	}
	ctx := context.Background()
	out, err := e.deliver(ctx)
	if err != nil || out.Kind != busysend.OutcomeNoIssue || out.Wait != store.BusySendWaitReadinessUnknown || len(e.route.prepared()) != 0 {
		t.Fatalf("Deliver = %+v, %v with %d prompts issued; want no issue, waiting readiness_unknown", out, err, len(e.route.prepared()))
	}
	if r, got := e.result(t, "m1"), e.places(t); r.Wait == nil || r.Wait.Reason != store.BusySendWaitReadinessUnknown ||
		r.LaunchToken != nil || !slices.Equal(got, []string{"held"}) {
		t.Errorf("after the refusal: Result %+v, custody %v; want Held, unbound, waiting readiness_unknown", r, got)
	}
	if out, err := e.deliver(ctx); err != nil || out.Kind != busysend.OutcomeStale || len(e.route.prepared()) != 0 {
		t.Errorf("old session's event = %+v, %v; want stale, nothing issued", out, err)
	}
	e.route.elig.Destination = rotated
	if out, err := e.deliver(ctx); err != nil || out.Kind != busysend.OutcomeIssued {
		t.Fatalf("current session's event = %+v, %v; want issued", out, err)
	}
	if issued := e.route.prepared(); len(issued) != 1 || issued[0].Destination != rotated {
		t.Errorf("issued %+v; want one prompt, to the current session %+v", issued, rotated)
	}
	if r := e.result(t, "m1"); !r.Final || r.SessionID != rotated.SessionID {
		t.Errorf("Result = %+v; want Final on session %s", r, rotated.SessionID)
	}
}

// TestCoordinatorKeepsOriginalBudgets (SR-3.2; M1 Expire and ReleaseExpired;
// xe-repair-2.md F1, F3): an invocation's issue deadline is fixed when it
// starts and never renewed. Once its clock passes that deadline, whether in
// eligibility or during a prompt's output, it issues no further prompt, and a
// canceled caller issues nothing. An issued
// prompt's own completion still secures its order after a later event expired
// the rest. What was never issued waits Held for the next eligible event:
// at once, or for a claim its job abandoned, once that claim's own deadline
// has passed.
func TestCoordinatorKeepsOriginalBudgets(t *testing.T) {
	t.Parallel()
	late := time.Minute + time.Second // past the issue phase, within the job
	once := func(f func()) func() {
		done := false
		return func() {
			if !done {
				done = true
				f()
			}
		}
	}
	for _, tc := range []struct {
		name    string
		arrange func(*testing.T, *coordEnv, context.CancelFunc)
		issued  int      // prompts the first event issues
		places  []string // custody after it
	}{
		{"deadline passes during prompt 0's output", func(_ *testing.T, e *coordEnv, _ context.CancelFunc) {
			e.route.issue = func(context.Context, busysend.Prepared) busysend.Disposition {
				e.clock.Advance(late)
				return delivered
			}
		}, 1, []string{"closed", "held"}},
		{"a later event expires the rest while prompt 0 is out (F1)", func(t *testing.T, e *coordEnv, _ context.CancelFunc) {
			e.route.issue = func(context.Context, busysend.Prepared) busysend.Disposition {
				if len(e.route.prepared()) == 1 {
					e.clock.Advance(late)
					if out, err := e.deliver(context.Background()); err != nil || out.Kind != busysend.OutcomeNoIssue ||
						out.Wait != store.BusySendWaitHandoverInProgress {
						t.Errorf("event during prompt 0's output = %+v, %v; want no issue, waiting handover_in_progress", out, err)
					}
				}
				return delivered
			}
		}, 1, []string{"closed", "held"}},
		{"eligibility outlasts the issue phase (F3)", func(_ *testing.T, e *coordEnv, _ context.CancelFunc) {
			e.route.eligibility = once(func() { e.clock.Advance(late) })
		}, 0, []string{"held", "held"}},
		{"caller cancels before the issue commit", func(_ *testing.T, e *coordEnv, cancel context.CancelFunc) {
			e.route.authorize = func(context.Context, busysend.Prepared) busysend.Authority {
				cancel()
				return busysend.Authority{Issuer: helper}
			}
		}, 0, []string{"claimed", "claimed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newCoordEnv(t)
			e.admit(t, "a", strings.Repeat("a", 4000))
			e.admit(t, "b", strings.Repeat("b", 4000))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tc.arrange(t, e, cancel)
			out, err := e.deliver(ctx)
			issued := e.route.prepared()
			if got := e.places(t); len(issued) != tc.issued || !slices.Equal(got, tc.places) {
				t.Fatalf("first event = %+v, %v: %d prompts issued, custody %v; want %d issued, custody %v",
					out, err, len(issued), got, tc.issued, tc.places)
			}
			if tc.issued > 0 && !issued[0].IssueDeadline.Equal(e.start.Add(time.Minute)) {
				t.Errorf("prompt 0's issue deadline %v; want the invocation's start plus its issue phase, %v",
					issued[0].IssueDeadline, e.start.Add(time.Minute))
			}
			if tc.places[0] != "claimed" && err != nil {
				t.Errorf("first event: %v", err)
			}
			if tc.places[0] == "claimed" {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("first event = %+v, %v; want the caller's cancellation", out, err)
				}
				// The abandoned claim holds until its own issue deadline.
				if out, err := e.deliver(context.Background()); err != nil || out.Kind != busysend.OutcomeNoIssue ||
					out.Wait != store.BusySendWaitHandoverInProgress || len(e.route.prepared()) != 0 {
					t.Errorf("event before the claim's deadline = %+v, %v; want no issue, waiting handover_in_progress", out, err)
				}
				e.clock.Advance(late)
			}
			if tc.issued > 0 {
				e.sight(t, issued[0].HandoverID, tc.issued)
			}
			if out, err := e.deliver(context.Background()); err != nil || len(e.route.prepared()) != 2 ||
				!slices.Equal(e.places(t), []string{"closed", "closed"}) {
				t.Errorf("next event = %+v, %v: %d prompts issued in all, custody %v; want each Message issued once and closed",
					out, err, len(e.route.prepared()), e.places(t))
			}
		})
	}
}

// TestCoordinatorBoundsEveryCall (SR-3.2; xe-repair-2.md F3): with no
// deadline from its caller, an invocation still bounds every call by phases
// fixed before its eligibility and never renewed: eligibility, the claim, the
// route's authority and the issue commit by the issue phase; the route's
// output by the job; completion, the release and the wait record by the job
// plus its cleanup allowance. An earlier caller deadline bounds them all.
func TestCoordinatorBoundsEveryCall(t *testing.T) {
	t.Parallel()
	b := busysend.Budgets{Issue: time.Minute, Job: 2 * time.Minute, Cleanup: 30 * time.Second}
	for _, tc := range []struct {
		name   string
		caller time.Duration // the caller's own deadline after entry; 0 for none
	}{{"unbounded caller", 0}, {"earlier caller deadline", 30 * time.Second}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newCoordEnv(t)
			e.admit(t, "a", strings.Repeat("a", 4000))
			e.admit(t, "b", strings.Repeat("b", 4000))
			var mu sync.Mutex
			var eligibilityAt time.Time
			seen := map[string][]time.Time{}
			observe := func(name string, ctx context.Context) {
				d, _ := ctx.Deadline()
				mu.Lock()
				defer mu.Unlock()
				if name == "eligibility" {
					eligibilityAt = time.Now()
				}
				seen[name] = append(seen[name], d)
			}
			e.route.observe, e.seam.observe = observe, observe
			// A partial write keeps the barrier, so this one invocation
			// records its completion, releases prompt 1 and records its wait.
			e.route.issue = func(context.Context, busysend.Prepared) busysend.Disposition {
				return busysend.Disposition{Output: store.BusySendOutputPartial, More: true}
			}
			ctx, entry := context.Background(), time.Now()
			if tc.caller > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, entry.Add(tc.caller))
				defer cancel()
			}
			c := busysend.Coordinator{Store: e.seam, Route: e.route, Budgets: b, Now: e.clock.Now}
			if out, err := c.Deliver(ctx, busysend.Event{InstanceID: e.id, Boundary: store.BusySendBoundaryTurnEnd}); err != nil ||
				out.Kind != busysend.OutcomeIssued {
				t.Fatalf("Deliver = %+v, %v; want prompt 0 issued", out, err)
			}
			for _, phase := range []struct {
				calls  []string
				budget time.Duration
			}{
				{[]string{"eligibility", "claim", "authorize", "issue commit"}, b.Issue},
				{[]string{"output"}, b.Job},
				{[]string{"completion", "release", "wait"}, b.Job + b.Cleanup},
			} {
				low, high := entry.Add(phase.budget), eligibilityAt.Add(phase.budget)
				if tc.caller > 0 {
					low, high = entry.Add(tc.caller), entry.Add(tc.caller)
				}
				for _, call := range phase.calls {
					if len(seen[call]) == 0 {
						t.Errorf("%s never ran", call)
					}
					for _, d := range seen[call] {
						if d.Before(low) || d.After(high) {
							t.Errorf("%s ran with deadline %v; want one fixed before eligibility, in [%v, %v]", call, d, low, high)
						}
					}
				}
			}
		})
	}
}

// TestCoordinatorIssueCommitBoundary (SR-2.4, SR-3.2; M2 P-C4.3; decision 9):
// the issue deadline is judged once, by the guard inside the issue commit.
// When the issue phase ends at the commit's BEGIN, before the guard reads the
// clock, nothing is issued and the claim returns to Held with the deadline
// wait. When it ends after the guard, before the commit returns, the confirmed
// commit authorizes its one Issue and the completion is recorded. A lost
// acknowledgement grants no output, and the landed issue keeps its barrier
// instead of being released as never issued. A later eligible event, after a
// reopen, delivers what is Held and outputs nothing behind a barrier.
func TestCoordinatorIssueCommitBoundary(t *testing.T) {
	t.Parallel()
	pastIssue := func(e *coordEnv) error {
		e.clock.Advance(2 * time.Minute)
		return nil
	}
	for _, tc := range []struct {
		name          string
		begin, commit func(*coordEnv) error // at the issue commit's BEGIN IMMEDIATE, and once SQLite committed it
		kind          busysend.OutcomeKind
		wait          store.BusySendWaitReason
		issued        int
		places        []string
		later         busysend.OutcomeKind // the reopened later event's outcome
		laterPlaces   []string
	}{
		{"phase ends before the guard", pastIssue, nil, busysend.OutcomeNoIssue, store.BusySendWaitReadinessUnknown, 0,
			[]string{"held", "held"}, busysend.OutcomeIssued, []string{"closed", "closed"}},
		{"phase ends after the guard", nil, pastIssue, busysend.OutcomeIssued, store.BusySendWaitReadinessUnknown, 1,
			[]string{"closed", "held"}, busysend.OutcomeIssued, []string{"closed", "closed"}},
		{"acknowledgement lost", nil, func(*coordEnv) error {
			return errors.New("the issue commit's acknowledgement was lost")
		}, busysend.OutcomeUncertain, "", 0, []string{"issued", "claimed"}, busysend.OutcomeNoIssue, []string{"issued", "held"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newCoordEnv(t)
			e.admit(t, "a", strings.Repeat("a", 4000))
			e.admit(t, "b", strings.Repeat("b", 4000))
			// The route's authority comes just before the issue commit, so the
			// next BEGIN IMMEDIATE and COMMIT are the issue commit's.
			var begin, commit atomic.Bool
			e.route.authorize = func(context.Context, busysend.Prepared) busysend.Authority {
				begin.Store(tc.begin != nil)
				commit.Store(tc.commit != nil)
				return busysend.Authority{Issuer: helper}
			}
			stop, err := store.ObserveSQLiteForTest(e.st, func(d driver.Driver) driver.Driver {
				return sqliteobserve.Wrap(d, sqliteobserve.Hooks{
					Before: func(_ context.Context, q string) error {
						if q == "BEGIN IMMEDIATE" && begin.CompareAndSwap(true, false) {
							return tc.begin(e)
						}
						return nil
					},
					AfterCommit: func(context.Context) error {
						if commit.CompareAndSwap(true, false) {
							return tc.commit(e)
						}
						return nil
					},
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := e.deliver(context.Background())
			if got := e.places(t); err != nil || out.Kind != tc.kind || out.Wait != tc.wait || len(e.route.prepared()) != tc.issued ||
				!slices.Equal(got, tc.places) {
				t.Fatalf("Deliver = %+v, %v: %d prompts issued, custody %v; want %s waiting %q, %d issued, custody %v",
					out, err, len(e.route.prepared()), got, tc.kind, tc.wait, tc.issued, tc.places)
			}
			if tc.wait != "" {
				first := []string{"a", "b"}[slices.Index(tc.places, "held")]
				if r := e.result(t, first); r.Wait == nil || r.Wait.Reason != tc.wait {
					t.Errorf("first Held Message %s's Result = %+v; want it waiting %s", first, r, tc.wait)
				}
			}
			if err := stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			e.reopen(t)
			e.clock.Advance(time.Hour)
			if tc.issued > 0 {
				e.sight(t, e.route.prepared()[0].HandoverID, tc.issued)
			}
			later, err := e.deliver(context.Background())
			if got := e.places(t); err != nil || later.Kind != tc.later || !slices.Equal(got, tc.laterPlaces) ||
				len(e.route.prepared()) != tc.issued+len(later.Issued) {
				t.Errorf("later event = %+v, %v: %d prompts issued in all, custody %v; want %s, custody %v",
					later, err, len(e.route.prepared()), got, tc.later, tc.laterPlaces)
			}
		})
	}
}

// TestCoordinatorHonorsRouteWaits (SR-2.6, SR-3.3): a wait the route reports
// forbids output. At eligibility nothing is claimed; at the route's final
// authority the claim is released. Either way the Message stays Held waiting
// that exact cause, and the next eligible event delivers it.
func TestCoordinatorHonorsRouteWaits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		arrange  func(*testing.T, *coordEnv)
		wait     store.BusySendWaitReason
		released bool
	}{
		{"eligibility reports compaction", func(t *testing.T, e *coordEnv) {
			e.route.elig.Wait = store.BusySendWaitCompaction
			e.route.authorize = func(context.Context, busysend.Prepared) busysend.Authority {
				t.Error("a prompt was prepared despite the eligibility wait")
				return busysend.Authority{Wait: store.BusySendWaitCompaction}
			}
		}, store.BusySendWaitCompaction, false},
		{"authority reports a running turn", func(_ *testing.T, e *coordEnv) {
			e.route.authorize = func(context.Context, busysend.Prepared) busysend.Authority {
				return busysend.Authority{Wait: store.BusySendWaitTurnRunning}
			}
		}, store.BusySendWaitTurnRunning, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newCoordEnv(t)
			e.admit(t, "m1", "hello")
			tc.arrange(t, e)
			out, err := e.deliver(context.Background())
			if err != nil || out.Kind != busysend.OutcomeNoIssue || out.Wait != tc.wait || (out.HandoverID != "") != tc.released ||
				len(e.route.prepared()) != 0 {
				t.Fatalf("Deliver = %+v, %v with %d prompts issued; want no issue, waiting %s", out, err, len(e.route.prepared()), tc.wait)
			}
			if r, got := e.result(t, "m1"), e.places(t); r.Wait == nil || r.Wait.Reason != tc.wait || !slices.Equal(got, []string{"held"}) {
				t.Errorf("Result %+v, custody %v; want Held, waiting %s", r, got, tc.wait)
			}
			e.route.elig.Wait, e.route.authorize = "", nil
			if out, err := e.deliver(context.Background()); err != nil || out.Kind != busysend.OutcomeIssued || len(e.route.prepared()) != 1 {
				t.Errorf("next eligible event = %+v, %v; want the Message issued", out, err)
			}
		})
	}
}
