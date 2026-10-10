package api_test

// sendkeys_rule7_test.go — b.146 rule 7, when send-keys is refused on account
// of the Spawn's permission requests (requests recorded from schema v7 on,
// each relay hook judged by its process), in every live state, and
// ErrRelayFallenBack's err_details from a plain send-keys (rule 15). Requests
// recorded before v7, judged by time, are sendkeys_test.go's. Fixture:
// pane_answer_fixture_test.go.

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// r7Step is one step of a rule-7 case's history on p.
type r7Step func(t *testing.T, p *paEnv)

var (
	// r7HookAlive: request A's relay hook runs.
	r7HookAlive r7Step = func(_ *testing.T, p *paEnv) { p.pc.Set(relayEnvHook.PID, procfix.Alive(relayEnvHook.Starttime)) }
	// r7Acked: decide's allow on A, acked by its relay hook.
	r7Acked r7Step = func(t *testing.T, p *paEnv) {
		if ok, err := p.st.DecideRelayRequest(p.r.ID, storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide,
			time.Time{}, store.DefaultLockWait); err != nil || !ok {
			t.Fatalf("decide = %v, %v", ok, err)
		}
		if _, _, ok, err := p.st.AckRelayDecision(p.r.ID, storefix.TestRequestTokenA, p.now, store.DefaultLockWait, nil); err != nil || !ok {
			t.Fatalf("ack = %v, %v", ok, err)
		}
	}
	// r7Decided: decide's allow on A, not acked.
	r7Decided r7Step = func(t *testing.T, p *paEnv) {
		if ok, err := p.st.DecideRelayRequest(p.r.ID, storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide,
			time.Time{}, store.DefaultLockWait); err != nil || !ok {
			t.Fatalf("decide = %v, %v", ok, err)
		}
	}
	// r7Outside: A recorded answered outside agent-director.
	r7Outside r7Step = func(t *testing.T, p *paEnv) {
		if ok, err := p.st.RecordPaneOutside(p.r.ID, storefix.TestRequestTokenA, "unknown", store.DefaultLockWait, nil); err != nil || !ok {
			t.Fatalf("RecordPaneOutside = %v, %v", ok, err)
		}
	}
	// r7ToolRan: the agent's PostToolUse for A's tool_use_id closed A.
	r7ToolRan r7Step = func(t *testing.T, p *paEnv) {
		if err := storefix.WithSeedPane(p.dbPath, p.r.ID, func(gate store.HookGate) error {
			_, err := p.st.CloseToolRanRequests(p.r.ID, gate, paToolUseID, p.now, func(store.PermissionRow) bool { return true })
			return err
		}); err != nil || p.request(t, storefix.TestRequestTokenA).PaneAnswer != store.PaneAnswerToolRan {
			t.Fatalf("tool-ran close = %v; want A closed", err)
		}
	}
	// r7OtherAlive: request B recorded by a relay hook that runs.
	r7OtherAlive r7Step = func(t *testing.T, p *paEnv) {
		hook := api.ProcessIdentity{PID: 54322, Starttime: relayEnvHook.Starttime, PIDNamespace: relayNS}
		p.pc.Set(hook.PID, procfix.Alive(hook.Starttime))
		p.seedRequest(t, storefix.TestRequestTokenB, "toolu_01B", hook)
	}
	// r7Mark: find-missing's mark of the row (closed_at on A, which it denies).
	r7Mark r7Step = func(t *testing.T, p *paEnv) {
		sp, err := p.st.GetSpawn(p.r.ID)
		if err != nil {
			t.Fatalf("GetSpawn: %v", err)
		}
		if _, res, err := p.st.MarkMissingIfSameLife(p.r.ID, sp.Snapshot); err != nil || res != store.CondApplied {
			t.Fatalf("mark = %v, %v", res, err)
		}
	}
	// r7End: the agent's SessionEnd ends the row.
	r7End r7Step = func(t *testing.T, p *paEnv) {
		if err := seedAgentState(p.st, p.dbPath, p.r.ID, store.StateEnded); err != nil {
			t.Fatalf("SessionEnd: %v", err)
		}
	}
	// r7Resume: the finished row resumed, its new agent reported in.
	r7Resume r7Step = func(t *testing.T, p *paEnv) { resumeFinishedRow(t, p.st, p.dbPath, p.r.ID) }
)

