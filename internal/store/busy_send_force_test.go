package store_test

// The force protocol's store matrix (Task t2.xis.p8.y2; SR-2.2, SR-3.2,
// SR-4.1 to SR-4.3, SR-5.6, SR-6.2), the Epic's one owner of force outcomes,
// races, abandonment, late call end, Launch End and restart, on real SQLite
// through busy_send_claim_test.go's inbox. M1 G3's obligations, M1's
// during-coding tests (a pending force's finish, the release's strict
// deadline compare, returned-equals-recorded) and M2 P-C4.4 are named where
// asserted. Force rows of the crash matrix are TestBusySendCustodyMatrix's and
// the force issue guard TestBusySendIssueRevalidation's; the one-prompt
// driver is internal/busysend's, the calls and their projections pkg/api's.

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// forceRequest is a force call's claim on in's published current Launch at
// at (zero: now): an hour's active phase, so wall-clock issue commits stay
// inside it, then a minute's cleanup.
func (in *busySendInbox) forceRequest(t *testing.T, at time.Time) store.BusySendForceClaimRequest {
	t.Helper()
	pub, err := in.s.GetBusySendPublishedLaunch(in.id)
	if err != nil {
		t.Fatal(err)
	}
	return store.BusySendForceClaimRequest{InstanceID: in.id, Launch: pub, ForceID: uuid.NewString(), OwnerCallID: uuid.NewString(),
		ActiveBudget: time.Hour, CleanupBudget: time.Minute, At: at}
}

// forceClaim claims as forceRequest, requires a running force and makes its
// authority the one in.commit issues under.
func (in *busySendInbox) forceClaim(t *testing.T, at time.Time) store.BusySendForceClaim {
	t.Helper()
	c, err := in.s.ClaimBusySendForce(context.Background(), in.forceRequest(t, at))
	if err != nil || c.Handover == nil || c.Force.Outcome != store.BusySendForceRunning {
		t.Fatalf("force claim %+v, %v; want a running force", c, err)
	}
	ref := c.Ref
	in.force = &ref
	return c
}

// readForce reads forceID as get-force does and requires it found.
func (in *busySendInbox) readForce(t *testing.T, forceID string) store.BusySendForce {
	t.Helper()
	l, err := in.s.ReadBusySendForce(forceID)
	if err != nil || l.ForceResult == nil {
		t.Fatalf("ReadBusySendForce(%s) = %+v, %v; want found", forceID, l, err)
	}
	return *l.ForceResult
}

// shownForce is the force get and status show as delivery.force, "" for none.
func (in *busySendInbox) shownForce(t *testing.T) string {
	t.Helper()
	o, err := in.s.ReadBusySendObservation(in.id, store.DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if o == nil || o.Force == nil {
		return ""
	}
	return o.Force.ForceID
}

// revision is h's current revision, the one its job's next step holds.
func (in *busySendInbox) revision(t *testing.T, h *store.BusySendHandover) int64 {
	t.Helper()
	now, err := in.s.ReadBusySendHandover(context.Background(), h.HandoverID)
	if err != nil {
		t.Fatal(err)
	}
	return now.Revision
}

// forceStep is one fact a force's prompt gets, or Launch End.
type forceStep func(t *testing.T, in *busySendInbox, h *store.BusySendHandover)

func forceIssue(k int) forceStep {
	return func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) { in.issue(t, h, k, in.revision(t, h)) }
}

// forceOutput records prompt k's completion; complete output carries
// Claude's order evidence at its unproven boundary.
func forceOutput(k int, out store.BusySendOutput) forceStep {
	return func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
		var order store.BusySendOrderKind
		if out == store.BusySendOutputComplete {
			order = store.BusySendOrderEmissionComplete
		}
		in.complete(t, h, k, in.revision(t, h), out, order, out == store.BusySendOutputComplete)
	}
}

// forceAdmit records the agent's own sighting of prompt k, its admission.
func forceAdmit(k int) forceStep {
	return func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
		if _, err := in.s.RecordBusySendEvidence(context.Background(), store.BusySendPromptEvidence{HandoverID: h.HandoverID, PromptIndex: k,
			LaunchID: in.dest.LaunchID, ConnectionGeneration: in.dest.ConnectionGeneration,
			Admission: store.BusySendAdmissionPromptSighting}); err != nil {
			t.Fatalf("admission of prompt %d: %v", k, err)
		}
	}
}

// forceNegate records a route's definitive negative for prompt k through the
// route-neutral step. Claude supplies none (Task t2.xis.p8.zp); no table row
// both admits and supplies one, so this stands in for a synthetic route's.
func forceNegate(k int) forceStep {
	return func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
		if res, err := in.s.RecordBusySendNegative(context.Background(), store.BusySendPromptNegative{HandoverID: h.HandoverID,
			PromptIndex: k, LaunchID: in.dest.LaunchID, ConnectionGeneration: in.dest.ConnectionGeneration}); err != nil || !res.Recorded {
			t.Fatalf("negative for prompt %d = %+v, %v; want it recorded", k, res, err)
		}
	}
}

// forceLaunchEnd ends the Launch by the agent's own end report.
func forceLaunchEnd(t *testing.T, in *busySendInbox, _ *store.BusySendHandover) {
	if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
		t.Fatalf("SessionEnd %+v; want applied", a)
	}
}

