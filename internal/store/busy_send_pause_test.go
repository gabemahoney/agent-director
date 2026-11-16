package store_test

// The pause claim's store protocol (Task t2.xis.8a.a4, t3.xis.8a.a4.1y;
// SR-6.5; M2 RoutePauseClaim, producer obligations P-C4.1, P-C4.5 to P-C4.7)
// on real SQLite through busy_send_claim_test.go's inbox: the claim's checks
// against raw state, a standing claim's answers for either option value,
// crashed and concurrent callers, owner writes and their revisions, budgets
// and commit outcomes, and the locked plain signal (E6 Main Agent decision 4).
// Claim end at each lifecycle transition is busy_send_lifecycle_test.go's;
// the issue fences are internal/busysend's and internal/hook's; the pause
// call, its signal and its interfaces are pkg/api's.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
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

// pauseClock is a pause call's injected clock, starting at the wall clock so
// the inbox's own wall-clock steps keep their order.
func pauseClock() *tmuxfix.Clock {
	return tmuxfix.NewClock(time.Now().UTC().Truncate(time.Millisecond))
}

// pauseRequest is a pause call capturing in's current Launch (in.dest) at
// clock, owned by owner ("" for a plain pause), its deadline budget from now.
func (in *busySendInbox) pauseRequest(clock *tmuxfix.Clock, owner string, budget time.Duration) store.BusySendPauseRequest {
	return store.BusySendPauseRequest{InstanceID: in.id, LaunchID: in.dest.LaunchID, LaunchToken: in.dest.LaunchToken,
		OwnerCallID: owner, Deadline: clock.Now().Add(budget), Now: clock.Now}
}

// pauseClaim claims req and requires a new standing claim at revision 1.
func (in *busySendInbox) pauseClaim(t *testing.T, req store.BusySendPauseRequest) store.BusySendPauseClaim {
	t.Helper()
	d, err := in.s.ClaimBusySendPauseWithin(context.Background(), req, time.Minute)
	if err != nil || d.Kind != store.BusySendPauseDecisionClaimed || d.Claim == nil || d.Claim.Ref.Revision != 1 ||
		d.Claim.State != store.BusySendPauseClaimStanding || d.Claim.Ref.OwnerCallID != req.OwnerCallID {
		t.Fatalf("pause claim %+v, %v; want a new standing claim", d, err)
	}
	return *d.Claim
}

// pauseIntent persists ref's signal intent at clock and requires NextRef.
func (in *busySendInbox) pauseIntent(t *testing.T, clock *tmuxfix.Clock, ref store.BusySendPauseRef) store.BusySendPauseRef {
	t.Helper()
	w, err := in.s.PersistBusySendPauseSignalWithin(context.Background(), store.BusySendPauseWriteRequest{Ref: ref, Now: clock.Now}, time.Minute)
	if err != nil || !w.Applied || w.NextRef == nil || w.NextRef.Revision != ref.Revision+1 {
		t.Fatalf("signal intent %+v, %v; want applied with the next ref", w, err)
	}
	return *w.NextRef
}

// plainSignal is a counted plain-signal callback returning ret.
type plainSignal struct {
	calls atomic.Int32
	ret   error
}

func (p *plainSignal) signal() error { p.calls.Add(1); return p.ret }

