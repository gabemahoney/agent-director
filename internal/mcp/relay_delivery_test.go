package mcp_test

// relay_delivery_test.go — b.146 step 2 through MCP: decide's max_wait_ms
// parameter (decision 9 B) and the delivery facts on decide's result,
// get-permission's and each request of get's and list's rows (rule 15).

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// mcpDeliveryKeys are a permission request's delivery facts (b.146 rule 15).
var mcpDeliveryKeys = []string{"delivery", "confirm_by", "hook_alive", "hook_gone_at", "attempted_decision", "attempted_at", "tool_use_id"}

// assertMCPDeliveryKeys fails unless obj carries every delivery fact.
func assertMCPDeliveryKeys(t *testing.T, what string, obj map[string]json.RawMessage) {
	t.Helper()
	for _, k := range mcpDeliveryKeys {
		if _, ok := obj[k]; !ok {
			t.Errorf("%s has no %q: %v", what, k, obj)
		}
	}
}

// seedRelayRequest seeds a relay-on row in check_permission with one open
// request and returns its id and the request's token.
func (e *mcpEnv) seedRelayRequest(t *testing.T, id string) string {
	t.Helper()
	if _, err := apitest.SeedSpawn(e.storePath, id, store.StateCheckPermission, t.TempDir(), "on", "", false); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
	req, err := apitest.SeedPermissionRequest(e.storePath, id, "Bash")
	if err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	return req.RequestToken
}

// onlyMCPRequest returns the one element of a row's permission_requests.
func onlyMCPRequest(t *testing.T, what string, row json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var r struct {
		PermissionRequests []map[string]json.RawMessage `json:"permission_requests"`
	}
	if err := json.Unmarshal(row, &r); err != nil || len(r.PermissionRequests) != 1 {
		t.Fatalf("%s permission_requests = %s (%v); want one", what, row, err)
	}
	return r.PermissionRequests[0]
}

// TestMCPRelayDeliveryFacts: tools/list gives decide an optional integer
// max_wait_ms; get-permission, each request of get's and list's rows, and
// decide's result carry every delivery fact; decide with max_wait_ms under a
// write lock held past it is ErrStoreBusy with nothing recorded, and a
// negative max_wait_ms is ErrInvalidFlags.
func TestMCPRelayDeliveryFacts(t *testing.T) {
	tools, _ := toolsList(t)
	i := slices.IndexFunc(tools, func(tl listedTool) bool { return tl.Name == "decide" })
	if i < 0 {
		t.Fatal("tools/list has no decide")
	}
	if p, ok := tools[i].InputSchema.Properties["max_wait_ms"]; !ok || p.Type != "integer" || slices.Contains(tools[i].InputSchema.Required, "max_wait_ms") {
		t.Errorf("decide's max_wait_ms schema = %v (present %v, required %v); want an optional integer", p, ok, tools[i].InputSchema.Required)
	}

	e := newEnv(t)
	const id = "mcp-relay"
	tok := e.seedRelayRequest(t, id)

	assertMCPDeliveryKeys(t, "get-permission", callToolText(t, e.d, "get-permission", paramJSON(t, map[string]any{"request_token": tok})))
	got := callToolText(t, e.d, "get", paramJSON(t, map[string]any{"claude_instance_id": id}))
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
	decided := callToolText(t, e.d, "decide", paramJSON(t, map[string]any{"claude_instance_id": id, "request_token": tok,
		"decision": "allow", "max_wait_ms": 2000}))
	assertMCPDeliveryKeys(t, "decide", decided)
	if string(decided["delivery"]) != `"not_confirmed"` {
		t.Errorf("decide delivery = %s; want not_confirmed (a request with no ack)", decided["delivery"])
	}

	const busyID = "mcp-relay-busy"
	busyTok := e.seedRelayRequest(t, busyID)
	if data := toolErrorData(t, callTool(t, e.d, "decide", paramJSON(t, map[string]any{"claude_instance_id": busyID,
		"request_token": busyTok, "decision": "allow", "max_wait_ms": -1}))); data.ErrName != "ErrInvalidFlags" {
		t.Errorf("decide max_wait_ms -1 err_name = %q; want ErrInvalidFlags", data.ErrName)
	}
	release := apitest.HoldWriteLock(t, e.storePath, time.Minute)
	start := time.Now()
	data := toolErrorData(t, callTool(t, e.d, "decide", paramJSON(t, map[string]any{"claude_instance_id": busyID,
		"request_token": busyTok, "decision": "allow", "max_wait_ms": 200})))
	took := time.Since(start)
	release()
	if data.ErrName != "ErrStoreBusy" || took > 200*time.Millisecond+time.Second {
		t.Errorf("decide under a held lock = %q after %v; want ErrStoreBusy within 1.2s", data.ErrName, took)
	}
	res := callToolText(t, e.d, "get-permission", paramJSON(t, map[string]any{"request_token": busyTok}))
	if string(res["decision"]) != "null" {
		t.Errorf("decision after ErrStoreBusy = %s; want null (nothing recorded)", res["decision"])
	}
}