// raceBusySendSteps runs first on in's store, held inside its write
// transaction at the first statement containing hold, until second, on
// another connection, reaches its BEGIN IMMEDIATE; second commits after it.
// Each step records its own results; neither may call t.Fatal.
func raceBusySendSteps(t *testing.T, in *busySendInbox, hold string, first, second func(*store.Store)) {
	t.Helper()
	rival, err := store.Open(in.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rival.Close() })
	holding, release, begun := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var holdOnce, releaseOnce, begunOnce sync.Once
	store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
		if strings.Contains(q, hold) {
			holdOnce.Do(func() { close(holding); <-release })
		}
		return nil
	}, nil)
	store.ObserveDecisionBoundaryForTest(t, rival, in.path, func(_ context.Context, q string) error {
		if q == "BEGIN IMMEDIATE" {
			begunOnce.Do(func() { close(begun) })
		}
		return nil
	}, nil)
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	done := make(chan struct{}, 2)
	go func() { first(in.s); done <- struct{}{} }()
	awaitSignal(t, holding, "the first step's held statement")
	go func() { second(rival); done <- struct{}{} }()
	awaitSignal(t, begun, "the second step's BEGIN IMMEDIATE")
	unblock()
	for range 2 {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("a raced step did not return within 10 s of the barrier's release")
		}
	}
}

// TestBusySendForceClaimRefusals (SR-4.2, SR-2.2; E5 Main Agent decision 5):
// a covered force claim refuses in the order agent_starting, launch_ended,
// route_unavailable, handover_in_progress, no_held_messages, earlier_message,
// input_unresolved, each row also holding the later conditions it must win
// over. Each refusal is its own stored attempt under its own force_id, bound
// to the published Launch it examined, returned as recorded, with custody
// untouched; it never replaces the force get shows (M2 P-C4.4: a pending force
// stays visible).
func TestBusySendForceClaimRefusals(t *testing.T) {
	t.Parallel()
	set := func(q string) func(*testing.T, *busySendInbox) {
		return func(t *testing.T, in *busySendInbox) { setBusySendFact(t, in.path, q, in.id) }
	}
	noRoute := set(`UPDATE busy_send_launches SET route_kind = NULL WHERE instance_id = ? AND phase = 'current'`)
	claimed := func(t *testing.T, in *busySendInbox) { in.claim(t, time.Time{}, false) }
	admit := func(text string) func(*testing.T, *busySendInbox) {
		return func(t *testing.T, in *busySendInbox) {
			if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte(text)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// unresolved leaves one handed-over prompt with no evidence the agent took
	// it (SR-3.3), the hand-over closed.
	unresolved := func(t *testing.T, in *busySendInbox) {
		h := in.claim(t, time.Time{}, false)
		in.issue(t, h, 0, h.Revision)
		forceOutput(0, store.BusySendOutputComplete)(t, in, h)
	}
	for _, tc := range []struct {
		name   string
		texts  []string
		setup  []func(*testing.T, *busySendInbox)
		reason store.BusySendForceReason
		open   bool // a pending force exists, which get must still show
	}{
		{name: "an uncovered Launch", texts: []string{"one"}, reason: store.BusySendForceAgentNotCovered,
			setup: []func(*testing.T, *busySendInbox){func(t *testing.T, in *busySendInbox) {
				if err := apitest.SeedBusySendCoverage(in.path, in.id, store.BusySendNotCovered, store.BusySendCovered); err != nil {
					t.Fatal(err)
				}
			}}},
		{name: "a starting agent with no route and nothing Held", reason: store.BusySendForceAgentStarting,
			setup: []func(*testing.T, *busySendInbox){set(`UPDATE spawns SET state = 'pending' WHERE instance_id = ?`), noRoute}},
		{name: "an ended agent with no route and nothing Held", reason: store.BusySendForceLaunchEnded,
			setup: []func(*testing.T, *busySendInbox){set(`UPDATE spawns SET state = 'ended' WHERE instance_id = ?`), noRoute}},
		{name: "no route while a hand-over owns the conversation", texts: []string{"one"}, reason: store.BusySendForceRouteUnavailable,
			setup: []func(*testing.T, *busySendInbox){claimed, noRoute}},
		{name: "an ordinary hand-over owning order with a later Message Held", texts: []string{"one"},
			reason: store.BusySendForceHandoverInProgress, setup: []func(*testing.T, *busySendInbox){claimed, admit("two")}},
		{name: "a pending force with nothing Held", texts: []string{"one"}, reason: store.BusySendForceHandoverInProgress, open: true,
			setup: []func(*testing.T, *busySendInbox){func(t *testing.T, in *busySendInbox) {
				c := in.forceClaim(t, time.Time{})
				forceIssue(0)(t, in, c.Handover)
				if end, err := in.s.EndBusySendForceCall(context.Background(), c.Ref, time.Now()); err != nil ||
					end.Force.Outcome != store.BusySendForcePending {
					t.Fatalf("call end %+v, %v; want pending", end, err)
				}
			}}},
		{name: "nothing Held with earlier input unresolved", texts: []string{"one"}, reason: store.BusySendForceNoHeldMessages,
			setup: []func(*testing.T, *busySendInbox){unresolved}},
		// A defensive guard: hand-over ownership answers first unless the
		// owner is detached, as here.
		{name: "an earlier Message claimed with no owner", texts: []string{"one"}, reason: store.BusySendForceEarlierMessage,
			setup: []func(*testing.T, *busySendInbox){claimed, admit("two"),
				set(`UPDATE busy_conversations SET owner_handover_id = NULL WHERE instance_id = ? AND publication = 'published'`)}},
		{name: "earlier input unresolved", texts: []string{"one"}, reason: store.BusySendForceInputUnresolved,
			setup: []func(*testing.T, *busySendInbox){unresolved, admit("two")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, tc.texts...)
			for _, s := range tc.setup {
				s(t, in)
			}
			open := ""
			if tc.open {
				open = in.force.ForceID
			}
			before := readBusySendTables(t, in.path)
			req := in.forceRequest(t, time.Time{})
			c, err := in.s.ClaimBusySendForce(context.Background(), req)
			f := c.Force
			if err != nil || c.Handover != nil || c.Ref != (store.BusySendForceRef{}) || f.Outcome != store.BusySendForceRefused ||
				f.Reason != tc.reason || f.ForceID != req.ForceID || f.LaunchID != req.Launch.LaunchID || f.HandoverID != "" ||
				len(f.CarriedPairs) != 0 || f.EndedAtMillis == 0 {
				t.Fatalf("force claim %+v, %v; want its own ended attempt refused %s, bound to Launch %s", c, err, tc.reason, req.Launch.LaunchID)
			}
			if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Error("a refused force changed custody")
			}
			if got := in.readForce(t, req.ForceID); !reflect.DeepEqual(got, f) {
				t.Errorf("get-force reads %+v; want what the claim returned, %+v", got, f)
			}
			if got := in.shownForce(t); got != open {
				t.Errorf("delivery.force names %q; want %q", got, open)
			}
		})
	}
}

