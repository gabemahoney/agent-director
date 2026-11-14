package store_test

// Durable emission's store transactions (Task t2.xis.oo.xe; SR-2.4 to SR-2.6,
// SR-3.2, SR-5.1, SR-5.2) on real SQLite, through the exported job steps and
// the Launch End seam (EndBusySendLaunchForTest). Each obligation is named
// where it is asserted: M2 P-C4.3 at the issue commit, M1 C01 for a stale
// owner, M1's repeated acc, rc and ad, barrier release and Held-to-Final
// progress as bounded event traces, and M1 G4 in the custody matrix, which
// later Tasks extend with rows rather than new scenarios. The coordinator's
// call order is internal/busysend's; Claude's hook output and lifecycle
// commands are Tasks 3's and 4's. Times are injected where the store takes
// them; an issue commit reads its Now inside its transaction (xe-repair-2 F2).

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// busySendTwoPrompts are two Messages that never share a Claude prompt: each
// is one byte over half of what the table's prompt leaves two Messages.
var busySendTwoPrompts = func() []string {
	l, _ := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendWaitTurn)
	n := int((l.PromptBytes-l.WrapperBytes-2*int64(len("[agent-director: message 1 of 2]\n"))-2)/2) + 1
	return []string{strings.Repeat("a", n), strings.Repeat("b", n)}
}()

// TestBusySendIssueCommitsIntentFirst (M2 P-C4.3, SR-2.4): the issue commit
// makes its connection WAL synchronous=FULL before BEGIN IMMEDIATE and
// commits everything before it returns authority: the prompt's emission
// intent and issue time, the hand-over's Turn Start counter and running-turn
// flag, the issued Messages bound to the captured Launch and session as
// non-Final sent_unconfirmed with their reservations, the issuer as the
// Launch's process and the pending owned-input obligation (issued, no
// admission yet). A reopened store reads all of it.
func TestBusySendIssueCommitsIntentFirst(t *testing.T) {
	t.Parallel()
	in := newBusySendInbox(t, "first", "second")
	setBusySendFact(t, in.path, `UPDATE spawns SET turn_starts = 3 WHERE instance_id = ?`, in.id)
	h := in.claim(t, time.Time{}, false)
	var mu sync.Mutex
	var statements []string
	store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
		mu.Lock()
		defer mu.Unlock()
		statements = append(statements, q)
		return nil
	}, nil)
	start := time.Now().UnixMilli()
	got := in.issue(t, h, 0, h.Revision)
	end := time.Now().UnixMilli()
	mu.Lock()
	full, begin, commit := slices.Index(statements, "PRAGMA synchronous = FULL"), slices.Index(statements, "BEGIN IMMEDIATE"), slices.Index(statements, "COMMIT")
	issued := slices.IndexFunc(statements, func(q string) bool { return strings.Contains(q, "SET place = 'issued'") })
	if full < 0 || begin <= full || issued <= begin || commit <= issued {
		t.Errorf("the issue ran %q; want PRAGMA synchronous = FULL, BEGIN IMMEDIATE, its Message update, then COMMIT", statements)
	}
	mu.Unlock()
	at := got.IssuedAt.UnixMilli()
	if got.Revision != h.Revision+1 || got.TurnStarts != 3 || got.RunningTurn || at < start || at > end {
		t.Errorf("issue %+v; want revision %d, Turn Starts 3, no running turn, issued in [%d, %d]", got, h.Revision+1, start, end)
	}

	in.reopen(t)
	var emission string
	var intentAt, issuedAt, handoverIntentAt, turnStarts int64
	var running, awaiting bool
	if err := openBusySendRaw(t, in.path).QueryRow(`SELECT p.emission, p.intent_at_ms, p.issued_at_ms, h.intent_at_ms,
	        h.intent_turn_starts, h.intent_running_turn, p.admission_kind IS NULL AND p.transport IS NULL
	    FROM busy_send_prompts p JOIN busy_send_handovers h ON h.handover_id = p.handover_id
	    WHERE p.handover_id = ? AND p.prompt_index = 0`, h.HandoverID).Scan(&emission, &intentAt, &issuedAt, &handoverIntentAt,
		&turnStarts, &running, &awaiting); err != nil {
		t.Fatal(err)
	}
	if emission != "issued" || intentAt != at || issuedAt != at || handoverIntentAt != at || turnStarts != 3 || running || !awaiting {
		t.Errorf("reopened intent %s at %d/%d, hand-over intent at %d with Turn Starts %d running %t, awaiting %t; want issued at %d, Turn Starts 3, not running, awaiting",
			emission, intentAt, issuedAt, handoverIntentAt, turnStarts, running, awaiting, at)
	}
	wantBusySendCustody(t, in.path, in.id,
		"issued sent_unconfirmed handover_in_progress reserved member", "issued sent_unconfirmed handover_in_progress reserved member")
	var bound int
	if err := openBusySendRaw(t, in.path).QueryRow(`SELECT COUNT(*) FROM busy_send_messages WHERE handover_id = ?
	    AND bound_launch_id = ? AND COALESCE(bound_launch_token, '') = ? AND bound_session_id = ?`,
		h.HandoverID, in.dest.LaunchID, in.dest.LaunchToken, in.dest.SessionID).Scan(&bound); err != nil || bound != 2 {
		t.Errorf("%d Messages bound to the captured Launch and session, %v; want 2", bound, err)
	}
	fence, err := in.s.ReadBusySendLaunchFence(context.Background(), in.dest.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := []store.BusySendFencedPrompt{{HandoverID: h.HandoverID, Destination: in.dest, Emission: store.BusySendEmissionIssued}}
	helper := slices.ContainsFunc(fence.Processes, func(p store.BusySendLaunchProcess) bool {
		return p.Role == busySendHelper.Role && p.PID == busySendHelper.PID && p.StartTime == busySendHelper.StartTime
	})
	if fence.Ended || !helper || !reflect.DeepEqual(fence.Prompts, wantPrompt) {
		t.Errorf("fence %+v; want the helper issuer and %+v", fence, wantPrompt)
	}
}

