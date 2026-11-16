package store_test

// Claude turn evidence and AD-owned work (Task t2.xis.oo.u8; SR-3.3, SR-3.5,
// SR-5.6; M2 P-C4.2; Main Agent decisions 10 and 11) on real SQLite, through
// busy_send_claim_test.go's inbox, with the parent Stop's take evidence for
// input steered into a running turn (Task t2.xis.wm.o7; E4 Main Agent
// decisions 1 and 2; M2 P-C4.2 and P-C4.3 at the new boundary). The Turn
// Start write is hook_gate_test.go's; the crash and custody boundaries of the
// obligation and the issuer are rows of TestBusySendCustodyMatrix. The hook's
// unwrapping, its Stop classification and the route's wiring are
// internal/hook's.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
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

// newBusySendIssuedAt is an inbox of one Message whose route claimed it at its
// clock's start and issued its one prompt 1 ms later: steered into a working
// agent's running turn at a parent tool boundary, Claude's wake at turn end,
// or a running force's into the working turn. The prompt's output is next.
func newBusySendIssuedAt(t *testing.T, boundary store.BusySendBoundary) (*busySendInbox, *store.BusySendHandover, int64) {
	t.Helper()
	in := newBusySendInbox(t)
	in.clock = tmuxfix.NewClock(time.Now().UTC().Truncate(time.Millisecond))
	working := func() {
		setBusySendFact(t, in.path, `UPDATE spawns SET state = 'working' WHERE instance_id = ?`, in.id)
	}
	var h *store.BusySendHandover
	switch boundary {
	case store.BusySendBoundaryTurnEnd:
		in.admit(t, store.BusySendWaitTurn, "wake")
		h = in.claim(t, in.clock.Now(), false)
	case store.BusySendBoundaryForce:
		in.admit(t, store.BusySendWaitTurn, "forced")
		working()
		h = in.forceClaim(t, in.clock.Now()).Handover
	default:
		in.admit(t, store.BusySendSteerTurn, "steered")
		working()
		h = in.steer(t, in.clock.Now())
	}
	in.clock.Advance(time.Millisecond)
	return in, h, in.issue(t, h, 0, h.Revision).Revision
}

// output records prompt 0's output out at in's clock: complete output with
// Claude's emission order at its unproven boundary, or out with none.
func (in *busySendInbox) output(t *testing.T, h *store.BusySendHandover, revision int64, out store.BusySendOutput) {
	t.Helper()
	if out == store.BusySendOutputComplete {
		in.complete(t, h, 0, revision, out, store.BusySendOrderEmissionComplete, true)
		return
	}
	in.complete(t, h, 0, revision, out, "", false)
}

// stop is in's own parent Stop at its current destination, observed at at,
// with its entry read made now (ReadBusySendTurnEndPrompts): only output
// already recorded now can count at it.
func (in *busySendInbox) stop(t *testing.T, at time.Time) store.BusySendTurnEnd {
	t.Helper()
	return in.entry(t, in.turnEnd(t, at))
}

// turnEnd is stop before its entry read.
func (in *busySendInbox) turnEnd(t *testing.T, at time.Time) store.BusySendTurnEnd {
	t.Helper()
	return store.BusySendTurnEnd{InstanceID: in.id, Gate: in.ownGate(t, "Stop"), Destination: in.dest, At: at}
}