// TestBusySendForceClaimFreezesHeld (SR-4.2, SR-2.2, SR-5.6; M1 G3
// ForceCarriesHeld): a claim for a Launch other than the published one writes
// nothing, not even an attempt. A force freezes every then-Held Message, in
// accepted order, into a force hand-over with the table's partition, the
// captured destination and its original deadlines; none shows forced until
// its prompt's issue commit. The frozen prompt survives a reopen; a later
// arrival waits outside it; a repeated or premature prompt commit is refused
// writing nothing, and the second prompt passes only after the first is
// admitted. Across turn and permission changes before each prompt, each
// issue returns its own current Turn Start counter, running turn and
// permission epoch, and the hand-over keeps the first prompt's (E5 Main Agent
// decision 9; test review y2 F1).
func TestBusySendForceClaimFreezesHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := newBusySendInbox(t, append([]string{"first"}, busySendTwoPrompts...)...)
	at := time.Now().UTC().Truncate(time.Millisecond)
	stale := in.forceRequest(t, at)
	stale.Launch.LaunchToken = "another-launch-token"
	before := readBusySendTables(t, in.path)
	if _, err := in.s.ClaimBusySendForce(ctx, stale); !errors.Is(err, store.ErrBusySendLaunchChanged) ||
		!reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
		t.Fatalf("claim for another Launch: %v; want ErrBusySendLaunchChanged, writing nothing", err)
	}
	if l, err := in.s.ReadBusySendForce(stale.ForceID); err != nil || l.Lookup != store.BusySendLookupUnknown {
		t.Fatalf("its attempt reads %+v, %v; want unknown", l, err)
	}

	c := in.forceClaim(t, at)
	h, f := c.Handover, c.Force
	held := wantBusySendCustody(t, in.path, in.id, "claimed held handover_in_progress reserved member",
		"claimed held handover_in_progress reserved member", "claimed held handover_in_progress reserved member")
	result, err := in.s.ReadBusySendResult(in.pair.CallerUUID, in.pair.DeliveryRequestID)
	if err != nil || result.Result == nil {
		t.Fatal(result, err)
	}
	if len(h.Prompts) != 2 || !reflect.DeepEqual(h.Prompts[0].MessageIDs, []string{held[0].id, held[1].id}) ||
		!reflect.DeepEqual(h.Prompts[1].MessageIDs, []string{held[2].id}) || h.Boundary != store.BusySendBoundaryForce ||
		h.ForceID != f.ForceID || h.Destination != in.dest || !h.ClaimedAt.Equal(at) || !h.IssueDeadline.Equal(at.Add(time.Hour)) ||
		!h.JobDeadline.Equal(at.Add(time.Hour+time.Minute)) {
		t.Fatalf("force hand-over %+v; want the three Messages in accepted order over two prompts, to %+v, deadlines at+1h and at+1h1m", h, in.dest)
	}
	if f.HandoverID != h.HandoverID || f.Revision != 1 || f.LaunchID != in.dest.LaunchID || f.CallDeadlineMillis != h.JobDeadline.UnixMilli() ||
		f.CallEndedAtMillis != 0 || f.EndedAtMillis != 0 || f.StartedAtMillis != at.UnixMilli() ||
		!reflect.DeepEqual(f.CarriedPairs, []store.BusySendPair{in.pair}) || !reflect.DeepEqual(f.CarriedResults, []store.BusySendResult{*result.Result}) ||
		result.Result.Forced {
		t.Fatalf("running force %+v; want revision 1 owning the hand-over until at+1h1m, carrying the confirmed pair and its unforced Result", f)
	}
	if got := in.shownForce(t); got != f.ForceID {
		t.Errorf("delivery.force names %q; want the running force", got)
	}

	in.reopen(t)
	fp, err := in.s.ReadBusySendForcePrompt(ctx, in.id)
	if err != nil || fp.Force != c.Ref || fp.Next != 0 || fp.Handover == nil || !reflect.DeepEqual(*fp.Handover, *h) {
		t.Fatalf("reopened force prompt %+v, %v; want the frozen hand-over at prompt 0 under %+v", fp, err, c.Ref)
	}
	if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte("late")}); err != nil {
		t.Fatal(err)
	}
	// stored is the hand-over's first-intent facts: Turn Starts, running turn
	// and permission epoch.
	stored := func() [3]int64 {
		var f [3]int64
		if err := openBusySendRaw(t, in.path).QueryRow(`SELECT intent_turn_starts, intent_running_turn, permission_epoch
		    FROM busy_send_handovers WHERE handover_id = ?`, h.HandoverID).Scan(&f[0], &f[1], &f[2]); err != nil {
			t.Fatal(err)
		}
		return f
	}
	setBusySendFact(t, in.path, `UPDATE spawns SET turn_starts = turn_starts + 1, permission_epoch = permission_epoch + 1,
	    state = 'working' WHERE instance_id = ?`, in.id)
	is := in.issue(t, h, 0, h.Revision)
	first := [3]int64{h.ExpectedTurnStarts + 1, 1, h.PermissionEpoch + 1}
	if got := [3]int64{is.TurnStarts, int64(boolCount(is.RunningTurn)), is.PermissionEpoch}; got != first || stored() != first {
		t.Fatalf("prompt 0's issue facts %v, stored %v; want the current %v", got, stored(), first)
	}
	wantBusySendCustody(t, in.path, in.id, "issued sent_unconfirmed handover_in_progress reserved member forced",
		"issued sent_unconfirmed handover_in_progress reserved member forced", "claimed held handover_in_progress reserved member",
		"held held handover_in_progress reserved")
	before = readBusySendTables(t, in.path)
	for k := range 2 {
		var guard *store.BusySendGuardError
		if _, err := in.commit(ctx, h, k, is.Revision); !errors.As(err, &guard) || guard.Failure != store.BusySendGuardPrompt {
			t.Errorf("prompt %d's commit before prompt 0's admission: %v; want prompt_state", k, err)
		}
	}
	if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
		t.Fatal("a refused force prompt commit wrote part of its intent")
	}
	forceOutput(0, store.BusySendOutputComplete)(t, in, h)
	if fp, err := in.s.ReadBusySendForcePrompt(ctx, in.id); err != nil || fp.Next != -1 {
		t.Errorf("force prompt with prompt 0 ordered but unadmitted %+v, %v; want none", fp, err)
	}
	forceAdmit(0)(t, in, h)
	if fp, err := in.s.ReadBusySendForcePrompt(ctx, in.id); err != nil || fp.Next != 1 {
		t.Errorf("force prompt with prompt 0 ordered and admitted %+v, %v; want prompt 1", fp, err)
	}
	setBusySendFact(t, in.path, `UPDATE spawns SET turn_starts = turn_starts + 1, permission_epoch = permission_epoch + 1,
	    state = 'waiting' WHERE instance_id = ?`, in.id)
	second := in.issue(t, h, 1, in.revision(t, h))
	if got, want := [3]int64{second.TurnStarts, int64(boolCount(second.RunningTurn)), second.PermissionEpoch},
		[3]int64{first[0] + 1, 0, first[2] + 1}; got != want || stored() != first {
		t.Errorf("prompt 1's issue facts %v, stored %v; want its own current %v, the hand-over keeping prompt 0's %v", got, stored(), want, first)
	}
	if got := in.readForce(t, f.ForceID); !reflect.DeepEqual(got.CarriedPairs, []store.BusySendPair{in.pair}) {
		t.Errorf("carried pairs after a later arrival %+v; want only the frozen confirmed pair", got.CarriedPairs)
	}
}

