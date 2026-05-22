package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
	"github.com/gabemahoney/agent-director/internal/mcp"
)

// fakeDispatcher is the test-side dispatcher. It records every call
// and returns a programmable result/error so the framing tests can
// drive both code paths without spinning up the live api wiring.
type fakeDispatcher struct {
	calls    []dispatchedCall
	result   any
	err      error
}

type dispatchedCall struct {
	name string
	args json.RawMessage
}

func (f *fakeDispatcher) Call(_ context.Context, name string, args json.RawMessage) (any, error) {
	f.calls = append(f.calls, dispatchedCall{name: name, args: args})
	return f.result, f.err
}

// runOne is a tiny driver: write one JSON-RPC request to a buffer,
// run Serve until stdin EOF, parse the response off stdout. Returns
// the parsed Response object (nil if the message was a notification
// and produced no response).
func runOne(t *testing.T, d mcp.Dispatcher, req mcp.Request) *mcp.Response {
	t.Helper()
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	if err := json.NewEncoder(in).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	srv := mcp.New(d, nil)
	if err := srv.Serve(context.Background(), in, out); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Serve: %v", err)
	}
	if out.Len() == 0 {
		return nil
	}
	var resp mcp.Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("parse response: %v\nraw=%s", err, out.String())
	}
	return &resp
}

func TestInitializeReturnsProtocolVersion(t *testing.T) {
	resp := runOne(t, &fakeDispatcher{}, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})
	if resp == nil {
		t.Fatalf("nil response")
	}
	if resp.Error != nil {
		t.Fatalf("Error = %+v; want nil", resp.Error)
	}
	body, _ := json.Marshal(resp.Result)
	var init mcp.InitializeResult
	if err := json.Unmarshal(body, &init); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if init.ProtocolVersion != mcp.ProtocolVersion {
		t.Errorf("protocolVersion = %q; want %q", init.ProtocolVersion, mcp.ProtocolVersion)
	}
	if init.ServerInfo.Name != mcp.ServerName {
		t.Errorf("serverInfo.name = %q; want %q", init.ServerInfo.Name, mcp.ServerName)
	}
}

func TestNotificationsInitializedProducesNoResponse(t *testing.T) {
	// JSON-RPC notifications (no id field) MUST NOT produce a response.
	resp := runOne(t, &fakeDispatcher{}, mcp.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if resp != nil {
		t.Errorf("notification produced a response: %+v", resp)
	}
}

func TestToolsListMatchesManifest(t *testing.T) {
	resp := runOne(t, &fakeDispatcher{}, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	var got struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	body, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}

	// Count expected = manifest minus the filtered verbs (hook, serve).
	expectedNames := map[string]bool{}
	for _, v := range manifest.Verbs {
		if mcp.ExposedVerb(v.Name) {
			expectedNames[mcp.ToolName(v.Name)] = true
		}
	}
	if len(got.Tools) != len(expectedNames) {
		t.Errorf("len(tools) = %d; want %d", len(got.Tools), len(expectedNames))
	}

	// Each manifest verb must appear in the tool list.
	have := map[string]bool{}
	for _, tool := range got.Tools {
		have[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %s has empty description (manifest drift?)", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %s has nil inputSchema", tool.Name)
		}
	}
	for name := range expectedNames {
		if !have[name] {
			t.Errorf("tool %s in manifest but missing from tools/list", name)
		}
	}
}

func TestToolsListFiltersInternalVerbs(t *testing.T) {
	// hook and serve must NOT appear — they're internal/self-referential.
	resp := runOne(t, &fakeDispatcher{}, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})
	body, _ := json.Marshal(resp.Result)
	if strings.Contains(string(body), `"hook"`) {
		t.Errorf("tools/list exposes hook: %s", body)
	}
	if strings.Contains(string(body), `"serve"`) {
		t.Errorf("tools/list exposes serve: %s", body)
	}
}

func TestToolsCallRoutesToDispatcher(t *testing.T) {
	d := &fakeDispatcher{result: map[string]any{"claude_instance_id": "id-x"}}
	resp := runOne(t, d, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"spawn","arguments":{"cwd":"/tmp"}}`),
	})
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/call failed: %+v", resp)
	}
	if len(d.calls) != 1 {
		t.Fatalf("dispatcher calls = %d; want 1", len(d.calls))
	}
	if d.calls[0].name != "spawn" {
		t.Errorf("dispatcher.name = %q; want spawn", d.calls[0].name)
	}
	if !strings.Contains(string(d.calls[0].args), `"cwd":"/tmp"`) {
		t.Errorf("dispatcher.args lost the cwd: %s", d.calls[0].args)
	}
}

func TestToolsCallSuccessResultShape(t *testing.T) {
	// MCP success envelope: {content: [{type: "text", text: <json>}]}.
	d := &fakeDispatcher{result: map[string]any{"claude_instance_id": "id-y"}}
	resp := runOne(t, d, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"spawn","arguments":{}}`),
	})
	body, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(body), `"type":"text"`) {
		t.Errorf("missing type=text content: %s", body)
	}
	if !strings.Contains(string(body), `"text":"{\"claude_instance_id\":\"id-y\"}"`) {
		t.Errorf("text field doesn't carry JSON-encoded result: %s", body)
	}
}