// entry is s with its entry read (ReadBusySendTurnEndPrompts) made now.
func (in *busySendInbox) entry(t *testing.T, s store.BusySendTurnEnd) store.BusySendTurnEnd {
	t.Helper()
	var err error
	if s.Prompts, err = in.s.ReadBusySendTurnEndPrompts(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	return s
}

// endTurn records the turn end s and requires no error.
func (in *busySendInbox) endTurn(t *testing.T, s store.BusySendTurnEnd) store.BusySendTurnEndResult {
	t.Helper()
	got, err := in.s.RecordBusySendTurnEnd(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// routeFacts is in's route facts as its own Stop hook reads them.
func (in *busySendInbox) routeFacts(t *testing.T) store.BusySendRouteFacts {
	t.Helper()
	f, err := in.s.ReadBusySendRouteFacts(context.Background(), in.id, in.ownGate(t, "Stop"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// wantAwaiting requires in's awaiting_turn_start to be n, both as get and
// status read it and as its hook's route facts read it.
func (in *busySendInbox) wantAwaiting(t *testing.T, n int64, when string) {
	t.Helper()
	if got, f := in.awaiting(t), in.routeFacts(t); got != n || f.Awaiting != n {
		t.Errorf("awaiting_turn_start %d, route facts %d %s; want %d", got, f.Awaiting, when, n)
	}
}

// admission is prompt k of h's recorded admission, its time and its revision.
func (in *busySendInbox) admission(t *testing.T, h *store.BusySendHandover, k int) (string, int64, int64) {
	t.Helper()
	var kind string
	var at, revision int64
	if err := openBusySendRaw(t, in.path).QueryRow(`SELECT COALESCE(admission_kind, ''), COALESCE(admitted_at_ms, 0), revision
	    FROM busy_send_prompts WHERE handover_id = ? AND prompt_index = ?`, h.HandoverID, k).Scan(&kind, &at, &revision); err != nil {
		t.Fatal(err)
	}
	return kind, at, revision
}

// readBusySendTablesButAdmission is readBusySendTables with every prompt's
// admission, its time and its revision left out: all a turn end may write.
func readBusySendTablesButAdmission(t *testing.T, path string) map[string][][]any {
	t.Helper()
	tables := readBusySendTables(t, path)
	rows, err := openBusySendRaw(t, path).Query(`SELECT * FROM busy_send_prompts LIMIT 0`)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := rows.Columns()
	_ = rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range tables["busy_send_prompts"] {
		for i, c := range cols {
			if c == "admission_kind" || c == "admitted_at_ms" || c == "revision" {
				row[i] = nil
			}
		}
	}
	return tables
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

// TestBusySendTurnEndAdmitsSteeredInput (SR-3.3, SR-5.6; E4 Main Agent
// decisions 1 and 2; M2 P-C4.2): a non-forced prompt issued into the running
// turn at a tool boundary awaits from its committed intent, before any output
// and across a reopen, and once its complete output is recorded a turn-end
// claim still waits readiness_unknown. The agent's own parent Stop, observed
// strictly after that output was recorded, admits it once as native_signal at
// the Stop's observation: the count get, status and the hook's route facts
// read drops to zero and the next turn-end claim proceeds. Nothing else
// changes: no custody, reservation, transport, order, Final Result, hand-over
// or conversation revision. A repeated Stop and a later exact sighting are
// inert; after a sighting first, the Stop admits nothing.
func TestBusySendTurnEndAdmitsSteeredInput(t *testing.T) {
	t.Parallel()
	for _, sightedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "Stop", true: "sighting, then Stop"}[sightedFirst], func(t *testing.T) {
			t.Parallel()
			in, h, revision := newBusySendIssuedAt(t, store.BusySendBoundaryToolBoundary)
			in.admit(t, store.BusySendWaitTurn, "after the turn")
			in.reopen(t)
			in.wantAwaiting(t, 1, "before the output")
			in.clock.Advance(time.Millisecond)
			in.output(t, h, revision, store.BusySendOutputComplete)
			before := readBusySendTables(t, in.path)
			if c, err := in.s.ClaimBusySendHandover(context.Background(), in.claimRequest(in.clock.Now(), false)); err != nil ||
				c.Outcome != store.BusySendClaimIneligible || c.Wait != store.BusySendWaitReadinessUnknown {
				t.Fatalf("turn-end claim after the output %+v, %v; want ineligible, readiness_unknown", c, err)
			}
			if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Fatal("the refused turn-end claim wrote")
			}
			in.wantAwaiting(t, 1, "after the output")

			in.clock.Advance(time.Millisecond)
			observed := in.clock.Now()
			want := store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndRecorded, Recorded: 1}
			if sightedFirst {
				if got := in.sight(t, in.sighting(t, h.Prompts[0].Body)); got.Outcome != store.BusySendSightingAdmitted {
					t.Fatalf("sighting = %+v; want admitted", got)
				}
				want = store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndUnchanged}
			}
			kind, at, revised := in.admission(t, h, 0)
			if !sightedFirst {
				kind, at, revised = string(store.BusySendAdmissionNativeSignal), observed.UnixMilli(), revised+1
			}
			frame := readBusySendTablesButAdmission(t, in.path)
			if got := in.endTurn(t, in.stop(t, observed)); got != want {
				t.Fatalf("Stop after the output = %+v; want %+v", got, want)
			}
			if gotKind, gotAt, gotRevision := in.admission(t, h, 0); gotKind != kind || gotAt != at || gotRevision != revised {
				t.Errorf("admission %q at %d, revision %d; want %q at %d, revision %d", gotKind, gotAt, gotRevision, kind, at, revised)
			}
			if !reflect.DeepEqual(frame, readBusySendTablesButAdmission(t, in.path)) {
				t.Error("the Stop changed more than the prompt's admission")
			}
			in.wantAwaiting(t, 0, "after the Stop")

			tables := readBusySendTables(t, in.path)
			if got := in.endTurn(t, in.stop(t, observed.Add(time.Millisecond))); got != (store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndUnchanged}) {
				t.Errorf("a repeated Stop = %+v; want unchanged", got)
			}
			if got := in.sight(t, in.sighting(t, h.Prompts[0].Body)); got.Outcome != store.BusySendSightingUnmatched {
				t.Errorf("a later sighting = %+v; want unmatched", got)
			}
			if !reflect.DeepEqual(tables, readBusySendTables(t, in.path)) {
				t.Error("a repeated Stop or a later sighting wrote")
			}
			if next := in.claim(t, in.clock.Now(), false); next.HandoverSeq != 2 || len(next.Prompts) != 1 {
				t.Errorf("turn-end claim after the Stop %+v; want hand-over 2 of the Held Message", next)
			}
		})
	}
}