// TestBusySendForceRacesClaim (SR-2.2, SR-4.2; M1 G3 ForceOwnSound, M2
// P-C4.4): with one claim held inside its write transaction at its hand-over
// insert, a simultaneous force call, or an ordinary claim, on another
// connection waits and then finds the hand-over under way: one owner claims
// every Message once, and the other claims nothing.
func TestBusySendForceRacesClaim(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		first, second string // "force" or "ordinary"
	}{
		{"two force calls", "force", "force"},
		{"a force before an ordinary claim", "force", "ordinary"},
		{"an ordinary claim before a force", "ordinary", "force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, "one", "two")
			outcomes := make([]string, 2)
			step := func(i int, kind string) func(*store.Store) {
				req := in.forceRequest(t, time.Time{})
				return func(s *store.Store) {
					if kind == "ordinary" {
						c, err := s.ClaimBusySendHandover(context.Background(), in.claimRequest(time.Time{}, false))
						outcomes[i] = string(c.Outcome) + " " + string(c.Wait)
						if err != nil {
							outcomes[i] = err.Error()
						}
						return
					}
					c, err := s.ClaimBusySendForce(context.Background(), req)
					outcomes[i] = string(c.Force.Outcome) + " " + string(c.Force.Reason)
					if err != nil {
						outcomes[i] = err.Error()
					}
				}
			}
			raceBusySendSteps(t, in, "INSERT INTO busy_send_handovers", step(0, tc.first), step(1, tc.second))
			want := map[string]string{"force": "running ", "ordinary": string(store.BusySendClaimMade) + " "}[tc.first]
			lost := map[string]string{"force": "refused handover_in_progress",
				"ordinary": string(store.BusySendClaimOwned) + " handover_in_progress"}[tc.second]
			if outcomes[0] != want || outcomes[1] != lost {
				t.Fatalf("outcomes %q; want %q then %q", outcomes, want, lost)
			}
			custody := wantBusySendCustody(t, in.path, in.id, "claimed held handover_in_progress reserved member",
				"claimed held handover_in_progress reserved member")
			var handovers int
			if err := openBusySendRaw(t, in.path).QueryRow(`SELECT COUNT(*) FROM busy_send_handovers`).Scan(&handovers); err != nil ||
				handovers != 1 || custody[0].handover != custody[1].handover {
				t.Errorf("%d hand-overs, %v; want both Messages in the one winner's", handovers, err)
			}
		})
	}
}