// TestSendKeysRule7 (b.146 rule 7, the round 3 §4.2 table, in its order; rule
// 12): request A fallen back unless a case says otherwise. Plain: a relay hook
// that may still answer is ErrSendKeysWhileRelayed (decide's advice for an
// open request, a later retry for an acked one), first in the order, even
// with A fallen back beside it; A fallen back is
// ErrRelayFallenBack; A closed (acked and its hook gone, answered outside, its
// tool ran, closed by find-missing's mark) holds nothing, nor does a request
// of a row that ended or went missing once the row is resumed, undecided or
// decided and not acked (b.59i, b.6qr). With A's token: another live relay
// hook is ErrSendKeysWhileRelayed naming it; A acked or answered at the pane
// is ErrAlreadyDecided, as is A denied by the mark; A closed by its row's
// SessionEnd is ErrNoOpenPermissionRequest; an unknown token is
// ErrNoOpenPermissionRequest. (The guard makes no tmux call.)
func TestSendKeysRule7(t *testing.T) {
	t.Parallel()
	tokA, tokB := storefix.TestRequestTokenA, storefix.TestRequestTokenB
	cases := []struct {
		name   string
		steps  []r7Step
		token  string // "" plain
		want   error  // nil: the guard releases
		advice string
	}{
		{"plain, A fallen back", nil, "", api.ErrRelayFallenBack, advRelayFallenBack},
		{"plain, A's relay hook alive", []r7Step{r7HookAlive}, "", api.ErrSendKeysWhileRelayed, advSendKeysAnswerWithDecide(tokA)},
		{"plain, A fallen back, B's relay hook alive", []r7Step{r7OtherAlive}, "", api.ErrSendKeysWhileRelayed, advSendKeysAnswerWithDecide(tokB)},
		{"plain, A acked, its relay hook still running", []r7Step{r7Acked, r7HookAlive}, "", api.ErrSendKeysWhileRelayed, advSendKeysRetryLater(tokA)},
		{"plain, A acked, its relay hook gone", []r7Step{r7Acked}, "", nil, ""},
		{"plain, A answered outside agent-director", []r7Step{r7Outside}, "", nil, ""},
		{"plain, A's tool ran", []r7Step{r7ToolRan}, "", nil, ""},
		{"plain, A closed by find-missing's mark, the row resumed", []r7Step{r7Mark, r7Resume}, "", nil, ""},
		{"plain, A open when the row ended, the row resumed", []r7Step{r7End, r7Resume}, "", nil, ""},
		{"plain, A decided, not acked, when the row ended, the row resumed", []r7Step{r7Decided, r7End, r7Resume}, "", nil, ""},
		{"A's token, B's relay hook alive", []r7Step{r7OtherAlive}, tokA, api.ErrSendKeysWhileRelayed, advSendKeysAnswerWithDecide(tokB)},
		{"A's token, A acked", []r7Step{r7Acked}, tokA, store.ErrAlreadyDecided, "already decided"},
		{"A's token, A answered outside agent-director", []r7Step{r7Outside}, tokA, store.ErrAlreadyDecided, `pane_answer "outside"`},
		{"A's token, A closed by find-missing's mark, the row resumed", []r7Step{r7Mark, r7Resume}, tokA, store.ErrAlreadyDecided, `"find_missing"`},
		{"A's token, A open when the row ended, the row resumed", []r7Step{r7End, r7Resume}, tokA, store.ErrNoOpenPermissionRequest,
			"its spawn ended, or find-missing marked it missing, before the request's relay hook delivered a verdict"},
		{"an unknown token", nil, "dddddddd-dddd-4ddd-addd-dddddddddddd", store.ErrNoOpenPermissionRequest, "has no permission request"},
		{"A's token, A fallen back", nil, tokA, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			for _, step := range tc.steps {
				step(t, p)
			}
			row, err := p.st.GetSpawn(p.r.ID)
			if err != nil || !(row.State == store.StateWaiting || row.State == store.StateWorking || row.State == store.StateCheckPermission) {
				t.Fatalf("row = %q, %v; want a live row", row.State, err)
			}
			params := p.plain("hi")
			if tc.token != "" {
				params = p.answer(tc.token, "allow", "1")
			}

			eval, refusal, err := api.EvaluateRelayGuardForTest(p.store, p.view(), 0, row, params)

			if err != nil {
				t.Fatalf("guard read: %v", err)
			}
			if tc.want == nil {
				if eval != "released" || refusal != nil {
					t.Errorf("guard = %q, %v; want released", eval, refusal)
				}
				return
			}
			if eval != "held" {
				t.Errorf("guard = %q; want held", eval)
			}
			adviceAssertAdvice(t, refusal, tc.want, tc.advice)
		})
	}
}

// TestSendKeysRule7EveryLiveState (b.146 rule 7): the guard reads a relay-on
// row's requests in every live state, not only check_permission (a row can
// read waiting while a request is still open), and on a relay-off row it does
// not apply.
func TestSendKeysRule7EveryLiveState(t *testing.T) {
	t.Parallel()
	p := newPAEnv(t)
	row, err := p.st.GetSpawn(p.r.ID)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	for _, state := range []string{store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
		row.State = state
		eval, refusal, err := api.EvaluateRelayGuardForTest(p.store, p.view(), 0, row, p.plain("hi"))
		if err != nil || eval != "held" {
			t.Errorf("%s: guard = %q, %v; want held", state, eval, err)
		}
		assertOneSentinel(t, refusal, api.ErrRelayFallenBack)
	}
	row.RelayMode = "off"
	if eval, refusal, err := api.EvaluateRelayGuardForTest(p.store, p.view(), 0, row, p.plain("hi")); eval != "not-applicable" || refusal != nil || err != nil {
		t.Errorf("relay off: guard = %q, %v, %v; want not-applicable", eval, refusal, err)
	}
}