// TestBusySendIssueRevalidation (SR-2.4; Main Agent decision 8, xe-answer-1):
// the issue commit runs its guard once against every captured authority. A
// changed owner revision, prompt order, Launch, coverage, session, connection
// generation, Turn Start counter, permission epoch or permission hold, or an
// issue deadline that passed after the commit began (its clock, the job's, is
// read inside the transaction), refuses with its failure
// and wait and writes nothing, so the prompt stays only claimed and the job's
// release returns it to Held waiting that cause: no guard strands an intent.
func TestBusySendIssueRevalidation(t *testing.T) {
	t.Parallel()
	set := func(q string) func(*testing.T, *busySendInbox) {
		return func(t *testing.T, in *busySendInbox) { setBusySendFact(t, in.path, q, in.id) }
	}
	for _, tc := range []struct {
		name    string
		change  func(*testing.T, *busySendInbox)
		prompt  int
		stale   bool // the job holds a revision the hand-over does not have
		cross   bool // the job's clock passes the issue deadline at the issue's BEGIN IMMEDIATE
		failure store.BusySendGuardFailure
		wait    store.BusySendWaitReason
	}{
		{name: "a revision the hand-over does not have", stale: true, failure: store.BusySendGuardOwner},
		{name: "not the next prompt", prompt: 1, failure: store.BusySendGuardPrompt},
		// xe-repair-2 F2: the clock is read inside the issue transaction, so a
		// commit entered before the deadline and crossing it while it waits for
		// the write lock is refused.
		{name: "the issue deadline passed while the commit waited", cross: true,
			failure: store.BusySendGuardDeadline, wait: store.BusySendWaitReadinessUnknown},
		{name: "the Launch ended", change: set(`UPDATE busy_send_launches SET ended_at_ms = 1, end_cause = 'process_exited'
		    WHERE instance_id = ? AND phase = 'current'`), failure: store.BusySendGuardLaunch, wait: store.BusySendWaitLaunchEnded},
		{name: "the Launch is no longer covered", change: func(t *testing.T, in *busySendInbox) {
			if err := apitest.SeedBusySendCoverage(in.path, in.id, store.BusySendNotCovered, store.BusySendCovered); err != nil {
				t.Fatal(err)
			}
		}, failure: store.BusySendGuardLaunch, wait: store.BusySendWaitLaunchEnded},
		{name: "the provider session changed", change: set(`UPDATE spawns SET session_id = 'rotated' WHERE instance_id = ?`),
			failure: store.BusySendGuardDestination, wait: store.BusySendWaitReadinessUnknown},
		{name: "the connection generation advanced", change: set(`UPDATE busy_send_launches SET connection_generation = 1
		    WHERE instance_id = ? AND phase = 'current'`), failure: store.BusySendGuardDestination, wait: store.BusySendWaitReadinessUnknown},
		{name: "a turn started", change: set(`UPDATE spawns SET turn_starts = turn_starts + 1 WHERE instance_id = ?`),
			failure: store.BusySendGuardTurn, wait: store.BusySendWaitTurnRunning},
		{name: "the permission epoch moved", change: set(`UPDATE spawns SET permission_epoch = permission_epoch + 1 WHERE instance_id = ?`),
			failure: store.BusySendGuardPermission, wait: store.BusySendWaitReadinessUnknown},
		{name: "a permission request is open", change: func(t *testing.T, in *busySendInbox) {
			storefix.SeedOpenPermissionRequests(t, in.s, in.id, []string{"tok-1"})
		}, failure: store.BusySendGuardPermission, wait: store.BusySendWaitDialog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, "only")
			h := in.claim(t, time.Time{}, false)
			if tc.change != nil {
				tc.change(t, in)
			}
			req := store.BusySendIssueRequest{Ref: store.BusySendPromptRef{HandoverID: h.HandoverID, PromptIndex: tc.prompt, Revision: h.Revision}}
			if tc.stale {
				req.Ref.Revision++
			}
			if tc.cross {
				var now atomic.Int64
				now.Store(h.ClaimedAt.Add(time.Second).UnixMilli())
				store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
					if q == "BEGIN IMMEDIATE" {
						now.Store(h.IssueDeadline.Add(time.Millisecond).UnixMilli())
					}
					return nil
				}, nil)
				req.Now = func() time.Time { return time.UnixMilli(now.Load()) }
			}
			before := readBusySendTables(t, in.path)
			got, err := in.s.CommitBusySendIssue(context.Background(), req)
			var guard *store.BusySendGuardError
			if !errors.As(err, &guard) || guard.Failure != tc.failure || guard.Wait != tc.wait || got != (store.BusySendIssue{}) {
				t.Fatalf("issue %+v, %v; want %s refused waiting %q", got, err, tc.failure, tc.wait)
			}
			if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Fatal("a refused issue wrote part of its intent")
			}
			if tc.wait == "" {
				return
			}
			r, err := in.s.ReleaseBusySendClaims(context.Background(), store.BusySendReleaseRequest{HandoverID: h.HandoverID,
				Revision: h.Revision, Wait: guard.Wait})
			if err != nil || r.Released != 1 || !r.Closed {
				t.Fatalf("the job's release %+v, %v; want the Message released and the hand-over closed", r, err)
			}
			wantBusySendCustody(t, in.path, in.id, "held held "+string(tc.wait)+" reserved")
		})
	}
}