// TestBusySendForceCallEndOutcomes (SR-4.3; M1 G3 ForceResponseRule,
// UnsafeFinalNeedsIntent, ForceOutcomeStable and ForceEndReleases, M1
// during-coding returned-equals-recorded): a two-prompt force's call end
// fences it, returns its never-issued Messages to Held in order, and chooses
// the outcome from its prompt facts alone. Member precedence: a Final Result
// stays byte for byte, a Message never intended is Held and unforced, and a
// failed force's other members become Final sent_unconfirmed. Effective
// issued accounting drops a not-submitted prompt; a negative revokes an
// admission; a route that can never admit (a synthetic provider the table
// does not name) fails rather than pends. A not-submitted for every issued
// prompt, or Launch End, finishes the force first: the call's end then loses
// and returns that recorded result. The end returns what get-force reads, and
// repeated ends, late admissions, Launch End and exit evidence never change a
// finished force.
func TestBusySendForceCallEndOutcomes(t *testing.T) {
	t.Parallel()
	const (
		heldFirst  = "held held readiness_unknown reserved"
		heldSecond = "held held earlier_message reserved"
		heldOwned  = "held held handover_in_progress reserved"
		heldEnded  = "held held launch_ended reserved"
		closedA    = "closed sent_unconfirmed final handed_over_not_proven forced"
		closedB    = "closed sent_unconfirmed final confirmation_off forced"
	)
	issueOut := []forceStep{forceIssue(0), forceOutput(0, store.BusySendOutputComplete)}
	admitted := slices.Concat(issueOut, []forceStep{forceAdmit(0)})
	then := func(steps ...forceStep) []forceStep { return slices.Concat(admitted, steps) }
	for _, tc := range []struct {
		name     string
		provider string // a synthetic provider the capability table does not name; "" for Claude
		steps    []forceStep
		ended    bool // the call's end records the outcome (false: it lost)
		outcome  store.BusySendForceOutcome
		reason   store.BusySendForceReason
		next     int // ReadBusySendForceProgress before the end, when it ends
		decided  bool
		a, b     string
	}{
		{name: "nothing issued", ended: true, outcome: store.BusySendForceRefused, reason: store.BusySendForceNotIssued,
			a: heldFirst, b: heldSecond},
		{name: "the issued prompt admitted, the next never issued", steps: admitted, ended: true, next: 1,
			outcome: store.BusySendForceHandedOver, reason: store.BusySendForceAdmitted, a: closedA, b: heldFirst},
		{name: "both prompts issued and admitted", ended: true, next: -1, decided: true,
			steps:   then(forceIssue(1), forceOutput(1, store.BusySendOutputComplete), forceAdmit(1)),
			outcome: store.BusySendForceHandedOver, reason: store.BusySendForceAdmitted, a: closedA, b: closedB},
		{name: "an issued prompt awaiting admission", steps: issueOut, ended: true, next: -1,
			outcome: store.BusySendForcePending, reason: store.BusySendForceAwaitingAdmission, a: closedA, b: heldFirst},
		{name: "a definitive negative", steps: []forceStep{forceIssue(0), forceNegate(0)}, ended: true, next: -1, decided: true,
			outcome: store.BusySendForceFailed, reason: store.BusySendForceAdmissionUnknown,
			a: "issued sent_unconfirmed final admission_unknown member forced", b: heldOwned},
		{name: "an admission revoked by a later negative", steps: []forceStep{forceIssue(0), forceAdmit(0), forceNegate(0)}, ended: true,
			next: -1, decided: true, outcome: store.BusySendForceFailed, reason: store.BusySendForceAdmissionUnknown,
			a: "issued sent_unconfirmed final admission_unknown member forced", b: heldOwned},
		{name: "a route that can never admit", provider: "synthetic", steps: issueOut, ended: true, next: -1,
			outcome: store.BusySendForceFailed, reason: store.BusySendForceAdmissionUnknown, a: closedA, b: heldFirst},
		{name: "every issued prompt not submitted", steps: []forceStep{forceIssue(0), forceOutput(0, store.BusySendOutputNotSubmitted)},
			outcome: store.BusySendForceRefused, reason: store.BusySendForceNotSubmitted, a: heldFirst, b: heldSecond},
		{name: "a not-submitted beside an admitted prompt", ended: true, next: -1, decided: true,
			steps:   then(forceIssue(1), forceOutput(1, store.BusySendOutputNotSubmitted)),
			outcome: store.BusySendForceHandedOver, reason: store.BusySendForceAdmitted, a: closedA, b: heldFirst},
		{name: "Launch End with nothing issued", steps: []forceStep{forceLaunchEnd},
			outcome: store.BusySendForceRefused, reason: store.BusySendForceNotIssued, a: heldEnded, b: heldEnded},
		{name: "Launch End with an issued prompt unadmitted", steps: []forceStep{forceIssue(0), forceLaunchEnd},
			outcome: store.BusySendForceFailed, reason: store.BusySendForceLaunchEnded,
			a: "issued sent_unconfirmed final launch_ended member forced", b: heldEnded},
		{name: "Launch End after every issued prompt was admitted", steps: then(forceLaunchEnd),
			outcome: store.BusySendForceHandedOver, reason: store.BusySendForceAdmitted, a: closedA, b: heldEnded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, busySendTwoPrompts...)
			if tc.provider != "" {
				setBusySendFact(t, in.path, `UPDATE busy_conversations SET provider = 'synthetic' WHERE instance_id = ? AND publication = 'published'`, in.id)
				setBusySendFact(t, in.path, `UPDATE busy_send_launches SET provider = 'synthetic' WHERE instance_id = ? AND phase = 'current'`, in.id)
				in.dest.Provider = tc.provider
			}
			c := in.forceClaim(t, time.Time{})
			h := c.Handover
			if len(h.Prompts) != 2 {
				t.Fatalf("%d prompts; want 2", len(h.Prompts))
			}
			for _, step := range tc.steps {
				step(t, in, h)
			}
			if tc.ended {
				p, err := in.s.ReadBusySendForceProgress(ctx, c.Force.ForceID)
				if err != nil || p != (store.BusySendForceProgress{Outcome: store.BusySendForceRunning, Next: tc.next, Decided: tc.decided}) {
					t.Errorf("progress before the end %+v, %v; want running, next %d, decided %t", p, err, tc.next, tc.decided)
				}
			}
			before := readBusySendCustody(t, in.path, in.id)
			at := time.Now().UTC().Truncate(time.Millisecond)
			end, err := in.s.EndBusySendForceCall(ctx, c.Ref, at)
			if err != nil || end.Ended != tc.ended || end.Force.Outcome != tc.outcome || end.Force.Reason != tc.reason {
				t.Fatalf("call end %+v, %v; want ended %t, %s %s", end, err, tc.ended, tc.outcome, tc.reason)
			}
			custody := wantBusySendCustody(t, in.path, in.id, tc.a, tc.b)
			for i := range before {
				if before[i].final && custody[i].result() != before[i].result() {
					t.Errorf("the end changed Final Message %d's Result from %s to %s", i, before[i].result(), custody[i].result())
				}
			}
			held := 0
			for _, m := range custody {
				if m.place == "held" {
					held++
				}
			}
			result, err := in.s.ReadBusySendResult(in.pair.CallerUUID, in.pair.DeliveryRequestID)
			if err != nil || result.Result == nil {
				t.Fatal(result, err)
			}
			f := end.Force
			callEnded := map[bool]int64{true: at.UnixMilli()}[tc.ended]
			if got := in.readForce(t, f.ForceID); !reflect.DeepEqual(got, f) || f.CallEndedAtMillis != callEnded ||
				(f.EndedAtMillis == 0) != (tc.outcome == store.BusySendForcePending) || end.HeldMembers != held || end.WakePath != c.WakePath ||
				!reflect.DeepEqual(f.CarriedResults, []store.BusySendResult{*result.Result}) {
				t.Errorf("call end %+v, get-force %+v; want them equal, call end at %d, an end time unless pending, %d Held and the wake %q",
					end, got, callEnded, held, c.WakePath)
			}

			if again, err := in.s.EndBusySendForceCall(ctx, c.Ref, at.Add(time.Minute)); err != nil || again.Ended ||
				!reflect.DeepEqual(again.Force, f) {
				t.Errorf("a repeated end %+v, %v; want it lost, returning %+v", again, err, f)
			}
			if tc.outcome == store.BusySendForcePending {
				return // TestBusySendForcePendingFinish finishes it.
			}
			forceAdmit(0)(t, in, h)
			forceAdmit(1)(t, in, h)
			if !strings.HasPrefix(tc.name, "Launch End") {
				if err := store.EndBusySendLaunchForTest(in.s, in.dest.LaunchID, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := in.s.EstablishBusySendExit(*exitEvidenceForTest(t, in.s, in.id)); err != nil {
				t.Fatalf("exit evidence: %v", err)
			}
			if got := in.readForce(t, f.ForceID); got.Outcome != f.Outcome || got.Reason != f.Reason || got.Revision != f.Revision ||
				got.EndedAtMillis != f.EndedAtMillis {
				t.Errorf("after late admissions, Launch End and exit evidence the force reads %+v; want it unchanged from %+v", got, f)
			}
		})
	}
}