// TestBusySendTurnEndKeepsTheObligation (SR-3.3, SR-3.5: AD never guesses; E4
// Main Agent decisions 1 and 2; M2 P-C4.2): only the current Launch's own
// parent Stop, observed strictly after a non-forced tool-boundary prompt's
// complete output was recorded for that same destination, admits it. A Stop
// in the output's millisecond or observed before it, no output recorded,
// partial or uncertain output, a prompt Claude's wake or a force issued
// (sighting-only), no observation, a gate that does not hold, another
// session, an advanced connection, a prompt captured before the session
// rotated, and a missing or ended Launch admit nothing and write nothing,
// whether its entry read already met them or only its write did (the Launch
// ended after the entry read found the prompt): the prompt still awaits,
// until Launch End reads zero. Later evidence still counts: a Stop after its
// complete output, or its exact sighting.
func TestBusySendTurnEndKeepsTheObligation(t *testing.T) {
	t.Parallel()
	set := func(q string) func(*testing.T, *busySendInbox) {
		return func(t *testing.T, in *busySendInbox) { setBusySendFact(t, in.path, q, in.id) }
	}
	ended := func(t *testing.T, in *busySendInbox) {
		if err := store.EndBusySendLaunchForTest(in.s, in.dest.LaunchID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	complete := store.BusySendOutputComplete
	for _, tc := range []struct {
		name     string
		boundary store.BusySendBoundary // the prompt's; "" is a tool boundary
		output   store.BusySendOutput   // recorded 1 ms after the issue; "" for none
		stop     time.Duration          // the Stop's observation after that
		change   func(t *testing.T, in *busySendInbox)
		edit     func(s *store.BusySendTurnEnd) // before its entry read
		between  func(t *testing.T, in *busySendInbox)
		want     store.BusySendTurnEndOutcome
		awaiting int64  // -1: no count is published
		then     string // what admits it later: "stop" (after complete output) or "sight"
	}{
		{name: "a Stop in the output's millisecond", output: complete, want: store.BusySendTurnEndUnchanged, awaiting: 1, then: "stop"},
		{name: "a Stop observed before the output", output: complete, stop: -time.Millisecond, want: store.BusySendTurnEndUnchanged,
			awaiting: 1, then: "stop"},
		{name: "no output recorded", stop: time.Millisecond, want: store.BusySendTurnEndUnchanged, awaiting: 1, then: "stop"},
		{name: "partial output", output: store.BusySendOutputPartial, stop: time.Millisecond, want: store.BusySendTurnEndUnchanged,
			awaiting: 1, then: "sight"},
		{name: "uncertain output", output: store.BusySendOutputUncertain, stop: time.Millisecond, want: store.BusySendTurnEndUnchanged,
			awaiting: 1, then: "sight"},
		{name: "a wake's prompt", boundary: store.BusySendBoundaryTurnEnd, output: complete, stop: time.Millisecond,
			want: store.BusySendTurnEndUnchanged, awaiting: 1, then: "sight"},
		{name: "a force's prompt", boundary: store.BusySendBoundaryForce, output: complete, stop: time.Millisecond,
			want: store.BusySendTurnEndUnchanged, awaiting: 1, then: "sight"},
		{name: "no observation", output: complete, edit: func(s *store.BusySendTurnEnd) { s.At = time.Time{} },
			want: store.BusySendTurnEndStale, awaiting: 1},
		{name: "a gate that does not hold", output: complete, stop: time.Millisecond, edit: func(s *store.BusySendTurnEnd) {
			s.Gate.ParentPID = storefix.ForeignHookParentPID(s.Gate.ParentPID)
		}, want: store.BusySendTurnEndStale, awaiting: 1},
		{name: "another session", output: complete, stop: time.Millisecond,
			edit: func(s *store.BusySendTurnEnd) { s.Destination.SessionID = "another-session" }, want: store.BusySendTurnEndStale, awaiting: 1},
		{name: "the connection generation advanced", output: complete, stop: time.Millisecond,
			change: set(`UPDATE busy_send_launches SET connection_generation = 1 WHERE instance_id = ? AND phase = 'current'`),
			want:   store.BusySendTurnEndStale, awaiting: 1},
		{name: "the session rotated after the issue", output: complete, stop: time.Millisecond,
			change: set(`UPDATE spawns SET session_id = 'rotated' WHERE instance_id = ?`),
			edit:   func(s *store.BusySendTurnEnd) { s.Destination.SessionID = "rotated" }, want: store.BusySendTurnEndUnchanged, awaiting: 1},
		{name: "the agent row is missing", output: complete, stop: time.Millisecond,
			change: set(`UPDATE spawns SET state = 'missing' WHERE instance_id = ?`), want: store.BusySendTurnEndStale, awaiting: -1},
		{name: "the Launch ended", output: complete, stop: time.Millisecond, change: ended, want: store.BusySendTurnEndStale},
		{name: "the Launch ended after the Stop's entry read", output: complete, stop: time.Millisecond, between: ended,
			want: store.BusySendTurnEndStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			boundary := tc.boundary
			if boundary == "" {
				boundary = store.BusySendBoundaryToolBoundary
			}
			in, h, revision := newBusySendIssuedAt(t, boundary)
			in.clock.Advance(time.Millisecond)
			if tc.output != "" {
				in.output(t, h, revision, tc.output)
			}
			if tc.change != nil {
				tc.change(t, in)
			}
			s := in.turnEnd(t, in.clock.Now().Add(tc.stop))
			if tc.edit != nil {
				tc.edit(&s)
			}
			s = in.entry(t, s)
			if tc.between != nil {
				if len(s.Prompts) != 1 {
					t.Fatalf("entry read %+v; want the prompt, so only the write can refuse", s.Prompts)
				}
				tc.between(t, in)
			}
			before := readBusySendTables(t, in.path)
			if got := in.endTurn(t, s); got != (store.BusySendTurnEndResult{Outcome: tc.want}) {
				t.Errorf("Stop = %+v; want %s, admitting nothing", got, tc.want)
			}
			if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Error("a Stop that admitted nothing wrote")
			}
			if tc.awaiting >= 0 {
				in.wantAwaiting(t, tc.awaiting, "after the Stop")
			}
			switch tc.then {
			case "stop":
				if tc.output == "" {
					in.output(t, h, revision, complete)
				}
				in.clock.Advance(time.Millisecond)
				if got := in.endTurn(t, in.stop(t, in.clock.Now())); got != (store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndRecorded, Recorded: 1}) {
					t.Errorf("a later Stop after the complete output = %+v; want one recorded", got)
				}
			case "sight":
				if got := in.sight(t, in.sighting(t, h.Prompts[0].Body)); got.Outcome != store.BusySendSightingAdmitted {
					t.Errorf("its sighting = %+v; want admitted", got)
				}
			}
			if tc.then != "" {
				in.wantAwaiting(t, 0, "once later evidence admitted it")
			}
		})
	}
}