// pauseClaims reads id's claims, oldest first, as "state closed_by outcome
// rN" with " intent" when a signal intent is recorded.
func pauseClaims(t *testing.T, path, id string) []string {
	t.Helper()
	rows, err := openBusySendRaw(t, path).Query(`SELECT state, COALESCE(closed_by, ''), COALESCE(call_outcome, ''), revision,
	        signal_intent_at_ms IS NOT NULL FROM busy_send_pause_claims WHERE instance_id = ? ORDER BY rowid`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var state, closedBy, outcome string
		var revision int64
		var intent bool
		if err := rows.Scan(&state, &closedBy, &outcome, &revision, &intent); err != nil {
			t.Fatal(err)
		}
		c := strings.Join(strings.Fields(fmt.Sprintf("%s %s %s r%d", state, closedBy, outcome, revision)), " ")
		if intent {
			c += " intent"
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func wantPauseClaims(t *testing.T, in *busySendInbox, want ...string) {
	t.Helper()
	if got := pauseClaims(t, in.path, in.id); !reflect.DeepEqual(got, want) {
		t.Fatalf("pause claims %q; want %q", got, want)
	}
}

// readPauseFrame is readBusySendTables with the force rows: no pause call
// stores, finishes or repairs a force.
func readPauseFrame(t *testing.T, path string) map[string][][]any {
	t.Helper()
	return readBusySendTables(t, path, "busy_send_forces", "busy_send_force_members")
}

// butPauseClaims is readPauseFrame without the claims, all a claim, an owner
// write or a closure of the claim itself may change.
func butPauseClaims(t *testing.T, path string) map[string][][]any {
	t.Helper()
	tables := readPauseFrame(t, path)
	delete(tables, "busy_send_pause_claims")
	return tables
}

// TestBusySendPauseEligibility (SR-6.5; M2 P-C4.1 authoritative raw state,
// P-C4.5 the atomic claim): a conditional claim judges the row's raw
// lifecycle state, never its settle-time display, then three facts of the
// captured Launch, each isolated here: an awaiting obligation (issued input
// not taken, even after complete output), an emission intent without completed
// written output (once admission or a negative cleared the obligation: no,
// partial or uncertain output, in an older order-closed hand-over), and a
// pending force (synthetic isolation; a newer refused attempt does not hide
// it). not_waiting, with the raw state, wins over turn_pending. A refusal
// writes nothing, force and Results included, and plain Check is clear on it
// without claiming. Resolved work claims: written output taken, a hand-over
// released before any intent, a running force that issued nothing and an
// earlier Launch's unresolved records.
func TestBusySendPauseEligibility(t *testing.T) {
	t.Parallel()
	type setup func(t *testing.T, in *busySendInbox)
	handover := func(steps ...forceStep) setup {
		return func(t *testing.T, in *busySendInbox) {
			h := in.claim(t, time.Time{}, false)
			for _, step := range steps {
				step(t, in, h)
			}
		}
	}
	ordered := func(k int) forceStep {
		return func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
			if res, err := in.s.RecordBusySendEvidence(context.Background(), store.BusySendPromptEvidence{HandoverID: h.HandoverID,
				PromptIndex: k, LaunchID: in.dest.LaunchID, ConnectionGeneration: in.dest.ConnectionGeneration,
				Order: store.BusySendOrderOrderedAcceptance}); err != nil || !res.Recorded {
				t.Fatalf("order evidence for prompt %d = %+v, %v; want it recorded", k, res, err)
			}
		}
	}
	working := func(t *testing.T, in *busySendInbox) {
		setBusySendFact(t, in.path, `UPDATE spawns SET state = 'working' WHERE instance_id = ?`, in.id)
	}
	hook := func(event string) setup {
		return func(t *testing.T, in *busySendInbox) {
			if a := storefix.ApplyAgentHook(t, in.s, in.id, event, in.dest.SessionID); !a.Applied {
				t.Fatalf("%s %+v; want applied", event, a)
			}
		}
	}
	forceIssued := func(t *testing.T, in *busySendInbox) {
		forceIssue(0)(t, in, in.forceClaim(t, time.Time{}).Handover)
	}
	for _, tc := range []struct {
		name     string
		texts    []string
		setup    setup
		display  string // a settle-time state shown instead of the raw one
		taken    bool   // the obligation is cleared: no awaiting obligation
		cause    string // "" claims
		observed string
	}{
		{name: "raw working shown waiting", setup: hook("UserPromptSubmit"), display: store.StateWaiting,
			cause: store.BusySendPauseNotWaiting, observed: store.StateWorking},
		{name: "raw waiting shown working", setup: func(t *testing.T, in *busySendInbox) { working(t, in); hook("Stop")(t, in) },
			display: store.StateWorking},
		{name: "complete output not yet taken", setup: handover(forceIssue(0), forceOutput(0, store.BusySendOutputComplete)),
			cause: store.BusySendPauseTurnPending},
		{name: "taken with no output", setup: handover(forceIssue(0), forceAdmit(0)), taken: true, cause: store.BusySendPauseTurnPending},
		{name: "taken after partial output", setup: handover(forceIssue(0), forceOutput(0, store.BusySendOutputPartial), forceAdmit(0)),
			taken: true, cause: store.BusySendPauseTurnPending},
		{name: "a negative with no output", setup: handover(forceIssue(0), forceNegate(0)), taken: true, cause: store.BusySendPauseTurnPending},
		{name: "an older order-closed hand-over's uncertain output", setup: func(t *testing.T, in *busySendInbox) {
			handover(forceIssue(0), forceOutput(0, store.BusySendOutputUncertain), ordered(0), forceAdmit(0))(t, in)
			in.admit(t, store.BusySendWaitTurn, "two")
			handover(forceIssue(0), forceOutput(0, store.BusySendOutputComplete), forceAdmit(0))(t, in)
		}, taken: true, cause: store.BusySendPauseTurnPending},
		{name: "written output taken", setup: handover(forceIssue(0), forceOutput(0, store.BusySendOutputComplete), forceAdmit(0)), taken: true},
		{name: "a hand-over released before any intent", setup: func(t *testing.T, in *busySendInbox) {
			h := in.claim(t, time.Time{}, false)
			if r, err := in.s.ReleaseBusySendClaims(context.Background(), store.BusySendReleaseRequest{HandoverID: h.HandoverID,
				Revision: h.Revision}); err != nil || r.Released != 1 {
				t.Fatalf("release %+v, %v; want its Message Held again", r, err)
			}
		}},
		{name: "a running force that issued nothing", setup: func(t *testing.T, in *busySendInbox) { in.forceClaim(t, time.Time{}) }},
		{name: "a running force's issued prompt", setup: forceIssued, cause: store.BusySendPauseTurnPending},
		{name: "a pending force, its prompts otherwise resolved", texts: busySendTwoPrompts, setup: func(t *testing.T, in *busySendInbox) {
			c := in.forceClaim(t, time.Time{})
			for _, step := range []forceStep{forceIssue(0), forceOutput(0, store.BusySendOutputComplete), forceAdmit(0), forceIssue(1)} {
				step(t, in, c.Handover)
			}
			if end, err := in.s.EndBusySendForceCall(context.Background(), c.Ref, time.Now()); err != nil ||
				end.Force.Outcome != store.BusySendForcePending {
				t.Fatalf("force call end %+v, %v; want pending", end, err)
			}
			// Synthetic: prompt 1's output and admission without the
			// writer that would finish the force, so only its pending
			// outcome remains.
			setBusySendFact(t, in.path, `UPDATE busy_send_prompts SET transport = 'written', transport_at_ms = 1,
			    admission_kind = 'prompt_sighting', admitted_at_ms = 1 WHERE handover_id = ? AND prompt_index = 1`, c.Handover.HandoverID)
			if newer, err := in.s.ClaimBusySendForce(context.Background(), in.forceRequest(t, time.Time{})); err != nil ||
				newer.Force.Outcome != store.BusySendForceRefused {
				t.Fatalf("a newer force attempt %+v, %v; want it refused", newer, err)
			}
		}, taken: true, cause: store.BusySendPauseTurnPending},
		{name: "an earlier Launch's unresolved intent", setup: func(t *testing.T, in *busySendInbox) {
			handover(forceIssue(0))(t, in)
			hook("SessionEnd")(t, in)
			const token = "1122334455667788"
			res, version, err := in.s.MoveToPendingWithExit(in.id, in.row(t).Snapshot, 2000, token, "", "", store.LaunchOwner{},
				store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, in.s, in.id))
			if err != nil || res != store.CondApplied {
				t.Fatalf("resume %v, %v; want applied", res, err)
			}
			if res, err := in.s.RecordLaunchIdentity(in.id, version, token, store.LaunchIdentity{}); err != nil || res != store.CondApplied {
				t.Fatalf("publication %v, %v; want applied", res, err)
			}
			setBusySendFact(t, in.path, `UPDATE spawns SET state = 'waiting' WHERE instance_id = ?`, in.id)
			pub, err := in.s.GetBusySendPublishedLaunch(in.id)
			if err != nil {
				t.Fatal(err)
			}
			in.dest.LaunchID, in.dest.LaunchToken = pub.LaunchID, pub.LaunchToken
		}},
		{name: "raw working with issued input unresolved", setup: func(t *testing.T, in *busySendInbox) { forceIssued(t, in); working(t, in) },
			cause: store.BusySendPauseNotWaiting, observed: store.StateWorking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			texts := tc.texts
			if texts == nil {
				texts = []string{"one"}
			}
			in := newBusySendInbox(t, texts...)
			tc.setup(t, in)
			if tc.display != "" {
				_, stamp, err := apitest.ReadSettle(in.path, in.id)
				if err != nil {
					t.Fatal(err)
				}
				shown, err := in.s.GetSpawnSettled(in.id, store.SettleRead{SettleMs: settleT, NowMs: stamp})
				if err != nil || shown.ReportedState != tc.display || shown.State == tc.display {
					t.Fatalf("settled read %s over raw %s, %v; want %s shown over another raw state", shown.ReportedState, shown.State, err, tc.display)
				}
			}
			if tc.taken && in.awaiting(t) != 0 {
				t.Fatal("the obligation is still awaited; the row would not isolate its fact")
			}
			clock := pauseClock()
			before := readPauseFrame(t, in.path)
			if tc.cause == "" {
				in.pauseClaim(t, in.pauseRequest(clock, "call-1", time.Minute))
				delete(before, "busy_send_pause_claims")
				if !reflect.DeepEqual(before, butPauseClaims(t, in.path)) {
					t.Fatal("the claim changed more than its own record")
				}
				wantPauseClaims(t, in, "standing r1")
				return
			}
			got, err := in.s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-1", time.Minute), time.Minute)
			want := store.BusySendPauseDecision{Kind: store.BusySendPauseDecisionRefused, Cause: tc.cause, ObservedState: tc.observed}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("claim %+v, %v; want %+v", got, err, want)
			}
			check, err := in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute)
			if err != nil || !reflect.DeepEqual(check, store.BusySendPauseDecision{Kind: store.BusySendPauseDecisionClear}) {
				t.Fatalf("plain Check %+v, %v; want clear", check, err)
			}
			if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
				t.Fatal("a refused claim or a clear Check wrote something")
			}
		})
	}
}