// TestBusySendForcePendingFinish (SR-4.3, SR-3.2, SR-6.2; M1 G3
// ForceAdmissionSound and ForceOutcomeStable, M1 during-coding pending
// finish, M2 P-C4.4): a pending force (prompt 0 admitted after its Result was
// Final, prompt 1 issued unadmitted) needs no worker and survives a reopen.
// Reads and an eligible event, a release and expiry a day later neither
// finish nor hide it nor change custody (another force's refusal is
// TestBusySendForceClaimRefusals'). Only the last prompt's admission
// (handed_over), a definitive negative (failed admission_unknown, member
// rules) or Launch End (failed launch_ended) finishes it, keeping the Final
// Result byte for byte, and nothing later reopens it.
func TestBusySendForcePendingFinish(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		finish  forceStep
		outcome store.BusySendForceOutcome
		reason  store.BusySendForceReason
		b       string
	}{
		{"the last prompt's admission", forceAdmit(1), store.BusySendForceHandedOver, store.BusySendForceAdmitted,
			"issued sent_unconfirmed handover_in_progress reserved member forced"},
		{"a definitive negative", forceNegate(1), store.BusySendForceFailed, store.BusySendForceAdmissionUnknown,
			"issued sent_unconfirmed final confirmation_off member forced"},
		{"Launch End", forceLaunchEnd, store.BusySendForceFailed, store.BusySendForceLaunchEnded,
			"issued sent_unconfirmed final confirmation_off member forced"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, busySendTwoPrompts...)
			c := in.forceClaim(t, time.Time{})
			h := c.Handover
			for _, step := range []forceStep{forceIssue(0), forceOutput(0, store.BusySendOutputComplete), forceAdmit(0), forceIssue(1)} {
				step(t, in, h)
			}
			end, err := in.s.EndBusySendForceCall(ctx, c.Ref, time.Now())
			if err != nil || !end.Ended || end.Force.Outcome != store.BusySendForcePending || end.Force.Reason != store.BusySendForceAwaitingAdmission {
				t.Fatalf("call end %+v, %v; want pending awaiting_admission", end, err)
			}
			in.reopen(t)
			pending := in.readForce(t, c.Force.ForceID)
			a := wantBusySendCustody(t, in.path, in.id, "closed sent_unconfirmed final handed_over_not_proven forced",
				"issued sent_unconfirmed handover_in_progress reserved member forced")[0]
			later := time.Now().Add(24 * time.Hour)
			before := readBusySendTables(t, in.path)
			if p, err := in.s.ReadBusySendForceProgress(ctx, pending.ForceID); err != nil || p.Outcome != store.BusySendForcePending {
				t.Errorf("progress %+v, %v; want pending", p, err)
			}
			if r, err := in.s.ReleaseAbandonedBusySendForce(ctx, in.id, later); err != nil || r.Released {
				t.Errorf("a release a day on %+v, %v; want nothing released", r, err)
			}
			if cl, err := in.s.ClaimBusySendHandover(ctx, in.claimRequest(later, false)); err != nil || cl.Outcome != store.BusySendClaimOwned {
				t.Errorf("an eligible event a day on %+v, %v; want owned behind the force's barrier", cl, err)
			}
			if _, err := in.s.ReleaseBusySendClaims(ctx, store.BusySendReleaseRequest{HandoverID: h.HandoverID, At: later}); err == nil {
				t.Error("generic expiry of the force's hand-over was accepted")
			}
			if got := in.readForce(t, pending.ForceID); !reflect.DeepEqual(got, pending) || in.shownForce(t) != pending.ForceID ||
				!reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Fatalf("after reads and a day the force reads %+v, shown %q; want it pending, shown, custody unchanged",
					got, in.shownForce(t))
			}

			tc.finish(t, in, h)
			done := in.readForce(t, pending.ForceID)
			after := wantBusySendCustody(t, in.path, in.id, "closed sent_unconfirmed final handed_over_not_proven forced", tc.b)
			if done.Outcome != tc.outcome || done.Reason != tc.reason || done.EndedAtMillis == 0 ||
				done.CallEndedAtMillis != pending.CallEndedAtMillis || after[0].result() != a.result() {
				t.Fatalf("finished force %+v, prompt 0's Result %s; want %s %s with an end time, its call end and %s kept",
					done, after[0].result(), tc.outcome, tc.reason, a.result())
			}
			forceAdmit(1)(t, in, h)
			if tc.reason != store.BusySendForceLaunchEnded {
				forceLaunchEnd(t, in, h)
			}
			if got := in.readForce(t, pending.ForceID); got.Outcome != done.Outcome || got.Reason != done.Reason ||
				got.Revision != done.Revision || got.EndedAtMillis != done.EndedAtMillis {
				t.Errorf("after a late admission and Launch End the force reads %+v; want it unchanged from %+v", got, done)
			}
		})
	}
}

