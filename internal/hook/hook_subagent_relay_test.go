package hook_test

// Subagent capture on a relay-on row (b.xjn; SRD SR-13, SR-3.3): an Agent
// PreToolUse or PostToolUse still makes the relay proofs it made before (a
// PostToolUse proves its request's dialog gone, a PreToolUse proves nothing),
// and the entry transaction runs after the applied row write, the proofs
// unmoved ahead of it. The store's own rules are internal/store's and the
// relay-off wiring is hook_subagent_records_test.go's.

import (
	"bytes"
	"errors"
	"log"
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
	// readErr, when set, is what the skip read of a main-agent Stop returns.
	readErr error
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

func (o orderStore) SubagentTurnEndHasWork(instanceID string, te store.SubagentTurnEnd) (bool, error) {
	if o.readErr != nil {
		return false, o.readErr
	}
	return o.Store.SubagentTurnEndHasWork(instanceID, te)
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

// TestSubagentMainStopSkipsEntryTransaction (SR-12.2): a main-agent Stop whose
// skip read finds nothing to change runs no entry transaction, and one that can
// change something runs it. A failing skip read logs one line and changes
// nothing, the hook still exiting 0 with empty stdout.
func TestSubagentMainStopSkipsEntryTransaction(t *testing.T) {
	const listedAt, pastGraceAt = 1, 6000
	cases := []struct {
		name     string
		seed     bool // a Stop listing a background subagent runs first, at at(listedAt)
		stop     []byte
		stopAtMs int64
		readErr  error
		wantTx   bool
		want     func(r *subRow) []wantEntry // the entries after the Stop under test
		wantLog  string                      // "" is no log line
	}{
		{name: "nothing to change: an empty snapshot and no entries", stop: mainStop(t), stopAtMs: at(listedAt),
			want: func(*subRow) []wantEntry { return nil }},
		{name: "no snapshot", stop: variant(t, fxMainStop, delKey("background_tasks")), stopAtMs: at(listedAt),
			want: func(*subRow) []wantEntry { return nil }},
		{name: "a listed subagent", stop: variant(t, fxMainStop), stopAtMs: at(listedAt), wantTx: true,
			want: func(*subRow) []wantEntry { return []wantEntry{listedRecord(subAgentID, subType, at(listedAt))} }},
		{name: "a background record within the grace", seed: true, stop: mainStop(t), stopAtMs: at(listedAt) + 1000,
			want: func(*subRow) []wantEntry { return []wantEntry{listedRecord(subAgentID, subType, at(listedAt))} }},
		{name: "a background record past the grace", seed: true, stop: mainStop(t), stopAtMs: at(listedAt) + pastGraceAt, wantTx: true,
			want: func(*subRow) []wantEntry {
				return []wantEntry{listedRecord(subAgentID, subType, at(listedAt)).lostAbsent(at(listedAt) + pastGraceAt)}
			}},
		{name: "the skip read fails", seed: true, stop: mainStop(t), stopAtMs: at(listedAt) + pastGraceAt,
			readErr: errors.New("read failed"), wantLog: "turn-end read",
			want: func(*subRow) []wantEntry { return []wantEntry{listedRecord(subAgentID, subType, at(listedAt))} }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newSubRow(t, "subagent-skip-"+string(rune('a'+i)))
			if tc.seed {
				r.fire(variant(t, fxMainStop), at(listedAt))
				assertEntries(t, r, listedRecord(subAgentID, subType, at(listedAt)))
			}
			hc := hookConfig(envHook(r.id, ""), agentParent(t, r.st, r.id))
			hc.Start = time.UnixMilli(tc.stopAtMs)
			var calls []string
			var logBuf bytes.Buffer

			out := ssgHandleWith(t, orderStore{Store: r.st, calls: &calls, readErr: tc.readErr}, hc, string(tc.stop), log.New(&logBuf, "", 0))

			if out != "" {
				t.Errorf("stdout = %q; want empty", out)
			}
			ran := false
			for _, c := range calls {
				ran = ran || c == callEntryTx
			}
			if ran != tc.wantTx {
				t.Errorf("calls = [%s]; entry transaction ran = %v, want %v", strings.Join(calls, ", "), ran, tc.wantTx)
			}
			if tc.wantLog == "" && logBuf.Len() != 0 {
				t.Errorf("log = %q; want none", logBuf.String())
			}
			if tc.wantLog != "" && (!strings.Contains(logBuf.String(), tc.wantLog) || strings.Count(logBuf.String(), "\n") != 1) {
				t.Errorf("log = %q; want one line with %q", logBuf.String(), tc.wantLog)
			}
			assertEntries(t, r, tc.want(r)...)
		})
	}
}

var _ hook.HookStore = orderStore{}