// TestPlainSendKeysRelayFallenBackDetails (b.146 rules 7 and 15; the ticket's
// rule 6): plain send-keys on a row whose request A fell back is
// ErrRelayFallenBack naming A, the oldest, with nothing sent; its err_details
// carry A's facts as get-permission gives them (the attempt a refused decide
// kept, hook_gone_at, tool_use_id, pane_answer none), the row's state and
// open_requests: B, another fallen-back request, and not C (acked) nor D
// (answered outside). The refusal records hook_gone_at on B, which had none;
// its description never says the dialog is on screen.
func TestPlainSendKeysRelayFallenBackDetails(t *testing.T) {
	t.Parallel()
	tokA, tokB, tokC, tokD := storefix.TestRequestTokenA, storefix.TestRequestTokenB, storefix.TestRequestTokenC,
		"dddddddd-dddd-4ddd-addd-dddddddddddd"
	p := newPAEnv(t)
	p.seedRequest(t, tokB, "toolu_01B", relayEnvHook)
	p.seedRequest(t, tokC, "toolu_01C", relayEnvHook)
	p.seedRequest(t, tokD, "toolu_01D", relayEnvHook)
	if ok, err := p.st.DecideRelayRequest(p.r.ID, tokC, "deny", store.DecisionReasonOperator, store.WriterProcessDecide, time.Time{}, store.DefaultLockWait); err != nil || !ok {
		t.Fatalf("decide C = %v, %v", ok, err)
	}
	if _, _, ok, err := p.st.AckRelayDecision(p.r.ID, tokC, p.now, store.DefaultLockWait, nil); err != nil || !ok {
		t.Fatalf("ack C = %v, %v", ok, err)
	}
	if ok, err := p.st.RecordPaneOutside(p.r.ID, tokD, "deny", store.DefaultLockWait, nil); err != nil || !ok {
		t.Fatalf("outside D = %v, %v", ok, err)
	}
	decidedAt := p.now
	if _, err := api.Decide(p.st, p.view(), api.DecideParams{ClaudeInstanceID: p.r.ID, RequestToken: tokA, Decision: "allow"}); err == nil {
		t.Fatal("decide on A = nil; want ErrRelayFallenBack")
	}
	p.now = p.now.Add(time.Minute)

	run := p.sendKeysRun(p.plain("hi"))

	adviceAssertAdvice(t, run.err, api.ErrRelayFallenBack, advRelayFallenBack)
	adviceAssertPhrase(t, run.err, "request "+tokA+" fell back")
	adviceAssertPhrase(t, run.err, "nothing was sent")
	if strings.Contains(run.err.Error(), "on screen") {
		t.Errorf("description %q says the dialog is on screen", run.err)
	}
	if len(run.calls) != 0 {
		t.Errorf("calls = %v; want none", paKinds(run.calls))
	}
	m := assertFallenBackDetails(t, run.err, tokA, store.StateCheckPermission)
	wantA := map[string]any{"tool_name": "Bash", "tool_input": `{"command":"ls"}`, "tool_use_id": paToolUseID,
		"attempted_decision": "allow", "hook_alive": false, "pane_answer": "none", "pane_as": nil, "decision": nil,
		"decision_reason": nil, "hook_gone_at": jsonTime(t, decidedAt), "attempted_at": jsonTime(t, decidedAt),
		"confirm_by": jsonTime(t, decidedAt.Add(time.Hour))}
	for k, v := range wantA {
		if m[k] != v {
			t.Errorf("err_details[%q] = %#v; want %#v", k, m[k], v)
		}
	}
	open, _ := m["open_requests"].([]any)
	if len(open) != 1 {
		t.Fatalf("open_requests = %v; want B only", m["open_requests"])
	}
	b := open[0].(map[string]any)
	if b["request_token"] != tokB || b["tool_name"] != "Bash" || b["delivery"] != api.DeliveryFallenBack ||
		b["hook_alive"] != false || b["pane_answer"] != "none" || b["requested_at"] == nil {
		t.Errorf("open_requests[0] = %v; want B fallen back, hook_alive false, pane_answer none", b)
	}
	if got := p.request(t, tokB).HookGoneAt; !got.Equal(p.now) {
		t.Errorf("B's hook_gone_at = %v; want %v, recorded by the refusal", got, p.now)
	}
}

// jsonTime is tm as a JSON time string, as err_details carries it.
func jsonTime(t *testing.T, tm time.Time) string {
	t.Helper()
	return strings.Trim(jsonOf(t, tm.UTC()), `"`)
}
