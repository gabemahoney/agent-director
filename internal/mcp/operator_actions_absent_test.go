package mcp_test

// operator_actions_absent_test.go pins b.vqr over MCP: tools/list has no
// delete tool, and a delete call is answered exactly as any unknown tool and
// removes nothing. That tools/list names no operator action is
// TestToolsListOmitsKillOptIn (server_test.go).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestToolsListHasNoDelete: tools/list offers no delete tool, while it still
// offers kill.
func TestToolsListHasNoDelete(t *testing.T) {
	resp := runOne(t, &fakeDispatcher{}, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"})
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	body, _ := json.Marshal(resp.Result)
	var got struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}
	var hasKill bool
	for _, tool := range got.Tools {
		hasKill = hasKill || tool.Name == "kill"
		if tool.Name == "delete" {
			t.Error("tools/list offers a delete tool; delete is an agent-director-admin verb only")
		}
	}
	if !hasKill {
		t.Error("tools/list has no kill tool; the checks above would pass vacuously")
	}
}

// TestDeleteToolCallIsUnknownTool: a delete call through the live dispatcher
// gets the same error as a tool that does not exist, and its row stays.
func TestDeleteToolCallIsUnknownTool(t *testing.T) {
	e := newPreTrustEnv(t)
	const id = "id-vqr-delete"
	seedFinished(t, e, id, store.StateEnded)

	ref := callTool(t, e.d, "no_such_tool", `{}`)
	resp := callTool(t, e.d, "delete", `{"claude_instance_id":["`+id+`"]}`)

	if ref == nil || ref.Error == nil {
		t.Fatalf("no_such_tool call = %+v; want the unknown-tool error", ref)
	}
	refData := toolErrorData(t, ref)
	if refData.ErrName != "ErrUnknownTool" {
		t.Fatalf("no_such_tool err_name = %q; want ErrUnknownTool (precondition)", refData.ErrName)
	}
	if resp == nil || resp.Error == nil {
		t.Errorf("delete call succeeded: %+v; want the unknown-tool error", resp)
	} else {
		data := toolErrorData(t, resp)
		want := strings.ReplaceAll(refData.ErrDescription, "no_such_tool", "delete")
		if resp.Error.Code != ref.Error.Code || data.ErrName != refData.ErrName || data.ErrDescription != want {
			t.Errorf("delete error = code %d %+v; want code %d {ErrName:%s ErrDescription:%s}",
				resp.Error.Code, data, ref.Error.Code, refData.ErrName, want)
		}
	}
	if _, err := apitest.ReadSpawnColumns(e.storePath, id); err != nil {
		t.Errorf("row %s after the delete call: %v; want it kept", id, err)
	}
}