// TestBusySendForceAbandonedRelease (SR-3.2, SR-4.3; M1 G3 ForceEndReleases
// and NoOrphanForceClaim, M1 during-coding strict deadline compare): a force
// whose call never reached its end, its prompt 0 issued, is finished by an
// explicit release, an eligible event's claim or an explicit force, only once
// its recorded call deadline has strictly passed. Before and at the deadline
// each changes nothing, and generic claim expiry never releases a force's
// claims. Just after it, the force is fenced by owner revision and finished
// by the call-end rules (pending) with no call end recorded; only the
// never-issued Message returns to Held, the issued one keeps its custody and
// unfenced barrier, and the old job can issue nothing more.
func TestBusySendForceAbandonedRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		release func(t *testing.T, in *busySendInbox, at time.Time) int // the Messages it returned
	}{
		{"an explicit release", func(t *testing.T, in *busySendInbox, at time.Time) int {
			r, err := in.s.ReleaseAbandonedBusySendForce(ctx, in.id, at)
			if err != nil {
				t.Fatal(err)
			}
			return r.Returned
		}},
		{"an eligible event's claim", func(t *testing.T, in *busySendInbox, at time.Time) int {
			c, err := in.s.ClaimBusySendHandover(ctx, in.claimRequest(at, false))
			if err != nil || c.Outcome != store.BusySendClaimOwned {
				t.Fatalf("claim %+v, %v; want owned behind the issued prompt's barrier", c, err)
			}
			return c.ReleasedExpired
		}},
		{"an explicit force", func(t *testing.T, in *busySendInbox, at time.Time) int {
			c, err := in.s.ClaimBusySendForce(ctx, in.forceRequest(t, at))
			if err != nil || c.Force.Reason != store.BusySendForceHandoverInProgress {
				t.Fatalf("force %+v, %v; want refused handover_in_progress", c, err)
			}
			return c.Returned
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, busySendTwoPrompts...)
			c := in.forceClaim(t, time.Time{})
			h := c.Handover
			forceIssue(0)(t, in, h)
			deadline := time.UnixMilli(c.Force.CallDeadlineMillis)
			before := readBusySendTables(t, in.path)
			if _, err := in.s.ReleaseBusySendClaims(ctx, store.BusySendReleaseRequest{HandoverID: h.HandoverID, At: deadline.Add(time.Hour)}); err == nil {
				t.Error("generic expiry of the force's hand-over was accepted")
			}
			for _, at := range []time.Time{deadline.Add(-time.Millisecond), deadline} {
				if n := tc.release(t, in, at); n != 0 || !reflect.DeepEqual(before, readBusySendTables(t, in.path)) ||
					in.readForce(t, c.Force.ForceID).Outcome != store.BusySendForceRunning {
					t.Fatalf("release at %v (deadline %v) returned %d; want nothing changed", at, deadline, n)
				}
			}
			if n := tc.release(t, in, deadline.Add(time.Millisecond)); n != 1 {
				t.Fatalf("release just after the deadline returned %d; want the never-issued Message", n)
			}
			f := in.readForce(t, c.Force.ForceID)
			if f.Outcome != store.BusySendForcePending || f.Revision != 2 || f.CallEndedAtMillis != 0 || in.shownForce(t) != f.ForceID {
				t.Fatalf("released force %+v; want pending at revision 2 with no call end, shown", f)
			}
			wantBusySendCustody(t, in.path, in.id, "issued sent_unconfirmed handover_in_progress reserved member forced",
				"held held handover_in_progress reserved")
			if fence := storefix.ReadLaunchFence(t, in.s, in.dest.LaunchID); len(fence.Prompts) != 1 || fence.Prompts[0].PromptIndex != 0 ||
				fence.Prompts[0].Fenced {
				t.Errorf("Launch fence %+v; want prompt 0's unresolved issue, not fenced", fence)
			}
			before = readBusySendTables(t, in.path)
			for k := range 2 {
				var guard *store.BusySendGuardError
				if _, err := in.commit(ctx, h, k, in.revision(t, h)); !errors.As(err, &guard) {
					t.Errorf("the old job's prompt %d: %v; want a guard refusal", k, err)
				}
			}
			if r, err := in.s.ReleaseAbandonedBusySendForce(ctx, in.id, deadline.Add(time.Hour)); err != nil || r.Released ||
				!reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Errorf("a second release %+v, %v; want nothing changed", r, err)
			}
		})
	}
}