// TestBusySendPauseTargetErrors (SR-6.5; M2 P-C4.5): a captured target that
// no longer holds is an error, never an answer or permission to fall back: a
// missing row, no consistent binding, another Launch or token, a row holding
// another token, an uncovered Launch. Claim, Check and the plain signal write
// nothing and never call the signal. A captured Launch whose End is recorded
// is a disposition: a conditional claim answers ended with no claim made, and
// Check and the plain signal behave as SR-6.4's plain pause (Main Agent's
// ruling on the a4 code-writer's Q1).
func TestBusySendPauseTargetErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, in *busySendInbox, req *store.BusySendPauseRequest)
		want  error
	}{
		{"a missing row", func(_ *testing.T, _ *busySendInbox, req *store.BusySendPauseRequest) {
			req.InstanceID = uuid.NewString()
		},
			store.ErrSpawnNotFound},
		{"no consistent binding", func(t *testing.T, in *busySendInbox, _ *store.BusySendPauseRequest) {
			storefix.UnbindBusySend(t, in.path, in.id)
		},
			store.ErrBusySendNoBinding},
		{"another Launch", func(_ *testing.T, _ *busySendInbox, req *store.BusySendPauseRequest) { req.LaunchID = uuid.NewString() },
			store.ErrBusySendLaunchChanged},
		{"another token", func(_ *testing.T, _ *busySendInbox, req *store.BusySendPauseRequest) {
			req.LaunchToken = "ffffffffffffffff"
		},
			store.ErrBusySendLaunchChanged},
		{"the row holds another token", func(t *testing.T, in *busySendInbox, _ *store.BusySendPauseRequest) {
			setBusySendFact(t, in.path, `UPDATE spawns SET launch_token = 'ffffffffffffffff' WHERE instance_id = ?`, in.id)
		}, store.ErrBusySendLaunchChanged},
		{"an uncovered Launch", func(t *testing.T, in *busySendInbox, _ *store.BusySendPauseRequest) {
			if err := apitest.SeedBusySendCoverage(in.path, in.id, store.BusySendNotCovered, store.BusySendCovered); err != nil {
				t.Fatal(err)
			}
		}, store.ErrBusySendLaunchChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, "held")
			clock := pauseClock()
			req := in.pauseRequest(clock, "call-1", time.Minute)
			tc.setup(t, in, &req)
			before := readPauseFrame(t, in.path)
			if d, err := in.s.ClaimBusySendPauseWithin(ctx, req, time.Minute); !errors.Is(err, tc.want) || !reflect.DeepEqual(d, store.BusySendPauseDecision{}) {
				t.Errorf("claim %+v, %v; want only %v", d, err, tc.want)
			}
			if d, err := in.s.CheckBusySendPauseWithin(ctx, req, time.Minute); !errors.Is(err, tc.want) || !reflect.DeepEqual(d, store.BusySendPauseDecision{}) {
				t.Errorf("Check %+v, %v; want only %v", d, err, tc.want)
			}
			var p plainSignal
			if r, err := in.s.SignalPlainBusySendPauseWithin(ctx, req, p.signal, time.Minute); !errors.Is(err, tc.want) ||
				!reflect.DeepEqual(r, store.BusySendPlainPauseSignalResult{}) || p.calls.Load() != 0 {
				t.Errorf("plain signal %+v, %v, %d calls; want only %v, no call", r, err, p.calls.Load(), tc.want)
			}
			if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
				t.Error("a call on a target that no longer holds wrote something")
			}
		})
	}
	t.Run("an ended current Launch", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		in := newBusySendInbox(t, "held")
		if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
			t.Fatalf("SessionEnd %+v; want applied", a)
		}
		clock := pauseClock()
		before := readPauseFrame(t, in.path)
		if d, err := in.s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-1", time.Minute), time.Minute); err != nil ||
			!reflect.DeepEqual(d, store.BusySendPauseDecision{Kind: store.BusySendPauseDecisionEnded}) {
			t.Errorf("claim %+v, %v; want ended with no claim", d, err)
		}
		if d, err := in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute); err != nil ||
			!reflect.DeepEqual(d, store.BusySendPauseDecision{Kind: store.BusySendPauseDecisionClear}) {
			t.Errorf("Check %+v, %v; want clear", d, err)
		}
		var p plainSignal
		if r, err := in.s.SignalPlainBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), p.signal, time.Minute); err != nil ||
			!r.Invoked || !r.Completed || p.calls.Load() != 1 {
			t.Errorf("plain signal %+v, %v, %d calls; want SR-6.4's one signal", r, err, p.calls.Load())
		}
		if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
			t.Error("a pause of an ended Launch wrote something")
		}
	})
}

// TestBusySendPauseRepeatAnswers (SR-6.5; M2 P-C4.5 RepeatDecision "for
// either option value", P-C4.7): a standing claim answers a later conditional
// claim, plain Check and the plain signal alike with its exact snapshot, cause
// included, its owner's continuation dropped and the store reopened: a
// recorded outcome wins over time and intent; otherwise pause_in_progress
// through the immutable deadline, intent or not; only strictly past it does an
// intent give pause_uncertain. The answer wins over the row's current
// eligibility: where the row has since left waiting, a fresh check would
// refuse not_waiting instead. Answers write nothing and suppress the signal,
// and reads, observations and a day's elapsed time leave the claim standing.
func TestBusySendPauseRepeatAnswers(t *testing.T) {
	t.Parallel()
	const budget = 10 * time.Second
	for _, tc := range []struct {
		name    string
		intent  bool
		outcome store.BusySendPauseCallOutcome
		working bool          // the row left waiting after the claim
		at      time.Duration // after the claim; its deadline is at budget
		kind    store.BusySendPauseDecisionKind
		cause   string
	}{
		{"no intent, at the deadline", false, "", true, budget, store.BusySendPauseDecisionRefused, store.BusySendPauseInProgress},
		{"intent, at the deadline", true, "", false, budget, store.BusySendPauseDecisionRefused, store.BusySendPauseInProgress},
		{"intent, past the deadline", true, "", true, budget + time.Millisecond, store.BusySendPauseDecisionRefused, store.BusySendPauseUncertain},
		{"paused, within the deadline", true, store.BusySendPausePaused, true, time.Second, store.BusySendPauseDecisionRecorded, ""},
		{"exit_uncertain, past the deadline", true, store.BusySendPauseExitUncertain, false, budget + time.Hour, store.BusySendPauseDecisionRecorded, ""},
		{"failed, past the deadline", true, store.BusySendPauseFailed, true, budget + time.Hour, store.BusySendPauseDecisionRecorded, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, "held")
			clock := pauseClock()
			start := clock.Now()
			c := in.pauseClaim(t, in.pauseRequest(clock, "call-1", budget))
			want := c // the owner's own last snapshot, retained apart from any answer
			if tc.intent {
				w, err := in.s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: c.Ref, Now: clock.Now}, time.Minute)
				if err != nil || !w.Applied {
					t.Fatalf("intent %+v, %v; want applied", w, err)
				}
				want = *w.Claim
				if tc.outcome != "" {
					if w, err = in.s.RecordBusySendPauseOutcomeWithin(ctx, store.BusySendPauseOutcomeRequest{Ref: *w.NextRef, Now: clock.Now,
						Outcome: tc.outcome, Cause: "cause-1", VerifiedExitAt: c.Deadline}, time.Minute); err != nil || !w.Applied {
						t.Fatalf("outcome %+v, %v; want applied", w, err)
					}
					want = *w.Claim
				}
			}
			if tc.working {
				setBusySendFact(t, in.path, `UPDATE spawns SET state = 'working' WHERE instance_id = ?`, in.id)
			}
			in.reopen(t) // the owner's continuation is gone
			clock.Advance(start.Add(tc.at).Sub(clock.Now()))
			stored := pauseClaims(t, in.path, in.id)
			before := readPauseFrame(t, in.path)
			answer := store.BusySendPauseDecision{Kind: tc.kind, Cause: tc.cause, Claim: &want}
			claim, err := in.s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-2", time.Minute), time.Minute)
			if err != nil || !reflect.DeepEqual(claim, answer) || (tc.outcome != "" && claim.Claim.CallCause != "cause-1") {
				t.Fatalf("claim %+v, %v; want %+v with %+v", claim, err, answer, want)
			}
			if check, err := in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute); err != nil ||
				!reflect.DeepEqual(check, answer) {
				t.Errorf("plain Check %+v, %v; want %+v", check, err, answer)
			}
			var p plainSignal
			r, err := in.s.SignalPlainBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), p.signal, time.Minute)
			if err != nil || r.Invoked || r.Completed || r.Recheck || r.Decision == nil || !reflect.DeepEqual(*r.Decision, answer) || p.calls.Load() != 0 {
				t.Errorf("plain signal %+v, %v, %d calls; want %+v and no call", r, err, p.calls.Load(), answer)
			}
			clock.Advance(24 * time.Hour)
			if _, err := in.s.ReadBusySendObservation(in.id, store.DefaultLockWait); err != nil {
				t.Fatal(err)
			}
			if _, err := in.s.GetSpawn(in.id); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
				t.Fatal("answers, reads or elapsed time changed the store")
			}
			wantPauseClaims(t, in, stored...)
		})
	}
}