// TestBusySendStaleOwnerCannotIssue (M1 C01, Plan b.xis ## Decisions): once a
// job's never-issued claim is released, by the job itself or by expiry, and
// another job claims the Message again in the same Launch, the old job can
// neither commit intent (under its claim's revision or the one its release
// left), release, complete nor record evidence; nothing changes, and the new
// owner issues.
func TestBusySendStaleOwnerCannotIssue(t *testing.T) {
	t.Parallel()
	for _, expiry := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner release", true: "expiry release"}[expiry], func(t *testing.T) {
			t.Parallel()
			in := newBusySendInbox(t, "only")
			old := in.claim(t, time.Time{}, false)
			var next *store.BusySendHandover
			if expiry {
				c, err := in.s.ClaimBusySendHandover(context.Background(), in.claimRequest(old.IssueDeadline.Add(time.Millisecond), false))
				if err != nil || c.Outcome != store.BusySendClaimMade || c.ReleasedExpired != 1 {
					t.Fatalf("claim after the deadline %+v, %v; want the expired claim released and claimed again", c, err)
				}
				next = c.Handover
			} else {
				r, err := in.s.ReleaseBusySendClaims(context.Background(), store.BusySendReleaseRequest{HandoverID: old.HandoverID, Revision: old.Revision})
				if err != nil || r.Released != 1 || !r.Closed {
					t.Fatalf("owner release %+v, %v", r, err)
				}
				next = in.claim(t, time.Time{}, false)
			}
			if next.HandoverSeq != 2 || !reflect.DeepEqual(next.Prompts[0].MessageIDs, old.Prompts[0].MessageIDs) {
				t.Fatalf("re-claim %+v; want hand-over 2 of the same Message", next)
			}
			before := readBusySendTables(t, in.path)
			for _, revision := range []int64{old.Revision, old.Revision + 1} {
				var guard *store.BusySendGuardError
				if _, err := in.commit(context.Background(), old, 0, revision); !errors.As(err, &guard) || guard.Failure != store.BusySendGuardOwner {
					t.Errorf("old job's issue at revision %d: %v; want owner_changed", revision, err)
				}
			}
			var guard *store.BusySendGuardError
			if _, err := in.s.ReleaseBusySendClaims(context.Background(), store.BusySendReleaseRequest{HandoverID: old.HandoverID,
				Revision: old.Revision}); !errors.As(err, &guard) || guard.Failure != store.BusySendGuardOwner {
				t.Errorf("old job's release: %v; want owner_changed", err)
			}
			if _, err := in.s.RecordBusySendCompletion(context.Background(), store.BusySendCompletion{
				Ref:    store.BusySendPromptRef{HandoverID: old.HandoverID, Revision: old.Revision},
				Output: store.BusySendOutputComplete, Order: store.BusySendOrderEmissionComplete}); err == nil {
				t.Error("old job's completion of a prompt it never issued was accepted")
			}
			if res, err := in.s.RecordBusySendEvidence(context.Background(), store.BusySendPromptEvidence{HandoverID: old.HandoverID,
				LaunchID: in.dest.LaunchID, Order: store.BusySendOrderOrderedAcceptance}); err != nil || res.Recorded {
				t.Errorf("evidence for the old hand-over %+v, %v; want nothing recorded", res, err)
			}
			if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Fatal("the stale owner changed custody")
			}
			in.issue(t, next, 0, next.Revision)
		})
	}
}

