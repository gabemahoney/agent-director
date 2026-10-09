package api_test

// relay_closed_test.go — a request find-missing's mark closed (b.146 rule 12,
// closed_at): decide refuses it as closed, never as fallen back, while its
// Spawn is missing and after a resume, recording nothing; get-permission
// derives its delivery by the ordinary rules; get and list no longer show it.
// Seeds: relay_delivery_test.go's relayEnv, its hook alive until closeGone.

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

// closeStep is one step of request A's history on e.
type closeStep func(t *testing.T, e *relayEnv)

var (
	// closeDecide records decide's allow on request A.
	closeDecide closeStep = func(t *testing.T, e *relayEnv) {
		if ok, err := e.s.DecideRelayRequest(e.id, storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide,
			time.Time{}, store.DefaultLockWait); err != nil || !ok {
			t.Fatalf("decide = %v, %v", ok, err)
		}
	}
	// closeMark is find-missing's mark of e's row.
	closeMark closeStep = func(t *testing.T, e *relayEnv) {
		sp, err := e.s.GetSpawn(e.id)
		if err != nil {
			t.Fatalf("GetSpawn: %v", err)
		}
		if _, res, err := e.s.MarkMissingIfSameLife(e.id, sp.Snapshot); err != nil || res != store.CondApplied {
			t.Fatalf("mark = %v, %v", res, err)
		}
	}
	// closeAck is request A's relay hook acking its verdict.
	closeAck closeStep = func(t *testing.T, e *relayEnv) {
		if _, _, ok, err := e.s.AckRelayDecision(e.id, storefix.TestRequestTokenA, e.now, store.DefaultLockWait, nil); err != nil || !ok {
			t.Fatalf("ack = %v, %v", ok, err)
		}
	}
	// closeResume resumes e's row.
	closeResume closeStep = func(t *testing.T, e *relayEnv) { resumeFinishedRow(t, e.s, e.dbPath, e.id) }
	// closeGone: request A's relay hook exits.
	closeGone closeStep = func(_ *testing.T, e *relayEnv) { e.pc.Set(relayEnvHook.PID, procfix.Gone()) }
)

// resumeFinishedRow is resume of id's finished row as the store sees it: the
// move to pending for a new launch, then the new agent's first hook, to
// waiting.
func resumeFinishedRow(t *testing.T, s *store.Store, dbPath, id string) {
	t.Helper()
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if res, _, err := s.MoveToPending(id, sp.Snapshot, time.Now().UnixMilli(), newToken(), apitest.TestSocket, "", store.LaunchOwner{}); err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending = %v, %v; want applied", res, err)
	}
	if err := seedAgentState(s, dbPath, id, store.StateWaiting); err != nil {
		t.Fatalf("the resumed agent's first hook: %v", err)
	}
}

