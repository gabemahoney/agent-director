package store_test

// Claude turn evidence and AD-owned work (Task t2.xis.oo.u8; SR-3.3, SR-3.5,
// SR-5.6; M2 P-C4.2; Main Agent decisions 10 and 11) on real SQLite, through
// busy_send_claim_test.go's inbox. The Turn Start write is
// hook_gate_test.go's; the crash and custody boundaries of the obligation and
// the issuer are rows of TestBusySendCustodyMatrix. The hook's unwrapping and
// the route's wiring are internal/hook's.

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ownGate is the gate of event from in's own agent, in its recorded session.
func (in *busySendInbox) ownGate(t *testing.T, event string) store.HookGate {
	t.Helper()
	pid, start, err := storefix.AgentHookParent(in.s, in.id)
	if err != nil {
		t.Fatal(err)
	}
	return store.HookGate{Event: event, ParentPID: pid, ParentStart: start, SessionID: in.dest.SessionID}
}

// sighting is the agent's own UserPromptSubmit sighting of body in its
// recorded session.
func (in *busySendInbox) sighting(t *testing.T, body []byte) store.BusySendSighting {
	t.Helper()
	return store.BusySendSighting{InstanceID: in.id, Gate: in.ownGate(t, "UserPromptSubmit"), SessionID: in.dest.SessionID, Body: body}
}

