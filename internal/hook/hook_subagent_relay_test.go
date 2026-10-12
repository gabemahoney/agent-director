package hook_test

// Subagent capture on a relay-on row (b.xjn; SRD SR-13, SR-3.3): an Agent
// PreToolUse or PostToolUse still makes the relay proofs it made before (a
// PostToolUse proves its request's dialog gone, a PreToolUse proves nothing),
// and the entry transaction runs after the applied row write, the proofs
// unmoved ahead of it. The store's own rules are internal/store's and the
// relay-off wiring is hook_subagent_records_test.go's.

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The store calls the order test tells apart.
const (
	callProve    = "prove requests gone"
	callToolRan  = "close tool-ran requests"
	callRowWrite = "row write"
	callEntryTx  = "entry transaction"
)

// orderStore is a real store that records, in order, the calls a hook makes of
// the relay proofs, the ordinary row write and the entry transaction.
type orderStore struct {
	*store.Store
	calls *[]string
}

func (o orderStore) note(call string) { *o.calls = append(*o.calls, call) }

func (o orderStore) ProveRequestsGone(instanceID string, gate store.HookGate, proof store.RequestProof) (int64, error) {
	o.note(callProve)
	return o.Store.ProveRequestsGone(instanceID, gate, proof)
}

func (o orderStore) CloseToolRanRequests(instanceID string, gate store.HookGate, toolUseID string, at time.Time, gone func(store.PermissionRow) bool) ([]string, error) {
	o.note(callToolRan)
	return o.Store.CloseToolRanRequests(instanceID, gate, toolUseID, at, gone)
}

func (o orderStore) ApplyHookTransitionResult(instanceID string, gate store.HookGate, newState string, softRefresh bool, triggeringEventName, jsonlPath string, jsonlPresent bool) (store.UpsertOutcome, store.HookApplied, error) {
	o.note(callRowWrite)
	return o.Store.ApplyHookTransitionResult(instanceID, gate, newState, softRefresh, triggeringEventName, jsonlPath, jsonlPresent)
}

func (o orderStore) ApplySubagentAfterWrite(instanceID string, gate store.HookGate, w store.SubagentAfterWrite) error {
	o.note(callEntryTx)
	return o.Store.ApplySubagentAfterWrite(instanceID, gate, w)
}

// TestSubagentRelayOnRowKeepsItsProofsAndOrder: on a relay-on row with one open
// relayed request for the launch's tool_use_id, an Agent PostToolUse proves the
// request gone by tool_ran and leaves it otherwise as recorded, an Agent
// PreToolUse proves nothing, and each opens or fills its entry after its row
// write. Another process's hook, which the gate refuses, writes nothing and
// runs no entry transaction.
func TestSubagentRelayOnRowKeepsItsProofsAndOrder(t *testing.T) {
	cases := []struct {
		name      string
		fixture   string
		foreign   bool
		wantCalls []string
		proven    string // proven_gone_how of the request; "" unproven
		check     func(t *testing.T, r *subRow, ms int64)
	}{
		{name: "PostToolUse(Agent)", fixture: fxResult,
			wantCalls: []string{callProve, callToolRan, callRowWrite, callEntryTx}, proven: store.ProvenGoneToolRan,
			check: func(t *testing.T, r *subRow, ms int64) {
				assertEntries(t, r, filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, ms))
			}},
		{name: "PreToolUse(Agent)", fixture: fxRequest,
			wantCalls: []string{callRowWrite, callEntryTx},
			check: func(t *testing.T, r *subRow, ms int64) {
				assertLaunches(t, r, wantLaunch{subToolUseID, subType, ms})
			}},
		{name: "PostToolUse(Agent) from another process", fixture: fxResult, foreign: true,
			wantCalls: []string{callProve, callToolRan, callRowWrite}, // the gated store refuses each; no entry transaction follows
			check: func(t *testing.T, r *subRow, _ int64) {
				assertEntries(t, r)
			}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "subagent-relay-" + string(rune('a'+i))
			r := newSubRow(t, id, apitest.WithRelayMode("on"))
			storefix.SeedRelayRequest(t, r.st, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Agent",
				ToolInput: `{}`, ToolUseID: subToolUseID, Hook: relayHookSelf, SettledAt: time.Now().Add(time.Hour)})
			parent := agentParent(t, r.st, id)
			if tc.foreign {
				parent = foreignParent(t, r.st, id)
			}
			hc := hookConfig(envWith(id), parent)
			hc.RelayHookGone = func(store.PermissionRow) bool { return false } // its relay hook may still answer
			hc.Start = time.UnixMilli(at(1))
			var calls []string

			out := ssgHandleWith(t, orderStore{Store: r.st, calls: &calls}, hc, string(variant(t, tc.fixture)), newSilentLogger())

			if out != "" {
				t.Errorf("stdout = %q; want empty", out)
			}
			if got, want := strings.Join(calls, ", "), strings.Join(tc.wantCalls, ", "); got != want {
				t.Errorf("calls = [%s]; want [%s]", got, want)
			}
			pr := onlyRequest(t, r.st, id)
			if pr.ProvenGoneHow != tc.proven {
				t.Errorf("proven_gone_how = %q; want %q", pr.ProvenGoneHow, tc.proven)
			}
			if tc.proven != "" && pr.ProvenGoneAt.UnixMilli() != hc.Now().UnixMilli() {
				t.Errorf("proven_gone_at = %v; want the hook's clock %v", pr.ProvenGoneAt, hc.Now())
			}
			if pr.PaneAnswer != store.PaneAnswerNone || pr.Decision != "" {
				t.Errorf("request = pane_answer %q, decision %q; want it as recorded (the proof closes nothing)", pr.PaneAnswer, pr.Decision)
			}
			tc.check(t, r, at(1))
		})
	}
}

var _ hook.HookStore = orderStore{}