// TestBusySendCustodyMatrix is E2's one store-level custody and crash matrix
// (M1 G4 and ReleaseExpired, M2 P-C4.3; Epic review note): a two-prompt
// hand-over whose first prompt's job got as far as the row says before the
// worker stopped, a reopened store, then the original issue deadline's expiry
// or Launch End. Disposition is per prompt. A prompt with no persisted intent
// (claimed only, a commit rolled back before COMMIT or cancelled before BEGIN,
// neither writing anything) returns to Held. A prompt with persisted intent
// (an unacknowledged commit, a lost worker, partial, uncertain or unqualified
// complete output) is never replayed, released, carried or not_started, by
// elapsed time, a later callback or a missing row: it keeps the order barrier,
// and Launch End makes it Final sent_unconfirmed (launch_ended), fenced, with
// its reservation freed, until exit evidence lifts the fence. Exit before
// Launch End is refused. Later Tasks add rows here.
func TestBusySendCustodyMatrix(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected issue failure")
	type drive func(t *testing.T, in *busySendInbox, h *store.BusySendHandover)
	unchanged := func(t *testing.T, in *busySendInbox, call func() error, want func(error) bool) {
		t.Helper()
		before := readBusySendTables(t, in.path)
		if err := call(); !want(err) {
			t.Fatalf("issue: unexpected error %v", err)
		}
		if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
			t.Fatal("an issue that did not commit wrote part of its intent")
		}
	}
	claimOnly := func(*testing.T, *busySendInbox, *store.BusySendHandover) {}
	rolledBack := func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
		store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(_ context.Context, q string) error {
			if strings.Contains(q, "SET place = 'issued'") {
				return injected
			}
			return nil
		}, nil)
		unchanged(t, in, func() error { _, err := in.commit(context.Background(), h, 0, h.Revision); return err }, func(err error) bool {
			var guard *store.BusySendGuardError
			return errors.Is(err, injected) && !store.CommitUnconfirmed(err) && !errors.As(err, &guard)
		})
	}
	cancelled := func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store.ObserveDecisionBoundaryForTest(t, in.s, in.path, func(sqlCtx context.Context, q string) error {
			if q != "PRAGMA synchronous = FULL" {
				return nil
			}
			cancel()
			select {
			case <-sqlCtx.Done():
				return sqlCtx.Err()
			case <-time.After(10 * time.Second):
				return errors.New("the caller's cancellation never reached the statement")
			}
		}, nil)
		unchanged(t, in, func() error { _, err := in.commit(ctx, h, 0, h.Revision); return err },
			func(err error) bool { return errors.Is(err, store.ErrStoreBusy) })
	}
	lostAck := func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
		store.ObserveDecisionBoundaryForTest(t, in.s, in.path, nil, func(context.Context) error {
			return errors.New("COMMIT acknowledgment lost")
		})
		if got, err := in.commit(context.Background(), h, 0, h.Revision); !store.CommitUnconfirmed(err) || got != (store.BusySendIssue{}) {
			t.Fatalf("issue %+v, %v; want only an unconfirmed commit, granting nothing", got, err)
		}
	}
	issued := func(out store.BusySendOutput) drive {
		return func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
			is := in.issue(t, h, 0, h.Revision)
			if out == "" {
				return // the worker is lost before its completion
			}
			if res := in.complete(t, h, 0, is.Revision, out, "", false); res.OrderSecured || res.Closed {
				t.Fatalf("%s output %+v; want the barrier kept", out, res)
			}
		}
	}
	missing := func(t *testing.T, in *busySendInbox, h *store.BusySendHandover) {
		issued("")(t, in, h)
		setBusySendFact(t, in.path, `UPDATE spawns SET state = 'missing' WHERE instance_id = ?`, in.id)
	}
	for _, tc := range []struct {
		name   string
		drive  drive
		intent bool
		end    string                     // "expiry" or "launch end"
		claim  store.BusySendClaimOutcome // an event's claim after expiry
	}{
		{"claimed only, worker lost", claimOnly, false, "expiry", store.BusySendClaimMade},
		{"issue rolled back before COMMIT", rolledBack, false, "expiry", store.BusySendClaimMade},
		{"issue cancelled before BEGIN", cancelled, false, "launch end", ""},
		{"issue COMMIT unacknowledged", lostAck, true, "expiry", store.BusySendClaimOwned},
		{"issued, worker lost before completion", issued(""), true, "launch end", ""},
		{"partial output", issued(store.BusySendOutputPartial), true, "expiry", store.BusySendClaimOwned},
		{"uncertain output", issued(store.BusySendOutputUncertain), true, "launch end", ""},
		{"complete output with no order evidence", issued(store.BusySendOutputComplete), true, "expiry", store.BusySendClaimOwned},
		{"issued, agent row missing", missing, true, "expiry", store.BusySendClaimIneligible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, busySendTwoPrompts...)
			h := in.claim(t, time.Time{}, false)
			tc.drive(t, in, h)
			in.reopen(t)
			if tc.intent {
				// No replay of the intent and no later prompt past its barrier,
				// whatever revision a job holds; a later callback clears nothing.
				now, err := in.s.ReadBusySendHandover(ctx, h.HandoverID)
				if err != nil {
					t.Fatal(err)
				}
				for k := range 2 {
					unchanged(t, in, func() error { _, err := in.commit(ctx, h, k, now.Revision); return err }, func(err error) bool {
						var guard *store.BusySendGuardError
						return errors.As(err, &guard)
					})
				}
				res, err := in.s.RecordBusySendEvidence(ctx, store.BusySendPromptEvidence{HandoverID: h.HandoverID, LaunchID: in.dest.LaunchID,
					Admission: store.BusySendAdmissionPromptSighting})
				if err != nil || res.OrderSecured || res.Closed {
					t.Fatalf("a later admission callback %+v, %v; want the barrier kept", res, err)
				}
			}
			a, b := "held held readiness_unknown reserved", "held held earlier_message reserved"
			if tc.end == "expiry" {
				r, err := in.s.ReleaseBusySendClaims(ctx, store.BusySendReleaseRequest{HandoverID: h.HandoverID, At: h.IssueDeadline.Add(time.Millisecond)})
				if want := 2 - boolCount(tc.intent); err != nil || r.Released != want || r.Closed == tc.intent {
					t.Fatalf("expiry %+v, %v; want %d released and closed %t", r, err, want, !tc.intent)
				}
				if tc.intent {
					a, b = "issued sent_unconfirmed handover_in_progress reserved member", "held held handover_in_progress reserved"
				}
				wantBusySendCustody(t, in.path, in.id, a, b)
				c, err := in.s.ClaimBusySendHandover(ctx, in.claimRequest(h.IssueDeadline.Add(time.Millisecond), false))
				if err != nil || c.Outcome != tc.claim || (c.Outcome == store.BusySendClaimMade && c.Handover.HandoverSeq != 2) {
					t.Fatalf("an event's claim after expiry %+v, %v; want %s", c, err, tc.claim)
				}
				return
			}
			before := readBusySendTables(t, in.path)
			if err := store.ExitBusySendLaunchForTest(in.s, in.dest.LaunchID, time.Now()); err == nil ||
				!reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Fatalf("exit before Launch End: %v; want it refused, changing nothing", err)
			}
			if err := store.EndBusySendLaunchForTest(in.s, in.dest.LaunchID, time.Now()); err != nil {
				t.Fatal(err)
			}
			a, b = "held held launch_ended reserved", "held held launch_ended reserved"
			if tc.intent {
				a = "issued sent_unconfirmed final launch_ended member"
			}
			ended := wantBusySendCustody(t, in.path, in.id, a, b)
			fence, err := in.s.ReadBusySendLaunchFence(ctx, in.dest.LaunchID)
			if err != nil {
				t.Fatal(err)
			}
			if !fence.Ended || len(fence.Prompts) != boolCount(tc.intent) {
				t.Fatalf("fence %+v; want the Launch ended and %d fenced prompt", fence, boolCount(tc.intent))
			}
			if !tc.intent {
				return
			}
			if p := fence.Prompts[0]; p.HandoverID != h.HandoverID || p.PromptIndex != 0 || p.Destination != in.dest || !p.Fenced ||
				!slices.ContainsFunc(fence.Processes, func(p store.BusySendLaunchProcess) bool { return p.PID == busySendHelper.PID }) {
				t.Fatalf("fence %+v; want prompt 0 fenced at its captured destination and its issuer listed", fence)
			}
			if err := store.ExitBusySendLaunchForTest(in.s, in.dest.LaunchID, time.Now()); err != nil {
				t.Fatal(err)
			}
			exited := wantBusySendCustody(t, in.path, in.id, "closed sent_unconfirmed final launch_ended", b)
			if exited[0].result() != ended[0].result() {
				t.Errorf("exit changed the Final Result %s to %s", ended[0].result(), exited[0].result())
			}
			if read, err := in.s.ReadBusySendHandover(ctx, h.HandoverID); err != nil || read.Open {
				t.Errorf("hand-over after exit %+v, %v; want it closed", read, err)
			}
		})
	}
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestBusySendEvidenceRepeatsAreInert (M1's repeated acc, rc and ad; SR-2.5,
// SR-3.3a, SR-5.1, SR-5.2): one issued prompt carrying a confirmed and an
// unconfirmed Message takes each row's evidence in order. Written, order,
// admission and receipt are separate facts: an admission releases nothing; an
// ordered acceptance frees custody and reservations, leaving a confirmed
// Message awaiting proof and making an unconfirmed one Final
// confirmation_off; Claude's complete emission at its unproven boundary makes
// both Final; the sticky race exposure outranks both reasons; a receipt alone
// makes the confirmed one delivered_confirmed. Evidence for another
// connection is inert. No step changes any public Result field, Lookup's
// compatibility outcome included, of a Message already Final (xe-repair-2 F4),
// and repeating the last step changes nothing at all.
func TestBusySendEvidenceRepeatsAreInert(t *testing.T) {
	t.Parallel()
	type step struct {
		name string
		run  func(t *testing.T, in *busySendInbox, h *store.BusySendHandover, revision int64) bool
	}
	complete := func(out store.BusySendOutput, order store.BusySendOrderKind, unproven bool) step {
		return step{"completion " + string(out), func(t *testing.T, in *busySendInbox, h *store.BusySendHandover, revision int64) bool {
			return in.complete(t, h, 0, revision, out, order, unproven).Recorded
		}}
	}
	evidence := func(name string, ev store.BusySendPromptEvidence) step {
		return step{name, func(t *testing.T, in *busySendInbox, h *store.BusySendHandover, _ int64) bool {
			e := ev
			e.HandoverID, e.LaunchID = h.HandoverID, in.dest.LaunchID
			res, err := in.s.RecordBusySendEvidence(context.Background(), e)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return res.Recorded
		}}
	}
	launchEnd := step{"Launch End", func(t *testing.T, in *busySendInbox, _ *store.BusySendHandover, _ int64) bool {
		if err := store.EndBusySendLaunchForTest(in.s, in.dest.LaunchID, time.Now()); err != nil {
			t.Fatal(err)
		}
		return true
	}}
	written := complete(store.BusySendOutputComplete, "", false)
	ad := evidence("admission", store.BusySendPromptEvidence{Admission: store.BusySendAdmissionPromptSighting})
	acc := evidence("acceptance", store.BusySendPromptEvidence{Order: store.BusySendOrderOrderedAcceptance})
	rc := evidence("receipt", store.BusySendPromptEvidence{Receipt: &store.BusySendReceipt{Kind: store.BusySendEvidenceCodexItemStarted,
		RecordID: "item-1", RecordSHA256: strings.Repeat("ab", 32)}})
	emitted := complete(store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true)
	const (
		inFlight = "issued sent_unconfirmed handover_in_progress reserved member"
		endedA   = "issued sent_unconfirmed final launch_ended member"
		endedB   = "issued sent_unconfirmed final confirmation_off member"
		offB     = "closed sent_unconfirmed final confirmation_off"
		receiptA = "closed delivered_confirmed final exact_receipt"
	)
	for _, tc := range []struct {
		name             string
		race             bool
		steps            []step
		a, b             string
		order, admission string
		open, partial    bool
	}{
		{name: "an admission keeps the barrier", steps: []step{written, ad}, a: inFlight, b: inFlight,
			admission: "prompt_sighting", open: true},
		{name: "an ordered acceptance", steps: []step{written, acc}, a: "closed sent_unconfirmed awaiting_proof", b: offB,
			order: "ordered_acceptance"},
		{name: "complete emission at the unproven boundary", steps: []step{emitted},
			a: "closed sent_unconfirmed final handed_over_not_proven", b: offB, order: "emission_complete"},
		{name: "a race-exposed hand-over", race: true, steps: []step{emitted}, a: "closed sent_unconfirmed final wait_turn_raced",
			b: "closed sent_unconfirmed final wait_turn_raced", order: "emission_complete"},
		{name: "an exact receipt", steps: []step{written, rc}, a: receiptA, b: offB, order: "exact_receipt", admission: "exact_receipt"},
		{name: "a receipt after an acceptance", steps: []step{written, acc, rc}, a: receiptA, b: offB,
			order: "ordered_acceptance", admission: "exact_receipt"},
		{name: "a receipt after Launch End", steps: []step{written, launchEnd, rc}, a: endedA, b: endedB,
			admission: "exact_receipt", open: true},
		{name: "partial output after Launch End", steps: []step{launchEnd, complete(store.BusySendOutputPartial, "", false)},
			a: endedA, b: endedB, open: true},
		{name: "partial output before Launch End", steps: []step{complete(store.BusySendOutputPartial, "", false), launchEnd, ad},
			a: endedA, b: endedB, admission: "prompt_sighting", open: true, partial: true},
		{name: "an acceptance for another connection", steps: []step{written, evidence("acceptance on connection 1",
			store.BusySendPromptEvidence{Order: store.BusySendOrderOrderedAcceptance, ConnectionGeneration: 1})},
			a: inFlight, b: inFlight, open: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, "confirmed", "unconfirmed")
			h := in.claim(t, time.Time{}, tc.race)
			revision := in.issue(t, h, 0, h.Revision).Revision
			lookup := func() store.BusySendResult {
				found, err := in.s.ReadBusySendResult(in.pair.CallerUUID, in.pair.DeliveryRequestID)
				if err != nil || found.Result == nil {
					t.Fatalf("Lookup %+v, %v", found, err)
				}
				return *found.Result
			}
			for _, s := range tc.steps {
				before := readBusySendCustody(t, in.path, in.id)
				result := lookup()
				s.run(t, in, h, revision)
				after := readBusySendCustody(t, in.path, in.id)
				for i := range before {
					if before[i].final && after[i].result() != before[i].result() {
						t.Errorf("%s changed Final Message %d's Result from %s to %s", s.name, i+1, before[i].result(), after[i].result())
					}
				}
				if got := lookup(); before[0].final && !reflect.DeepEqual(got, result) {
					t.Errorf("%s changed the Final Lookup from %+v to %+v", s.name, result, got)
				}
			}
			wantBusySendCustody(t, in.path, in.id, tc.a, tc.b)
			var order, admission string
			if err := openBusySendRaw(t, in.path).QueryRow(`SELECT COALESCE(order_kind, ''), COALESCE(admission_kind, '')
			    FROM busy_send_prompts WHERE handover_id = ? AND prompt_index = 0`, h.HandoverID).Scan(&order, &admission); err != nil {
				t.Fatal(err)
			}
			read, err := in.s.ReadBusySendHandover(ctx, h.HandoverID)
			if err != nil || order != tc.order || admission != tc.admission || read.Open != tc.open || lookup().PartialTransport != tc.partial {
				t.Errorf("order %q, admission %q, open %t, partial %t, %v; want %q, %q, %t, %t",
					order, admission, read.Open, lookup().PartialTransport, err, tc.order, tc.admission, tc.open, tc.partial)
			}
			last := tc.steps[len(tc.steps)-1]
			before := readBusySendTables(t, in.path)
			if last.run(t, in, h, revision) || !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
				t.Errorf("repeating %s changed the inbox", last.name)
			}
		})
	}
}