// sight records s and requires no error.
func (in *busySendInbox) sight(t *testing.T, s store.BusySendSighting) store.BusySendSightingResult {
	t.Helper()
	got, err := in.s.RecordBusySendSighting(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// awaiting is in's awaiting_turn_start as get and status read it.
func (in *busySendInbox) awaiting(t *testing.T) int64 {
	t.Helper()
	o, err := in.s.ReadBusySendObservation(in.id, store.DefaultLockWait)
	if err != nil || o == nil {
		t.Fatalf("ReadBusySendObservation = %+v, %v", o, err)
	}
	return o.AwaitingTurnStart
}

// listsIssuer reports whether in's Launch fence lists issuer's process.
func (in *busySendInbox) listsIssuer(t *testing.T, issuer store.BusySendIssuer) bool {
	t.Helper()
	fence := storefix.ReadLaunchFence(t, in.s, in.dest.LaunchID)
	return slices.ContainsFunc(fence.Processes, func(p store.BusySendLaunchProcess) bool {
		return p.Role == issuer.Role && p.PID == issuer.PID && p.StartTime == issuer.StartTime
	})
}

// newBusySendIssuedPair is an inbox of two Messages, one per prompt of one
// hand-over, both issued by busySendHelper: prompt 0's complete output at the
// unproven boundary secures its order, so prompt 1 may issue, and prompt 1's
// output is not yet accounted (its issue revision is returned). Neither has
// evidence the agent took it.
func newBusySendIssuedPair(t *testing.T, texts []string, race bool) (*busySendInbox, *store.BusySendHandover, int64) {
	t.Helper()
	in := newBusySendInbox(t, texts...)
	h := in.claim(t, time.Time{}, race)
	if len(h.Prompts) != 2 {
		t.Fatalf("%d prompts; want 2", len(h.Prompts))
	}
	is := in.issue(t, h, 0, h.Revision)
	res := in.complete(t, h, 0, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true)
	return in, h, in.issue(t, h, 1, res.Revision).Revision
}

// TestBusySendSightingBindsOnlyItsPrompt (SR-3.3, SR-3.5; Research
// t1.p8z.9e): the agent's own sighting of an issued prompt's exact body, in
// the current Launch, session and connection, is that prompt's take evidence,
// whether it lands before or after the prompt's completion accounting. It
// resolves only that prompt (the hand-over awaits until both are sighted) and
// changes no custody or Result: the race-exposed Messages end Final
// wait_turn_raced, never delivered_confirmed, the in-flight prompt's
// completion still applies after its sighting, and a repeat binds nothing.
func TestBusySendSightingBindsOnlyItsPrompt(t *testing.T) {
	t.Parallel()
	in, h, revision := newBusySendIssuedPair(t, busySendTwoPrompts, true)
	lookup := func() store.BusySendResultLookup {
		r, err := in.s.ReadBusySendResult(in.pair.CallerUUID, in.pair.DeliveryRequestID)
		if err != nil || r.Result == nil {
			t.Fatalf("Lookup %+v, %v", r, err)
		}
		return r
	}
	final := lookup()

	before := readBusySendCustody(t, in.path, in.id)
	if got, want := in.sight(t, in.sighting(t, h.Prompts[1].Body)), (store.BusySendSightingResult{Outcome: store.BusySendSightingAdmitted,
		HandoverID: h.HandoverID, PromptIndex: 1}); got != want {
		t.Fatalf("sighting of prompt 1 before its completion = %+v; want %+v", got, want)
	}
	if after := readBusySendCustody(t, in.path, in.id); !reflect.DeepEqual(after, before) {
		t.Errorf("the sighting changed custody:\n before %v\n after  %v", before, after)
	}
	if n := in.awaiting(t); n != 1 {
		t.Errorf("awaiting_turn_start %d with prompt 0 unsighted; want 1", n)
	}
	if res := in.complete(t, h, 1, revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true); !res.OrderSecured || !res.Closed {
		t.Fatalf("prompt 1's completion after its sighting %+v; want its order secured and the hand-over closed", res)
	}
	raced := "closed sent_unconfirmed final wait_turn_raced"
	wantBusySendCustody(t, in.path, in.id, raced, raced)

	if got, want := in.sight(t, in.sighting(t, h.Prompts[0].Body)), (store.BusySendSightingResult{Outcome: store.BusySendSightingAdmitted,
		HandoverID: h.HandoverID}); got != want {
		t.Fatalf("sighting of prompt 0 after its completion = %+v; want %+v", got, want)
	}
	wantBusySendCustody(t, in.path, in.id, raced, raced)
	if n := in.awaiting(t); n != 0 {
		t.Errorf("awaiting_turn_start %d with both prompts sighted; want 0", n)
	}
	if got := lookup(); !reflect.DeepEqual(got, final) {
		t.Errorf("the sightings changed the Final Lookup from %+v to %+v", final, got)
	}
	tables := readBusySendTables(t, in.path)
	if got := in.sight(t, in.sighting(t, h.Prompts[0].Body)); got.Outcome != store.BusySendSightingUnmatched ||
		!reflect.DeepEqual(tables, readBusySendTables(t, in.path)) {
		t.Errorf("a repeated sighting = %+v; want unmatched, changing nothing", got)
	}
}

// TestBusySendSightingRefusals (SR-3.5: AD never guesses): a sighting that
// cannot bind to exactly one awaiting prompt of the current Launch, session
// and connection records nothing, so the obligation stands: a body two
// awaiting prompts share (identical texts, or an escaping collision), any body
// while a candidate's text is gone, a body that is not exactly a prompt's, and
// stale evidence (a gate that does not hold, no, another or a rotated session,
// an advanced connection, an uncovered or ended Launch). After Launch End the
// public count is zero while the fence still holds the prompt and its issuer:
// zero is not exit evidence.
func TestBusySendSightingRefusals(t *testing.T) {
	t.Parallel()
	set := func(q string) func(*testing.T, *busySendInbox, *store.BusySendHandover) {
		return func(t *testing.T, in *busySendInbox, _ *store.BusySendHandover) {
			setBusySendFact(t, in.path, q, in.id)
		}
	}
	for _, tc := range []struct {
		name     string
		texts    []string // nil: busySendTwoPrompts
		change   func(t *testing.T, in *busySendInbox, h *store.BusySendHandover)
		edit     func(s *store.BusySendSighting) // of prompt 1's exact sighting
		want     store.BusySendSightingOutcome
		awaiting int64 // -1: no count is published
	}{
		{name: "two awaiting prompts with this body", texts: []string{busySendTwoPrompts[0], busySendTwoPrompts[0]},
			want: store.BusySendSightingAmbiguous, awaiting: 1},
		{name: "a candidate's text is gone", change: func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
			setBusySendFact(t, in.path, `UPDATE busy_send_messages SET retention = 'expired', expired_at_ms = 1, text = NULL,
			    text_sha256 = NULL, result_class = NULL, result_reason = NULL WHERE message_id = ?`, h.Prompts[0].MessageIDs[0])
		}, want: store.BusySendSightingAmbiguous, awaiting: 1},
		{name: "a body that is not exactly a prompt's", edit: func(s *store.BusySendSighting) { s.Body = s.Body[:len(s.Body)-1] },
			want: store.BusySendSightingUnmatched, awaiting: 1},
		{name: "a gate that does not hold", edit: func(s *store.BusySendSighting) {
			s.Gate.ParentPID = storefix.ForeignHookParentPID(s.Gate.ParentPID)
		}, want: store.BusySendSightingStale, awaiting: 1},
		{name: "no session", edit: func(s *store.BusySendSighting) { s.SessionID = "" }, want: store.BusySendSightingStale, awaiting: 1},
		{name: "another session", edit: func(s *store.BusySendSighting) { s.SessionID = "another-session" },
			want: store.BusySendSightingStale, awaiting: 1},
		{name: "the session rotated after the issue", change: set(`UPDATE spawns SET session_id = 'rotated' WHERE instance_id = ?`),
			edit: func(s *store.BusySendSighting) { s.SessionID = "rotated" }, want: store.BusySendSightingStale, awaiting: 1},
		{name: "the connection generation advanced", change: set(`UPDATE busy_send_launches SET connection_generation = 1
		    WHERE instance_id = ? AND phase = 'current'`), want: store.BusySendSightingStale, awaiting: 1},
		{name: "an uncovered Launch", change: func(t *testing.T, in *busySendInbox, _ *store.BusySendHandover) {
			if err := apitest.SeedBusySendCoverage(in.path, in.id, store.BusySendNotCovered, store.BusySendCovered); err != nil {
				t.Fatal(err)
			}
		}, want: store.BusySendSightingStale, awaiting: -1},
		{name: "the Launch ended", change: func(t *testing.T, in *busySendInbox, _ *store.BusySendHandover) {
			if err := store.EndBusySendLaunchForTest(in.s, in.dest.LaunchID, time.Now()); err != nil {
				t.Fatal(err)
			}
		}, want: store.BusySendSightingStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			texts := tc.texts
			if texts == nil {
				texts = busySendTwoPrompts
			}
			in, h, _ := newBusySendIssuedPair(t, texts, false)
			if tc.change != nil {
				tc.change(t, in, h)
			}
			s := in.sighting(t, h.Prompts[1].Body)
			if tc.edit != nil {
				tc.edit(&s)
			}
			before := readBusySendTables(t, in.path)
			if got := in.sight(t, s); got != (store.BusySendSightingResult{Outcome: tc.want}) {
				t.Errorf("sighting = %+v; want %s, bound to nothing", got, tc.want)
			}
			if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Error("a sighting that bound nothing wrote")
			}
			if tc.awaiting >= 0 {
				if n := in.awaiting(t); n != tc.awaiting {
					t.Errorf("awaiting_turn_start %d; want %d", n, tc.awaiting)
				}
			}
			if tc.awaiting == 0 {
				fence := storefix.ReadLaunchFence(t, in.s, in.dest.LaunchID)
				if !fence.Ended || len(fence.Prompts) != 1 || fence.Prompts[0].PromptIndex != 1 ||
					!in.listsIssuer(t, busySendHelper) {
					t.Errorf("fence %+v; want the ended Launch fencing prompt 1 and listing its issuer", fence)
				}
			}
		})
	}
}

