package mcp_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.fji literal-follow tests for the MCP server's own advice (inventory
// section I): trigger the refusal, assert the advice, re-send the request the
// way the text says and assert it is dispatched.

// advCLIServer returns a live dispatcher over an isolated store holding one
// ended row per id in ended, with a tmuxfix.Recorder and no tmux environment.
func advCLIServer(t *testing.T, ended ...string) (mcp.Dispatcher, *api.Client) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	dir := t.TempDir()
	storePath, cfgPath := filepath.Join(dir, "state.db"), filepath.Join(dir, "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	for _, id := range ended {
		if _, err := apitest.SeedSpawn(storePath, id, store.StateEnded, "/tmp", "off", "", true); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	client, err := api.New(api.Options{StorePath: storePath, ConfigPath: cfgPath, CreateIfMissing: true,
		TmuxClient: tmuxfix.NewRecorder()})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mcp.NewLiveDispatcher(client), client
}

// advCLICall sends one tools/call with params and returns the response.
func advCLICall(t *testing.T, d mcp.Dispatcher, params map[string]any) *mcp.Response {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp := runOne(t, d, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: body})
	if resp == nil {
		t.Fatalf("tools/call %s gave no response", body)
	}
	return resp
}

// advCLIRefused fails unless resp is an error whose message carries want.
func advCLIRefused(t *testing.T, resp *mcp.Response, want string) {
	t.Helper()
	if resp.Error == nil {
		t.Fatalf("tools/call succeeded (%+v); want a refusal carrying %q", resp.Result, want)
	}
	if !strings.Contains(resp.Error.Message, want) {
		t.Fatalf("error message %q lacks %q", resp.Error.Message, want)
	}
}

// advCLIResult asserts resp is a success and decodes its one text part into v.
func advCLIResult(t *testing.T, resp *mcp.Response, v any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("followed call refused: %+v", resp.Error)
	}
	body, _ := json.Marshal(resp.Result)
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Content) != 1 {
		t.Fatalf("result = %s; want one text content part", body)
	}
	if err := json.Unmarshal([]byte(env.Content[0].Text), v); err != nil {
		t.Fatalf("decode result text %q: %v", env.Content[0].Text, err)
	}
}

// TestAdviceFollow_I1_ToolsCallNameRequired: I1 "tools/call: name is required".
func TestAdviceFollow_I1_ToolsCallNameRequired(t *testing.T) {
	d, _ := advCLIServer(t, "advcli-listed")
	params := map[string]any{"arguments": map[string]any{}}
	resp := advCLICall(t, d, params)
	advCLIRefused(t, resp, "tools/call: name is required")
	if resp.Error.Code != -32602 {
		t.Errorf("code = %d; want -32602", resp.Error.Code)
	}

	params["name"] = "list"
	var list api.ListResult
	advCLIResult(t, advCLICall(t, d, params), &list)
	if len(list.Spawns) != 1 || list.Spawns[0].ClaudeInstanceID != "advcli-listed" {
		t.Errorf("list = %+v; want the one seeded row", list.Spawns)
	}
}

// TestAdviceFollow_I2_DeleteIDsRequired: I2 "delete: claude_instance_id is required (≥1)".
func TestAdviceFollow_I2_DeleteIDsRequired(t *testing.T) {
	const id = "advcli-delete-me"
	for name, args := range map[string]map[string]any{
		"absent": {},
		"empty":  {"claude_instance_id": []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			d, client := advCLIServer(t, id)
			params := map[string]any{"name": "delete", "arguments": args}
			advCLIRefused(t, advCLICall(t, d, params), "delete: claude_instance_id is required (≥1)")
			if _, err := client.Get(id); err != nil {
				t.Fatalf("refused delete touched the row: Get: %v", err)
			}

			args["claude_instance_id"] = []string{id}
			var res api.DeleteResult
			advCLIResult(t, advCLICall(t, d, params), &res)
			if res.Results[id] != "ok" {
				t.Errorf("results = %v; want %s: ok", res.Results, id)
			}
			if _, err := client.Get(id); !errors.Is(err, api.ErrSpawnNotFound) {
				t.Errorf("Get after delete: %v; want ErrSpawnNotFound", err)
			}
		})
	}
}

// TestAdviceFollow_I3_InvalidLabelWantKeyValue: I3 "invalid label %q (want key=value)".
func TestAdviceFollow_I3_InvalidLabelWantKeyValue(t *testing.T) {
	cases := []struct {
		tool  string
		args  func(t *testing.T) map[string]any
		check func(t *testing.T, client *api.Client, resp *mcp.Response)
	}{
		{
			tool: "spawn",
			args: func(t *testing.T) map[string]any {
				return map[string]any{"cwd": t.TempDir(), "claude_instance_id": "advcli-labelled"}
			},
			check: func(t *testing.T, client *api.Client, resp *mcp.Response) {
				var res api.SpawnResult
				advCLIResult(t, resp, &res)
				row, err := client.Get("advcli-labelled")
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if row.Labels["foo"] != "bar" {
					t.Errorf("labels = %v; want foo=bar", row.Labels)
				}
			},
		},
		{
			tool: "make_template",
			args: func(*testing.T) map[string]any { return map[string]any{"name": "advcli-labelled"} },
			check: func(t *testing.T, _ *api.Client, resp *mcp.Response) {
				var res api.MakeTemplateResult
				advCLIResult(t, resp, &res)
				body, err := os.ReadFile(res.Path)
				if err != nil {
					t.Fatalf("read template: %v", err)
				}
				if !strings.Contains(string(body), "foo") || !strings.Contains(string(body), "bar") {
					t.Errorf("template %q lacks the label foo=bar", body)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			d, client := advCLIServer(t)
			args := tc.args(t)
			args["label"] = []string{"foo"}
			params := map[string]any{"name": tc.tool, "arguments": args}
			advCLIRefused(t, advCLICall(t, d, params), `invalid label "foo" (want key=value)`)

			args["label"] = []string{"foo=bar"}
			tc.check(t, client, advCLICall(t, d, params))
		})
	}
}