// TestDecideRefusesAClosedRequest (b.146 rule 12): decide on a request the
// mark closed records nothing, bounded or not, and never reports it fallen
// back, its hook gone though it is: closed before its hook acked the verdict
// recorded on it, it is ErrNoOpenPermissionRequest, the Spawn missing or
// resumed; the mark's own deny, and a verdict acked after the mark, are
// ErrAlreadyDecided.
func TestDecideRefusesAClosedRequest(t *testing.T) {
	t.Parallel()
	const closedText = "find-missing marked the spawn missing before the request's relay hook delivered a verdict, so nothing was recorded; do not answer it at the pane"
	cases := []struct {
		name             string
		steps            []closeStep
		want             error
		phrase           string
		decision, reason string // request A's, kept
	}{
		{"decided, not acked, the spawn missing", []closeStep{closeDecide, closeMark}, store.ErrNoOpenPermissionRequest, closedText, "allow", ""},
		{"decided, not acked, the spawn resumed", []closeStep{closeDecide, closeMark, closeResume}, store.ErrNoOpenPermissionRequest, closedText, "allow", ""},
		{"the mark's deny, the spawn missing", []closeStep{closeMark}, store.ErrAlreadyDecided, `(decision_reason "find_missing")`, "deny", store.DecisionReasonFindMissing},
		{"the mark's deny, the spawn resumed", []closeStep{closeMark, closeResume}, store.ErrAlreadyDecided, `(decision_reason "find_missing")`, "deny", store.DecisionReasonFindMissing},
		{"decided, acked after the mark, the spawn resumed", []closeStep{closeDecide, closeMark, closeAck, closeResume}, store.ErrAlreadyDecided, `already decided as "allow"`, "allow", ""},
	}
	for _, tc := range cases {
		for _, bound := range []*int64{nil, ptr(int64(1000))} {
			t.Run(fmt.Sprintf("%s/max_wait_ms %v", tc.name, bound != nil), func(t *testing.T) {
				t.Parallel()
				e := newRelayEnv(t, relayEnvHook)
				for _, step := range tc.steps {
					step(t, e)
				}
				closeGone(t, e)

				_, waited, err := e.decideWith(t, e.s, "deny", bound, nil)

				assertOneSentinel(t, err, tc.want)
				adviceAssertPhrase(t, err, tc.phrase)
				assertNoPaneAdvice(t, err)
				if waited != 0 {
					t.Errorf("decide waited %v; want the refusal at once", waited)
				}
				pr := e.request(t)
				if pr.Decision != tc.decision || pr.DecisionReason != tc.reason || pr.AttemptedDecision != "" || !pr.Closed() {
					t.Errorf("request = decision %q (%q), attempted %q, closed %v; want %q (%q) kept, nothing attempted, closed",
						pr.Decision, pr.DecisionReason, pr.AttemptedDecision, pr.Closed(), tc.decision, tc.reason)
				}
			})
		}
	}
}

// TestClosedRequestDelivery (b.146 rules 5, 12, 15): a closed request's
// delivery follows the ordinary rules: not_confirmed while its hook may still
// ack, fallen_back once the hook is gone without an ack (hook_gone_at
// recorded), delivered once acked. Get and list no longer show it, the Spawn
// missing or resumed.
func TestClosedRequestDelivery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		steps []closeStep
		want  string
		alive string
	}{
		{"decided, not acked, its hook alive", []closeStep{closeDecide, closeMark}, api.DeliveryNotConfirmed, "true"},
		{"decided, not acked, its hook gone, the spawn resumed", []closeStep{closeDecide, closeMark, closeResume, closeGone}, api.DeliveryFallenBack, "false"},
		{"the mark's deny, its hook gone", []closeStep{closeMark, closeGone}, api.DeliveryFallenBack, "false"},
		{"acked after the mark, its hook gone since, the spawn resumed", []closeStep{closeDecide, closeMark, closeAck, closeResume, closeGone}, api.DeliveryDelivered, "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newRelayEnv(t, relayEnvHook)
			for _, step := range tc.steps {
				step(t, e)
			}

			res := e.getPermission(t, e.s)
			fromGet, fromList := e.listed(t)

			if res.Delivery != tc.want || aliveIs(res.HookAlive) != tc.alive || !res.ConfirmBy.Equal(e.settled) {
				t.Errorf("get-permission = delivery %q, hook_alive %s, confirm_by %v; want %q, %s, %v",
					res.Delivery, aliveIs(res.HookAlive), res.ConfirmBy, tc.want, tc.alive, e.settled)
			}
			if gone := res.HookGoneAt != nil; gone != (tc.want == api.DeliveryFallenBack) {
				t.Errorf("hook_gone_at = %v; want it recorded only when fallen back", res.HookGoneAt)
			}
			if fromGet != nil || fromList != nil {
				t.Errorf("get / list show %+v / %+v; want no request (a closed one no longer awaits an answer)", fromGet, fromList)
			}
		})
	}
}
