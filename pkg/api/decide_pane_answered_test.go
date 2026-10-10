package api_test

// decide_pane_answered_test.go — decide on a request closed at the pane (b.146
// step 2b: pane_answer sent, outside or tool_ran): ErrAlreadyDecided naming its
// pane_answer, nothing recorded (not even as attempted), no pane advice and no
// wait, on each path decide reads the request on: its first read, a finished
// Spawn's, and the re-read after a verdict write that matched nothing (a
// request from v7 on, and one from before v7, which then does not wait for its
// relay hook's settle instant). Also fallenBackUnshown's "open too" rule
// (PermissionRow.AwaitsAnswer). Seeds: relay_delivery_test.go's relayEnv.

import (
	"fmt"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// paneClose is one way request A of e is closed at the pane, with the
// pane_answer and decision it leaves; v7Only marks one a request from before
// v7 cannot get (the PostToolUse close needs a tool_use_id).
type paneClose struct {
	name, paneAnswer, decision string
	v7Only                     bool
	close                      func(t *testing.T, e *relayEnv)
}

// paneOutside records request A of e answered outside agent-director with claim.
func paneOutside(claim string) func(t *testing.T, e *relayEnv) {
	return func(t *testing.T, e *relayEnv) {
		t.Helper()
		if ok, err := e.s.RecordPaneOutside(e.id, storefix.TestRequestTokenA, claim, store.DefaultLockWait, nil); err != nil || !ok {
			t.Fatalf("RecordPaneOutside(%s) = %v, %v", claim, ok, err)
		}
	}
}

// paneCloses are the completed pane answers on request A.
var paneCloses = []paneClose{
	{name: "a pane answer sent", paneAnswer: store.PaneAnswerSent, decision: "deny", close: func(t *testing.T, e *relayEnv) {
		t.Helper()
		intent, ok, err := e.s.RecordPaneIntent(e.id, storefix.TestRequestTokenA, "deny", store.ProcessIdentity{}, nil, store.DefaultLockWait, nil)
		if err == nil && ok {
			ok, err = e.s.RecordPaneSent(e.id, storefix.TestRequestTokenA, intent, store.DefaultLockWait)
		}
		if err != nil || !ok {
			t.Fatalf("pane answer = %v, %v", ok, err)
		}
	}},
	{name: "answered outside, allow", paneAnswer: store.PaneAnswerOutside, decision: "allow", close: paneOutside("allow")},
	{name: "answered outside, deny", paneAnswer: store.PaneAnswerOutside, decision: "deny", close: paneOutside("deny")},
	{name: "answered outside, unknown", paneAnswer: store.PaneAnswerOutside, close: paneOutside("unknown")},
	{name: "its tool ran", paneAnswer: store.PaneAnswerToolRan, decision: "allow", v7Only: true, close: func(t *testing.T, e *relayEnv) {
		t.Helper()
		if err := storefix.WithSeedPane(e.dbPath, e.id, func(gate store.HookGate) error {
			closed, err := e.s.CloseToolRanRequests(e.id, gate, "toolu_01", e.now, func(store.PermissionRow) bool { return true })
			if err == nil && len(closed) != 1 {
				err = fmt.Errorf("closed %v; want A", closed)
			}
			return err
		}); err != nil {
			t.Fatalf("tool-ran close: %v", err)
		}
	}},
}

// newLegacyRelayEnv is a relayEnv whose request A was recorded before schema
// v7 (no relay hook identity, no settle instant), created age ago; others
// are recorded first (older), from before v7 too.
func newLegacyRelayEnv(t *testing.T, age time.Duration, others ...string) *relayEnv {
	t.Helper()
	s, dbPath := apitest.SeedDecideFixture(t, "on")
	if len(others) > 0 {
		storefix.SeedOpenPermissionRequests(t, s, "id-d-1", others)
	}
	apitest.SeedPermissionRow(t, s, "id-d-1")
	if age > 0 {
		storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-d-1", storefix.TestRequestTokenA, age)
	}
	return &relayEnv{s: s, dbPath: dbPath, id: "id-d-1", pc: procfix.New(), ns: relayNS, now: time.Now().UTC().Truncate(time.Millisecond)}
}

// assertPaneAnsweredRefusal fails unless err is decide's ErrAlreadyDecided for
// a request closed at the pane with paneAnswer, advising no pane answer.
func assertPaneAnsweredRefusal(t *testing.T, err error, paneAnswer string) {
	t.Helper()
	assertOneSentinel(t, err, store.ErrAlreadyDecided)
	adviceAssertPhrase(t, err, fmt.Sprintf("already has a pane answer recorded (pane_answer %q); nothing was recorded", paneAnswer))
	assertNoPaneAdvice(t, err)
}

// assertNothingAttempted fails unless request A of e reads closed at the pane
// with paneAnswer and decision, and no attempt stored.
func assertNothingAttempted(t *testing.T, e *relayEnv, paneAnswer, decision string) {
	t.Helper()
	if pr := e.request(t); pr.PaneAnswer != paneAnswer || pr.Decision != decision || pr.AttemptedDecision != "" || !pr.AttemptedAt.IsZero() {
		t.Errorf("request A = pane_answer %q, decision %q, attempted %q at %v; want %q, %q, nothing attempted",
			pr.PaneAnswer, pr.Decision, pr.AttemptedDecision, pr.AttemptedAt, paneAnswer, decision)
	}
}

// TestDecideRefusesARequestAnsweredAtThePane (b.146 step 2b): decide on a
// request closed at the pane, from v7 on (its relay hook gone) or before, its
// Spawn live or ended since, is ErrAlreadyDecided naming the pane_answer, at
// once, never ErrRelayFallenBack; the request keeps its pane answer and gets
// no attempt.
func TestDecideRefusesARequestAnsweredAtThePane(t *testing.T) {
	t.Parallel()
	for _, pc := range paneCloses {
		for _, legacy := range []bool{false, true} {
			for _, ended := range []bool{false, true} {
				if legacy && pc.v7Only {
					continue
				}
				t.Run(fmt.Sprintf("%s/before v7 %v/the spawn ended %v", pc.name, legacy, ended), func(t *testing.T) {
					t.Parallel()
					var e *relayEnv
					if legacy {
						e = newLegacyRelayEnv(t, 0)
					} else {
						e = newRelayEnv(t, relayEnvHook)
						closeGone(t, e)
					}
					pc.close(t, e)
					if ended {
						closeEnd(t, e)
					}

					_, waited, err := e.decideWith(t, e.s, "allow", nil, nil)

					assertPaneAnsweredRefusal(t, err, pc.paneAnswer)
					if waited != 0 {
						t.Errorf("decide waited %v; want the refusal at once", waited)
					}
					assertNothingAttempted(t, e, pc.paneAnswer, pc.decision)
				})
			}
		}
	}
}

// paneRaceStore is a DecideStore on which request A is closed at the pane
// (close) just before decide's verdict write, once.
type paneRaceStore struct {
	*store.Store
	close func()
}

// DecideRelayRequest runs close once, then delegates.
func (r *paneRaceStore) DecideRelayRequest(id, token, decision, reason, writer string, cutoff time.Time, maxWait time.Duration) (bool, error) {
	if r.close != nil {
		r.close()
		r.close = nil
	}
	return r.Store.DecideRelayRequest(id, token, decision, reason, writer, cutoff, maxWait)
}

// TestDecideRefusesAPaneAnswerRecordedMeanwhile (b.146 step 2b): a request
// read open whose answer outside agent-director is recorded before decide's
// verdict write is ErrAlreadyDecided naming pane_answer outside, nothing
// recorded: one from v7 on (its relay hook exits meanwhile), and one from
// before v7 inside its relay hook's settle wait, which decide no longer
// sleeps out.
func TestDecideRefusesAPaneAnswerRecordedMeanwhile(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("before v7 %v", legacy), func(t *testing.T) {
			t.Parallel()
			var e *relayEnv
			if legacy {
				// Past its deliverability cutoff, 1 to 2 s before its relay hook settles.
				e = newLegacyRelayEnv(t, time.Hour)
			} else {
				e = newRelayEnv(t, relayEnvHook)
			}
			race := &paneRaceStore{Store: e.s, close: func() {
				closeGone(t, e)
				paneOutside("unknown")(t, e)
			}}

			_, slept, err := e.decideWith(t, race, "allow", nil, nil)

			assertPaneAnsweredRefusal(t, err, store.PaneAnswerOutside)
			if race.close != nil {
				t.Fatal("decide made no verdict write; want the race to reach it")
			}
			if slept != 0 {
				t.Errorf("decide slept %v; want the refusal at once", slept)
			}
			assertNothingAttempted(t, e, store.PaneAnswerOutside, "")
		})
	}
}