// TestBusySendTurnEndWaitsForTakeEvidence (SR-3.3's AD-owned-work rule; Main
// Agent decision 10): while an issued prompt of the current Launch has no
// evidence the agent took it, a turn-end claim of a later Held Message is
// refused readiness_unknown and writes nothing, even with the prompt's
// hand-over closed Final, after the agent's own turn started and ended after
// the output (turn starts never clear an obligation) and after a sighting of
// other text. The prompt's exact sighting lifts the refusal.
func TestBusySendTurnEndWaitsForTakeEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := newBusySendInbox(t, "first")
	h := in.claim(t, time.Time{}, false)
	is := in.issue(t, h, 0, h.Revision)
	if res := in.complete(t, h, 0, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true); !res.Closed {
		t.Fatalf("completion %+v; want the hand-over closed", res)
	}
	if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte("second")}); err != nil {
		t.Fatal(err)
	}
	refused := func(after string) {
		t.Helper()
		before := readBusySendTables(t, in.path)
		c, err := in.s.ClaimBusySendHandover(ctx, in.claimRequest(time.Time{}, false))
		if err != nil || c.Outcome != store.BusySendClaimIneligible || c.Wait != store.BusySendWaitReadinessUnknown || c.Handover != nil {
			t.Fatalf("claim %s = %+v, %v; want ineligible, readiness_unknown", after, c, err)
		}
		if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
			t.Fatalf("the refused claim %s wrote", after)
		}
		if n := in.awaiting(t); n != 1 {
			t.Fatalf("awaiting_turn_start %d %s; want 1", n, after)
		}
	}
	refused("after the output")
	for _, hook := range []struct{ event, state string }{{"UserPromptSubmit", store.StateWorking}, {"Stop", store.StateWaiting}} {
		g := in.ownGate(t, hook.event)
		g.ClaudeTurnStart = hook.event == "UserPromptSubmit"
		if got, err := in.s.ApplyHookTransition(in.id, g, hook.state, false, hook.event, "", false); err != nil || !got.Applied {
			t.Fatalf("%s = %+v, %v; want applied", hook.event, got, err)
		}
	}
	if c, err := apitest.ReadSpawnColumns(in.path, in.id); err != nil || c.TurnStarts != int64(1) {
		t.Fatalf("turn_starts %v, %v; want 1", c.TurnStarts, err)
	}
	refused("after a later turn started and ended")
	if got := in.sight(t, in.sighting(t, []byte("other text"))); got.Outcome != store.BusySendSightingUnmatched {
		t.Fatalf("sighting of other text = %+v; want unmatched", got)
	}
	refused("after a sighting of other text")
	if got := in.sight(t, in.sighting(t, h.Prompts[0].Body)); got.Outcome != store.BusySendSightingAdmitted {
		t.Fatalf("sighting of the prompt = %+v; want admitted", got)
	}
	if next := in.claim(t, time.Time{}, false); next.HandoverSeq != 2 || len(next.Prompts) != 1 {
		t.Errorf("claim after the sighting %+v; want hand-over 2 of the Held Message", next)
	}
}