// TestBusySendCompletionAfterExpiry (xe-repair-2 F1; M1 Expire, RecAcc,
// RecNs): an eligible event after the original issue deadline releases only
// never-issued prompts, and one with nothing to release changes no revision.
// The issued prompt's own completion still applies afterwards: complete
// output with order evidence secures its order and a provider's
// not-submitted returns its Message to Held, so the hand-over closes and the
// inbox is never stranded behind a known outcome.
func TestBusySendCompletionAfterExpiry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		texts    []string
		output   store.BusySendOutput
		released int
		want     []string
		next     store.BusySendClaimOutcome
	}{
		{"one prompt, nothing to release, complete output", []string{"only"}, store.BusySendOutputComplete, 0,
			[]string{"closed sent_unconfirmed final handed_over_not_proven"}, store.BusySendClaimEmpty},
		{"the remainder released, complete output", busySendTwoPrompts, store.BusySendOutputComplete, 1,
			[]string{"closed sent_unconfirmed final handed_over_not_proven", "held held readiness_unknown reserved"}, store.BusySendClaimMade},
		{"the remainder released, not submitted", busySendTwoPrompts, store.BusySendOutputNotSubmitted, 1,
			[]string{"held held readiness_unknown reserved", "held held earlier_message reserved"}, store.BusySendClaimMade},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			in := newBusySendInbox(t, tc.texts...)
			h := in.claim(t, time.Time{}, false)
			is := in.issue(t, h, 0, h.Revision)
			late, err := in.s.ClaimBusySendHandover(ctx, in.claimRequest(h.IssueDeadline.Add(time.Millisecond), false))
			if err != nil || late.Outcome != store.BusySendClaimOwned || late.ReleasedExpired != tc.released {
				t.Fatalf("an event after the deadline %+v, %v; want owned with %d released", late, err, tc.released)
			}
			if read, err := in.s.ReadBusySendHandover(ctx, h.HandoverID); tc.released == 0 && (err != nil || read.Revision != is.Revision) {
				t.Errorf("a no-op expiry left revision %d, %v; want %d", read.Revision, err, is.Revision)
			}
			complete := tc.output == store.BusySendOutputComplete
			var order store.BusySendOrderKind
			if complete {
				order = store.BusySendOrderEmissionComplete
			}
			res := in.complete(t, h, 0, is.Revision, tc.output, order, complete)
			if !res.Closed || res.OrderSecured != complete || res.Returned != 1-boolCount(complete) {
				t.Errorf("completion after expiry %+v; want it applied and the hand-over closed", res)
			}
			wantBusySendCustody(t, in.path, in.id, tc.want...)
			if next, err := in.s.ClaimBusySendHandover(ctx, in.claimRequest(time.Time{}, false)); err != nil || next.Outcome != tc.next {
				t.Errorf("the next event's claim %+v, %v; want %s", next, err, tc.next)
			}
		})
	}
}

