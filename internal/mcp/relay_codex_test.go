package mcp_test

// relay_codex_test.go — the Codex cells of the relay's MCP surface (b.ssz Epic
// 4): err_details, ErrDialogMaybeOpen's description and the delivery facts on
// a Codex row. Each asserts what the MCP tool says of a row seeded with
// apitest.WithAgent("codex"); the proofs and holds themselves are internal/hook's
// and pkg/api's.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// seedAgentRelayRequest is seedRelayRequest for a row of agent.
func (e *mcpEnv) seedAgentRelayRequest(t *testing.T, id, agent string) apitest.PermissionRequestSeed {
	t.Helper()
	if _, err := apitest.SeedSpawn(e.storePath, id, store.StateCheckPermission, t.TempDir(), "on", "", false, apitest.WithAgent(agent)); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
	req, err := apitest.SeedPermissionRequest(e.storePath, id, "Bash")
	if err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	return req
}

// TestMCPCodexErrDetails: record-pane-answer's ErrClaimTooSoon on a Codex
// row's request inside its relay window carries the same err_details as on a
// Claude row (the request, hook_alive and hook_gone_at null, not_before): no
// Codex-only fact or key.
func TestMCPCodexErrDetails(t *testing.T) {
	e := newEnv(t)
	req := e.seedAgentRelayRequest(t, "mcp-codex-err-details", "codex")

	data := toolErrorData(t, callTool(t, e.d, "record-pane-answer", paramJSON(t, map[string]any{"request_token": req.RequestToken,
		"as": "unknown", "expect_pane_sha256": strings.Repeat("0", 64)})))

	details, ok := data.ErrDetails.(map[string]any)
	if data.ErrName != "ErrClaimTooSoon" || !ok || details["request_token"] != req.RequestToken {
		t.Fatalf("record-pane-answer error = %q, err_details %#v; want ErrClaimTooSoon naming %s", data.ErrName, data.ErrDetails, req.RequestToken)
	}
	for _, k := range []string{"hook_alive", "hook_gone_at"} {
		if v, present := details[k]; !present || v != nil {
			t.Errorf("err_details[%q] = %v (present %v); want null", k, v, present)
		}
	}
	if _, isText := details["not_before"].(string); !isText {
		t.Errorf("err_details not_before = %v; want a timestamp", details["not_before"])
	}
}

// TestMCPDialogMaybeOpenByAgent: a fallen-back request closed through the
// record-pane-answer tool is not proven gone on either agent's row, so the
// send-keys tool's plain call is ErrDialogMaybeOpen with the same err_details
// (tool_use_id null: the seeded request has none, as every Codex request) and
// nothing sent; its description names the agent's own proofs: Claude's text as
// it shipped, Codex's Stop or Interrupt with no PostToolUse.
func TestMCPDialogMaybeOpenByAgent(t *testing.T) {
	cases := []struct {
		agent string
		want  func() apitest.DescCase
	}{
		{"claude", apitest.DescDialogMaybeOpenClaude},
		{"codex", apitest.DescDialogMaybeOpenCodex},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			e := newEnv(t)
			id := "mcp-maybe-open-" + tc.agent
			req := e.seedAgentRelayRequest(t, id, tc.agent)
			if err := apitest.AgePermissionRequest(e.storePath, req.RequestID, 48*time.Hour, time.Minute); err != nil {
				t.Fatalf("age the request: %v", err)
			}
			e.rec.SeedRowSession(t, e.storePath, id)
			const pane = "the dialog answered at tmux\n"
			e.rec.SetCapture(apitest.TestSocket, apitest.TestPaneID, pane)
			sum := sha256.Sum256([]byte(pane))
			callToolText(t, e.d, "record-pane-answer", paramJSON(t, map[string]any{"request_token": req.RequestToken, "as": "unknown",
				"expect_pane_sha256": hex.EncodeToString(sum[:])}))

			data := toolErrorData(t, callTool(t, e.d, "send-keys", paramJSON(t, map[string]any{"instance_id": id, "text": "hi"})))

			details, _ := data.ErrDetails.(map[string]any)
			if data.ErrName != "ErrDialogMaybeOpen" || details["request_token"] != req.RequestToken || details["pane_answer"] != "outside" ||
				details["unproven_since"] == nil || details["proven_gone_at"] != nil || details["state"] != store.StateCheckPermission {
				t.Fatalf("send-keys error = %q (%s), err_details %v; want ErrDialogMaybeOpen naming %s, outside, unproven",
					data.ErrName, data.ErrDescription, data.ErrDetails, req.RequestToken)
			}
			if v, present := details["tool_use_id"]; !present || v != nil {
				t.Errorf("err_details tool_use_id = %v (present %v); want null", v, present)
			}
			apitest.AssertDescription(t, data.ErrDescription, tc.want())
		})
	}
}

// TestMCPCodexRelayDeliveryFacts: get-permission, each request of get's and
// list's rows and decide's result carry every delivery fact for a Codex row,
// with the Codex request's tool_use_id null and nothing proven; the facts are
// those of a Claude row (the delivery value set is unchanged).
func TestMCPCodexRelayDeliveryFacts(t *testing.T) {
	e := newEnv(t)
	const id = "mcp-codex-relay"
	req := e.seedAgentRelayRequest(t, id, "codex")
	args := paramJSON(t, map[string]any{"request_token": req.RequestToken})

	perm := callToolText(t, e.d, "get-permission", args)
	assertMCPDeliveryKeys(t, "get-permission", perm)
	for _, k := range []string{"tool_use_id", "proven_gone_at", "proven_gone_how"} {
		if string(perm[k]) != "null" {
			t.Errorf("get-permission %s = %s; want null on an open Codex request", k, perm[k])
		}
	}
	got := callToolText(t, e.d, "get", paramJSON(t, map[string]any{"instance_id": id}))
	body, _ := json.Marshal(got)
	assertMCPDeliveryKeys(t, "get's request", onlyMCPRequest(t, "get", body))
	var list struct {
		Spawns []json.RawMessage `json:"spawns"`
	}
	listed := callToolText(t, e.d, "list", `{}`)
	if err := json.Unmarshal(listed["spawns"], &list.Spawns); err != nil || len(list.Spawns) != 1 {
		t.Fatalf("list spawns = %s (%v); want the one row", listed["spawns"], err)
	}
	assertMCPDeliveryKeys(t, "list's request", onlyMCPRequest(t, "list", list.Spawns[0]))
	decided := callToolText(t, e.d, "decide", paramJSON(t, map[string]any{"instance_id": id, "request_token": req.RequestToken,
		"decision": "allow", "max_wait_ms": 2000}))
	assertMCPDeliveryKeys(t, "decide", decided)
	if string(decided["delivery"]) != `"not_confirmed"` {
		t.Errorf("decide delivery = %s; want not_confirmed (a request with no ack)", decided["delivery"])
	}
}