// TestBusySendHelperSlotKeepsAnUnresolvedIssuer (Main Agent decision 5; M1 G4
// at its producer): an issue commit registers its issuer only while no other
// issued prompt of the Launch lacks a recorded outcome. With prompt 0 issued
// and unresolved, another hook identity's issue is refused issuer_unresolved,
// waiting handover_in_progress, and writes nothing, so the slot keeps the
// issuer that may still emit; the same identity issues. The order barrier
// keeps prompt 1 behind prompt 0's order, so the test moves the cursor itself
// to reach the guard. Replacement once the outcome is recorded is
// TestBusySendSafeReleaseThenProgress's; exit evidence is Task 4's.
func TestBusySendHelperSlotKeepsAnUnresolvedIssuer(t *testing.T) {
	t.Parallel()
	in := newBusySendInbox(t, busySendTwoPrompts...)
	h := in.claim(t, time.Time{}, false)
	is := in.issue(t, h, 0, h.Revision)
	setBusySendFact(t, in.path, `UPDATE busy_send_handovers SET prompt_cursor = 1 WHERE handover_id = ?`, h.HandoverID)
	other := store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: 4343, StartTime: "200"}
	before := readBusySendTables(t, in.path)
	_, err := in.s.CommitBusySendIssue(context.Background(), store.BusySendIssueRequest{Issuer: &other,
		Ref: store.BusySendPromptRef{HandoverID: h.HandoverID, PromptIndex: 1, Revision: is.Revision}})
	var guard *store.BusySendGuardError
	if !errors.As(err, &guard) || guard.Failure != store.BusySendGuardIssuer || guard.Wait != store.BusySendWaitHandoverInProgress {
		t.Fatalf("another identity's issue: %v; want issuer_unresolved waiting handover_in_progress", err)
	}
	if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
		t.Fatal("the refused issue wrote")
	}
	in.issue(t, h, 1, is.Revision)
	if !in.listsIssuer(t, busySendHelper) || in.listsIssuer(t, other) {
		t.Error("the helper slot does not hold the first issuer alone")
	}
}

