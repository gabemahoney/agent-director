package hook_test

// request_proof_test.go — b.146 step 2c through Handle on a real store: which
// hooks of a relayed row's own agent prove its permission requests' dialogs
// gone (a PostToolUse or PostToolUseFailure by tool_use_id, the main agent's
// Stop or idle prompt after the request) and which do not. The store's rules
// are internal/store/request_proof_test.go's.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// proofUseID is the tool_use_id of the request the proof tests record.
const proofUseID = "toolu_01PROOF"

// payloadWith is fixture with fields set (a nil value removes the field).
func payloadWith(t *testing.T, fixture string, fields map[string]any) []byte {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(readPayloadFixture(t, fixture), &p); err != nil {
		t.Fatalf("parse %s: %v", fixture, err)
	}
	for k, v := range fields {
		if v == nil {
			delete(p, k)
			continue
		}
		p[k] = v
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// TestHookProvesRequestsGone (b.146 step 2c rule 1): on a relayed row, the
// agent's PostToolUse or PostToolUseFailure carrying a request's tool_use_id
// proves it gone (tool_ran), a subagent's request included, whatever
// agent-director's records say (here its relay hook may still answer, so it is
// not closed); the main agent's Stop or idle-prompt Notification proves every
// request with no agent_id recorded before it (turn_end), one recorded before
// v7 included. Another tool_use_id, a PreToolUse, a hook that came before the
// request, a subagent's request at the end of the main turn, a subagent's
// idle prompt or a teammate's Stop (an agent_id), another Notification, a
// relay-off hook or another process's hook proves nothing.
func TestHookProvesRequestsGone(t *testing.T) {
	const subagent = "a5b92f0e7d3c6184"
	post := func(fixture, id string) func(t *testing.T) []byte {
		return func(t *testing.T) []byte { return payloadWith(t, fixture, map[string]any{"tool_use_id": id}) }
	}
	stop := func(t *testing.T) []byte { return readPayloadFixture(t, "stop.json") }
	notify := func(ntype, agentID string) func(t *testing.T) []byte {
		return func(t *testing.T) []byte { return notificationPayload(t, ntype, agentID) }
	}
	cases := []struct {
		name     string
		request  string // "main", "subagent" or "pre-v7"
		payload  func(t *testing.T) []byte
		first    bool // the hook fires before the request is recorded
		relayOff bool
		foreign  bool
		want     string // proven_gone_how; "" unproven
	}{
		{name: "PostToolUse, its tool_use_id", request: "main", payload: post("post-tool-use.json", proofUseID), want: store.ProvenGoneToolRan},
		{name: "PostToolUseFailure, its tool_use_id", request: "main", payload: post("post-tool-use-failure.json", proofUseID),
			want: store.ProvenGoneToolRan},
		{name: "PostToolUse, a subagent's request, its tool_use_id", request: "subagent", payload: post("post-tool-use.json", proofUseID),
			want: store.ProvenGoneToolRan},
		{name: "PostToolUse, another tool_use_id", request: "main", payload: post("post-tool-use.json", "toolu_01OTHER")},
		{name: "PreToolUse, its tool_use_id", request: "main", payload: post("pre-tool-use-bash.json", proofUseID)},
		{name: "Stop after a main-agent request", request: "main", payload: stop, want: store.ProvenGoneTurnEnd},
		{name: "idle prompt after a main-agent request", request: "main", payload: notify("idle_prompt", ""), want: store.ProvenGoneTurnEnd},
		{name: "Stop after a request recorded before v7", request: "pre-v7", payload: stop, want: store.ProvenGoneTurnEnd},
		{name: "idle prompt after a request recorded before v7", request: "pre-v7", payload: notify("idle_prompt", ""),
			want: store.ProvenGoneTurnEnd},
		{name: "Stop before a main-agent request", request: "main", payload: stop, first: true},
		{name: "idle prompt before a main-agent request", request: "main", payload: notify("idle_prompt", ""), first: true},
		{name: "Stop after a subagent's request", request: "subagent", payload: stop},
		{name: "idle prompt after a subagent's request", request: "subagent", payload: notify("idle_prompt", "")},
		{name: "a subagent's idle prompt after a main-agent request", request: "main", payload: notify("idle_prompt", subagent)},
		{name: "a teammate's Stop after a main-agent request", request: "main", payload: func(t *testing.T) []byte {
			return payloadWith(t, "stop.json", map[string]any{"agent_id": subagent})
		}},
		{name: "permission_prompt Notification after a main-agent request", request: "main", payload: notify("permission_prompt", "")},
		{name: "Stop, relay off", request: "main", payload: stop, relayOff: true},
		{name: "PostToolUse, relay off", request: "main", payload: post("post-tool-use.json", proofUseID), relayOff: true},
		{name: "Stop from another process", request: "main", payload: stop, foreign: true},
		{name: "PostToolUse from another process", request: "main", payload: post("post-tool-use.json", proofUseID), foreign: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "request-proof-" + string(rune('a'+i))
			st, _ := seedAgentRow(t, id, store.StateWorking)
			record := func() {
				if tc.request == "pre-v7" {
					storefix.SeedOpenPermissionRequests(t, st, id, []string{storefix.TestRequestTokenA})
					return
				}
				req := store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash", ToolInput: `{}`,
					ToolUseID: proofUseID, Hook: relayHookSelf, SettledAt: time.Now().Add(time.Hour)}
				if tc.request == "subagent" {
					req.AgentID = subagent
				}
				storefix.SeedRelayRequest(t, st, id, req)
			}
			parent := agentParent(t, st, id)
			if tc.foreign {
				parent = foreignParent(t, st, id)
			}
			env := envWith(id)
			if tc.relayOff {
				env = envHook(id, "")
			}
			hc := hookConfig(env, parent)
			hc.RelayHookGone = func(store.PermissionRow) bool { return false } // its relay hook may still answer
			if !tc.first {
				record()
			}

			ssgHandleWith(t, st, hc, string(tc.payload(t)), newSilentLogger())

			if tc.first {
				record()
			}
			pr := onlyRequest(t, st, id)
			if pr.ProvenGoneHow != tc.want {
				t.Fatalf("proven_gone_how = %q; want %q", pr.ProvenGoneHow, tc.want)
			}
			if tc.want == "" {
				if pr.ProvenGone() {
					t.Errorf("proven_gone_at = %v; want unproven", pr.ProvenGoneAt)
				}
				return
			}
			if at := hc.Now(); pr.ProvenGoneAt.UnixMilli() != at.UnixMilli() {
				t.Errorf("proven_gone_at = %v; want the hook's clock %v", pr.ProvenGoneAt, at)
			}
			if pr.PaneAnswer != store.PaneAnswerNone || pr.Decision != "" {
				t.Errorf("request = pane_answer %q, decision %q; want it as recorded (the proof closes nothing)", pr.PaneAnswer, pr.Decision)
			}
		})
	}
}