// TestDecidePreV7FallenBackOpenTooAwaitsAnAnswer (b.t6e; b.146 rule 9):
// decide's ErrRelayFallenBack on a request from before v7 that fell back by
// time stands while no other request of the Spawn awaits an answer: an older
// one answered outside agent-director with unknown (no decision) leaves it
// alone; an older one from v7 on decided but not acked is open too, so the
// refusal is ErrNoOpenPermissionRequest, with no pane advice.
func TestDecidePreV7FallenBackOpenTooAwaitsAnAnswer(t *testing.T) {
	t.Parallel()
	tokB := storefix.TestRequestTokenB
	t.Run("an older request answered outside, unknown", func(t *testing.T) {
		t.Parallel()
		e := newLegacyRelayEnv(t, 2*time.Hour, tokB)
		if ok, err := e.s.RecordPaneOutside(e.id, tokB, "unknown", store.DefaultLockWait, nil); err != nil || !ok {
			t.Fatalf("RecordPaneOutside(B) = %v, %v", ok, err)
		}

		_, _, err := e.decideWith(t, e.s, "allow", nil, nil)

		assertOneSentinel(t, err, api.ErrRelayFallenBack)
		if d := assertFallenBackDetails(t, err, storefix.TestRequestTokenA, store.StateCheckPermission); len(d["open_requests"].([]any)) != 0 {
			t.Errorf("open_requests = %v; want none (B is closed at the pane)", d["open_requests"])
		}
	})
	t.Run("an older request from v7 on, decided, not acked", func(t *testing.T) {
		t.Parallel()
		s, dbPath := apitest.SeedDecideFixture(t, "on")
		storefix.SeedRelayRequest(t, s, "id-d-1", store.RelayRequest{RequestToken: tokB, ToolName: "Bash", ToolInput: `{"cmd":"pwd"}`,
			ToolUseID: "toolu_02", Hook: relayEnvHook, SettledAt: time.Now().Add(time.Hour)})
		if ok, err := s.DecideRelayRequest("id-d-1", tokB, "allow", "", store.WriterProcessDecide, time.Time{}, store.DefaultLockWait); err != nil || !ok {
			t.Fatalf("decide B = %v, %v", ok, err)
		}
		apitest.SeedPermissionRow(t, s, "id-d-1")
		storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-d-1", storefix.TestRequestTokenA, 2*time.Hour)
		e := &relayEnv{s: s, dbPath: dbPath, id: "id-d-1", pc: procfix.New(), ns: relayNS, now: time.Now().UTC().Truncate(time.Millisecond)}

		_, _, err := e.decideWith(t, e.s, "allow", nil, nil)

		adviceAssertAdvice(t, err, store.ErrNoOpenPermissionRequest, "the spawn's request "+tokB+" is open too")
		assertNoPaneAdvice(t, err)
	})
}
