package api_test

// pause_claim_test.go: the pause call under its Launch's pause claim (SR-6.4,
// SR-6.5; M2 P-C4.1, P-C4.5 to P-C4.7) on pause_keyless_test.go's covered-row
// fixture, real SQLite and internal/store's real claim writers, injecting only
// the signal, time, causal interleavings and errors. The store's predicate,
// repeat, CAS, lifetime and locked-method matrix is internal/store's
// (busy_send_pause_test.go, busy_send_lifecycle_test.go); the issue fences are
// internal/busysend's and internal/hook's; interface parity is the CLI's, the
// MCP server's and the TS client's envelope tests, and Docker the testplans';
// the answers' advice is followed in advice_follow_pause_claim_test.go. Paths
// whose disposition the claim does not change keep their keyless pause tests
// in pause_keyless_test.go. Every test here replaces keyless pause's
// process-wide signal hook, so all are serial.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// pcInjected is a store failure a test injects at a store boundary: neither
// the budget nor the caller's cancellation, so pause returns it unchanged.
var pcInjected = errors.New("injected store failure")

// pcBusy is a store call the pause budget cut short, as the store reports it.
var pcBusy = fmt.Errorf("store: injected: %w", store.ErrStoreBusy)

// now is keyless pause's clock as e skews it (api.SetPauseNowForTest).
func (e *kpEnv) now() time.Time { return time.Now().Add(time.Duration(e.skew.Load())) }

