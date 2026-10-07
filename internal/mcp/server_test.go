package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestServeFraming: a malformed line is answered with -32700 and the session
// keeps reading; a notification (no id) gets no response; initialize returns
// the protocol version and server name; an unknown method is -32601.
func TestServeFraming(t *testing.T) {
	in := strings.NewReader("not-json\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"totally/made_up"}` + "\n")
	out := &bytes.Buffer{}
	if err := mcp.New(&fakeDispatcher{}, nil).Serve(context.Background(), in, out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("got %d response lines; want 3 (none for the notification):\n%s", len(lines), out)
	}
	var resp [3]mcp.Response
	for i, l := range lines {
		if err := json.Unmarshal(l, &resp[i]); err != nil {
			t.Fatalf("parse response %d: %v", i, err)
		}
	}
	if resp[0].Error == nil || resp[0].Error.Code != -32700 {
		t.Errorf("malformed line: error = %+v; want -32700", resp[0].Error)
	}
	body, _ := json.Marshal(resp[1].Result)
	var init mcp.InitializeResult
	if err := json.Unmarshal(body, &init); err != nil || resp[1].Error != nil ||
		init.ProtocolVersion != mcp.ProtocolVersion || init.ServerInfo.Name != mcp.ServerName {
		t.Errorf("initialize = %s (error %+v); want protocolVersion %q, serverInfo.name %q", body, resp[1].Error, mcp.ProtocolVersion, mcp.ServerName)
	}
	if resp[2].Error == nil || resp[2].Error.Code != -32601 {
		t.Errorf("unknown method: error = %+v; want -32601 (method not found)", resp[2].Error)
	}
}

// TestToolsListMatchesManifest: tools/list is exactly the manifest's exposed
// verbs (not hook, serve or trail-emit; no delete, an agent-director-admin
// verb only, b.vqr), each with a description, and nothing in it names an
// operator action (SR-6.8). TestMCPParamToolsListNames pins the schemas.
func TestToolsListMatchesManifest(t *testing.T) {
	tools, body := toolsList(t)
	var got, want []string
	listed := map[string]bool{}
	for _, tool := range tools {
		got, listed[tool.Name] = append(got, tool.Name), true
		if tool.Description == "" {
			t.Errorf("tool %s has an empty description", tool.Name)
		}
	}
	for _, v := range exposedVerbs() {
		want = append(want, mcp.ToolName(v.Name))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tools = %v; want the manifest's exposed verbs %v", got, want)
	}
	for _, name := range []string{"hook", "serve", "trail-emit", "delete"} {
		if listed[mcp.ToolName(name)] {
			t.Errorf("tools/list exposes %s", name)
		}
	}
	if m := apitest.OperatorActionNames.FindString(body); m != "" {
		t.Errorf("SR-6.8: tools/list names the operator action %q: %s", m, body)
	}
}

// TestToolsCallFraming: tools/call hands the tool name and arguments to the
// dispatcher; a result is one text part holding its JSON; an error is
// JSON-RPC -32000 whose data carries the catalog err_name of a wrapped
// sentinel, or ErrInternal for an unregistered error.
func TestToolsCallFraming(t *testing.T) {
	d := &fakeDispatcher{result: map[string]any{"claude_instance_id": "id-y"}}
	if obj := callToolText(t, d, "spawn", `{"cwd":"/tmp"}`); string(obj["claude_instance_id"]) != `"id-y"` {
		t.Errorf("result = %v; want the dispatcher's JSON", obj)
	}
	if len(d.calls) != 1 || d.calls[0].name != "spawn" || string(d.calls[0].args) != `{"cwd":"/tmp"}` {
		t.Errorf("dispatcher calls = %+v; want one spawn call with the arguments", d.calls)
	}
	for err, want := range map[error]string{
		errors.Join(spawn.ErrCwdMissing, errors.New("context detail")): "ErrCwdMissing",
		errors.New("some unregistered error"):                          "ErrInternal",
	} {
		resp := callTool(t, &fakeDispatcher{err: err}, "spawn", `{}`)
		if data := toolErrorData(t, resp); data.ErrName != want || resp.Error.Code != -32000 {
			t.Errorf("error %v: code %d err_name %q; want -32000 %s", err, resp.Error.Code, data.ErrName, want)
		}
	}
}

// TestToolNameMapping: tool names use underscores where verbs use hyphens, and
// ToolName and VerbNameFromTool round-trip every verb.
func TestToolNameMapping(t *testing.T) {
	for _, v := range manifest.Verbs {
		toolName := mcp.ToolName(v.Name)
		if strings.ContainsRune(toolName, '-') || mcp.VerbNameFromTool(toolName) != v.Name {
			t.Errorf("%q → %q → %q; want no hyphen and the verb back", v.Name, toolName, mcp.VerbNameFromTool(toolName))
		}
	}
}

// TestDeleteToolCallIsUnknownTool (b.vqr): a delete call through the live
// dispatcher gets the same error as a tool that does not exist, and its row stays.
func TestDeleteToolCallIsUnknownTool(t *testing.T) {
	e := newEnv(t)
	const id = "id-vqr-delete"
	seedFinished(t, e, id, store.StateEnded)

	ref, resp := callTool(t, e.d, "no_such_tool", `{}`), callTool(t, e.d, "delete", `{"claude_instance_id":["`+id+`"]}`)

	refData, data := toolErrorData(t, ref), toolErrorData(t, resp)
	if refData.ErrName != "ErrUnknownTool" {
		t.Fatalf("no_such_tool err_name = %q; want ErrUnknownTool (precondition)", refData.ErrName)
	}
	if want := strings.ReplaceAll(refData.ErrDescription, "no_such_tool", "delete"); resp.Error.Code != ref.Error.Code ||
		data.ErrName != refData.ErrName || data.ErrDescription != want {
		t.Errorf("delete error = code %d %+v; want code %d %s %q", resp.Error.Code, data, ref.Error.Code, refData.ErrName, want)
	}
	readColumns(t, e.storePath, id)
}

// TestHelpToolOmitsKillOptIn (SR-6.8, b.vqr): the MCP help tool lists kill and
// never names its former opt-in, the admin binary or its kill-finished verb.
func TestHelpToolOmitsKillOptIn(t *testing.T) {
	obj := callToolText(t, mcp.NewLiveDispatcher(nil), "help", `{}`) // help reads only the manifest
	body, _ := json.Marshal(obj)
	if m := apitest.OperatorActionNames.FindString(string(body)); m != "" {
		t.Errorf("SR-6.8: MCP help names the operator action %q: %s", m, body)
	}
	var verbs []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(obj["verbs"], &verbs); err != nil {
		t.Fatalf("parse help verbs: %v", err)
	}
	for _, v := range verbs {
		if v.Name == "kill" {
			return
		}
	}
	t.Errorf("MCP help does not list kill; the SR-6.8 check would pass vacuously: %+v", verbs)
}