// TestBusySendForceLateCallEnd (SR-4.3; M1 G3 ForceOwnSound and
// ForceFencedAtCallEnd, M1 during-coding returned-equals-recorded): a call
// end and an abandoned release arbitrate at the transaction boundary, with
// the first held inside its write transaction while the second waits for the
// lock. Whichever commits first finishes the force; a losing end changes
// nothing and returns the force's recorded result, a losing release changes
// nothing, and the old job's issue commit after a release is refused. Later,
// with a newer force running, the old call's end still returns its own
// force's recorded result and leaves the newer force and custody untouched.
func TestBusySendForceLateCallEnd(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		issued        bool // prompt 0 was issued and ordered before the deadline
		first, second string
	}{
		{"a release before the late call end", true, "release", "end"},
		{"the call end before a release", true, "end", "release"},
		{"a release before the old job's issue commit", false, "release", "issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, busySendTwoPrompts...)
			c := in.forceClaim(t, time.Time{})
			h := c.Handover
			if tc.issued {
				forceIssue(0)(t, in, h)
				forceOutput(0, store.BusySendOutputComplete)(t, in, h)
			}
			late := time.UnixMilli(c.Force.CallDeadlineMillis + 1)
			jobRevision := in.revision(t, h)
			var end store.BusySendForceEnd
			var rel store.BusySendForceRelease
			var endErr, relErr, issueErr error
			steps := map[string]func(*store.Store){
				"end": func(s *store.Store) { end, endErr = s.EndBusySendForceCall(ctx, c.Ref, late) },
				"release": func(s *store.Store) {
					rel, relErr = s.ReleaseAbandonedBusySendForce(ctx, in.id, late)
				},
				// The job's own clock still reads the claim's time.
				"issue": func(s *store.Store) {
					issuer, ref := busySendHelper, c.Ref
					_, issueErr = s.CommitBusySendIssue(ctx, store.BusySendIssueRequest{Ref: store.BusySendPromptRef{HandoverID: h.HandoverID,
						Revision: jobRevision}, Force: &ref, Issuer: &issuer, Now: func() time.Time { return h.ClaimedAt }})
				},
			}
			raceBusySendSteps(t, in, "SET emission = 'released'", steps[tc.first], steps[tc.second])
			if endErr != nil || relErr != nil {
				t.Fatalf("end %v, release %v", endErr, relErr)
			}
			f := in.readForce(t, c.Force.ForceID)
			switch tc.second {
			case "end":
				if !rel.Released || rel.Returned != 1 || end.Ended || !reflect.DeepEqual(end.Force, f) || end.HeldMembers != 1 ||
					f.Outcome != store.BusySendForcePending || f.CallEndedAtMillis != 0 {
					t.Fatalf("release %+v, then end %+v; want the release's pending result, returned as recorded (%+v)", rel, end, f)
				}
			case "release":
				if !end.Ended || rel.Released || f.Outcome != store.BusySendForcePending || f.CallEndedAtMillis != late.UnixMilli() {
					t.Fatalf("end %+v, then release %+v, force %+v; want the end's pending result and the release inert", end, rel, f)
				}
			case "issue":
				var guard *store.BusySendGuardError
				if !rel.Released || rel.Returned != 2 || !errors.As(issueErr, &guard) || guard.Failure != store.BusySendGuardOwner ||
					f.Outcome != store.BusySendForceRefused || f.Reason != store.BusySendForceNotIssued {
					t.Fatalf("release %+v, then the job's issue %v, force %+v; want refused not_issued and the issue owner_changed", rel, issueErr, f)
				}
				wantBusySendCustody(t, in.path, in.id, "held held readiness_unknown reserved", "held held earlier_message reserved")
				return
			}

			forceAdmit(0)(t, in, h)
			old := in.readForce(t, c.Force.ForceID)
			in.force = nil
			newer := in.forceClaim(t, time.Time{})
			before, newest := readBusySendTables(t, in.path), in.readForce(t, newer.Force.ForceID)
			again, err := in.s.EndBusySendForceCall(ctx, c.Ref, late.Add(time.Minute))
			if err != nil || again.Ended || old.Outcome != store.BusySendForceHandedOver || !reflect.DeepEqual(again.Force, old) {
				t.Fatalf("the old call's end with a newer force running %+v, %v; want its own handed_over result %+v", again, err, old)
			}
			if got := in.readForce(t, newer.Force.ForceID); !reflect.DeepEqual(got, newest) || !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Errorf("the old call's end changed the newer force (%+v, was %+v) or custody", got, newest)
			}
		})
	}
}
