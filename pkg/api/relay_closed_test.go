package api_test

// relay_closed_test.go — a request closed with its Spawn (b.146 rule 12,
// closed_at; b.59i, b.6qr) by find-missing's mark or by the Spawn's terminal
// SessionEnd: decide refuses it as closed, never as fallen back, while its
// Spawn is finished and after a resume, recording nothing; get-permission
// derives its delivery by the ordinary rules; get and list no longer show it,
// and it holds nothing for the resumed agent. Seeds: relay_delivery_test.go's
// relayEnv, its hook alive until closeGone.

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
	// closeEnd is the terminal SessionEnd of e's agent: the row ends.
	closeEnd closeStep = func(t *testing.T, e *relayEnv) {
		if err := seedAgentState(e.s, e.dbPath, e.id, store.StateEnded); err != nil {
			t.Fatalf("SessionEnd: %v", err)
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

// TestDecideRefusesAClosedRequest (b.146 rule 12; b.59i, b.6qr): decide on a
// request a close closed records nothing, bounded or not, and never reports it
// fallen back, its hook gone though it is: closed before its hook acked a
// verdict find-missing did not write (one recorded on it, or the SessionEnd
// close's deny), it is ErrNoOpenPermissionRequest, the Spawn finished or
// resumed; the mark's own deny, and a verdict acked after the mark, are
// ErrAlreadyDecided.
func TestDecideRefusesAClosedRequest(t *testing.T) {
	t.Parallel()
	const closedText = "its spawn ended, or find-missing marked it missing, before the request's relay hook delivered a verdict, so nothing was recorded; do not answer it at the pane"
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
		{"decided, not acked, the spawn ended", []closeStep{closeDecide, closeEnd}, store.ErrNoOpenPermissionRequest, closedText, "allow", ""},
		{"decided, not acked, the spawn ended and resumed", []closeStep{closeDecide, closeEnd, closeResume}, store.ErrNoOpenPermissionRequest, closedText, "allow", ""},
		{"the SessionEnd's deny, the spawn ended", []closeStep{closeEnd}, store.ErrNoOpenPermissionRequest, closedText, "deny", store.DecisionReasonEnded},
		{"the SessionEnd's deny, the spawn ended and resumed", []closeStep{closeEnd, closeResume}, store.ErrNoOpenPermissionRequest, closedText, "deny", store.DecisionReasonEnded},
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

// TestClosedRequestDelivery (b.146 rules 5, 12, 15; b.59i): a closed
// request's delivery follows the ordinary rules: not_confirmed while its hook
// may still ack, fallen_back once the hook is gone without an ack
// (hook_gone_at recorded), delivered once acked. Get and list no longer show
// it, the Spawn finished or resumed.
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
		{"decided, not acked, the spawn ended, its hook alive", []closeStep{closeDecide, closeEnd}, api.DeliveryNotConfirmed, "true"},
		{"the SessionEnd's deny, its hook gone", []closeStep{closeEnd, closeGone}, api.DeliveryFallenBack, "false"},
		{"the SessionEnd's deny, its hook gone, the spawn resumed", []closeStep{closeEnd, closeResume, closeGone}, api.DeliveryFallenBack, "false"},
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

// TestEndedSpawnsRequestHoldsNothingAfterResume (b.59i, b.6qr; b.146 rule
// 12): request A, undecided or decided and not acked when its agent's
// SessionEnd ended the Spawn, holds nothing once the Spawn is resumed: the
// resumed agent's move to working goes ahead, and when the agent's next
// request B puts the row in check_permission again, get and list show B alone,
// which holds the move to working as a request of the current agent does.
func TestEndedSpawnsRequestHoldsNothingAfterResume(t *testing.T) {
	t.Parallel()
	hookB := store.ProcessIdentity{PID: 54322, Starttime: relayEnvHook.Starttime, PIDNamespace: relayNS}
	for _, tc := range []struct {
		name  string
		steps []closeStep
	}{
		{"undecided", []closeStep{closeEnd, closeGone, closeResume}},
		{"decided, not acked", []closeStep{closeDecide, closeEnd, closeGone, closeResume}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newRelayEnv(t, relayEnvHook)
			for _, step := range tc.steps {
				step(t, e)
			}
			toWorking := func() string { // the agent's move to working, and the state it leaves
				t.Helper()
				if err := seedAgentState(e.s, e.dbPath, e.id, store.StateWorking); err != nil {
					t.Fatalf("the agent's move to working: %v", err)
				}
				sp, err := e.s.GetSpawn(e.id)
				if err != nil {
					t.Fatalf("GetSpawn: %v", err)
				}
				return sp.State
			}

			if got := toWorking(); got != store.StateWorking {
				t.Errorf("state after the resumed agent's move to working = %q; want working, not held by A", got)
			}
			e.pc.Set(hookB.PID, procfix.Alive(hookB.Starttime))
			storefix.SeedRelayRequest(t, e.s, e.id, store.RelayRequest{RequestToken: storefix.TestRequestTokenB, ToolName: "Bash",
				ToolInput: `{"cmd":"pwd"}`, ToolUseID: "toolu_02", Hook: hookB, SettledAt: e.settled})
			fromGet, fromList := e.listed(t)
			for reader, got := range map[string]*api.PermissionRequestInfo{"get": fromGet, "list": fromList} {
				if got == nil || got.RequestToken != storefix.TestRequestTokenB {
					t.Errorf("%s's permission_requests = %+v; want B alone", reader, got)
				}
			}
			if got := toWorking(); got != store.StateCheckPermission {
				t.Errorf("state after a move to working with B open = %q; want check_permission, held by B", got)
			}
		})
	}
}