// TestBusySendPauseAbandonment (SR-6.5; M2 P-C4.5, P-C4.7): a claim whose
// owner died before its signal intent is abandoned only strictly past its
// deadline, and only an explicit pause acts on it, keeping its history: a
// conditional claim releases it (abandoned_before_signal) and, in the same
// transaction, makes its own fresh attempt, a new claim or the exact refusal
// with the release committed; plain Check releases it and answers clear,
// claiming nothing. The dead owner's ref is stale for good.
func TestBusySendPauseAbandonment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		working bool
		check   bool
		claims  []string
	}{
		{"a conditional claim reclaims", false, false, []string{"released abandoned_before_signal r2", "standing r1"}},
		{"a conditional claim refuses", true, false, []string{"released abandoned_before_signal r2"}},
		{"plain Check is clear", false, true, []string{"released abandoned_before_signal r2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, "held")
			clock := pauseClock()
			c := in.pauseClaim(t, in.pauseRequest(clock, "call-1", 10*time.Second))
			in.reopen(t)
			clock.Advance(10*time.Second + time.Millisecond)
			if tc.working {
				setBusySendFact(t, in.path, `UPDATE spawns SET state = 'working' WHERE instance_id = ?`, in.id)
			}
			before := butPauseClaims(t, in.path)
			var d store.BusySendPauseDecision
			var err error
			if tc.check {
				d, err = in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute)
			} else {
				d, err = in.s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-2", time.Minute), time.Minute)
			}
			released := func(c *store.BusySendPauseClaim) bool {
				return c != nil && c.Ref.ClaimID != "" && c.State == store.BusySendPauseClaimReleased &&
					c.ClosedBy == store.BusySendPauseAbandonedBeforeSignal && c.ClosedAt.Equal(clock.Now()) && c.CallOutcome == ""
			}
			switch {
			case err != nil:
				t.Fatal(err)
			case tc.check && (d.Kind != store.BusySendPauseDecisionClear || !released(d.Claim)):
				t.Fatalf("Check %+v; want clear with the released claim", d)
			case tc.working && (d.Kind != store.BusySendPauseDecisionRefused || d.Cause != store.BusySendPauseNotWaiting ||
				d.ObservedState != store.StateWorking || !released(d.Claim)):
				t.Fatalf("claim %+v; want the fresh not_waiting refusal with the released claim", d)
			case !tc.check && !tc.working && (d.Kind != store.BusySendPauseDecisionClaimed || d.Claim.Ref.OwnerCallID != "call-2"):
				t.Fatalf("claim %+v; want a new claim", d)
			}
			wantPauseClaims(t, in, tc.claims...)
			if !reflect.DeepEqual(before, butPauseClaims(t, in.path)) {
				t.Fatal("the release or fresh attempt changed more than the claims")
			}
			if w, err := in.s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: c.Ref, Now: clock.Now}, time.Minute); err != nil || w.Applied {
				t.Fatalf("the dead owner's intent %+v, %v; want nothing applied", w, err)
			}
		})
	}
}

// TestBusySendPauseConcurrentClaims (SR-6.5; M2 P-C4.5): two stores racing
// for one Launch, the first held inside its claim transaction until the
// second reaches BEGIN IMMEDIATE, give one claim and one pause_in_progress
// answering it, also when both find an abandoned claim to release.
func TestBusySendPauseConcurrentClaims(t *testing.T) {
	t.Parallel()
	for _, reclaim := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "reclaim"}[reclaim], func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, "held")
			clock := pauseClock()
			claims := []string{"standing r1"}
			if reclaim {
				in.pauseClaim(t, in.pauseRequest(clock, "call-0", time.Second))
				clock.Advance(time.Second + time.Millisecond)
				claims = []string{"released abandoned_before_signal r2", "standing r1"}
			}
			var first, second store.BusySendPauseDecision
			var firstErr, secondErr error
			raceBusySendSteps(t, in, "INSERT INTO busy_send_pause_claims", func(s *store.Store) {
				first, firstErr = s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-1", time.Minute), time.Minute)
			}, func(s *store.Store) {
				second, secondErr = s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-2", time.Minute), time.Minute)
			})
			if firstErr != nil || first.Kind != store.BusySendPauseDecisionClaimed {
				t.Fatalf("first claim %+v, %v; want claimed", first, firstErr)
			}
			if secondErr != nil || second.Cause != store.BusySendPauseInProgress || second.Claim == nil || second.Claim.Ref != first.Claim.Ref {
				t.Fatalf("second claim %+v, %v; want pause_in_progress answering the first", second, secondErr)
			}
			wantPauseClaims(t, in, claims...)
		})
	}
}

// TestBusySendPauseOwnerWrites (SR-6.5; M2 P-C4.5 to P-C4.7): the owning
// call's writes compare every captured key and the revision, and advance it
// once. The signal intent keeps claimed-at and the deadline and returns
// NextRef only to its owner; an outcome records its exact cause at its own
// time, never NextRef, and is never replaced; paused needs an exit verified
// within the original deadline; a refusal proven before the signal, before or
// after the intent, records refused and releases the claim in one write,
// keeping any intent. A wrong store, instance, Launch, owner, revision or
// phase is inert. Only the claim changes: Held Messages, their order, options
// and reservations stay as they were.
func TestBusySendPauseOwnerWrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	type write func(s *store.Store, ref store.BusySendPauseRef, now func() time.Time) (store.BusySendPauseWriteResult, error)
	intent := func(s *store.Store, ref store.BusySendPauseRef, now func() time.Time) (store.BusySendPauseWriteResult, error) {
		return s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: ref, Now: now}, time.Minute)
	}
	outcome := func(o store.BusySendPauseCallOutcome, exit time.Time) write {
		return func(s *store.Store, ref store.BusySendPauseRef, now func() time.Time) (store.BusySendPauseWriteResult, error) {
			return s.RecordBusySendPauseOutcomeWithin(ctx, store.BusySendPauseOutcomeRequest{Ref: ref, Now: now, Outcome: o,
				Cause: "cause-" + string(o), VerifiedExitAt: exit}, time.Minute)
		}
	}
	release := func(s *store.Store, ref store.BusySendPauseRef, now func() time.Time) (store.BusySendPauseWriteResult, error) {
		return s.ReleaseBusySendPauseBeforeSignalWithin(ctx, store.BusySendPauseRefusalRequest{Ref: ref, Now: now, Cause: "agent_process_unverifiable"}, time.Minute)
	}
	wrong := map[string]func(*store.BusySendPauseRef){
		"claim":    func(r *store.BusySendPauseRef) { r.ClaimID = uuid.NewString() },
		"instance": func(r *store.BusySendPauseRef) { r.InstanceID = uuid.NewString() },
		"Launch":   func(r *store.BusySendPauseRef) { r.LaunchID = uuid.NewString() },
		"owner":    func(r *store.BusySendPauseRef) { r.OwnerCallID = "call-2" },
		"older":    func(r *store.BusySendPauseRef) { r.Revision-- },
		"newer":    func(r *store.BusySendPauseRef) { r.Revision++ },
	}
	for _, tc := range []struct {
		name   string
		intent bool
		last   write
		claims []string
	}{
		{"paused", true, nil, []string{"standing paused r3 intent"}},
		{"exit_uncertain", true, nil, []string{"standing exit_uncertain r3 intent"}},
		{"failed", true, nil, []string{"standing failed r3 intent"}},
		{"refused before the intent", false, release, []string{"released refused_before_signal refused r2"}},
		{"refused after the intent", true, release, []string{"released refused_before_signal refused r3 intent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, "one", "two")
			other := newBusySendInbox(t)
			clock := pauseClock()
			c := in.pauseClaim(t, in.pauseRequest(clock, "call-1", 10*time.Second))
			before := butPauseClaims(t, in.path)
			inert := func(what string, w write, ref store.BusySendPauseRef) {
				t.Helper()
				claims := pauseClaims(t, in.path, in.id)
				if got, err := w(in.s, ref, clock.Now); err != nil || got.Applied || got.NextRef != nil {
					t.Fatalf("%s %+v, %v; want nothing applied", what, got, err)
				}
				wantPauseClaims(t, in, claims...)
			}
			for name, mutate := range wrong {
				ref := c.Ref
				mutate(&ref)
				inert("intent with the wrong "+name, intent, ref)
			}
			if got, err := intent(other.s, c.Ref, clock.Now); err != nil || got != (store.BusySendPauseWriteResult{}) {
				t.Fatalf("intent on another store %+v, %v; want nothing", got, err)
			}
			inert("an outcome before the intent", outcome(store.BusySendPauseFailed, time.Time{}), c.Ref)
			ref := c.Ref
			if tc.intent {
				clock.Advance(time.Second)
				w, err := intent(in.s, ref, clock.Now)
				if err != nil || !w.Applied || w.NextRef == nil || *w.NextRef != w.Claim.Ref || w.NextRef.Revision != 2 ||
					!w.Claim.SignalIntentAt.Equal(clock.Now()) || !w.Claim.ClaimedAt.Equal(c.ClaimedAt) || !w.Claim.Deadline.Equal(c.Deadline) {
					t.Fatalf("intent %+v, %v; want applied at the clock, claimed-at and deadline kept, revision 2", w, err)
				}
				inert("the stale ref's intent", intent, ref)
				ref = *w.NextRef
				inert("a second intent", intent, ref)
			}
			for name, mutate := range wrong {
				stale := ref
				mutate(&stale)
				inert("release with the wrong "+name, release, stale)
			}
			last := tc.last
			if last == nil {
				o := store.BusySendPauseCallOutcome(tc.name)
				if o == store.BusySendPausePaused {
					for _, bad := range []time.Time{{}, c.Deadline.Add(time.Millisecond)} {
						claims := pauseClaims(t, in.path, in.id)
						if w, err := outcome(o, bad)(in.s, ref, clock.Now); err == nil || w.Applied {
							t.Fatalf("paused with its exit verified at %v: %+v, %v; want an error", bad, w, err)
						}
						wantPauseClaims(t, in, claims...)
					}
				}
				if w, err := outcome(store.BusySendPauseRefused, c.Deadline)(in.s, ref, clock.Now); err == nil || w.Applied {
					t.Fatalf("refused as a recorded outcome %+v, %v; want an error", w, err)
				}
				last = outcome(o, c.Deadline)
			}
			clock.Advance(time.Second)
			w, err := last(in.s, ref, clock.Now)
			if err != nil || !w.Applied || w.NextRef != nil || w.Claim.Ref.Revision != ref.Revision+1 || !w.Claim.CallOutcomeAt.Equal(clock.Now()) {
				t.Fatalf("outcome %+v, %v; want applied at the clock with no next ref", w, err)
			}
			if tc.last != nil && (w.Claim.CallCause != "agent_process_unverifiable" || !w.Claim.ClosedAt.Equal(clock.Now())) {
				t.Errorf("refusal %+v; want its cause and the release at the clock", w.Claim)
			}
			if tc.last == nil && w.Claim.CallCause != "cause-"+tc.name {
				t.Errorf("outcome cause %q; want cause-%s", w.Claim.CallCause, tc.name)
			}
			wantPauseClaims(t, in, tc.claims...)
			for _, again := range []write{intent, release, outcome(store.BusySendPauseFailed, c.Deadline), outcome(store.BusySendPauseExitUncertain, time.Time{})} {
				inert("a write after the outcome", again, w.Claim.Ref)
			}
			if !reflect.DeepEqual(before, butPauseClaims(t, in.path)) {
				t.Fatal("an owner write changed more than its claim")
			}
		})
	}
}