// TestBusySendSafeReleaseThenProgress is a bounded event trace of M1's safe
// release, barrier release and Held-to-Final progress (SR-2.4, SR-2.5). A
// provider's not-submitted for the first prompt returns both Messages to Held
// at once, in accepted order, with their reservations, current membership
// cleared and the attempt kept as history; the hand-over closes, so its job
// can neither issue nor repeat it. The next eligible event claims them again;
// order evidence for its first prompt, with no intake proof, lets the second
// issue, by a new issuer once the first issuer's prompt is resolved, and both
// Messages end Final. Progress here is conditional on those events.
func TestBusySendSafeReleaseThenProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := newBusySendInbox(t, busySendTwoPrompts...)
	h := in.claim(t, time.Time{}, false)
	is := in.issue(t, h, 0, h.Revision)
	res := in.complete(t, h, 0, is.Revision, store.BusySendOutputNotSubmitted, "", false)
	if res.Returned != 2 || !res.Closed || res.OrderSecured {
		t.Fatalf("not-submitted %+v; want both Messages returned and the hand-over closed", res)
	}
	wantBusySendCustody(t, in.path, in.id, "held held readiness_unknown reserved", "held held earlier_message reserved")
	first, err := in.s.ReadBusySendHandover(ctx, h.HandoverID)
	if err != nil || first.Open || first.Prompts[0].Transport != store.BusySendTransportUnwritten ||
		first.Prompts[1].Emission != store.BusySendEmissionReleased || !reflect.DeepEqual(first.Prompts[1].MessageIDs, h.Prompts[1].MessageIDs) {
		t.Fatalf("the returned hand-over %+v, %v; want it closed with its attempt and membership kept", first, err)
	}
	before := readBusySendTables(t, in.path)
	var guard *store.BusySendGuardError
	if _, err := in.commit(ctx, h, 1, res.Revision); !errors.As(err, &guard) || guard.Failure != store.BusySendGuardOwner {
		t.Errorf("the old job's next prompt: %v; want owner_changed", err)
	}
	if again := in.complete(t, h, 0, is.Revision, store.BusySendOutputNotSubmitted, "", false); again.Recorded {
		t.Errorf("a repeated not-submitted %+v; want nothing recorded", again)
	}
	if !reflect.DeepEqual(before, readBusySendTables(t, in.path)) {
		t.Fatal("the closed hand-over's job changed custody")
	}

	next := in.claim(t, time.Time{}, false)
	if next.HandoverSeq != 2 || len(next.Prompts) != 2 {
		t.Fatalf("the next claim %+v; want hand-over 2 with two prompts", next)
	}
	is = in.issue(t, next, 0, next.Revision)
	res = in.complete(t, next, 0, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true)
	if !res.OrderSecured || res.Closed {
		t.Fatalf("first prompt's completion %+v; want its order secured and the hand-over still open", res)
	}
	wantBusySendCustody(t, in.path, in.id, "closed sent_unconfirmed final handed_over_not_proven",
		"claimed held handover_in_progress reserved member")
	other := store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: 4343, StartTime: "200"}
	is, err = in.s.CommitBusySendIssue(ctx, store.BusySendIssueRequest{Issuer: &other,
		Ref: store.BusySendPromptRef{HandoverID: next.HandoverID, PromptIndex: 1, Revision: res.Revision}})
	if err != nil {
		t.Fatalf("second prompt's issue by a new issuer: %v", err)
	}
	if res = in.complete(t, next, 1, is.Revision, store.BusySendOutputComplete, store.BusySendOrderEmissionComplete, true); !res.Closed {
		t.Fatalf("second prompt's completion %+v; want the hand-over closed", res)
	}
	wantBusySendCustody(t, in.path, in.id, "closed sent_unconfirmed final handed_over_not_proven", "closed sent_unconfirmed final confirmation_off")
	fence, err := in.s.ReadBusySendLaunchFence(ctx, in.dest.LaunchID)
	if err != nil || len(fence.Prompts) != 0 || !slices.ContainsFunc(fence.Processes, func(p store.BusySendLaunchProcess) bool {
		return p.Role == other.Role && p.PID == other.PID && p.StartTime == other.StartTime
	}) {
		t.Errorf("fence %+v, %v; want the new issuer recorded and no unresolved prompt", fence, err)
	}
}

