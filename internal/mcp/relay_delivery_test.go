package mcp_test

// relay_delivery_test.go — b.146 steps 2 and 2b through MCP: decide's
// max_wait_ms parameter (decision 9 B), the delivery facts on decide's result,
// get-permission's and each request of get's and list's rows, and a refusal's
// err_details (rule 15).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// mcpDeliveryKeys are a permission request's delivery facts (b.146 rule 15),
// pane_answer and pane_as included (step 2b).
var mcpDeliveryKeys = []string{"delivery", "confirm_by", "hook_alive", "hook_gone_at", "attempted_decision", "attempted_at", "tool_use_id",
	"pane_answer", "pane_as"}

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

// TestMCPErrDetails (b.146 rule 15, decision 8 A): a refusal that carries
// facts returns them as the error data's err_details object (here
// record-pane-answer's ErrClaimTooSoon on a request recorded before schema v7
// inside its relay window, whose relay hook cannot be checked: hook_alive and
// hook_gone_at null, not_before its confirm_by plus 2 s); an error without
// them has no err_details key.
func TestMCPErrDetails(t *testing.T) {
	e := newEnv(t)
	tok := e.seedRelayRequest(t, "mcp-err-details")
	args := func(token string) string {
		return paramJSON(t, map[string]any{"request_token": token, "as": "unknown", "expect_pane_sha256": strings.Repeat("0", 64)})
	}

	data := toolErrorData(t, callTool(t, e.d, "record-pane-answer", args(tok)))

	details, ok := data.ErrDetails.(map[string]any)
	if data.ErrName != "ErrClaimTooSoon" || !ok || details["request_token"] != tok {
		t.Fatalf("record-pane-answer error = %q, err_details %#v; want ErrClaimTooSoon naming %s", data.ErrName, data.ErrDetails, tok)
	}
	for _, k := range []string{"hook_alive", "hook_gone_at"} {
		if v, present := details[k]; !present || v != nil {
			t.Errorf("err_details[%q] = %v (present %v); want null", k, v, present)
		}
	}
	var confirmBy time.Time
	perm := callToolText(t, e.d, "get-permission", paramJSON(t, map[string]any{"request_token": tok}))
	notBefore, isText := details["not_before"].(string)
	got, err := time.Parse(time.RFC3339Nano, notBefore)
	if json.Unmarshal(perm["confirm_by"], &confirmBy) != nil || !isText || err != nil || !got.Equal(confirmBy.Add(2*time.Second)) {
		t.Errorf("err_details not_before = %v; want get-permission's confirm_by %s plus 2 s", details["not_before"], perm["confirm_by"])
	}
	resp := callTool(t, e.d, "record-pane-answer", args("bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"))
	raw, _ := json.Marshal(resp.Error.Data)
	if data := toolErrorData(t, resp); data.ErrName != "ErrPermissionRequestNotFound" || strings.Contains(string(raw), "err_details") {
		t.Errorf("unknown token's error data = %s; want ErrPermissionRequestNotFound with no err_details key", raw)
	}
}

// TestMCPPaneAnswerKeySent (b.146 rule 8, rule 15): a pane answer through the
// send-keys tool whose sent write fails after its one key (no Enter) returns
// ErrInternal with err_details {key_sent: true, request_token}, so a caller
// tells it from one that sent nothing.
func TestMCPPaneAnswerKeySent(t *testing.T) {
	e := newEnv(t)
	const id = "mcp-pane-answer"
	if _, err := apitest.SeedSpawn(e.storePath, id, store.StateCheckPermission, t.TempDir(), "on", "", false); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	req, err := apitest.SeedPermissionRequest(e.storePath, id, "Bash")
	if err == nil { // past the relay window: fallen back
		err = apitest.AgePermissionRequest(e.storePath, req.RequestID, 48*time.Hour, 0)
	}
	if err != nil {
		t.Fatalf("seed a fallen-back request: %v", err)
	}
	e.rec.SeedRowSession(t, e.storePath, id)
	const pane = "Allow Bash? 1 yes, 2 no\n"
	e.rec.SetCapture(apitest.TestSocket, apitest.TestPaneID, pane)
	storefix.InjectWriteFailure(t, e.storePath, storefix.WriteFailPermissionDecision, id)
	sum := sha256.Sum256([]byte(pane))

	data := toolErrorData(t, callTool(t, e.d, "send-keys", paramJSON(t, map[string]any{"claude_instance_id": id,
		"request_token": req.RequestToken, "as": "allow", "key": "1", "expect_pane_sha256": hex.EncodeToString(sum[:])})))

	want := map[string]any{"key_sent": true, "request_token": req.RequestToken}
	if details, _ := data.ErrDetails.(map[string]any); data.ErrName != "ErrInternal" || fmt.Sprint(details) != fmt.Sprint(want) {
		t.Errorf("send-keys error = %q (%s), err_details %#v; want ErrInternal, %v", data.ErrName, data.ErrDescription, data.ErrDetails, want)
	}
	sent := e.rec.SocketCallsOf(tmux.CallSendText)
	if len(sent) != 1 || sent[0].Text != "1" || sent[0].PressEnter || len(e.rec.SocketCallsOf(tmux.CallSendEnter)) != 0 {
		t.Errorf("text calls = %+v, Enter calls %d; want the key 1 alone, no Enter", sent, len(e.rec.SocketCallsOf(tmux.CallSendEnter)))
	}
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