// TestBusySendTurnEndCountsHandovers (SR-3.3, SR-5.6; M2 P-C4.2): the count
// is of hand-overs, and a turn end admits exactly the ones that qualify. With
// Claude's wake prompt unsighted, two Steer Turn prompts issued at consecutive
// tool boundaries (the second while the first still awaited) with complete
// output, and a third issued with no output recorded yet, a parent Stop admits
// the two: the count goes from 4 to 2. The third's later output and a later
// Stop admit it; the wake's prompt awaits until its own sighting.
func TestBusySendTurnEndCountsHandovers(t *testing.T) {
	t.Parallel()
	complete := store.BusySendOutputComplete
	in, wake, revision := newBusySendIssuedAt(t, store.BusySendBoundaryTurnEnd)
	in.clock.Advance(time.Millisecond)
	in.output(t, wake, revision, complete)
	setBusySendFact(t, in.path, `UPDATE spawns SET state = 'working' WHERE instance_id = ?`, in.id)
	steered := make([]*store.BusySendHandover, 3)
	for i := range steered {
		in.admit(t, store.BusySendSteerTurn, fmt.Sprintf("steered %d", i))
		in.clock.Advance(time.Millisecond)
		steered[i] = in.steer(t, in.clock.Now())
		in.clock.Advance(time.Millisecond)
		revision = in.issue(t, steered[i], 0, steered[i].Revision).Revision
		if i < 2 {
			in.clock.Advance(time.Millisecond)
			in.output(t, steered[i], revision, complete)
		}
	}
	in.wantAwaiting(t, 4, "with no prompt taken")
	kinds := func() (out []string) {
		for _, h := range append([]*store.BusySendHandover{wake}, steered...) {
			kind, _, _ := in.admission(t, h, 0)
			out = append(out, kind)
		}
		return out
	}
	in.clock.Advance(time.Millisecond)
	if got := in.endTurn(t, in.stop(t, in.clock.Now())); got != (store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndRecorded, Recorded: 2}) {
		t.Fatalf("Stop = %+v; want two recorded", got)
	}
	if got := kinds(); !slices.Equal(got, []string{"", "native_signal", "native_signal", ""}) {
		t.Errorf("admissions %q; want the two complete Steer Turn prompts'", got)
	}
	in.wantAwaiting(t, 2, "after the Stop")
	in.clock.Advance(time.Millisecond)
	in.output(t, steered[2], revision, complete)
	in.clock.Advance(time.Millisecond)
	if got := in.endTurn(t, in.stop(t, in.clock.Now())); got != (store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndRecorded, Recorded: 1}) {
		t.Fatalf("a later Stop = %+v; want one recorded", got)
	}
	if got := kinds(); !slices.Equal(got, []string{"", "native_signal", "native_signal", "native_signal"}) {
		t.Errorf("admissions %q; want every Steer Turn prompt's", got)
	}
	in.wantAwaiting(t, 1, "with the wake's prompt unsighted")
	if got := in.sight(t, in.sighting(t, wake.Prompts[0].Body)); got.Outcome != store.BusySendSightingAdmitted {
		t.Fatalf("the wake's sighting = %+v; want admitted", got)
	}
	in.wantAwaiting(t, 0, "after the wake's sighting")
}