// TestBusySendPauseStaleAuthority (SR-6.5; M2 P-C4.7 revisions, late
// callbacks): once a claim is released (refused before its signal, or
// abandoned and released), ended by its Launch's verified exit before or after
// an outcome, or its Launch replaced, the owner's refs and every snapshot
// returned to it are inert for each owner write. A lifecycle end keeps the
// intent and any outcome and cause, so ended with no outcome stays apart from
// a recorded one; the ended claim is the answer a conditional claim, plain
// Check and the plain signal all give, its exact snapshot, and the signal is
// not called (apart from Q1's ended Launch with no claim,
// TestBusySendPauseTargetErrors'). A new Launch is claimed afresh. While a
// claim stands on a Launch whose End is recorded, its intent is refused.
func TestBusySendPauseStaleAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	exitEnd := func(t *testing.T, in *busySendInbox) {
		if ended, err := in.s.EndBusySendLaunch(in.id, in.dest.LaunchToken); err != nil || !ended {
			t.Fatalf("EndBusySendLaunch %t, %v; want the Launch ended", ended, err)
		}
	}
	// lateEnded is a late owner write's no-op result on an ended claim: its
	// snapshot, retained apart from any answer.
	lateEnded := func(t *testing.T, late store.BusySendPauseWriteResult, err error, outcome store.BusySendPauseCallOutcome, cause string) *store.BusySendPauseClaim {
		t.Helper()
		if err != nil || late.Applied || late.Claim == nil || late.Claim.State != store.BusySendPauseClaimEnded ||
			late.Claim.ClosedBy != store.BusySendPauseLaunchEnd || late.Claim.SignalIntentAt.IsZero() ||
			late.Claim.CallOutcome != outcome || late.Claim.CallCause != cause {
			t.Fatalf("the late write %+v, %v; want the ended snapshot keeping %q %q, nothing applied", late, err, outcome, cause)
		}
		return late.Claim
	}
	type closed func(t *testing.T, in *busySendInbox, clock *tmuxfix.Clock, c store.BusySendPauseClaim) ([]store.BusySendPauseRef, *store.BusySendPauseClaim)
	for _, tc := range []struct {
		name   string
		close  closed // the refs left to the owner, and the ended claim, if any
		claims []string
	}{
		{"refused before its signal", func(t *testing.T, in *busySendInbox, clock *tmuxfix.Clock, c store.BusySendPauseClaim) ([]store.BusySendPauseRef, *store.BusySendPauseClaim) {
			next := in.pauseIntent(t, clock, c.Ref)
			w, err := in.s.ReleaseBusySendPauseBeforeSignalWithin(ctx, store.BusySendPauseRefusalRequest{Ref: next, Now: clock.Now}, time.Minute)
			if err != nil || !w.Applied {
				t.Fatalf("release %+v, %v; want applied", w, err)
			}
			return []store.BusySendPauseRef{c.Ref, next, w.Claim.Ref}, nil
		}, []string{"released refused_before_signal refused r3 intent", "standing r1"}},
		{"abandoned and released", func(t *testing.T, in *busySendInbox, clock *tmuxfix.Clock, c store.BusySendPauseClaim) ([]store.BusySendPauseRef, *store.BusySendPauseClaim) {
			clock.Advance(10*time.Second + time.Millisecond)
			d, err := in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute)
			if err != nil || d.Claim == nil {
				t.Fatalf("Check %+v, %v; want the abandoned claim released", d, err)
			}
			return []store.BusySendPauseRef{c.Ref, d.Claim.Ref}, nil
		}, []string{"released abandoned_before_signal r2", "standing r1"}},
		{"ended before an outcome", func(t *testing.T, in *busySendInbox, clock *tmuxfix.Clock, c store.BusySendPauseClaim) ([]store.BusySendPauseRef, *store.BusySendPauseClaim) {
			next := in.pauseIntent(t, clock, c.Ref)
			exitEnd(t, in)
			late, err := in.s.RecordBusySendPauseOutcomeWithin(ctx, store.BusySendPauseOutcomeRequest{Ref: next, Now: clock.Now,
				Outcome: store.BusySendPausePaused, VerifiedExitAt: clock.Now()}, time.Minute)
			ended := lateEnded(t, late, err, "", "")
			return []store.BusySendPauseRef{c.Ref, next, ended.Ref}, ended
		}, []string{"ended launch_end r3 intent"}},
		{"ended after an outcome", func(t *testing.T, in *busySendInbox, clock *tmuxfix.Clock, c store.BusySendPauseClaim) ([]store.BusySendPauseRef, *store.BusySendPauseClaim) {
			next := in.pauseIntent(t, clock, c.Ref)
			w, err := in.s.RecordBusySendPauseOutcomeWithin(ctx, store.BusySendPauseOutcomeRequest{Ref: next, Now: clock.Now,
				Outcome: store.BusySendPauseExitUncertain, Cause: "exit_not_observed"}, time.Minute)
			if err != nil || !w.Applied {
				t.Fatalf("outcome %+v, %v; want applied", w, err)
			}
			exitEnd(t, in)
			late, err := in.s.ReleaseBusySendPauseBeforeSignalWithin(ctx, store.BusySendPauseRefusalRequest{Ref: w.Claim.Ref, Now: clock.Now}, time.Minute)
			ended := lateEnded(t, late, err, store.BusySendPauseExitUncertain, "exit_not_observed")
			return []store.BusySendPauseRef{c.Ref, next, w.Claim.Ref, ended.Ref}, ended
		}, []string{"ended launch_end exit_uncertain r4 intent"}},
		{"its Launch replaced", func(t *testing.T, in *busySendInbox, clock *tmuxfix.Clock, c store.BusySendPauseClaim) ([]store.BusySendPauseRef, *store.BusySendPauseClaim) {
			if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
				t.Fatalf("SessionEnd %+v; want applied", a)
			}
			const token = "1122334455667788"
			res, version, err := in.s.MoveToPendingWithExit(in.id, in.row(t).Snapshot, 2000, token, "", "", store.LaunchOwner{},
				store.BusySendDefaultsChoice{}, exitEvidenceForTest(t, in.s, in.id))
			if err != nil || res != store.CondApplied {
				t.Fatalf("resume %v, %v; want applied", res, err)
			}
			if res, err := in.s.RecordLaunchIdentity(in.id, version, token, store.LaunchIdentity{}); err != nil || res != store.CondApplied {
				t.Fatalf("publication %v, %v; want applied", res, err)
			}
			setBusySendFact(t, in.path, `UPDATE spawns SET state = 'waiting' WHERE instance_id = ?`, in.id)
			pub, err := in.s.GetBusySendPublishedLaunch(in.id)
			if err != nil {
				t.Fatal(err)
			}
			in.dest.LaunchID, in.dest.LaunchToken = pub.LaunchID, pub.LaunchToken
			return []store.BusySendPauseRef{c.Ref}, nil
		}, []string{"ended launch_end r2", "standing r1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, "held")
			clock := pauseClock()
			c := in.pauseClaim(t, in.pauseRequest(clock, "call-1", 10*time.Second))
			refs, ended := tc.close(t, in, clock, c)
			before := readPauseFrame(t, in.path)
			d, err := in.s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-2", time.Minute), time.Minute)
			if ended == nil {
				if err != nil || d.Kind != store.BusySendPauseDecisionClaimed {
					t.Fatalf("a later claim %+v, %v; want a fresh claim", d, err)
				}
			} else {
				answer := store.BusySendPauseDecision{Kind: store.BusySendPauseDecisionEnded, Claim: ended}
				if err != nil || !reflect.DeepEqual(d, answer) {
					t.Fatalf("a later claim %+v, %v; want %+v", d, err, answer)
				}
				if check, err := in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute); err != nil ||
					!reflect.DeepEqual(check, answer) {
					t.Errorf("plain Check %+v, %v; want %+v", check, err, answer)
				}
				var p plainSignal
				r, err := in.s.SignalPlainBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), p.signal, time.Minute)
				if err != nil || r.Invoked || r.Completed || r.Recheck || r.Decision == nil || !reflect.DeepEqual(*r.Decision, answer) || p.calls.Load() != 0 {
					t.Errorf("plain signal %+v, %v, %d calls; want %+v and no call", r, err, p.calls.Load(), answer)
				}
				if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
					t.Fatal("an ended claim's answers changed the store")
				}
			}
			wantPauseClaims(t, in, tc.claims...)
			before = readPauseFrame(t, in.path)
			for _, ref := range refs {
				for name, call := range map[string]func() (store.BusySendPauseWriteResult, error){
					"intent": func() (store.BusySendPauseWriteResult, error) {
						return in.s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: ref, Now: clock.Now}, time.Minute)
					},
					"paused": func() (store.BusySendPauseWriteResult, error) {
						return in.s.RecordBusySendPauseOutcomeWithin(ctx, store.BusySendPauseOutcomeRequest{Ref: ref, Now: clock.Now,
							Outcome: store.BusySendPausePaused, VerifiedExitAt: c.Deadline}, time.Minute)
					},
					"failed": func() (store.BusySendPauseWriteResult, error) {
						return in.s.RecordBusySendPauseOutcomeWithin(ctx, store.BusySendPauseOutcomeRequest{Ref: ref, Now: clock.Now,
							Outcome: store.BusySendPauseFailed}, time.Minute)
					},
					"release": func() (store.BusySendPauseWriteResult, error) {
						return in.s.ReleaseBusySendPauseBeforeSignalWithin(ctx, store.BusySendPauseRefusalRequest{Ref: ref, Now: clock.Now}, time.Minute)
					},
				} {
					if w, err := call(); err != nil || w.Applied || w.NextRef != nil {
						t.Errorf("%s with the old ref at revision %d: %+v, %v; want nothing applied", name, ref.Revision, w, err)
					}
				}
			}
			if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
				t.Fatal("an old ref's write changed the store")
			}
		})
	}
	t.Run("intent on a logically ended Launch", func(t *testing.T) {
		t.Parallel()
		in := newBusySendInbox(t, "held")
		clock := pauseClock()
		c := in.pauseClaim(t, in.pauseRequest(clock, "call-1", 10*time.Second))
		if a := storefix.ApplyAgentHook(t, in.s, in.id, "SessionEnd", in.dest.SessionID); !a.Applied {
			t.Fatalf("SessionEnd %+v; want applied", a)
		}
		if w, err := in.s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: c.Ref, Now: clock.Now}, time.Minute); err != nil ||
			w.Applied || w.NextRef != nil || w.Claim == nil {
			t.Fatalf("intent %+v, %v; want the standing snapshot, nothing applied", w, err)
		}
		wantPauseClaims(t, in, "standing r1")
	})
}