// TestBusySendWaitCause (SR-2.6): an event's no-issue cause is recorded on
// the first Held Message; a new cause restarts its since, a repeated one keeps
// it, a stale Launch's cause changes nothing, and while a hand-over owns the
// conversation a Held Message waits handover_in_progress whatever the event
// said. Long-held work stays Held, with no age deadline: neither time nor a
// wait changes the agent row or its Launch, so no needs_intelligence or
// lifecycle event is invented.
func TestBusySendWaitCause(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := newBusySendInbox(t, "first", "second")
	rows := readBusySendTables(t, in.path)
	admitted := readBusySendCustody(t, in.path, in.id)
	base := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name         string
		launch       string
		reason, want store.BusySendWaitReason
		at           time.Time
		changed      bool
		since        time.Time
	}{
		{"a cause", in.dest.LaunchID, store.BusySendWaitTurnRunning, store.BusySendWaitTurnRunning, base, true, base},
		{"the same cause later", in.dest.LaunchID, store.BusySendWaitTurnRunning, store.BusySendWaitTurnRunning, base.Add(time.Minute), false, base},
		{"a new cause", in.dest.LaunchID, store.BusySendWaitDialog, store.BusySendWaitDialog, base.Add(2 * time.Minute), true, base.Add(2 * time.Minute)},
		{"another Launch's cause", "another-launch", store.BusySendWaitTurnRunning, store.BusySendWaitDialog, base.Add(3 * time.Minute), false,
			base.Add(2 * time.Minute)},
		{"a cause thirty days on", in.dest.LaunchID, store.BusySendWaitReadinessUnknown, store.BusySendWaitReadinessUnknown,
			base.Add(30 * 24 * time.Hour), true, base.Add(30 * 24 * time.Hour)},
	} {
		changed, err := in.s.RecordBusySendWait(ctx, store.BusySendWaitRequest{InstanceID: in.id, LaunchID: tc.launch, Reason: tc.reason, At: tc.at})
		got := wantBusySendCustody(t, in.path, in.id, "held held "+string(tc.want)+" reserved", "held held earlier_message reserved")
		if err != nil || changed != tc.changed || got[0].since != tc.since.UnixMilli() || got[1].since != admitted[1].since {
			t.Errorf("%s: changed %t, %v, since %d and %d; want %t, since %d and %d", tc.name, changed, err, got[0].since, got[1].since,
				tc.changed, tc.since.UnixMilli(), admitted[1].since)
		}
	}
	after := readBusySendTables(t, in.path)
	for _, table := range []string{"spawns", "busy_send_launches", "busy_send_handovers"} {
		if !reflect.DeepEqual(rows[table], after[table]) {
			t.Errorf("recording waits changed %s", table)
		}
	}

	in.claim(t, time.Time{}, false)
	if _, err := in.s.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: in.id, Text: []byte("third")}); err != nil {
		t.Fatal(err)
	}
	if _, err := in.s.RecordBusySendWait(ctx, store.BusySendWaitRequest{InstanceID: in.id, LaunchID: in.dest.LaunchID,
		Reason: store.BusySendWaitTurnRunning, At: base.Add(31 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	wantBusySendCustody(t, in.path, in.id, "claimed held handover_in_progress reserved member",
		"claimed held handover_in_progress reserved member", "held held handover_in_progress reserved")
}