// TestBusySendTurnEndBindsItsLaunch (SR-3.3, SR-6.1; M1's old callbacks
// inert; E4 Main Agent decision 1): a parent Stop of the old Launch, whose
// entry read found that Launch's prompt, writes only after the Launch ended
// and a resume published a new one in the same provider session and pane.
// It admits nothing, though its gate holds, its session is the current one
// and the new Launch's steered output is recorded by then; the new Launch's
// own Stop admits only its own prompt, never the old Launch's.
func TestBusySendTurnEndBindsItsLaunch(t *testing.T) {
	t.Parallel()
	complete := store.BusySendOutputComplete
	in, old, revision := newBusySendIssuedAt(t, store.BusySendBoundaryToolBoundary)
	in.clock.Advance(time.Millisecond)
	in.output(t, old, revision, complete)
	in.clock.Advance(time.Millisecond)
	late := in.stop(t, in.clock.Now())
	if len(late.Prompts) != 1 {
		t.Fatalf("the old Launch's entry read %+v; want its prompt", late.Prompts)
	}
	prior, oldDest := in.row(t).Identity, in.dest
	if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", oldDest.SessionID); !a.Applied {
		t.Fatalf("SessionEnd %+v; want applied", a)
	}
	const token = "1122334455667788"
	res, version, err := in.s.MoveToPendingWithExit(in.id, in.row(t).Snapshot, 2000, token, "", "", store.LaunchOwner{},
		store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, in.s, in.id))
	if err != nil || res != store.CondApplied {
		t.Fatalf("resume %v, %v; want applied", res, err)
	}
	if res, err := in.s.RecordLaunchIdentity(in.id, version, token,
		store.LaunchIdentity{PanePID: prior.PanePID, PaneStarttime: prior.PaneStarttime}); err != nil || res != store.CondApplied {
		t.Fatalf("publication %v, %v; want applied", res, err)
	}
	if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionStart", oldDest.SessionID); !a.Applied {
		t.Fatalf("SessionStart %+v; want applied", a)
	}
	if in.dest = in.routeFacts(t).Destination; in.dest.LaunchID == oldDest.LaunchID || in.dest.SessionID != oldDest.SessionID {
		t.Fatalf("destination after the resume %+v; want a new Launch in session %s", in.dest, oldDest.SessionID)
	}
	in.admit(t, store.BusySendSteerTurn, "steered into the new Launch")
	setBusySendFact(t, in.path, `UPDATE spawns SET state = 'working' WHERE instance_id = ?`, in.id)
	in.clock.Advance(time.Millisecond)
	h := in.steer(t, in.clock.Now())
	in.clock.Advance(time.Millisecond)
	revision = in.issue(t, h, 0, h.Revision).Revision
	in.clock.Advance(time.Millisecond)
	in.output(t, h, revision, complete)
	in.clock.Advance(time.Millisecond)

	before := readBusySendTables(t, in.path)
	if got := in.endTurn(t, late); got != (store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndStale}) {
		t.Errorf("the old Launch's late Stop = %+v; want stale", got)
	}
	if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
		t.Fatal("the old Launch's late Stop wrote")
	}
	in.wantAwaiting(t, 1, "after the old Launch's Stop")
	kind, at, oldRevision := in.admission(t, old, 0)
	if got := in.endTurn(t, in.stop(t, in.clock.Now())); got != (store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndRecorded, Recorded: 1}) {
		t.Errorf("the new Launch's Stop = %+v; want one recorded", got)
	}
	if k, a, r := in.admission(t, old, 0); k != kind || a != at || r != oldRevision {
		t.Errorf("the old Launch's prompt now admitted %q at %d, revision %d; want it unchanged (%q at %d, revision %d)",
			k, a, r, kind, at, oldRevision)
	}
	if k, _, _ := in.admission(t, h, 0); k != string(store.BusySendAdmissionNativeSignal) {
		t.Errorf("the new Launch's prompt admitted %q; want native_signal", k)
	}
	in.wantAwaiting(t, 0, "after the new Launch's Stop")
}

