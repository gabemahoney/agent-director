package mcp_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// newSpawnInputServer returns a live dispatcher over a Client with an isolated
// store, an empty config and a tmuxfix.Recorder, plus the Client and Recorder.
func newSpawnInputServer(t *testing.T) (mcp.Dispatcher, *api.Client, *tmuxfix.Recorder) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	rec := tmuxfix.NewRecorder()
	client, err := api.New(api.Options{
		StorePath:       filepath.Join(dir, "state.db"),
		ConfigPath:      cfgPath,
		CreateIfMissing: true,
		TmuxClient:      rec,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mcp.NewLiveDispatcher(client), client, rec
}

// callSpawn sends one tools/call spawn request with cwd and id through the
// JSON-RPC server and returns the response.
func callSpawn(t *testing.T, d mcp.Dispatcher, cwd, id string) *mcp.Response {
	t.Helper()
	args, err := json.Marshal(map[string]string{"cwd": cwd, "claude_instance_id": id})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	params, err := json.Marshal(map[string]any{"name": "spawn", "arguments": json.RawMessage(args)})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return runOne(t, d, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  params,
	})
}

// TestToolsCallSpawnRejectsControlCharacterID drives tools/call spawn with
// control-character ids (SR-9.1, AC-SPN-02): ErrInvalidFlags, no row, no tmux.
func TestToolsCallSpawnRejectsControlCharacterID(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"newline", "mcp-head\nmcp-tail"},
		{"tab", "mcp-head\tmcp-tail"},
		{"del", "mcp-head\x7fmcp-tail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, client, rec := newSpawnInputServer(t)
			resp := callSpawn(t, d, t.TempDir(), tc.id)

			if resp == nil || resp.Error == nil {
				t.Fatalf("tools/call spawn succeeded; want an ErrInvalidFlags error: %+v", resp)
			}
			dataBody, _ := json.Marshal(resp.Error.Data)
			var data mcp.ToolErrorData
			if err := json.Unmarshal(dataBody, &data); err != nil {
				t.Fatalf("parse error data: %v", err)
			}
			if data.ErrName != "ErrInvalidFlags" {
				t.Errorf("err_name = %q; want ErrInvalidFlags", data.ErrName)
			}
			if !strings.Contains(data.ErrDescription, "the instance id contains a control character") {
				t.Errorf("err_description = %q; want the control-character phrase", data.ErrDescription)
			}
			for _, text := range []string{data.ErrDescription, resp.Error.Message} {
				if strings.Contains(text, tc.id) || strings.Contains(text, "mcp-head") || strings.Contains(text, "mcp-tail") {
					t.Errorf("error text %q contains the instance id", text)
				}
			}
			if n, s := len(rec.Calls()), len(rec.SocketCalls()); n != 0 || s != 0 {
				t.Errorf("tmux calls = %d name-based, %d socket; want none", n, s)
			}
			list, err := client.List(api.ListParams{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(list.Spawns) != 0 {
				t.Errorf("store rows = %d; want 0", len(list.Spawns))
			}
		})
	}
}

// TestToolsCallSpawnPlainIDControl is the control for the rejection test: the
// same harness with a plain id creates one row and one tmux session.
func TestToolsCallSpawnPlainIDControl(t *testing.T) {
	d, client, rec := newSpawnInputServer(t)
	resp := callSpawn(t, d, t.TempDir(), "mcp-plain-id")

	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/call spawn failed: %+v", resp)
	}
	if n := len(rec.CallsOfKind(tmuxfix.CallNewSession)); n != 1 {
		t.Errorf("NewSession calls = %d; want 1", n)
	}
	list, err := client.List(api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Spawns) != 1 || list.Spawns[0].ClaudeInstanceID != "mcp-plain-id" {
		t.Errorf("store rows = %+v; want one row for mcp-plain-id", list.Spawns)
	}
}