// TestBusySendPauseBudget (SR-6.5; M2 P-C4.5, P-C4.7 fixed budget): the clock
// is read once the write lock is held, so a deadline reached while the lock
// was awaited leaves a claim, an intent and the plain signal without
// authority; so does a canceled caller or a lock not won within the remaining
// budget. Nothing is written, and the plain signal, not admitted, is never
// called later, also when its caller stopped waiting while its continuation
// stood just before admission, then went on with an unspent clock.
func TestBusySendPauseBudget(t *testing.T) {
	t.Parallel()
	type call func(ctx context.Context, in *busySendInbox, clock *tmuxfix.Clock, ref store.BusySendPauseRef, p *plainSignal, wait time.Duration) (bool, error)
	calls := map[string]call{
		"claim": func(ctx context.Context, in *busySendInbox, clock *tmuxfix.Clock, _ store.BusySendPauseRef, _ *plainSignal, wait time.Duration) (bool, error) {
			d, err := in.s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-2", 5*time.Second), wait)
			return d.Kind == store.BusySendPauseDecisionClaimed, err
		},
		"intent": func(ctx context.Context, in *busySendInbox, clock *tmuxfix.Clock, ref store.BusySendPauseRef, _ *plainSignal, wait time.Duration) (bool, error) {
			w, err := in.s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: ref, Now: clock.Now}, wait)
			return w.Applied || w.NextRef != nil, err
		},
		"plain signal": func(ctx context.Context, in *busySendInbox, clock *tmuxfix.Clock, _ store.BusySendPauseRef, p *plainSignal, wait time.Duration) (bool, error) {
			r, err := in.s.SignalPlainBusySendPauseWithin(ctx, in.pauseRequest(clock, "", 5*time.Second), p.signal, wait)
			return r.Invoked || r.Completed, err
		},
	}
	for name, run := range calls {
		for _, cut := range []string{"the deadline passes at BEGIN", "the caller is canceled", "the write lock is held"} {
			t.Run(name+"/"+cut, func(t *testing.T) {
				t.Parallel()
				in := newBusySendInbox(t, "held")
				clock := pauseClock()
				var ref store.BusySendPauseRef
				if name == "intent" {
					ref = in.pauseClaim(t, in.pauseRequest(clock, "call-1", 5*time.Second)).Ref
				}
				before := readPauseFrame(t, in.path)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				wait := time.Minute
				var want []error
				var lock *sql.Conn
				switch cut {
				case "the deadline passes at BEGIN":
					var once sync.Once
					store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
						if q == "BEGIN IMMEDIATE" {
							once.Do(func() { clock.Advance(5 * time.Second) })
						}
						return nil
					}, nil)
					if name != "intent" { // an intent past its deadline is a no-op, not an error
						want = []error{store.ErrStoreBusy, context.DeadlineExceeded}
					}
				case "the caller is canceled":
					cancel()
					want = []error{store.ErrStoreBusy, context.Canceled}
				case "the write lock is held":
					var err error
					if lock, err = openBusySendRaw(t, in.path).Conn(context.Background()); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = lock.Close() })
					if _, err := lock.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
						t.Fatal(err)
					}
					wait, want = 100*time.Millisecond, []error{store.ErrStoreBusy}
				}
				var p plainSignal
				granted, err := run(ctx, in, clock, ref, &p, wait)
				for _, w := range want {
					if !errors.Is(err, w) {
						t.Errorf("error %v; want it to hold %v", err, w)
					}
				}
				if (len(want) == 0) != (err == nil) || granted {
					t.Fatalf("granted %t, %v; want no authority", granted, err)
				}
				if lock != nil {
					if _, err := lock.ExecContext(context.Background(), "ROLLBACK"); err != nil {
						t.Fatal(err)
					}
				}
				// The store's next operation waits for any continuation of the
				// refused one, so a delayed callback would have run by now.
				if _, err := in.s.CheckBusySendPauseWithin(context.Background(), in.pauseRequest(clock, "", time.Minute), time.Minute); err != nil {
					t.Fatal(err)
				}
				if p.calls.Load() != 0 {
					t.Fatalf("the signal was called %d times; want none", p.calls.Load())
				}
				if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
					t.Fatal("a call without authority wrote something")
				}
			})
		}
	}
	t.Run("plain signal/the caller stops waiting just before admission", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		in := newBusySendInbox(t, "held")
		clock := pauseClock()
		before := readPauseFrame(t, in.path)
		reached, release := make(chan struct{}), make(chan struct{})
		var reachedOnce, releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		req := in.pauseRequest(clock, "", time.Hour)
		// With no claim, the plain signal reads its clock once: at its last
		// deadline check, after its target and claim reads and just before
		// admission. Hold it there until the caller has stopped waiting, then
		// answer with a time well inside the deadline.
		req.Now = func() time.Time {
			reachedOnce.Do(func() { close(reached); <-release })
			return clock.Now()
		}
		type result struct {
			r   store.BusySendPlainPauseSignalResult
			err error
		}
		done := make(chan result, 1)
		var p plainSignal
		go func() {
			// Long enough to reach the clock on a loaded runner; the call
			// then returns at its end, the continuation still held.
			r, err := in.s.SignalPlainBusySendPauseWithin(ctx, req, p.signal, 5*time.Second)
			done <- result{r, err}
		}()
		awaitSignal(t, reached, "the plain signal's last deadline check")
		var got result
		select {
		case got = <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the caller did not stop waiting at the end of its budget")
		}
		releaseOnce.Do(func() { close(release) })
		if !errors.Is(got.err, store.ErrStoreBusy) || store.CommitUnconfirmed(got.err) ||
			!reflect.DeepEqual(got.r, store.BusySendPlainPauseSignalResult{}) {
			t.Fatalf("plain signal %+v, %v; want a busy refusal with no invocation facts", got.r, got.err)
		}
		// The next operation waits for the released continuation to finish.
		if _, err := in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute); err != nil {
			t.Fatal(err)
		}
		if p.calls.Load() != 0 {
			t.Fatalf("the abandoned continuation called the signal %d times; want none", p.calls.Load())
		}
		if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
			t.Fatal("the abandoned plain signal wrote something")
		}
	})
}