// TestBusySendTurnEndRaces (M2 P-C4.2, P-C4.3; E4 Main Agent decision 2): a
// parent Stop admits only output whose completion was durably recorded by its
// entry read, by commit order, whichever of its write and the completion's
// commits first; the output observed before the Stop is not enough. Code
// review round 1 F1's interleaving: a completion on a second handle holds its
// write transaction, its transport already written, across the Stop's entry
// read (which neither waits for it nor sees it), then commits before the
// Stop's lifecycle write and its evidence write: that Stop admits nothing. The
// other order: a completion that begins while the Stop holds its evidence
// write (a causal barrier holds it until the completion has begun) waits
// behind it and gives it nothing either. Each time the prompt still awaits,
// and a later Stop admits it.
func TestBusySendTurnEndRaces(t *testing.T) {
	t.Parallel()
	for _, stopFirst := range []bool{false, true} {
		name := map[bool]string{false: "a completion held across the Stop's entry read",
			true: "a completion waiting behind the Stop's write"}[stopFirst]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in, h, revision := newBusySendIssuedAt(t, store.BusySendBoundaryToolBoundary)
			rival, err := store.Open(in.path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rival.Close() })
			observed := in.clock.Now().Add(5 * time.Millisecond)
			completion := store.BusySendCompletion{Ref: store.BusySendPromptRef{HandoverID: h.HandoverID, Revision: revision},
				Output: store.BusySendOutputComplete, Order: store.BusySendOrderEmissionComplete, UnprovenBoundary: true,
				At: observed.Add(-time.Millisecond)}
			lifecycle := func() {
				if got, err := in.s.ApplyHookTransition(in.id, in.ownGate(t, "Stop"), store.StateWaiting, false, "Stop", "", false); err != nil ||
					!got.Applied {
					t.Fatalf("the Stop's lifecycle write = %+v, %v; want applied", got, err)
				}
			}
			holding, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			// hold makes s's next write transaction wait at the first statement
			// held matches until the barrier's release.
			hold := func(s *store.Store, held func(began bool, q string) bool) {
				var began, once atomic.Bool
				store.ObserveDecisionBoundaryForTest(t, s, in.path, func(_ context.Context, q string) error {
					if q == "BEGIN IMMEDIATE" {
						began.Store(true)
					} else if held(began.Load(), q) && once.CompareAndSwap(false, true) {
						close(holding)
						<-release
					}
					return nil
				}, nil)
			}
			wait := func(done chan error, what string) {
				t.Helper()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					t.Fatalf("%s did not return within 10 s of the barrier's release", what)
				}
			}
			completed, recorded := make(chan error, 1), make(chan error, 1)
			var got store.BusySendTurnEndResult
			if !stopFirst {
				hold(rival, func(_ bool, q string) bool { return q == "COMMIT" })
				go func() { _, err := rival.RecordBusySendCompletion(ctx, completion); completed <- err }()
				awaitSignal(t, holding, "the completion's COMMIT")
				stop := in.stop(t, observed)
				if len(stop.Prompts) != 0 {
					t.Fatalf("the entry read found %+v with the completion uncommitted; want none", stop.Prompts)
				}
				unblock()
				wait(completed, "the completion")
				lifecycle()
				before := readBusySendTables(t, in.path)
				got = in.endTurn(t, stop)
				if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
					t.Error("the Stop that admitted nothing wrote")
				}
			} else {
				stop := in.stop(t, observed)
				lifecycle()
				hold(in.s, func(began bool, _ string) bool { return began })
				begun := make(chan struct{})
				var begunOnce sync.Once
				store.ObserveDecisionBoundaryForTest(t, rival, in.path, func(_ context.Context, q string) error {
					if q == "BEGIN IMMEDIATE" {
						begunOnce.Do(func() { close(begun) })
					}
					return nil
				}, nil)
				go func() {
					var err error
					got, err = in.s.RecordBusySendTurnEnd(ctx, stop)
					recorded <- err
				}()
				awaitSignal(t, holding, "the Stop's write transaction")
				go func() { _, err := rival.RecordBusySendCompletion(ctx, completion); completed <- err }()
				awaitSignal(t, begun, "the completion's BEGIN IMMEDIATE")
				unblock()
				wait(recorded, "the Stop")
				wait(completed, "the completion")
			}
			if got != (store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndUnchanged}) {
				t.Errorf("Stop = %+v; want unchanged", got)
			}
			in.wantAwaiting(t, 1, "after both writes")
			if got := in.endTurn(t, in.stop(t, observed.Add(2*time.Millisecond))); got != (store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndRecorded, Recorded: 1}) {
				t.Errorf("a later Stop = %+v; want one recorded", got)
			}
			in.wantAwaiting(t, 0, "after a later Stop")
		})
	}
}