// pcClaims reads id's pause claims raw through a connection of its own, oldest
// first, as "state [closed_by] [outcome[:cause]] rN", with " intent" when a
// signal intent is recorded: what is durable, not what a call holds.
func pcClaims(t *testing.T, path, id string) []string {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	rows, err := raw.Query(`SELECT state, COALESCE(closed_by, ''), COALESCE(call_outcome, ''), COALESCE(call_cause, ''), revision,
	        signal_intent_at_ms IS NOT NULL FROM busy_send_pause_claims WHERE instance_id = ? ORDER BY rowid`, id)
	if err != nil {
		t.Fatalf("read pause claims: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var state, closedBy, outcome, cause string
		var revision int64
		var intent bool
		if err := rows.Scan(&state, &closedBy, &outcome, &cause, &revision, &intent); err != nil {
			t.Fatal(err)
		}
		c := strings.Join(strings.Fields(state+" "+closedBy+" "+outcome), " ")
		if cause != "" {
			c += ":" + cause
		}
		c += fmt.Sprintf(" r%d", revision)
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

// assertClaims fails unless r's durable pause claims are want (pcClaims).
func (e *kpEnv) assertClaims(t *testing.T, want ...string) {
	t.Helper()
	if got := pcClaims(t, e.dbPath, e.r.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("pause claims %q; want %q", got, want)
	}
}

// pcAssertErr fails unless a pause returned want: "" success, "canceled" the
// bare context error, "injected" pcInjected as the store returned it, both
// with no err_details, else ErrSpawnNotPausable with err_details JSON want.
func pcAssertErr(t *testing.T, err error, want string) {
	t.Helper()
	switch want {
	case "":
		if err != nil {
			t.Errorf("Pause: %v; want success", err)
		}
	case "canceled":
		if !errors.Is(err, context.Canceled) || api.ErrDetails(err) != nil {
			t.Errorf("Pause = %v (details %+v); want the bare context error", err, api.ErrDetails(err))
		}
	case "injected":
		if !errors.Is(err, pcInjected) || api.ErrDetails(err) != nil {
			t.Errorf("Pause = %v (details %+v); want the store's own error unchanged", err, api.ErrDetails(err))
		}
	default:
		assertOneName(t, err, "ErrSpawnNotPausable")
		if got := jsonOf(t, api.ErrDetails(err)); got != want {
			t.Errorf("err_details = %s (%v); want %s", got, err, want)
		}
	}
}

// pcWait reports whether ch closed within 30 s, failing the test if not.
// It may run on the store's own goroutine (a plain signal's callback).
func pcWait(t *testing.T, ch <-chan struct{}, what string) bool {
	select {
	case <-ch:
		return true
	case <-time.After(30 * time.Second):
		t.Errorf("%s did not happen within 30 s", what)
		return false
	}
}

// claimNow makes a conditional call's claim on r's current Launch through the
// store's own writer, on e's clock, its deadline budget away, and returns the
// owner's ref.
func (e *kpEnv) claimNow(t *testing.T, budget time.Duration) store.BusySendPauseRef {
	t.Helper()
	pub, err := e.st.GetBusySendPublishedLaunch(e.r.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.st.ClaimBusySendPauseWithin(context.Background(), store.BusySendPauseRequest{InstanceID: e.r.ID,
		LaunchID: pub.LaunchID, LaunchToken: pub.LaunchToken, OwnerCallID: uuid.NewString(), Deadline: e.now().Add(budget),
		Now: e.now}, time.Minute)
	if err != nil || d.Kind != store.BusySendPauseDecisionClaimed || d.Claim == nil {
		t.Fatalf("claim = %+v, %v; want a new standing claim", d, err)
	}
	return d.Claim.Ref
}

// intentNow persists ref's signal intent through the store's writer and
// returns the owner's next ref.
func (e *kpEnv) intentNow(t *testing.T, ref store.BusySendPauseRef) store.BusySendPauseRef {
	t.Helper()
	w, err := e.st.PersistBusySendPauseSignalWithin(context.Background(), store.BusySendPauseWriteRequest{Ref: ref, Now: e.now}, time.Minute)
	if err != nil || !w.Applied || w.NextRef == nil {
		t.Fatalf("signal intent = %+v, %v; want applied", w, err)
	}
	return *w.NextRef
}

// recordNow records ref's outcome with cause through the store's writer, a
// paused one with its exit verified now.
func (e *kpEnv) recordNow(t *testing.T, ref store.BusySendPauseRef, outcome store.BusySendPauseCallOutcome, cause string) {
	t.Helper()
	req := store.BusySendPauseOutcomeRequest{Ref: ref, Now: e.now, Outcome: outcome, Cause: cause}
	if outcome == store.BusySendPausePaused {
		req.VerifiedExitAt = e.now()
	}
	if w, err := e.st.RecordBusySendPauseOutcomeWithin(context.Background(), req, time.Minute); err != nil || !w.Applied {
		t.Fatalf("outcome %s = %+v, %v; want applied", outcome, w, err)
	}
}

// issue issues r's Held Messages' first prompt with no recorded issuer
// (busyIssue) and admits one more Message, which stays Held; it returns the
// prompt as the issuing job holds it.
func (e *kpEnv) issue(t *testing.T) store.BusySendPromptRef {
	t.Helper()
	ref := e.busyIssue(t, e.r, nil)
	busyAdmit(t, e.c, api.SendKeysParams{InstanceID: e.r.ID, Text: "third"})
	e.woken()
	return ref
}

// takeTurn ends ref's turn through the route's real writers: its output is
// written complete and the agent's own sighting admits it, so the Launch has
// no turn pending while the row stays raw waiting.
func (e *kpEnv) takeTurn(t *testing.T, ref store.BusySendPromptRef) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.st.RecordBusySendCompletion(ctx, store.BusySendCompletion{Ref: ref, Output: store.BusySendOutputComplete,
		Order: store.BusySendOrderEmissionComplete, UnprovenBoundary: true}); err != nil {
		t.Fatalf("completion: %v", err)
	}
	pub, err := e.st.GetBusySendPublishedLaunch(e.r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.RecordBusySendEvidence(ctx, store.BusySendPromptEvidence{HandoverID: ref.HandoverID, PromptIndex: ref.PromptIndex,
		LaunchID: pub.LaunchID, Admission: store.BusySendAdmissionPromptSighting}); err != nil {
		t.Fatalf("admission: %v", err)
	}
}

// pcStore is a real store whose pause claim calls (pkg/api's pauseClaimStore
// seam), Launch End and the row read again before the signal can be wrapped.
// Each hook gets the call's number (from 1) and the real call as next, and
// may run a race before it, replace its result after it, or answer without
// it. It keeps each plain signal's request and counts the owner's release
// and outcome writes.
type pcStore struct {
	*store.Store
	claim, check func(n int, next func() (store.BusySendPauseDecision, error)) (store.BusySendPauseDecision, error)
	persist      func(next func() (store.BusySendPauseWriteResult, error)) (store.BusySendPauseWriteResult, error)
	release      func(next func() (store.BusySendPauseWriteResult, error)) (store.BusySendPauseWriteResult, error)
	record       func(next func() (store.BusySendPauseWriteResult, error)) (store.BusySendPauseWriteResult, error)
	plain        func(n int, signal func() error, next func(func() error) (store.BusySendPlainPauseSignalResult, error)) (store.BusySendPlainPauseSignalResult, error)
	end          func(next func() (bool, error)) (bool, error)
	reread       func()
	published    func()
	claims       int
	checks       int
	releases     int
	records      int
	plainReqs    []store.BusySendPauseRequest
}

func (s *pcStore) ClaimBusySendPauseWithin(ctx context.Context, req store.BusySendPauseRequest, wait time.Duration) (store.BusySendPauseDecision, error) {
	s.claims++
	next := func() (store.BusySendPauseDecision, error) { return s.Store.ClaimBusySendPauseWithin(ctx, req, wait) }
	if s.claim == nil {
		return next()
	}
	return s.claim(s.claims, next)
}

func (s *pcStore) CheckBusySendPauseWithin(ctx context.Context, req store.BusySendPauseRequest, wait time.Duration) (store.BusySendPauseDecision, error) {
	s.checks++
	next := func() (store.BusySendPauseDecision, error) { return s.Store.CheckBusySendPauseWithin(ctx, req, wait) }
	if s.check == nil {
		return next()
	}
	return s.check(s.checks, next)
}

func (s *pcStore) PersistBusySendPauseSignalWithin(ctx context.Context, req store.BusySendPauseWriteRequest, wait time.Duration) (store.BusySendPauseWriteResult, error) {
	next := func() (store.BusySendPauseWriteResult, error) {
		return s.Store.PersistBusySendPauseSignalWithin(ctx, req, wait)
	}
	if s.persist == nil {
		return next()
	}
	return s.persist(next)
}

func (s *pcStore) ReleaseBusySendPauseBeforeSignalWithin(ctx context.Context, req store.BusySendPauseRefusalRequest, wait time.Duration) (store.BusySendPauseWriteResult, error) {
	s.releases++
	next := func() (store.BusySendPauseWriteResult, error) {
		return s.Store.ReleaseBusySendPauseBeforeSignalWithin(ctx, req, wait)
	}
	if s.release == nil {
		return next()
	}
	return s.release(next)
}

func (s *pcStore) RecordBusySendPauseOutcomeWithin(ctx context.Context, req store.BusySendPauseOutcomeRequest, wait time.Duration) (store.BusySendPauseWriteResult, error) {
	s.records++
	next := func() (store.BusySendPauseWriteResult, error) {
		return s.Store.RecordBusySendPauseOutcomeWithin(ctx, req, wait)
	}
	if s.record == nil {
		return next()
	}
	return s.record(next)
}

func (s *pcStore) SignalPlainBusySendPauseWithin(ctx context.Context, req store.BusySendPauseRequest, signal func() error, wait time.Duration) (store.BusySendPlainPauseSignalResult, error) {
	s.plainReqs = append(s.plainReqs, req)
	next := func(signal func() error) (store.BusySendPlainPauseSignalResult, error) {
		return s.Store.SignalPlainBusySendPauseWithin(ctx, req, signal, wait)
	}
	if s.plain == nil {
		return next(signal)
	}
	return s.plain(len(s.plainReqs), signal, next)
}

func (s *pcStore) EndBusySendLaunchWithin(ctx context.Context, instanceID, token string, wait time.Duration) (bool, error) {
	next := func() (bool, error) { return s.Store.EndBusySendLaunchWithin(ctx, instanceID, token, wait) }
	if s.end == nil {
		return next()
	}
	return s.end(next)
}

// GetBusySendPublishedLaunchWithin is keyless pause's published Launch read;
// published runs once, before the first.
func (s *pcStore) GetBusySendPublishedLaunchWithin(ctx context.Context, id string, wait time.Duration) (store.BusySendPublishedLaunch, error) {
	if f := s.published; f != nil {
		s.published = nil
		f()
	}
	return s.Store.GetBusySendPublishedLaunchWithin(ctx, id, wait)
}

// GetSpawnContext is keyless pause's row read again just before the signal;
// reread runs once, before it.
func (s *pcStore) GetSpawnContext(ctx context.Context, id string, wait time.Duration) (api.Spawn, error) {
	if f := s.reread; f != nil {
		s.reread = nil
		f()
	}
	return s.Store.GetSpawnContext(ctx, id, wait)
}

// TestPauseClaimEligibility (SR-6.5; M2 P-C4.1 authoritative raw state, P-C4.5):
// a conditional pause, through Client.Pause and the exported api.Pause alike,
// refuses not_waiting with the raw state even while the settle-time display
// shows waiting, and turn_pending, with no observed_state, for issued input
// the agent has not started. A refusal signals, claims and changes nothing:
// the row, its delivery facts and Held Messages, no pane, input or wake. The
// claim's other predicates and their precedence are internal/store's
// (TestBusySendPauseEligibility); an uncovered row's agent_not_covered is
// TestBusySendUncoveredRefusalMatrix's.
func TestPauseClaimEligibility(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, e *kpEnv)
		want  string
	}{
		{"raw working shown waiting", func(t *testing.T, e *kpEnv) {
			if a := apitest.ApplyAgentHook(t, e.dbPath, e.r.ID, "UserPromptSubmit", e.r.Spawn.SessionID); !a.Applied {
				t.Fatalf("UserPromptSubmit not applied: %s", a.Reason)
			}
			_, stamp, err := apitest.ReadSettle(e.dbPath, e.r.ID)
			if err != nil {
				t.Fatal(err)
			}
			shown, err := e.st.GetSpawnSettled(e.r.ID, store.SettleRead{SettleMs: 5000, NowMs: stamp})
			if err != nil || shown.ReportedState != store.StateWaiting || shown.State != store.StateWorking {
				t.Fatalf("settled read %s over raw %s, %v; want waiting shown over raw working", shown.ReportedState, shown.State, err)
			}
		}, `{"cause":"not_waiting","observed_state":"working"}`},
		{"issued input not started", func(t *testing.T, e *kpEnv) { e.issue(t) }, `{"cause":"turn_pending"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			tc.setup(t, e)
			e.woken()
			held, facts, cols := e.held(t), busyFacts(t, e.dbPath, e.r.ID), e.columns(t, e.r.ID)

			_, viaClient := e.c.Pause(context.Background(), api.PauseParams{InstanceID: e.r.ID, OnlyIfWaiting: true})
			viaExported := e.pauseOpt(context.Background(), nil, kpBudget, true)

			pcAssertErr(t, viaClient, tc.want)
			pcAssertErr(t, viaExported, tc.want)
			e.assertSignals(t, 0)
			e.assertClaims(t)
			e.assertRowUnchanged(t, e.r.ID, cols)
			if after := busyFacts(t, e.dbPath, e.r.ID); !reflect.DeepEqual(after, facts) {
				t.Errorf("delivery facts after the refusal = %+v; want unchanged %+v", after, facts)
			}
			e.assertKept(t, held)
		})
	}
}

// TestPauseClaimStandingAnswers (SR-6.5; M2 P-C4.5 RepeatDecision, P-C4.6): a
// claim standing on the current Launch answers a pause with either option
// value, sending nothing and changing nothing: in progress within its owner's
// deadline, intent or not; pause_uncertain past it with an intent and no
// outcome; a recorded exit_uncertain with signal_sent true, or a recorded
// failure with its cause or, with none, pause_uncertain, signal_sent
// otherwise omitted, never derived from the intent. A Launch whose row the
// agent's SessionEnd finished, a logical End, is still answered by its
// standing claim, and a paused recorded there is replayed as success, nothing
// written. Once the agent's exit is known on a live row (the claim ended on
// kill's exit evidence, or paused recorded with its Launch End not yet
// written) the claim answers nothing: the first pause finishes the shutdown
// with no signal (Launch End, exclusion, row end) and the next is the
// finished row's no-op; with the agent still running, a contradiction the
// fixture makes, both are refused owned_process_running with nothing
// signalled. The states are made by the store's own claim, intent and
// outcome writers; deadline equality and the repeat precedence are
// internal/store's (TestBusySendPauseRepeatAnswers).
func TestPauseClaimStandingAnswers(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	recorded := func(outcome store.BusySendPauseCallOutcome, cause string) func(t *testing.T, e *kpEnv) {
		return func(t *testing.T, e *kpEnv) {
			e.recordNow(t, e.intentNow(t, e.claimNow(t, 15*time.Second)), outcome, cause)
		}
	}
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, e *kpEnv)
		want   string
		claims string
		replay bool // the answer replays a recorded failure or uncertain exit
		ends   bool // the first pause ends the row with no signal, not an answer
	}{
		{"in progress", func(t *testing.T, e *kpEnv) { e.claimNow(t, 15*time.Second) }, `{"cause":"pause_in_progress"}`, "standing r1", false, false},
		{"in progress with its intent", func(t *testing.T, e *kpEnv) { e.intentNow(t, e.claimNow(t, 15*time.Second)) },
			`{"cause":"pause_in_progress"}`, "standing r2 intent", false, false},
		{"intent past its deadline", func(t *testing.T, e *kpEnv) {
			e.intentNow(t, e.claimNow(t, 15*time.Second))
			e.advance(time.Minute)
		}, `{"cause":"pause_uncertain"}`, "standing r2 intent", false, false},
		{"paused recorded, its Launch End not written", func(t *testing.T, e *kpEnv) {
			recorded(store.BusySendPausePaused, "")(t, e)
			e.pc.Set(e.r.AgentPID, procfix.Gone()) // as its verified exit says
		}, "", "ended launch_end paused r4 intent", false, true},
		{"paused recorded, the agent still running", recorded(store.BusySendPausePaused, ""),
			`{"cause":"owned_process_running","signal_sent":false}`, "standing paused r3 intent", false, false},
		{"paused recorded, row ended by its SessionEnd", func(t *testing.T, e *kpEnv) {
			recorded(store.BusySendPausePaused, "")(t, e)
			e.endBySessionEnd(t, e.r)
		}, "", "standing paused r3 intent", false, false},
		{"exit_uncertain recorded", recorded(store.BusySendPauseExitUncertain, "exit_uncertain"),
			`{"cause":"exit_uncertain","signal_sent":true}`, "standing exit_uncertain:exit_uncertain r3 intent", true, false},
		{"failure recorded with its cause", recorded(store.BusySendPauseFailed, "owned_process_running"),
			`{"cause":"owned_process_running"}`, "standing failed:owned_process_running r3 intent", true, false},
		{"failure recorded with no cause", recorded(store.BusySendPauseFailed, ""), `{"cause":"pause_uncertain"}`, "standing failed r3 intent", true, false},
		{"row ended by its SessionEnd", func(t *testing.T, e *kpEnv) {
			e.claimNow(t, 15*time.Second)
			e.endBySessionEnd(t, e.r)
		}, `{"cause":"pause_in_progress"}`, "standing r1", false, false},
		{"claim ended on kill's exit evidence", func(t *testing.T, e *kpEnv) {
			e.claimNow(t, 15*time.Second)
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), e.r.AgentPID)
			if res, err := e.c.Kill(api.KillParams{InstanceID: e.r.ID}); err != nil || !res.KillSent {
				t.Fatalf("kill = %+v, %v; want the agent's pane killed", res, err)
			}
		}, "", "ended launch_end r2", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			e.step = kpStep // a refusal for a running agent waits its budget out
			tc.setup(t, e)
			e.answer = func(kpSignal) error { t.Error("a pause the claim answered signalled"); return nil }
			held, facts, cols := e.held(t), busyFacts(t, e.dbPath, e.r.ID), e.columns(t, e.r.ID)

			for _, only := range []bool{false, true} {
				err := e.pauseOpt(context.Background(), nil, kpBudget, only)
				pcAssertErr(t, err, tc.want)
				if tc.replay {
					adviceAssertPhrase(t, err, "an earlier pause of this Launch")
				}
			}

			e.assertSignals(t, 0)
			e.assertClaims(t, tc.claims)
			if tc.ends {
				e.assertEnded(t)
				if after := e.held(t); !reflect.DeepEqual(after, held) {
					t.Errorf("Held Messages after the row ended = %+v; want unchanged %+v", after, held)
				}
			} else {
				e.assertRowUnchanged(t, e.r.ID, cols)
				if after := busyFacts(t, e.dbPath, e.r.ID); !reflect.DeepEqual(after, facts) {
					t.Errorf("delivery facts after the answers = %+v; want unchanged %+v", after, facts)
				}
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallSendText)) + len(e.rec.SocketCallsOf(tmux.CallSendKey)); n != 0 {
				t.Errorf("%d pane inputs; want none", n)
			}
		})
	}
}

// TestPauseClaimIntentBeforeSignal (SR-6.5; M2 P-C4.6 PausePersistIntentBy,
// at most one signal per standing claim; P-C4.7): a conditional
// pause of a waiting agent persists its signal intent, observed committed
// through a connection of its own at the signal, before its one SIGTERM.
// While it stands, before its intent, at the signal and at its first exit
// poll, a plain and a conditional pause each get pause_in_progress and send
// nothing. It records paused once the agent's exit is verified, then its
// Launch End ends the claim and the row ends, its session kept; a later pause
// is the ended row's no-op, with no second signal. Held Messages stay Held.
func TestPauseClaimIntentBeforeSignal(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	e := newKeylessEnv(t, killRowSpec{})
	held := e.held(t)
	contend := func(when string) {
		t.Helper()
		for _, only := range []bool{false, true} {
			err := e.pauseOpt(context.Background(), nil, kpBudget, only)
			assertOneName(t, err, "ErrSpawnNotPausable")
			if got := jsonOf(t, api.ErrDetails(err)); got != `{"cause":"pause_in_progress"}` {
				t.Errorf("pause %s (only_if_waiting %t): err_details %s; want pause_in_progress", when, only, got)
			}
		}
	}
	st := &pcStore{Store: e.st, persist: func(next func() (store.BusySendPauseWriteResult, error)) (store.BusySendPauseWriteResult, error) {
		e.assertClaims(t, "standing r1")
		contend("before the owner's intent")
		return next()
	}}
	e.answer = func(s kpSignal) error {
		if len(e.sent) > 1 {
			return nil // a contender's signal: assertSignals reports it
		}
		e.assertClaims(t, "standing r2 intent")
		contend("at the owner's signal")
		return nil // the agent exits only at the first poll
	}
	e.atSleep = func() {
		contend("at the owner's first exit poll")
		e.pc.Set(e.r.AgentPID, procfix.Gone())
	}

	err := e.pauseOpt(context.Background(), st, kpBudget, true)

	pcAssertErr(t, err, "")
	e.assertSignals(t, 1)
	e.assertClaims(t, "ended launch_end paused r4 intent")
	e.assertEnded(t)
	e.assertKept(t, held)
	for _, only := range []bool{false, true} {
		pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, only), "")
	}
	e.assertSignals(t, 1)
	e.assertClaims(t, "ended launch_end paused r4 intent")
}

// TestPauseClaimOwnerAuthority (SR-6.5; M2 P-C4.6, P-C4.7): the owner's
// signal is authorized only by its own applied intent. An intent write that
// failed, one whose commit was not confirmed, one that lost its claim to a
// plain pause's release of it (abandoned past its deadline) or to kill's exit
// evidence, and a cancellation or spent budget just after a committed intent
// each send no signal and leave the claim as durably written, never retried
// or refreshed; a cancellation before the intent leaves the claim standing
// with none. Later pauses (a minute on, both option values) then read that
// state: a claim abandoned before its intent, or released, is claimed afresh
// and the agent paused; an intent with no outcome is pause_uncertain for good,
// with no second signal; an ended claim is the no-op.
func TestPauseClaimOwnerAuthority(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	type next = func() (store.BusySendPauseWriteResult, error)
	const reclaimed = "ended launch_end paused r4 intent"
	for _, tc := range []struct {
		name    string
		persist func(t *testing.T, e *kpEnv, cancel context.CancelFunc, next next) (store.BusySendPauseWriteResult, error)
		reread  func(e *kpEnv, cancel context.CancelFunc)
		want    string
		signals int    // the owner's call included
		claims  string // after the owner's call
		later   string // the later pauses: "reclaim", "uncertain" or "noop"
	}{
		{name: "intent write fails", persist: func(_ *testing.T, _ *kpEnv, _ context.CancelFunc, _ next) (store.BusySendPauseWriteResult, error) {
			return store.BusySendPauseWriteResult{}, pcInjected
		}, want: "injected", claims: "released refused_before_signal refused r2", later: "reclaim"},
		{name: "intent commit unconfirmed", persist: func(_ *testing.T, _ *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseWriteResult, error) {
			_, _ = next()
			return store.BusySendPauseWriteResult{}, pcInjected
		}, want: "injected", claims: "standing r2 intent", later: "uncertain"},
		{name: "abandoned before its intent, then a plain pause", persist: func(t *testing.T, e *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseWriteResult, error) {
			e.advance(time.Minute)
			if err := e.pauseOpt(context.Background(), nil, kpBudget, false); err != nil {
				t.Errorf("plain pause past the owner's deadline: %v; want success", err)
			}
			return next()
		}, want: `{"cause":"budget_exhausted","signal_sent":false}`, signals: 1, claims: "released abandoned_before_signal r2", later: "noop"},
		{name: "claim ended by kill before its intent", persist: func(t *testing.T, e *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseWriteResult, error) {
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), e.r.AgentPID)
			if _, err := e.c.Kill(api.KillParams{InstanceID: e.r.ID}); err != nil {
				t.Errorf("kill: %v", err)
			}
			return next()
		}, want: `{"cause":"launch_changed","signal_sent":false}`, claims: "ended launch_end r2", later: "noop"},
		{name: "cancelled after its intent", persist: func(_ *testing.T, _ *kpEnv, cancel context.CancelFunc, next next) (store.BusySendPauseWriteResult, error) {
			defer cancel()
			return next()
		}, want: "canceled", claims: "standing r2 intent", later: "uncertain"},
		{name: "budget spent after its intent", persist: func(_ *testing.T, e *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseWriteResult, error) {
			defer e.advance(time.Hour)
			return next()
		}, want: `{"cause":"budget_exhausted","signal_sent":false}`, claims: "standing r2 intent", later: "uncertain"},
		{name: "cancelled before its intent", reread: func(_ *kpEnv, cancel context.CancelFunc) { cancel() },
			want: "canceled", claims: "standing r1", later: "reclaim"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := &pcStore{Store: e.st}
			if tc.persist != nil {
				st.persist = func(next next) (store.BusySendPauseWriteResult, error) { return tc.persist(t, e, cancel, next) }
			}
			if tc.reread != nil {
				st.reread = func() { tc.reread(e, cancel) }
			}

			err := e.pauseOpt(ctx, st, kpBudget, true)

			pcAssertErr(t, err, tc.want)
			e.assertSignals(t, tc.signals)
			e.assertClaims(t, tc.claims)
			if cause, _ := e.launchEnd(t); tc.signals == 0 && tc.later != "noop" && cause != "" {
				t.Errorf("Launch End = %q; want none after a call that sent nothing", cause)
			}

			e.advance(time.Minute)
			switch tc.later {
			case "reclaim":
				pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, true), "")
				e.assertSignals(t, 1)
				want := tc.claims
				if want == "standing r1" {
					want = "released abandoned_before_signal r2"
				}
				e.assertClaims(t, want, reclaimed)
			case "uncertain", "noop":
				want := `{"cause":"pause_uncertain"}`
				if tc.later == "noop" {
					want = ""
				}
				for _, only := range []bool{false, true} {
					pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, only), want)
				}
				e.assertSignals(t, tc.signals)
				e.assertClaims(t, tc.claims)
			}
		})
	}
}

// TestPauseClaimPlainSignalOrder (SR-6.4, SR-6.5; M2 P-C4.6): a
// plain pause signals only inside the store's locked check that no claim
// stands. A conditional pause that claims, persists its intent and signals
// after the plain pause's Check answered clear, before its signal, is answered
// there: the plain pause gets pause_in_progress and its signal is never
// called, and the owner finishes. In the reverse order, a conditional pause
// reaching its claim while the plain signal runs claims only after it: it
// finds the agent gone, signals nothing and records no intent, and both
// succeed. Exactly one signal either way. The lock itself is internal/store's
// (TestBusySendPausePlainSignalLockOrder).
func TestPauseClaimPlainSignalOrder(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	t.Run("conditional between the plain Check and its signal", func(t *testing.T) {
		e := newKeylessEnv(t, killRowSpec{})
		held := e.held(t)
		atSignal, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		var once sync.Once
		e.answer = func(s kpSignal) error {
			once.Do(func() {
				close(atSignal)
				<-release
			})
			e.pc.Set(s.pid, procfix.Gone())
			return nil
		}
		var plainRes store.BusySendPlainPauseSignalResult
		st := &pcStore{Store: e.st, plain: func(_ int, signal func() error, next func(func() error) (store.BusySendPlainPauseSignalResult, error)) (store.BusySendPlainPauseSignalResult, error) {
			go func() { done <- e.pauseOpt(context.Background(), nil, kpBudget, true) }()
			pcWait(t, atSignal, "the conditional pause's signal")
			res, err := next(signal)
			plainRes = res
			return res, err
		}}

		err := e.pauseOpt(context.Background(), st, kpBudget, false)
		close(release)
		owner := joinWithin(t, done)

		pcAssertErr(t, err, `{"cause":"pause_in_progress"}`)
		if plainRes.Invoked {
			t.Errorf("plain signal result %+v; want its callback never called", plainRes)
		}
		pcAssertErr(t, owner, "")
		e.assertSignals(t, 1)
		e.assertClaims(t, "ended launch_end paused r4 intent")
		e.assertEnded(t)
		e.assertKept(t, held)
	})
	t.Run("conditional at the plain signal", func(t *testing.T) {
		e := newKeylessEnv(t, killRowSpec{})
		held := e.held(t)
		other, err := store.Open(e.dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = other.Close() })
		entered, done := make(chan struct{}), make(chan error, 1)
		contender := &pcStore{Store: other, claim: func(_ int, next func() (store.BusySendPauseDecision, error)) (store.BusySendPauseDecision, error) {
			close(entered)
			return next()
		}}
		e.answer = func(s kpSignal) error {
			if len(e.sent) == 1 {
				// The plain signal, under the store's write lock: a conditional
				// pause through a store of its own reaches its claim meanwhile.
				go func() { done <- e.pauseOpt(context.Background(), contender, kpBudget, true) }()
				pcWait(t, entered, "the contender's claim")
			}
			e.pc.Set(s.pid, procfix.Gone())
			return nil
		}
		var contended error
		st := &pcStore{Store: e.st, plain: func(_ int, signal func() error, next func(func() error) (store.BusySendPlainPauseSignalResult, error)) (store.BusySendPlainPauseSignalResult, error) {
			res, err := next(signal)
			contended = joinWithin(t, done) // the contender finishes before the plain pause goes on
			return res, err
		}}

		err = e.pauseOpt(context.Background(), st, kpBudget, false)

		pcAssertErr(t, err, "")
		pcAssertErr(t, contended, "")
		e.assertSignals(t, 1)
		e.assertClaims(t, "ended launch_end r2")
		e.assertEnded(t)
		e.assertKept(t, held)
	})
}

// TestPauseClaimPlainRecheck (SR-6.5; M2 P-C4.5, P-C4.7): a claim
// abandoned before its intent found by the plain pause's locked check is
// released only by a separately committed Check, after which the locked check
// runs again for the same captured Launch and deadline: with no claim it
// signals once; a claim made in that gap answers it. A release whose commit
// is not confirmed (its error returned unchanged), a cancellation or spent
// budget after it, and a Launch changed before it send nothing and do not try
// the signal again. A claim whose agent's exit is known by then (ended on exit
// evidence, or a standing paused, here injected) is no answer: nothing is
// signalled and the pause goes on as for an agent already gone, ending the
// row, or refusing owned_process_running when the agent still runs. Recheck
// is no cause or field of any answer.
func TestPauseClaimPlainRecheck(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	type next = func() (store.BusySendPauseDecision, error)
	const released = "released abandoned_before_signal r2"
	// endBefore records the Launch End, which ends the abandoned claim, just
	// before the Recheck's Check, the agent gone or still running.
	endBefore := func(gone bool) func(*testing.T, *kpEnv, context.CancelFunc, next) (store.BusySendPauseDecision, error) {
		return func(t *testing.T, e *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseDecision, error) {
			if gone {
				e.pc.Set(e.r.AgentPID, procfix.Gone())
			}
			if ended, err := e.st.EndBusySendLaunchWithin(context.Background(), e.r.ID, e.r.Token, time.Minute); err != nil || !ended {
				t.Fatalf("Launch End = %t, %v; want recorded", ended, err)
			}
			return next()
		}
	}
	for _, tc := range []struct {
		name    string
		recheck func(t *testing.T, e *kpEnv, cancel context.CancelFunc, next next) (store.BusySendPauseDecision, error)
		want    string
		signals int
		plains  int
		claims  []string
	}{
		{"released, then signalled", nil, "", 1, 2, []string{released}},
		{"a new claim in the gap", func(t *testing.T, e *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseDecision, error) {
			d, err := next()
			e.claimNow(t, 15*time.Second)
			return d, err
		}, `{"cause":"pause_in_progress"}`, 0, 2, []string{released, "standing r1"}},
		{"release unconfirmed", func(_ *testing.T, _ *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseDecision, error) {
			_, _ = next()
			return store.BusySendPauseDecision{}, pcInjected
		}, "injected", 0, 1, []string{released}},
		{"cancelled after the release", func(_ *testing.T, _ *kpEnv, cancel context.CancelFunc, next next) (store.BusySendPauseDecision, error) {
			defer cancel()
			return next()
		}, "canceled", 0, 1, []string{released}},
		{"budget spent after the release", func(_ *testing.T, e *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseDecision, error) {
			defer e.advance(time.Hour)
			return next()
		}, `{"cause":"budget_exhausted","signal_sent":false}`, 0, 1, []string{released}},
		{"Launch changed before the release", func(t *testing.T, e *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseDecision, error) {
			e.replace(t)
			return next()
		}, `{"cause":"launch_changed","signal_sent":false}`, 0, 1, []string{"ended launch_end r2"}},
		{"claim ended before the release, agent gone", endBefore(true), "", 0, 1, []string{"ended launch_end r2"}},
		{"claim ended before the release, agent running", endBefore(false), `{"cause":"owned_process_running","signal_sent":false}`, 0, 1,
			[]string{"ended launch_end r2"}},
		{"a standing paused found by the release's Check", func(_ *testing.T, e *kpEnv, _ context.CancelFunc, next next) (store.BusySendPauseDecision, error) {
			_, err := next()
			e.pc.Set(e.r.AgentPID, procfix.Gone())
			return store.BusySendPauseDecision{Kind: store.BusySendPauseDecisionRecorded,
				Claim: &store.BusySendPauseClaim{State: store.BusySendPauseClaimStanding, CallOutcome: store.BusySendPausePaused}}, err
		}, "", 0, 1, []string{released}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			e.step = kpStep // a refusal for a running agent waits its budget out
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var first store.BusySendPlainPauseSignalResult
			st := &pcStore{Store: e.st}
			st.plain = func(n int, signal func() error, next func(func() error) (store.BusySendPlainPauseSignalResult, error)) (store.BusySendPlainPauseSignalResult, error) {
				if n > 1 {
					return next(signal)
				}
				// A conditional caller claimed after the plain Check and was
				// abandoned before its intent.
				e.claimNow(t, time.Second)
				e.advance(2 * time.Second)
				res, err := next(signal)
				first = res
				return res, err
			}
			if tc.recheck != nil {
				st.check = func(n int, next next) (store.BusySendPauseDecision, error) {
					if n != 2 {
						return next()
					}
					return tc.recheck(t, e, cancel, next)
				}
			}

			err := e.pauseOpt(ctx, st, kpBudget, false)

			pcAssertErr(t, err, tc.want)
			if !first.Recheck || first.Invoked {
				t.Errorf("first plain signal result %+v; want Recheck, not invoked", first)
			}
			if len(st.plainReqs) != tc.plains {
				t.Errorf("%d plain signal calls; want %d", len(st.plainReqs), tc.plains)
			}
			for i := 1; i < len(st.plainReqs); i++ {
				if a, b := st.plainReqs[i], st.plainReqs[0]; a.InstanceID != b.InstanceID || a.LaunchID != b.LaunchID ||
					a.LaunchToken != b.LaunchToken || a.OwnerCallID != "" || !a.Deadline.Equal(b.Deadline) {
					t.Errorf("plain signal request again %+v; want the same captured target and deadline as %+v", a, b)
				}
			}
			if strings.Contains(strings.ToLower(errText(err)), "recheck") {
				t.Errorf("description %q names Recheck; want it internal", errText(err))
			}
			e.assertSignals(t, tc.signals)
			e.assertClaims(t, tc.claims...)
			if tc.want == "" {
				// Success means the shutdown finished: Launch End, exclusion and
				// the row's end, never an early answer.
				e.assertEnded(t)
			}
		})
	}
}

// TestPauseClaimPlainSignalResult (SR-6.4): a plain signal, sent under the
// store's write lock, keeps its own fact apart from the store method's outer
// error, which comes first. With an outer error the caller's cancellation is
// its bare error, a budget the call ran out of (ErrStoreBusy or its deadline
// passed) is budget_exhausted with that fact (signal_sent true for a sent
// signal, false for a target gone, unverifiable or unsupported, omitted when
// the signal's result is unknown), and any other outer error is returned
// unchanged; with none, the signal's own refusal or fact answers. A call the
// store did not admit keeps keyless pause's answers. The store method is
// called once and the signal at most once: no outer error or result repeats
// it. Nothing is recorded: no claim, no Launch End. Results are injected at
// the store method around the real one; an invocation not completed is
// fabricated without a call (the store's own unconfirmed return is
// internal/store's TestBusySendPausePlainSignalFacts).
func TestPauseClaimPlainSignalResult(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	gone := func(e *kpEnv, _ context.CancelFunc) func(kpSignal) error {
		return func(s kpSignal) error { e.pc.Set(s.pid, procfix.Gone()); return probe.ErrSignalTargetGone }
	}
	returns := func(err error) func(*kpEnv, context.CancelFunc) func(kpSignal) error {
		return func(*kpEnv, context.CancelFunc) func(kpSignal) error { return func(kpSignal) error { return err } }
	}
	failed := returns(errors.New("operation not permitted"))
	outer := func(err error) func(*kpEnv, context.CancelFunc) error {
		return func(*kpEnv, context.CancelFunc) error { return err }
	}
	cancelled := func(_ *kpEnv, cancel context.CancelFunc) error {
		cancel()
		return fmt.Errorf("store: plain busy-send pause signal: %w", context.Canceled)
	}
	pastDeadline := func(e *kpEnv, _ context.CancelFunc) error { e.advance(time.Hour); return pcInjected }
	changed := fmt.Errorf("store: plain busy-send pause signal: %w", store.ErrBusySendLaunchChanged)
	const budgetSent, budgetNotSent = `{"cause":"budget_exhausted","signal_sent":true}`, `{"cause":"budget_exhausted","signal_sent":false}`
	for _, tc := range []struct {
		name    string
		answer  func(e *kpEnv, cancel context.CancelFunc) func(kpSignal) error // the signal's own return; nil: sent, the agent exits
		call    string                                                         // "" the real method; "unfinished": invoked, not completed; "unadmitted": not invoked
		outer   func(e *kpEnv, cancel context.CancelFunc) error                // nil: the real method's own
		want    string
		signals int
		follow  bool // follow budget_exhausted's advice: get, then retry pause
	}{
		{name: "sent, store busy", outer: outer(pcBusy), want: budgetSent, signals: 1, follow: true},
		{name: "sent, deadline passed", outer: pastDeadline, want: budgetSent, signals: 1},
		{name: "sent, cancelled", outer: cancelled, want: "canceled", signals: 1},
		{name: "sent, cancelled in the signal", answer: func(e *kpEnv, cancel context.CancelFunc) func(kpSignal) error {
			return func(s kpSignal) error { cancel(); e.pc.Set(s.pid, procfix.Gone()); return nil }
		}, want: "canceled", signals: 1},
		{name: "sent, other outer error", outer: outer(pcInjected), want: "injected", signals: 1},
		{name: "target gone, store busy", answer: gone, outer: outer(pcBusy), want: budgetNotSent, signals: 1},
		{name: "unverifiable, store busy", answer: returns(probe.ErrSignalTargetUnverifiable), outer: outer(pcBusy), want: budgetNotSent, signals: 1},
		{name: "unverifiable, deadline passed", answer: returns(probe.ErrSignalTargetUnverifiable), outer: pastDeadline, want: budgetNotSent, signals: 1},
		{name: "unverifiable, cancelled", answer: returns(probe.ErrSignalTargetUnverifiable), outer: cancelled, want: "canceled", signals: 1},
		{name: "unverifiable, other outer error", answer: returns(probe.ErrSignalTargetUnverifiable), outer: outer(pcInjected), want: "injected", signals: 1},
		{name: "unsupported, store busy", answer: returns(probe.ErrProbeUnsupported), outer: outer(pcBusy), want: budgetNotSent, signals: 1},
		{name: "unsupported, other outer error", answer: returns(probe.ErrProbeUnsupported), outer: outer(pcInjected), want: "injected", signals: 1},
		{name: "signal failed, store busy", answer: failed, outer: outer(pcBusy), want: `{"cause":"budget_exhausted"}`, signals: 1},
		{name: "signal failed, other outer error", answer: failed, outer: outer(pcInjected), want: "injected", signals: 1},
		{name: "unfinished, other outer error", call: "unfinished", outer: outer(pcInjected), want: "injected"},
		{name: "unfinished, store busy", call: "unfinished", outer: outer(pcBusy), want: `{"cause":"budget_exhausted"}`},
		{name: "unfinished, cancelled", call: "unfinished", outer: cancelled, want: "canceled"},
		{name: "unfinished, no outer error", call: "unfinished", outer: outer(nil), want: `{"cause":"signal_uncertain"}`},
		{name: "unadmitted, store busy", call: "unadmitted", outer: outer(pcBusy), want: budgetNotSent},
		{name: "unadmitted, Launch changed", call: "unadmitted", outer: outer(changed), want: `{"cause":"launch_changed","signal_sent":false}`},
		{name: "unadmitted, other store error", call: "unadmitted", outer: outer(pcInjected), want: "injected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			held := e.held(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.answer != nil {
				e.answer = tc.answer(e, cancel)
			}
			st := &pcStore{Store: e.st, plain: func(_ int, signal func() error, next func(func() error) (store.BusySendPlainPauseSignalResult, error)) (store.BusySendPlainPauseSignalResult, error) {
				var res store.BusySendPlainPauseSignalResult
				var err error
				switch tc.call {
				case "":
					if res, err = next(signal); !res.Invoked || !res.Completed {
						t.Errorf("real plain signal = %+v, %v; want invoked and completed", res, err)
					}
				case "unfinished":
					res = store.BusySendPlainPauseSignalResult{Invoked: true}
				}
				if tc.outer != nil {
					err = tc.outer(e, cancel)
				}
				return res, err
			}}

			err := e.pauseOpt(ctx, st, kpBudget, false)

			pcAssertErr(t, err, tc.want)
			if len(st.plainReqs) != 1 {
				t.Errorf("%d plain signal calls; want 1, never repeated", len(st.plainReqs))
			}
			e.assertSignals(t, tc.signals)
			e.assertClaims(t)
			if cause, _ := e.launchEnd(t); cause != "" {
				t.Errorf("Launch End = %q; want none recorded", cause)
			}
			e.assertKept(t, held)
			if tc.follow {
				e.followBudgetAdvice(t, err, "inspect with get before more input, and retry pause", true, true, held)
			}
		})
	}
}

// TestPauseClaimOwnerDispositions (SR-6.5; M2 P-C4.6, P-C4.7): the keyless
// return paths whose claim disposition the conditional call changes, as the
// claim then durably reads and as a later pause (a minute on, both option
// values) answers it; the call's own result is keyless pause's
// (TestPauseKeylessOutcomes and the budget, boundary and replacement tests).
// A refusal proven before any signal
// releases the claim (refused, keeping any intent), and a released claim
// never answers: the next conditional pause makes its own. A signal whose
// result is unknown records failed with its cause, replayed later. No intent
// means no paused: an agent gone before any signal is the ended no-op. With
// an intent, paused is recorded at the verified exit, before the Launch End
// that ends the claim. Neither a paused claim whose Launch End failed nor a
// claim ended before an exclusion was refused answers on the live row: a
// retry finishes the shutdown with no signal (Launch End, exclusion, row
// end), refused owned_process_running with nothing signalled while a helper
// runs, ending the row once it exited. Past the budget or after a
// cancellation nothing is recorded: the intent stands, pause_uncertain. A
// Launch replaced before the signal
// ends the claim by its own exit evidence, never by this call; a sweep that
// ends the claim on the agent's exit first leaves the owner's paused
// unrecorded while it still ends the row. A release or outcome write
// that fails before its commit, or commits and loses its acknowledgement,
// is tried once and never changes the call's own result: a later pause
// answers what is durable (the intent's fence, pause_uncertain, or the
// recorded replay), and a released claim answers nothing. Held Messages stay
// Held.
func TestPauseClaimOwnerDispositions(t *testing.T) {
	// Serial: it replaces keyless pause's signal, the exit judgement's pid namespace and the pause knobs (process-wide).
	returns := func(err error) func(*testing.T, *kpEnv) func(kpSignal) error {
		return func(*testing.T, *kpEnv) func(kpSignal) error { return func(kpSignal) error { return err } }
	}
	keepsRunning := returns(nil)
	reused := func(t *testing.T, e *kpEnv) { e.pc.Set(e.r.AgentPID, procfix.Alive("999999")) }
	failEnd := func(_ *testing.T, e *kpEnv, _ context.CancelFunc) api.PauseStore {
		return &pcStore{Store: e.st, end: func(func() (bool, error)) (bool, error) { return false, pcInjected }}
	}
	// failWrite fails the owner's outcome write (record) or release once:
	// before the real write, or, committed, after it (its acknowledgement lost).
	failWrite := func(record, committed bool) func(*testing.T, *kpEnv, context.CancelFunc) api.PauseStore {
		return func(_ *testing.T, e *kpEnv, _ context.CancelFunc) api.PauseStore {
			fail := func(next func() (store.BusySendPauseWriteResult, error)) (store.BusySendPauseWriteResult, error) {
				if committed {
					_, _ = next()
				}
				return store.BusySendPauseWriteResult{}, pcInjected
			}
			if record {
				return &pcStore{Store: e.st, record: fail}
			}
			return &pcStore{Store: e.st, release: fail}
		}
	}
	const paused = "ended launch_end paused r4 intent"
	unverifiable := `{"cause":"agent_process_unverifiable","signal_sent":false}`
	unknown := returns(errors.New("operation not permitted"))
	for _, tc := range []struct {
		name    string
		spec    killRowSpec
		setup   func(t *testing.T, e *kpEnv)
		answer  func(t *testing.T, e *kpEnv) func(kpSignal) error
		store   func(t *testing.T, e *kpEnv, cancel context.CancelFunc) api.PauseStore
		atSleep func(e *kpEnv, cancel context.CancelFunc)
		want    string
		signals int
		claims  []string
		ended   bool           // the row ended, its session kept
		retry   string         // both later pauses' answer; "-": not retried
		replay  bool           // the retry replays a recorded failure
		after   []string       // the claims after the retry, when not claims
		clears  func(e *kpEnv) // then the retry's condition clears, and a pause ends the row
		writes  int            // the owner's release and outcome writes: exactly this many, never retried
		resent  int            // the retry's own signals (a released claim answers nothing)
		rended  bool           // the retry ends the row
	}{
		{name: "agent gone before any signal", setup: reused, claims: []string{"ended launch_end r2"}, ended: true},
		{name: "agent unverifiable before any signal", spec: killRowSpec{Agent: agentUnreadable}, want: unverifiable,
			claims: []string{"released refused_before_signal refused:agent_process_unverifiable r2"}, retry: unverifiable,
			after: []string{"released refused_before_signal refused:agent_process_unverifiable r2",
				"released refused_before_signal refused:agent_process_unverifiable r2"}},
		{name: "agent gone at the signal", answer: func(_ *testing.T, e *kpEnv) func(kpSignal) error {
			return func(s kpSignal) error { e.pc.Set(s.pid, procfix.Gone()); return probe.ErrSignalTargetGone }
		}, signals: 1, claims: []string{paused}, ended: true},
		{name: "unverifiable at the signal", answer: returns(probe.ErrSignalTargetUnverifiable), want: unverifiable, signals: 1,
			claims: []string{"released refused_before_signal refused:agent_process_unverifiable r3 intent"}, retry: "-"},
		{name: "signalling unsupported", answer: returns(probe.ErrProbeUnsupported), want: `{"cause":"signal_unsupported","signal_sent":false}`,
			signals: 1, claims: []string{"released refused_before_signal refused:signal_unsupported r3 intent"}, retry: "-"},
		{name: "signal call failed", answer: returns(errors.New("operation not permitted")), want: `{"cause":"signal_uncertain"}`, signals: 1,
			claims: []string{"standing failed:signal_uncertain r3 intent"}, retry: `{"cause":"signal_uncertain"}`, replay: true},
		{name: "agent ignores SIGTERM", answer: keepsRunning, want: `{"cause":"exit_uncertain","signal_sent":true}`, signals: 1,
			claims: []string{"standing r2 intent"}, retry: `{"cause":"pause_uncertain"}`},
		{name: "cancelled waiting for the agent", answer: keepsRunning, atSleep: func(_ *kpEnv, cancel context.CancelFunc) { cancel() },
			want: "canceled", signals: 1, claims: []string{"standing r2 intent"}, retry: `{"cause":"pause_uncertain"}`},
		{name: "helper still runs at the deadline", setup: func(t *testing.T, e *kpEnv) { e.takeTurn(t, e.issueHelper(t, kpHelper)) },
			want: `{"cause":"owned_process_running","signal_sent":true}`, signals: 1, claims: []string{paused},
			retry: `{"cause":"owned_process_running","signal_sent":false}`, clears: func(e *kpEnv) { e.pc.Set(kpHelper.PID, procfix.Gone()) }},
		{name: "Launch End fails after the verified exit", store: failEnd, want: "injected", signals: 1,
			claims: []string{"standing paused r3 intent"}, after: []string{paused}, rended: true},
		{name: "Launch End fails after the verified exit, a helper still runs", setup: func(t *testing.T, e *kpEnv) { e.takeTurn(t, e.issueHelper(t, kpHelper)) },
			store: failEnd, want: "injected", signals: 1, claims: []string{"standing paused r3 intent"},
			retry: `{"cause":"owned_process_running","signal_sent":false}`, after: []string{paused},
			clears: func(e *kpEnv) { e.pc.Set(kpHelper.PID, procfix.Gone()) }},
		{name: "Launch End fails with no signal needed", setup: reused, store: failEnd, want: "injected",
			claims: []string{"released refused_before_signal refused r2"}},
		{name: "Launch replaced before the signal", store: func(t *testing.T, e *kpEnv, _ context.CancelFunc) api.PauseStore {
			return &pcStore{Store: e.st, reread: func() { e.replace(t) }}
		}, want: `{"cause":"launch_changed","signal_sent":false}`, claims: []string{"ended launch_end r2"}, retry: "-"},
		{name: "budget ends after the row's end", store: func(_ *testing.T, e *kpEnv, _ context.CancelFunc) api.PauseStore {
			return &kpExitHookStore{Store: e.st, afterEnd: func() { e.advance(time.Hour) }}
		}, want: `{"cause":"budget_exhausted","signal_sent":true}`, signals: 1, claims: []string{paused}, ended: true},
		{name: "a sweep ends the claim first", answer: func(t *testing.T, e *kpEnv) func(kpSignal) error {
			return func(s kpSignal) error {
				e.pc.Set(s.pid, procfix.Gone())
				if _, err := e.c.Sweep(context.Background()); err != nil {
					t.Error(err)
				}
				assertReportLookupsOnly(t, e.rec, e.r.Socket) // b.9ps: calls belong to this sweep only.
				e.rec.Reset()                                 // The remaining pause path must still make no tmux call.
				return nil
			}
		}, signals: 1, claims: []string{"ended launch_end r3 intent"}, ended: true},
		{name: "release fails, its intent's fence stands", answer: returns(probe.ErrSignalTargetUnverifiable), store: failWrite(false, false),
			want: unverifiable, signals: 1, claims: []string{"standing r2 intent"}, retry: `{"cause":"pause_uncertain"}`, writes: 1},
		{name: "release committed, its acknowledgement lost", answer: returns(probe.ErrSignalTargetUnverifiable), store: failWrite(false, true),
			want: unverifiable, signals: 1, claims: []string{"released refused_before_signal refused:agent_process_unverifiable r3 intent"},
			retry: unverifiable, resent: 2, writes: 1, after: []string{"released refused_before_signal refused:agent_process_unverifiable r3 intent",
				"released refused_before_signal refused:agent_process_unverifiable r3 intent"}},
		{name: "outcome write fails after an unknown signal", answer: unknown, store: failWrite(true, false), want: `{"cause":"signal_uncertain"}`,
			signals: 1, claims: []string{"standing r2 intent"}, retry: `{"cause":"pause_uncertain"}`, writes: 1},
		{name: "outcome committed, its acknowledgement lost", answer: unknown, store: failWrite(true, true), want: `{"cause":"signal_uncertain"}`,
			signals: 1, claims: []string{"standing failed:signal_uncertain r3 intent"}, retry: `{"cause":"signal_uncertain"}`, replay: true, writes: 1},
		{name: "paused write fails at the verified exit", store: failWrite(true, false), signals: 1,
			claims: []string{"ended launch_end r3 intent"}, ended: true, writes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, tc.spec)
			e.step = kpStep // the deadline refusals wait their budget out
			t.Cleanup(api.SetBusySendExitNamespaceForTest(func() (string, bool) { return "pid:[4026531999]", true }))
			if tc.setup != nil {
				tc.setup(t, e)
			}
			if tc.answer != nil {
				e.answer = tc.answer(t, e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.atSleep != nil {
				e.atSleep = func() { tc.atSleep(e, cancel) }
			}
			var st api.PauseStore
			if tc.store != nil {
				st = tc.store(t, e, cancel)
			}
			held := e.held(t)

			err := e.pauseOpt(ctx, st, kpBudget, true)

			pcAssertErr(t, err, tc.want)
			if len(e.sent) != tc.signals {
				t.Errorf("%d signals; want %d", len(e.sent), tc.signals)
			}
			if ps, ok := st.(*pcStore); ok && tc.writes > 0 && ps.releases+ps.records != tc.writes {
				t.Errorf("%d release and %d outcome writes; want %d in all, never retried", ps.releases, ps.records, tc.writes)
			}
			e.assertClaims(t, tc.claims...)
			e.assertKept(t, held)
			if tc.ended {
				e.assertEnded(t)
			}
			if tc.retry == "-" {
				return
			}
			e.advance(time.Minute)
			signals := len(e.sent)
			for _, only := range []bool{false, true} {
				err := e.pauseOpt(context.Background(), nil, kpBudget, only)
				pcAssertErr(t, err, tc.retry)
				if tc.replay {
					adviceAssertPhrase(t, err, "an earlier pause of this Launch failed (signal_uncertain) after its signal intent")
				}
			}
			if len(e.sent) != signals+tc.resent {
				t.Errorf("%d signals after the later pauses; want %d", len(e.sent), signals+tc.resent)
			}
			after := tc.after
			if after == nil {
				after = tc.claims
			}
			e.assertClaims(t, after...)
			if tc.rended {
				e.assertEnded(t)
			}
			signals = len(e.sent)
			if tc.clears != nil {
				tc.clears(e)
				for _, only := range []bool{false, true} {
					pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, only), "")
				}
				e.assertEnded(t)
				if len(e.sent) != signals {
					t.Errorf("%d signals once cleared; want still %d", len(e.sent), signals)
				}
				e.assertClaims(t, after...)
			}
		})
	}
}

// TestPauseClaimEndedLaunch (SR-6.4, SR-6.5; M2 P-C4.6, P-C4.7): a live row
// whose agent's exit is known (its Launch End recorded, its claim ended on
// exit evidence, or a standing paused claim) is never signalled under a claim
// and gets no claim answer. With no claim, a conditional pause makes none (the
// store claims no ended Launch): its agent gone, it finishes the shutdown and
// ends the row; its agent still running, it waits the budget and refuses
// owned_process_running with nothing signalled; an agent that can no longer be
// judged during that wait refuses agent_process_unverifiable at once, writing
// nothing. A plain pause of that unclaimed row is SR-6.4's, one signal. A
// claim ended with its agent still running (a contradiction the store's own
// Launch End writer makes here) gives either option that refusal, never
// exit_uncertain. A standing paused claim, before the pause or between the
// plain Check and the locked plain signal, is finished the same way: the
// callback is not called, its Launch End is recorded now and the row ends.
// The Recheck's Check is TestPauseClaimPlainRecheck's.
func TestPauseClaimEndedLaunch(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	const running = `{"cause":"owned_process_running","signal_sent":false}`
	const unverifiable = `{"cause":"agent_process_unverifiable","signal_sent":false}`
	for _, tc := range []struct {
		name       string
		claim      bool // a claim is made before the Launch End
		locked     bool // the End is recorded after the plain Check, before the locked signal
		only       bool
		alive      bool // the agent still runs
		unreadable bool // then becomes unreadable at the no-signal wait's first poll
		paused     bool // the claim records paused instead of the Launch End
		want       string
		signals    int
		claims     []string
	}{
		{name: "unclaimed End, conditional, agent gone", only: true},
		{name: "unclaimed End, conditional, agent running", only: true, alive: true, want: running},
		{name: "unclaimed End, plain, agent running", alive: true, signals: 1},
		{name: "ended claim, plain, agent running", claim: true, alive: true, want: running, claims: []string{"ended launch_end r2"}},
		{name: "ended claim, conditional, agent running", claim: true, only: true, alive: true, want: running, claims: []string{"ended launch_end r2"}},
		{name: "claim ended at the plain signal, agent gone", claim: true, locked: true, claims: []string{"ended launch_end r2"}},
		{name: "claim ended at the plain signal, agent running", claim: true, locked: true, alive: true, want: running,
			claims: []string{"ended launch_end r2"}},
		{name: "unclaimed End, conditional, agent unreadable while waited for", only: true, alive: true, unreadable: true, want: unverifiable},
		{name: "ended claim, plain, agent unreadable while waited for", claim: true, alive: true, unreadable: true, want: unverifiable,
			claims: []string{"ended launch_end r2"}},
		{name: "standing paused, conditional, agent gone", claim: true, paused: true, only: true, claims: []string{"ended launch_end paused r4 intent"}},
		{name: "paused at the plain signal, agent gone", claim: true, paused: true, locked: true, claims: []string{"ended launch_end paused r4 intent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKeylessEnv(t, killRowSpec{})
			e.step = kpStep // a refusal for a running agent waits its budget out
			held := e.held(t)
			end := func() {
				var ref store.BusySendPauseRef
				if tc.claim {
					ref = e.claimNow(t, 15*time.Second)
				}
				if !tc.alive {
					e.pc.Set(e.r.AgentPID, procfix.Gone())
				}
				if tc.paused {
					e.recordNow(t, e.intentNow(t, ref), store.BusySendPausePaused, "")
					return
				}
				if ended, err := e.st.EndBusySendLaunchWithin(context.Background(), e.r.ID, e.r.Token, time.Minute); err != nil || !ended {
					t.Fatalf("Launch End = %t, %v; want recorded", ended, err)
				}
			}
			var plainRes *store.BusySendPlainPauseSignalResult
			st := &pcStore{Store: e.st}
			if tc.locked {
				st.plain = func(_ int, signal func() error, next func(func() error) (store.BusySendPlainPauseSignalResult, error)) (store.BusySendPlainPauseSignalResult, error) {
					end()
					res, err := next(signal)
					plainRes = &res
					return res, err
				}
			} else {
				end()
			}
			if tc.unreadable {
				e.atSleep = func() { e.pc.Set(e.r.AgentPID, procfix.Unreadable()) }
			}
			facts, cols := busyFacts(t, e.dbPath, e.r.ID), e.columns(t, e.r.ID)

			err := e.pauseOpt(context.Background(), st, kpBudget, tc.only)

			pcAssertErr(t, err, tc.want)
			if tc.want == running {
				adviceAssertPhrase(t, err, "its Launch has ended, so this pause sends it no signal")
			}
			if waited := time.Duration(e.skew.Load()); tc.unreadable && waited >= kpBudget*time.Second/2 {
				t.Errorf("refused after %v of the %d s budget; want at once, at the first judgement it cannot make", waited, kpBudget)
			}
			if tc.want != "" && !tc.locked {
				e.assertRowUnchanged(t, e.r.ID, cols)
				if after := busyFacts(t, e.dbPath, e.r.ID); !reflect.DeepEqual(after, facts) {
					t.Errorf("delivery facts after the refusal = %+v; want unchanged %+v", after, facts)
				}
			}
			if tc.locked && (plainRes == nil || plainRes.Invoked) {
				t.Errorf("locked plain signal result %+v; want it reached, its callback never called", plainRes)
			}
			e.assertSignals(t, tc.signals)
			e.assertClaims(t, tc.claims...)
			e.assertKept(t, held)
			if tc.want == "" {
				e.assertEnded(t)
			} else if row, err := e.st.GetSpawn(e.r.ID); err != nil || !store.IsLiveState(row.State) {
				t.Errorf("row after the refusal = %s, %v; want still live", row.State, err)
			}
		})
	}
}

// newTokenlessEnv is a kpEnv over apitest.OpenStoreWithRow's row: a covered
// Claude row InsertPending made with no launch identity, so no launch token,
// moved to state. Its signal hook only records, and it has no Held Messages.
func newTokenlessEnv(t *testing.T, state string) *kpEnv {
	t.Helper()
	e := &kpEnv{killEnv: newKillEnv(t)}
	id := "kp-" + uuid.NewString()[:8]
	e.st, e.dbPath = apitest.OpenStoreWithRow(t, id, "kp-"+id, state, "off")
	e.r = killRow{ID: id}
	e.c, _ = e.client(t)
	t.Cleanup(api.SetPauseTerminateForTest(func(pid int, start string) error {
		e.sent = append(e.sent, kpSignal{pid, start})
		return nil
	}))
	t.Cleanup(api.SetPauseNowForTest(e.now))
	return e
}

// pcSetToken sets id's row's launch token raw ("" clears it), as a damaged
// store would leave it, leaving its published Launch as it is.
func pcSetToken(t *testing.T, path, id, token string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var value any
	if token != "" {
		value = token
	}
	if _, err := raw.Exec(`UPDATE spawns SET launch_token = ? WHERE instance_id = ?`, value, id); err != nil {
		t.Fatalf("set %s's launch token: %v", id, err)
	}
}

// TestPauseClaimFinishedRows (SR-6.4, SR-6.5): a covered finished row that no
// claim can answer for, because the row holds no launch token, its published
// Launch is not its own covered one (another token, or recorded uncovered
// between the row read and the Launch read) or it has no consistent binding,
// is the no-op success for either option, through api.Pause and Client.Pause:
// no signal, no claim, nothing written. A tokenless row returns before the
// budget and cancellation checks, so a cancellation or spent budget during its
// first row read still gives the no-op (a context already cancelled at entry
// is still its bare error). A live row with no token or another token is still
// launch_changed with nothing signalled. A finished row holding its published
// covered Launch's token reads its claim (TestPauseClaimStandingAnswers).
func TestPauseClaimFinishedRows(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	const other = "0123456789abcdef"
	launchChanged := `{"cause":"launch_changed","signal_sent":false}`
	keyed := func(state string, damage func(t *testing.T, e *kpEnv)) func(t *testing.T) *kpEnv {
		return func(t *testing.T) *kpEnv {
			e := newKeylessEnv(t, killRowSpec{State: state})
			if damage != nil {
				damage(t, e)
			}
			return e
		}
	}
	tokenless := func(state string) func(t *testing.T) *kpEnv {
		return func(t *testing.T) *kpEnv { return newTokenlessEnv(t, state) }
	}
	otherToken := func(t *testing.T, e *kpEnv) { pcSetToken(t, e.dbPath, e.r.ID, other) }
	for _, tc := range []struct {
		name string
		env  func(t *testing.T) *kpEnv
		// at interposes on the call through api.Pause only: "cancel" or
		// "spend" during the first row read, "uncovered" just before the
		// published Launch read.
		at   string
		want string
	}{
		{name: "ended, no launch token", env: tokenless(store.StateEnded)},
		{name: "missing, no launch token", env: tokenless(store.StateMissing)},
		{name: "ended, no launch token, cancelled during its first read", env: tokenless(store.StateEnded), at: "cancel"},
		{name: "ended, no launch token, budget spent during its first read", env: tokenless(store.StateEnded), at: "spend"},
		{name: "ended, another launch token", env: keyed(store.StateEnded, otherToken)},
		{name: "ended, its published Launch recorded uncovered", env: keyed(store.StateEnded, nil), at: "uncovered"},
		{name: "ended, no consistent binding", env: keyed(store.StateEnded, func(t *testing.T, e *kpEnv) { storefix.UnbindBusySend(t, e.dbPath, e.r.ID) })},
		{name: "waiting, no launch token", env: tokenless(store.StateWaiting), want: launchChanged},
		{name: "waiting, another launch token", env: keyed(store.StateWaiting, otherToken), want: launchChanged},
	} {
		for _, only := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/only_if_waiting=%t", tc.name, only), func(t *testing.T) {
				e := tc.env(t)
				type snapshot struct {
					cols  apitest.SpawnColumns
					facts apitest.BusySendFacts
				}
				snap := func() snapshot { return snapshot{e.columns(t, e.r.ID), busyFacts(t, e.dbPath, e.r.ID)} }
				before := snap()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var st api.PauseStore
				switch tc.at {
				case "cancel":
					st = &kpHookedStore{Store: e.st, first: cancel}
				case "spend":
					st = &kpHookedStore{Store: e.st, first: func() { e.advance(time.Hour) }}
				case "uncovered":
					st = &pcStore{Store: e.st, published: func() {
						if err := apitest.SeedBusySendCoverage(e.dbPath, e.r.ID, store.BusySendNotCovered, store.BusySendCovered); err != nil {
							t.Fatal(err)
						}
						before = snap()
					}}
				}

				errs := map[string]error{"api.Pause": e.pauseOpt(ctx, st, kpBudget, only)}
				if tc.at == "" {
					_, errs["Client.Pause"] = e.c.Pause(context.Background(), api.PauseParams{InstanceID: e.r.ID, OnlyIfWaiting: only})
				}

				for entry, err := range errs {
					t.Run(entry, func(t *testing.T) { pcAssertErr(t, err, tc.want) })
				}
				e.assertSignals(t, 0)
				e.assertClaims(t)
				if after := snap(); !reflect.DeepEqual(after, before) {
					t.Errorf("row and delivery facts after pause = %+v; want unchanged %+v", after, before)
				}
			})
		}
	}
}

// TestPauseClaimKillResume (SR-6.5; M2 P-C4.7): a claim a pause left
// standing (its signal's result unknown, failed recorded) stays, with its
// recorded answer, through a refused resume, a kill whose agent did not exit
// and, once the agent's SessionEnd finished the row (no exit evidence), a
// kill of the finished row; each later pause gets that answer and sends
// nothing. A kill with exit evidence ends it (launch_end), after which pause
// finishes the row's shutdown with no claim and no signal, or is the finished
// row's no-op; find-missing then marks the row and resume publishes a new
// Launch, whose conditional pause judges only its own raw state, refusing
// not_waiting while pending and claiming and pausing its own agent once
// waiting, with no answer from the old Launch's claim.
func TestPauseClaimKillResume(t *testing.T) {
	// Serial: it replaces keyless pause's signal, clock and poll hooks (process-wide).
	const standing = "standing failed:signal_uncertain r3 intent"
	setup := func(t *testing.T) *kpEnv {
		e := newKeylessEnv(t, killRowSpec{CWD: t.TempDir()})
		e.answer = func(kpSignal) error { return errors.New("operation not permitted") }
		pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, true), `{"cause":"signal_uncertain"}`)
		e.assertClaims(t, standing)
		e.answer = func(kpSignal) error { t.Error("a pause the claim answered signalled"); return nil }
		return e
	}
	answered := func(t *testing.T, e *kpEnv, after string) {
		t.Helper()
		for _, only := range []bool{false, true} {
			err := e.pauseOpt(context.Background(), nil, kpBudget, only)
			pcAssertErr(t, err, `{"cause":"signal_uncertain"}`)
			adviceAssertPhrase(t, err, "an earlier pause of this Launch failed (signal_uncertain)")
		}
		e.assertSignals(t, 1)
		e.assertClaims(t, standing)
		if n := storefix.ReadBusySendCounts(t, e.dbPath).InputAttempts; n != 0 {
			t.Errorf("%d input journal attempts after %s; want none", n, after)
		}
	}
	t.Run("kept by refusals", func(t *testing.T) {
		e := setup(t)
		answered(t, e, "the failed signal")

		_, err := e.resume(e.r.ID)
		assertOneSentinel(t, err, api.ErrSpawnNotResumable)
		answered(t, e, "a refused resume")

		_, err = e.c.Kill(api.KillParams{InstanceID: e.r.ID})
		assertOneName(t, err, "ErrTmuxKillFailed")
		answered(t, e, "a kill whose agent did not exit")

		e.endBySessionEnd(t, e.r)
		answered(t, e, "the agent's SessionEnd")
		if _, err := e.c.Kill(api.KillParams{InstanceID: e.r.ID}); err != nil {
			t.Fatalf("kill of the finished row: %v; want the no-op", err)
		}
		answered(t, e, "a kill of the finished row")
	})
	t.Run("ended by kill, then a new Launch", func(t *testing.T) {
		e := setup(t)
		e.setAfterCall(tmux.CallKillPane, procfix.Gone(), e.r.AgentPID)
		if res, err := e.c.Kill(api.KillParams{InstanceID: e.r.ID}); err != nil || !res.KillSent {
			t.Fatalf("kill = %+v, %v; want the agent's pane killed", res, err)
		}
		const ended = "ended launch_end failed:signal_uncertain r4 intent"
		e.assertClaims(t, ended)
		for _, only := range []bool{false, true} {
			pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, only), "")
		}
		e.assertSignals(t, 1)

		if _, err := e.c.FindMissing(context.Background()); err != nil {
			t.Fatalf("find-missing: %v", err)
		}
		e.agentOnCreate(e.r.ID, agentAlive)
		if _, err := e.resume(e.r.ID); err != nil {
			t.Fatalf("resume: %v", err)
		}
		pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, true), `{"cause":"not_waiting","observed_state":"pending"}`)
		e.assertClaims(t, ended)

		row, err := e.st.GetSpawn(e.r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if a := apitest.ApplyAgentHook(t, e.dbPath, e.r.ID, "SessionStart", e.r.Spawn.SessionID); !a.Applied {
			t.Fatalf("new Launch's SessionStart not applied: %s", a.Reason)
		}
		if e.columns(t, e.r.ID).State != store.StateWaiting {
			if a := apitest.ApplyAgentHook(t, e.dbPath, e.r.ID, "Stop", e.r.Spawn.SessionID); !a.Applied {
				t.Fatalf("new Launch's Stop not applied: %s", a.Reason)
			}
		}
		if got := e.columns(t, e.r.ID).State; got != store.StateWaiting {
			t.Fatalf("new Launch's row state = %s; want waiting", got)
		}
		e.answer = nil
		pcAssertErr(t, e.pauseOpt(context.Background(), nil, kpBudget, true), "")
		if len(e.sent) != 2 || e.sent[1].pid != row.Identity.PanePID {
			t.Errorf("signals = %+v; want the old agent's, then the new pane's agent %d", e.sent, row.Identity.PanePID)
		}
		e.assertClaims(t, ended, "ended launch_end paused r4 intent")
	})
}