func TestToolsCallErrorCarriesErrName(t *testing.T) {
	// A typed sentinel registered via RegisterError must surface in
	// the response's error.data.err_name field.
	sentinel := errors.New("ErrCustomSentinel")
	mcp.RegisterError("ErrCustomSentinel", sentinel)
	d := &fakeDispatcher{err: sentinel}
	resp := runOne(t, d, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"spawn","arguments":{}}`),
	})
	if resp.Error == nil {
		t.Fatalf("expected error response")
	}
	dataBody, _ := json.Marshal(resp.Error.Data)
	var data mcp.ToolErrorData
	if err := json.Unmarshal(dataBody, &data); err != nil {
		t.Fatalf("parse data: %v", err)
	}
	if data.ErrName != "ErrCustomSentinel" {
		t.Errorf("err_name = %q; want ErrCustomSentinel", data.ErrName)
	}
}

func TestToolsCallUnregisteredErrorFallsBackToInternal(t *testing.T) {
	d := &fakeDispatcher{err: errors.New("some unregistered error")}
	resp := runOne(t, d, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"spawn","arguments":{}}`),
	})
	dataBody, _ := json.Marshal(resp.Error.Data)
	var data mcp.ToolErrorData
	_ = json.Unmarshal(dataBody, &data)
	if data.ErrName != "ErrInternal" {
		t.Errorf("err_name = %q; want ErrInternal (unregistered fallback)", data.ErrName)
	}
}

func TestUnknownMethodReturnsMethodNotFound(t *testing.T) {
	resp := runOne(t, &fakeDispatcher{}, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "totally/made_up",
	})
	if resp.Error == nil {
		t.Fatalf("expected error for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error.code = %d; want -32601 (method not found)", resp.Error.Code)
	}
}

func TestParseErrorReturnsResponse(t *testing.T) {
	// A bad line on stdin shouldn't tear down the session — the server
	// emits a -32700 parse error and keeps reading. Drive this with a
	// raw byte stream containing two messages: one malformed, one valid.
	in := strings.NewReader("not-json\n" +
		`{"jsonrpc":"2.0","id":2,"method":"initialize"}` + "\n")
	out := &bytes.Buffer{}
	srv := mcp.New(&fakeDispatcher{}, nil)
	if err := srv.Serve(context.Background(), in, out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d response lines; want 2", len(lines))
	}
	var first mcp.Response
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("parse first: %v", err)
	}
	if first.Error == nil || first.Error.Code != -32700 {
		t.Errorf("first response code = %v; want -32700 parse error", first.Error)
	}
	var second mcp.Response
	if err := json.Unmarshal(lines[1], &second); err != nil {
		t.Fatalf("parse second: %v", err)
	}
	if second.Error != nil {
		t.Errorf("second (valid) request errored: %+v", second.Error)
	}
}

func TestToolNameMapping(t *testing.T) {
	// MCP tool names use underscores; manifest verb names use hyphens.
	// The mapping is symmetric for every current verb.
	for _, v := range manifest.Verbs {
		toolName := mcp.ToolName(v.Name)
		if strings.ContainsRune(toolName, '-') {
			t.Errorf("tool name %q contains hyphen (should be underscore)", toolName)
		}
		round := mcp.VerbNameFromTool(toolName)
		if round != v.Name {
			t.Errorf("round-trip %q → %q → %q (mismatch)", v.Name, toolName, round)
		}
	}
}

func TestUnknownToolReturnsErrUnknownTool(t *testing.T) {
	// The MCP server's dispatcher (LiveDispatcher) returns ErrUnknownTool
	// when the tool name isn't in its switch. The fake dispatcher
	// doesn't replicate that, so this test pins via the live dispatcher
	// directly. We construct one with a nil client because the
	// unknown-tool path short-circuits before any client method is called.
	d := mcp.NewLiveDispatcher(nil)
	_, err := d.Call(context.Background(), "totally_made_up_tool", json.RawMessage(`{}`))
	if !errors.Is(err, mcp.ErrUnknownTool) {
		t.Fatalf("err = %v; want ErrUnknownTool", err)
	}
}