// TestBusySendPauseCommitOutcome (SR-6.5; M2 P-C4.5, P-C4.7): a claim, an
// abandonment release or an intent whose transaction fails before COMMIT
// writes nothing and grants nothing. One whose COMMIT lands with its
// acknowledgment lost is unconfirmed and grants nothing either (no claim,
// clear or next ref); after a reopen the landed write is answered by the
// repeat rules, never retried blindly: a landed claim is pause_in_progress,
// a landed release leaves the Launch clear, and a landed intent makes its
// owner's ref stale and the claim pause_uncertain past its deadline. A failed
// release supplies no clearance: the plain signal still asks for a Recheck.
func TestBusySendPauseCommitOutcome(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected statement failure")
	for _, step := range []string{"claim", "release", "intent"} {
		for _, lost := range []bool{false, true} {
			t.Run(step+map[bool]string{false: "/failure before COMMIT", true: "/lost COMMIT acknowledgment"}[lost], func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				in := newBusySendInbox(t, "held")
				clock := pauseClock()
				var c store.BusySendPauseClaim
				if step != "claim" {
					c = in.pauseClaim(t, in.pauseRequest(clock, "call-1", 10*time.Second))
				}
				if step == "release" {
					clock.Advance(10*time.Second + time.Millisecond)
				}
				hold := map[string]string{"claim": "INSERT INTO busy_send_pause_claims", "release": "'abandoned_before_signal'",
					"intent": "signal_intent_at_ms = ?"}[step]
				before := readPauseFrame(t, in.path)
				var fired atomic.Bool
				store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
					if !lost && strings.Contains(q, hold) && fired.CompareAndSwap(false, true) {
						return injected
					}
					return nil
				}, func(context.Context) error {
					if lost && fired.CompareAndSwap(false, true) {
						return errors.New("COMMIT acknowledgment lost")
					}
					return nil
				})
				var granted bool
				var err error
				switch step {
				case "claim":
					var d store.BusySendPauseDecision
					d, err = in.s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-1", 10*time.Second), time.Minute)
					granted = d != (store.BusySendPauseDecision{})
				case "release":
					var d store.BusySendPauseDecision
					d, err = in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute)
					granted = d != (store.BusySendPauseDecision{})
				case "intent":
					var w store.BusySendPauseWriteResult
					w, err = in.s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: c.Ref, Now: clock.Now}, time.Minute)
					granted = w != (store.BusySendPauseWriteResult{})
				}
				if granted || (lost && !store.CommitUnconfirmed(err)) || (!lost && (!errors.Is(err, injected) || store.CommitUnconfirmed(err))) {
					t.Fatalf("%s granted %t, %v; want only the %s error", step, granted, err, map[bool]string{false: "injected", true: "unconfirmed"}[lost])
				}
				in.reopen(t)
				if !lost {
					if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
						t.Fatal("a transaction that failed before COMMIT wrote part of itself")
					}
					if step == "release" {
						var p plainSignal
						if r, err := in.s.SignalPlainBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), p.signal, time.Minute); err != nil ||
							!r.Recheck || r.Invoked || p.calls.Load() != 0 {
							t.Fatalf("plain signal after the failed release %+v, %v; want a Recheck and no call", r, err)
						}
					}
					return
				}
				switch step {
				case "claim":
					wantPauseClaims(t, in, "standing r1")
					if d, err := in.s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-2", time.Minute), time.Minute); err != nil ||
						d.Cause != store.BusySendPauseInProgress || d.Claim.Ref.OwnerCallID != "call-1" {
						t.Fatalf("a later claim %+v, %v; want pause_in_progress for the landed claim", d, err)
					}
				case "release":
					wantPauseClaims(t, in, "released abandoned_before_signal r2")
					if d, err := in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute); err != nil ||
						!reflect.DeepEqual(d, store.BusySendPauseDecision{Kind: store.BusySendPauseDecisionClear}) {
						t.Fatalf("Check %+v, %v; want clear, nothing left to release", d, err)
					}
				case "intent":
					wantPauseClaims(t, in, "standing r2 intent")
					if w, err := in.s.PersistBusySendPauseSignalWithin(ctx, store.BusySendPauseWriteRequest{Ref: c.Ref, Now: clock.Now}, time.Minute); err != nil || w.Applied {
						t.Fatalf("the owner's intent again %+v, %v; want nothing applied", w, err)
					}
					clock.Advance(10*time.Second + time.Millisecond)
					if d, err := in.s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-2", time.Minute), time.Minute); err != nil ||
						d.Cause != store.BusySendPauseUncertain {
						t.Fatalf("a later claim %+v, %v; want pause_uncertain", d, err)
					}
				}
			})
		}
	}
}