// TestBusySendTurnEndCommitOutcome (M2 P-C4.2: no false zero): a turn end
// whose admission write fails before COMMIT returns that failure and writes
// nothing, so the prompt still awaits; one whose COMMIT lands with its
// acknowledgment lost reports only that it is unconfirmed, with no outcome,
// and its repeat is inert.
func TestBusySendTurnEndCommitOutcome(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected admission write failure")
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure before COMMIT", true: "lost COMMIT acknowledgment"}[lost], func(t *testing.T) {
			t.Parallel()
			in, h, revision := newBusySendIssuedAt(t, store.BusySendBoundaryToolBoundary)
			in.clock.Advance(time.Millisecond)
			in.output(t, h, revision, store.BusySendOutputComplete)
			in.clock.Advance(time.Millisecond)
			stop := in.stop(t, in.clock.Now())
			before := readBusySendTables(t, in.path)
			var fired atomic.Bool
			store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
				if !lost && strings.Contains(q, "SET admission_kind") && fired.CompareAndSwap(false, true) {
					return injected
				}
				return nil
			}, func(context.Context) error {
				if lost && fired.CompareAndSwap(false, true) {
					return errors.New("COMMIT acknowledgment lost")
				}
				return nil
			})
			got, err := in.s.RecordBusySendTurnEnd(context.Background(), stop)
			wantErr := store.CommitUnconfirmed(err)
			if !lost {
				wantErr = errors.Is(err, injected) && !store.CommitUnconfirmed(err)
			}
			if !wantErr || got != (store.BusySendTurnEndResult{}) {
				t.Fatalf("Stop = %+v, %v; want only the %s error", got, err, map[bool]string{false: "injected", true: "unconfirmed"}[lost])
			}
			in.reopen(t)
			if !lost {
				if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
					t.Error("a Stop that failed before COMMIT wrote part of itself")
				}
				in.wantAwaiting(t, 1, "after the failed Stop")
				return
			}
			if kind, _, _ := in.admission(t, h, 0); kind != string(store.BusySendAdmissionNativeSignal) {
				t.Fatalf("admission %q after the landed COMMIT; want native_signal", kind)
			}
			tables := readBusySendTables(t, in.path)
			if again := in.endTurn(t, stop); again != (store.BusySendTurnEndResult{Outcome: store.BusySendTurnEndUnchanged}) ||
				!reflect.DeepEqual(tables, readBusySendTables(t, in.path)) {
				t.Errorf("the Stop again = %+v; want unchanged, writing nothing", again)
			}
			in.wantAwaiting(t, 0, "after the landed Stop")
		})
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