// TestBusySendResumeRecordsItsOwnRoute (SR-3.4, SR-7.2; u8 test review F3):
// a covered Claude resume's production staging (MoveToPending) records route
// A on the candidate Launch itself: claude_hooks, the candidate's Launch id as
// route identity, connection generation 0 and its own token's wake endpoint.
// Until publication the route facts an owned callback reads stay the
// published Launch's, so the candidate cannot redirect them; publication
// makes its route current. A covered Codex resume (a conversation a later
// release covered) records no route, and its published Launch reads as
// uncovered by a route. (Every Claude Launch is covered: there is no
// uncovered Claude resume.)
func TestBusySendResumeRecordsItsOwnRoute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		agent   string
		covered bool
		route   bool
	}{
		{"covered Claude resume", store.AgentClaude, true, true},
		{"covered Codex resume", store.AgentCodex, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, path := storefix.OpenTempStore(t)
			id, err := apitest.SeedSpawn(path, "", store.StateEnded, t.TempDir(), "off", uuid.NewString(), false, apitest.WithAgent(tc.agent))
			if err != nil {
				t.Fatal(err)
			}
			if tc.covered {
				if err := apitest.SeedBusySendCoverage(path, id, store.BusySendCovered, store.BusySendCovered); err != nil {
					t.Fatal(err)
				}
			}
			if tc.route {
				if err := apitest.SeedBusySendRoute(path, id, string(store.BusySendRouteClaudeHooks), filepath.Join(t.TempDir(), "prior-wake")); err != nil {
					t.Fatal(err)
				}
			}
			prior, err := s.ReadBusySendRouteFacts(ctx, id, store.HookGate{})
			if err != nil {
				t.Fatal(err)
			}
			row, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			const token = "1122334455667788"
			res, version, err := s.MoveToPendingWithExit(id, row.Snapshot, 2000, token, "", "", store.LaunchOwner{},
				store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, s, id))
			if err != nil || res != store.CondApplied {
				t.Fatalf("MoveToPending = %v, %v; want applied", res, err)
			}
			var candidate, kind, routeID, wake string
			var generation int64
			if err := openBusySendRaw(t, path).QueryRow(`SELECT launch_id, COALESCE(route_kind, ''), COALESCE(route_id, ''),
			        connection_generation, COALESCE(wake_path, '')
			    FROM busy_send_launches WHERE instance_id = ? AND phase = 'candidate'`, id).Scan(&candidate, &kind, &routeID, &generation, &wake); err != nil {
				t.Fatal(err)
			}
			var want store.BusySendDestination
			wantWake := ""
			if tc.route {
				if wantWake, err = store.BusySendWakePath(s.StoreID(), token); err != nil {
					t.Fatal(err)
				}
				want = store.BusySendDestination{RouteKind: store.BusySendRouteClaudeHooks, RouteID: candidate}
			}
			if kind != string(want.RouteKind) || routeID != want.RouteID || generation != 0 || wake != wantWake {
				t.Fatalf("candidate route %q %q generation %d wake %q; want %q %q generation 0 wake %q",
					kind, routeID, generation, wake, want.RouteKind, want.RouteID, wantWake)
			}
			staged, err := s.ReadBusySendRouteFacts(ctx, id, store.HookGate{})
			if err != nil || staged.Covered != prior.Covered || staged.WakePath != prior.WakePath ||
				staged.Destination.LaunchID != prior.Destination.LaunchID || staged.Destination.RouteID != prior.Destination.RouteID {
				t.Fatalf("route facts before publication %+v, %v; want the published Launch's %+v", staged, err, prior)
			}
			if res, err := s.RecordLaunchIdentity(id, version, token, store.LaunchIdentity{}); err != nil || res != store.CondApplied {
				t.Fatalf("publication = %v, %v; want applied", res, err)
			}
			got, err := s.ReadBusySendRouteFacts(ctx, id, store.HookGate{})
			if err != nil || got.Covered != tc.route || got.WakePath != wantWake || got.Destination.LaunchID != candidate ||
				got.Destination.RouteKind != want.RouteKind || got.Destination.RouteID != want.RouteID || got.Destination.ConnectionGeneration != 0 {
				t.Errorf("route facts after publication %+v, %v; want the candidate %s with route %+v and wake %q", got, err, candidate, want, wantWake)
			}
		})
	}
}