// TestBusySendPausePlainSignalLockOrder (E6 Main Agent decision 4; M2
// PauseSignalBy(FALSE), P-C4.5, P-C4.6): the plain signal and a conditional
// claim on two stores serialize on the write lock in both orders. A claim
// committed first gives the plain signal its answer, and the signal is not
// called. A plain signal admitted first holds the lock through its callback,
// as a nonwaiting write-lock attempt from another connection observes: the
// claim, already at BEGIN IMMEDIATE, commits only after the callback returned
// and the plain transaction rolled back, the plain signal having written
// nothing and left no claim.
func TestBusySendPausePlainSignalLockOrder(t *testing.T) {
	t.Parallel()
	t.Run("claim first", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		in := newBusySendInbox(t, "held")
		clock := pauseClock()
		var claim store.BusySendPauseDecision
		var plain store.BusySendPlainPauseSignalResult
		var claimErr, plainErr error
		var p plainSignal
		raceBusySendSteps(t, in, "INSERT INTO busy_send_pause_claims", func(s *store.Store) {
			claim, claimErr = s.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-1", time.Minute), time.Minute)
		}, func(s *store.Store) {
			plain, plainErr = s.SignalPlainBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), p.signal, time.Minute)
		})
		if claimErr != nil || claim.Kind != store.BusySendPauseDecisionClaimed {
			t.Fatalf("claim %+v, %v; want claimed", claim, claimErr)
		}
		if plainErr != nil || plain.Invoked || plain.Decision == nil || plain.Decision.Cause != store.BusySendPauseInProgress ||
			plain.Decision.Claim.Ref != claim.Claim.Ref || p.calls.Load() != 0 {
			t.Fatalf("plain signal %+v, %v, %d calls; want the claim's pause_in_progress and no call", plain, plainErr, p.calls.Load())
		}
	})
	t.Run("plain signal first", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		in := newBusySendInbox(t, "held")
		clock := pauseClock()
		rival, err := store.Open(in.path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rival.Close() })
		begun, admitted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var begunOnce, releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		store.ObserveDecisionBoundaryForTest(t, rival, in.path, func(_ context.Context, q string) error {
			if q == "BEGIN IMMEDIATE" {
				begunOnce.Do(func() { close(begun) })
			}
			return nil
		}, nil)
		var calls atomic.Int32
		var returned atomic.Bool
		signal := func() error {
			if calls.Add(1) == 1 {
				close(admitted)
			}
			<-release
			returned.Store(true)
			return nil
		}
		before := butPauseClaims(t, in.path)
		type plainOut struct {
			r   store.BusySendPlainPauseSignalResult
			err error
		}
		type claimOut struct {
			d             store.BusySendPauseDecision
			err           error
			afterCallback bool
		}
		plainDone, claimDone := make(chan plainOut, 1), make(chan claimOut, 1)
		go func() {
			r, err := in.s.SignalPlainBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), signal, time.Minute)
			plainDone <- plainOut{r, err}
		}()
		awaitSignal(t, admitted, "the plain signal's admitted callback")
		// While the callback runs, the write lock is held: another
		// connection's nonwaiting attempt finds it taken.
		probe, err := openBusySendRaw(t, in.path).Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = probe.Close() })
		if _, err := probe.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
			t.Fatal(err)
		}
		if _, err := probe.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil || !strings.Contains(err.Error(), "database is locked") {
			if err == nil {
				_, _ = probe.ExecContext(ctx, "ROLLBACK")
			}
			t.Fatalf("a write-lock attempt during the callback: %v; want the lock held", err)
		}
		go func() {
			d, err := rival.ClaimBusySendPauseWithin(ctx, in.pauseRequest(clock, "call-1", time.Minute), time.Minute)
			claimDone <- claimOut{d, err, returned.Load()}
		}()
		awaitSignal(t, begun, "the claim's BEGIN IMMEDIATE")
		releaseOnce.Do(func() { close(release) })
		var plain plainOut
		var claim claimOut
		select {
		case plain = <-plainDone:
		case <-time.After(10 * time.Second):
			t.Fatal("the plain signal did not return within 10 s of its callback's release")
		}
		select {
		case claim = <-claimDone:
		case <-time.After(10 * time.Second):
			t.Fatal("the claim did not return within 10 s of the plain signal")
		}
		if plain.err != nil || !plain.r.Invoked || !plain.r.Completed || plain.r.SignalError != nil || plain.r.Decision != nil ||
			plain.r.Recheck || calls.Load() != 1 {
			t.Fatalf("plain signal %+v, %v, %d calls; want one completed call", plain.r, plain.err, calls.Load())
		}
		if claim.err != nil || claim.d.Kind != store.BusySendPauseDecisionClaimed || !claim.afterCallback {
			t.Fatalf("claim %+v, %v, after the callback %t; want claimed only after it", claim.d, claim.err, claim.afterCallback)
		}
		wantPauseClaims(t, in, "standing r1")
		if !reflect.DeepEqual(before, butPauseClaims(t, in.path)) {
			t.Fatal("the plain signal or the claim changed more than the claim")
		}
	})
}

// TestBusySendPausePlainSignalFacts (E6 Main Agent decision 4; M2 P-C4.5 to
// P-C4.7): the plain signal writes nothing, whether its callback returns
// nothing or its own error, and its facts survive whatever the call itself
// returns: the callback's error stays apart from the outer one; a ROLLBACK
// failing after the callback keeps Invoked and Completed; a caller that stops
// waiting after admission gets an unconfirmed error with Invoked true and
// Completed false, and the callback, once it returns, is not called again.
func TestBusySendPausePlainSignalFacts(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("signal refused by the process check")
	for _, tc := range []string{"the callback succeeds", "the callback returns its error", "ROLLBACK fails", "the caller stops waiting"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, "held")
			clock := pauseClock()
			before := readPauseFrame(t, in.path)
			p := plainSignal{}
			signal, wait := p.signal, time.Minute
			want := store.BusySendPlainPauseSignalResult{Invoked: true, Completed: true}
			admitted, release := make(chan struct{}), make(chan struct{})
			switch tc {
			case "the callback returns its error":
				p.ret, want.SignalError = sentinel, sentinel
			case "ROLLBACK fails":
				store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
					if q == "ROLLBACK" {
						return errors.New("injected ROLLBACK failure")
					}
					return nil
				}, nil)
			case "the caller stops waiting":
				// Long enough to be admitted on a loaded runner; the call
				// then returns at its end, the callback still held.
				wait, want.Completed = 5*time.Second, false
				signal = func() error { close(admitted); <-release; return p.signal() }
			}
			r, err := in.s.SignalPlainBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), signal, wait)
			if tc == "the caller stops waiting" {
				if !store.CommitUnconfirmed(err) || errors.Is(err, store.ErrStoreBusy) {
					t.Errorf("error %v; want only an unconfirmed outcome", err)
				}
				awaitSignal(t, admitted, "the callback")
				close(release)
				// The store's next operation waits for the abandoned one.
				if _, err := in.s.CheckBusySendPauseWithin(ctx, in.pauseRequest(clock, "", time.Minute), time.Minute); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Errorf("error %v; want none", err)
			}
			if !reflect.DeepEqual(r, want) || !errors.Is(r.SignalError, want.SignalError) {
				t.Errorf("result %+v; want %+v", r, want)
			}
			if n := p.calls.Load(); n != 1 {
				t.Errorf("the signal was called %d times; want once", n)
			}
			if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
				t.Error("the plain signal wrote something")
			}
		})
	}
}

// TestBusySendPausePlainSignalRecheck (E6 Main Agent decision 4; M2 P-C4.5,
// P-C4.7): a prior clear Check is no signal authority. A claim made after it
// and abandoned before its intent makes the plain signal answer Recheck, with
// that claim's snapshot and no answer, calling nothing and releasing nothing;
// only a separately committed Check releases it, and a claim made in the gap
// before the next signal is answered then.
func TestBusySendPausePlainSignalRecheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := newBusySendInbox(t, "held")
	clock := pauseClock()
	plainReq := func() store.BusySendPauseRequest { return in.pauseRequest(clock, "", time.Minute) }
	if d, err := in.s.CheckBusySendPauseWithin(ctx, plainReq(), time.Minute); err != nil || d.Kind != store.BusySendPauseDecisionClear {
		t.Fatalf("Check %+v, %v; want clear", d, err)
	}
	c := in.pauseClaim(t, in.pauseRequest(clock, "call-1", time.Second))
	clock.Advance(time.Second + time.Millisecond)
	before := readPauseFrame(t, in.path)
	var p plainSignal
	r, err := in.s.SignalPlainBusySendPauseWithin(ctx, plainReq(), p.signal, time.Minute)
	if err != nil || !r.Recheck || r.Invoked || r.Completed || r.Decision == nil || r.Decision.Kind != "" ||
		r.Decision.Claim == nil || !reflect.DeepEqual(*r.Decision.Claim, c) || p.calls.Load() != 0 {
		t.Fatalf("plain signal %+v, %v; want Recheck with the abandoned claim, no call", r, err)
	}
	if !reflect.DeepEqual(before, readPauseFrame(t, in.path)) {
		t.Fatal("the plain signal released or wrote something")
	}
	if d, err := in.s.CheckBusySendPauseWithin(ctx, plainReq(), time.Minute); err != nil || d.Kind != store.BusySendPauseDecisionClear ||
		d.Claim == nil || d.Claim.ClosedBy != store.BusySendPauseAbandonedBeforeSignal {
		t.Fatalf("Check %+v, %v; want clear with the release", d, err)
	}
	gap := in.pauseClaim(t, in.pauseRequest(clock, "call-2", time.Minute))
	if r, err := in.s.SignalPlainBusySendPauseWithin(ctx, plainReq(), p.signal, time.Minute); err != nil || r.Invoked ||
		r.Decision == nil || r.Decision.Cause != store.BusySendPauseInProgress || r.Decision.Claim.Ref != gap.Ref || p.calls.Load() != 0 {
		t.Fatalf("plain signal %+v, %v; want the gap claim's pause_in_progress, no call", r, err)
	}
	wantPauseClaims(t, in, "released abandoned_before_signal r2", "standing r1")
}
