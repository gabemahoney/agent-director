package mcp_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// mcpLaunchShape is one seeded row shape; want is the exact wire value of
// launch_started_at, or "" when the key must be absent.
type mcpLaunchShape struct {
	name, id, state string
	opt             apitest.SpawnOption
	want            string
}

var mcpLaunchShapes = []mcpLaunchShape{
	{"pending ms fraction", "mls-pending", "pending", apitest.WithLaunchStartedAt(1790000000123), "2026-09-21T14:13:20.123Z"},
	{"waiting with launch start", "mls-waiting", "waiting", apitest.WithLaunchStartedAt(1790000000123), ""},
	{"pending non-integer", "mls-pending-raw", "pending", apitest.WithRawLaunchStartedAt("not-a-number"), ""},
	{"pending NULL", "mls-pending-null", "pending", apitest.WithNoLaunchStartedAt(), ""},
}

// newLaunchShapesDispatcher seeds every mcpLaunchShapes row into a temp store
// and returns a live dispatcher over a real Client on it.
func newLaunchShapesDispatcher(t *testing.T) mcp.Dispatcher {
	t.Helper()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state.db")
	cfgPath := filepath.Join(dir, "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	for _, sh := range mcpLaunchShapes {
		if _, err := apitest.SeedSpawn(storePath, sh.id, sh.state, "/tmp", "off", "", true, sh.opt); err != nil {
			t.Fatalf("seed %s: %v", sh.id, err)
		}
	}
	client, err := api.New(api.Options{StorePath: storePath, ConfigPath: cfgPath, CreateIfMissing: true})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mcp.NewLiveDispatcher(client)
}

// callToolText sends one tools/call through the server, requires a success
// response and returns the decoded JSON text content as raw keys.
func callToolText(t *testing.T, d mcp.Dispatcher, tool, args string) map[string]json.RawMessage {
	t.Helper()
	resp := runOne(t, d, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"` + tool + `","arguments":` + args + `}`),
	})
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/call %s %s failed: %+v", tool, args, resp)
	}
	body, _ := json.Marshal(resp.Result)
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Content) != 1 || env.Content[0].Type != "text" {
		t.Fatalf("tools/call %s result = %s; want one text content part", tool, body)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(env.Content[0].Text), &obj); err != nil {
		t.Fatalf("parse %s text %q: %v", tool, env.Content[0].Text, err)
	}
	return obj
}

// mcpVerbObject returns status/get's result for id, or id's row from list.
func mcpVerbObject(t *testing.T, d mcp.Dispatcher, verb, id string) map[string]json.RawMessage {
	t.Helper()
	if verb != "list" {
		return callToolText(t, d, verb, `{"claude_instance_id":"`+id+`"}`)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(callToolText(t, d, "list", `{}`)["spawns"], &rows); err != nil {
		t.Fatalf("parse list spawns: %v", err)
	}
	for _, row := range rows {
		if string(row["claude_instance_id"]) == `"`+id+`"` {
			return row
		}
	}
	t.Fatalf("list has no row %q", id)
	return nil
}

// TestLaunchStartedAtMCP pins SR-22.2/SR-5.5 on MCP: status, get and list carry
// launch_started_at only for a readable pending row; no case errors.
func TestLaunchStartedAtMCP(t *testing.T) {
	d := newLaunchShapesDispatcher(t)
	for _, verb := range []string{"status", "get", "list"} {
		for _, sh := range mcpLaunchShapes {
			t.Run(verb+"/"+sh.name, func(t *testing.T) {
				raw, present := mcpVerbObject(t, d, verb, sh.id)["launch_started_at"]
				switch {
				case sh.want == "" && present:
					t.Errorf("launch_started_at = %s; want key absent", raw)
				case sh.want != "" && string(raw) != `"`+sh.want+`"`:
					t.Errorf("launch_started_at = %s (present=%v); want %q", raw, present, sh.want)
				}
			})
		}
	}
}
