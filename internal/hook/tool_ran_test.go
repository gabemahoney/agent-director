package hook_test

// tool_ran_test.go — b.146 rule 13's PostToolUse close: the agent's
// PostToolUse or PostToolUseFailure carrying a fallen-back request's
// tool_use_id closes it as tool_ran / allow, before the hook's own write.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// toolRanUseID is the tool_use_id of the request the close tests record.
const toolRanUseID = "toolu_01RAN"

// toolRanPayload is fixture with its tool_use_id replaced by toolUseID.
func toolRanPayload(t *testing.T, fixture, toolUseID string) []byte {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(readPayloadFixture(t, fixture), &p); err != nil {
		t.Fatalf("parse %s: %v", fixture, err)
	}
	p["tool_use_id"] = toolUseID
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// TestToolRanClosesTheFallenBackRequest (b.146 rule 13): a PostToolUse or
// PostToolUseFailure from the row's own agent on a relayed row, carrying the
// tool_use_id of a request whose relay hook is judged gone, closes it
// (pane_answer tool_ran, decision allow, decision_reason tool_ran, hook_gone_at
// recorded), so the hook's move to working is no longer held; another
// tool_use_id, a hook that may still answer, a relay-off hook or another
// process's hook changes nothing.
func TestToolRanClosesTheFallenBackRequest(t *testing.T) {
	cases := []struct {
		name      string
		fixture   string
		toolUseID string
		gone      bool // what RelayHookGone answers
		relayOff  bool
		foreign   bool
		closed    bool
	}{
		{"PostToolUse, the request's tool_use_id", "post-tool-use.json", toolRanUseID, true, false, false, true},
		{"PostToolUseFailure, the request's tool_use_id", "post-tool-use-failure.json", toolRanUseID, true, false, false, true},
		{"PostToolUse, another tool_use_id", "post-tool-use.json", "toolu_01OTHER", true, false, false, false},
		{"PostToolUse, the relay hook may still answer", "post-tool-use.json", toolRanUseID, false, false, false, false},
		{"PostToolUse, relay off", "post-tool-use.json", toolRanUseID, true, true, false, false},
		{"PostToolUse from another process", "post-tool-use.json", toolRanUseID, true, false, true, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "tool-ran-" + string(rune('a'+i))
			st, _ := seedAgentRow(t, id, store.StateWorking)
			parent := agentParent(t, st, id)
			storefix.SeedRelayRequest(t, st, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
				ToolInput: `{}`, ToolUseID: toolRanUseID, Hook: relayHookSelf, SettledAt: time.Now().Add(time.Hour)})
			if tc.foreign {
				parent = foreignParent(t, st, id)
			}
			env := envWith(id)
			if tc.relayOff {
				env = envHook(id, "")
			}
			hc := hookConfig(env, parent)
			var judged []string
			hc.RelayHookGone = func(pr store.PermissionRow) bool {
				judged = append(judged, pr.RequestToken)
				return tc.gone
			}
			at := hc.Now()
			before := len(readTrailLines(t, trailFile()))

			ssgHandleWith(t, st, hc, string(toolRanPayload(t, tc.fixture, tc.toolUseID)), newSilentLogger())

			pr := onlyRequest(t, st, id)
			state := mustGetSpawn(t, st, id).State
			committed := linesAfter(t, before, "ad.row_mutation.committed", id)
			if !tc.closed {
				if pr.PaneAnswer != store.PaneAnswerNone || pr.Decision != "" || !pr.HookGoneAt.IsZero() {
					t.Errorf("request = pane_answer %q, decision %q, hook_gone_at %v; want it untouched", pr.PaneAnswer, pr.Decision, pr.HookGoneAt)
				}
				if state != store.StateCheckPermission {
					t.Errorf("state = %q; want check_permission, held by the open request", state)
				}
				if len(committed) != 0 {
					t.Errorf("ad.row_mutation.committed = %v; want none", committed)
				}
				return
			}
			if pr.PaneAnswer != store.PaneAnswerToolRan || pr.Decision != "allow" || pr.DecisionReason != store.DecisionReasonToolRan ||
				pr.HookGoneAt.UnixMilli() != at.UnixMilli() {
				t.Errorf("request = pane_answer %q, decision %q, decision_reason %q, hook_gone_at %v; want tool_ran, allow, tool_ran, %v",
					pr.PaneAnswer, pr.Decision, pr.DecisionReason, pr.HookGoneAt, at)
			}
			if len(judged) != 1 || judged[0] != storefix.TestRequestTokenA {
				t.Errorf("relay hooks judged = %v; want request A's, before the close", judged)
			}
			if state != store.StateWorking {
				t.Errorf("state = %q; want working (the closed request no longer holds the move)", state)
			}
			if len(committed) != 1 || committed[0]["writer_process"] != store.WriterProcessHook ||
				committed[0]["decision"] != "allow" || committed[0]["decision_reason"] != store.DecisionReasonToolRan {
				t.Errorf("ad.row_mutation.committed = %v; want one, writer hook, allow, tool_ran", committed)
			}
		})
	}
}

// TestToolRanWithNoJudgeClosesNothing: a HandleConfig with no RelayHookGone
// (a caller that cannot judge relay hooks) closes no request.
func TestToolRanWithNoJudgeClosesNothing(t *testing.T) {
	const id = "tool-ran-no-judge"
	st, _ := seedAgentRow(t, id, store.StateWorking)
	storefix.SeedRelayRequest(t, st, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
		ToolInput: `{}`, ToolUseID: toolRanUseID, Hook: relayHookSelf, SettledAt: time.Now().Add(-time.Hour)})
	hc := hookConfig(envWith(id), agentParent(t, st, id))

	ssgHandleWith(t, st, hc, string(toolRanPayload(t, "post-tool-use.json", toolRanUseID)), newSilentLogger())

	if pr := onlyRequest(t, st, id); pr.PaneAnswer != store.PaneAnswerNone || pr.Decision != "" {
		t.Errorf("request = pane_answer %q, decision %q; want it untouched", pr.PaneAnswer, pr.Decision)
	}
}
