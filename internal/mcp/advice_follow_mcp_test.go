package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	if len(list.Spawns) != 1 || list.Spawns[0].InstanceID != "advcli-listed" {
		t.Errorf("list = %+v; want the one seeded row", list.Spawns)
	}
}

// TestAdviceFollow_I3_InvalidLabelWantKeyValue: I3 "%s: parameter "label" entry
// %q must be key=value", ErrInvalidFlags with nothing written (b.anw).
func TestAdviceFollow_I3_InvalidLabelWantKeyValue(t *testing.T) {
	cases := []struct {
		tool, verb string
		args       func(t *testing.T) map[string]any
		check      func(t *testing.T, client *api.Client, resp *mcp.Response)
	}{
		{
			tool: "spawn", verb: "spawn",
			args: func(t *testing.T) map[string]any {
				return map[string]any{"cwd": t.TempDir(), "agent": "claude", "instance_id": "advcli-labelled"}
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
			tool: "make_template", verb: "make-template",
			args: func(*testing.T) map[string]any { return map[string]any{"name": "advcli-labelled", "agent": "claude"} },
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
			params := map[string]any{"name": tc.tool, "arguments": args}
			for bad, labels := range map[string][]string{"foo": {"foo"}, "=v": {"k=v", "=v"}} {
				args["label"] = labels
				resp := advCLICall(t, d, params)
				advCLIRefused(t, resp, `ErrInvalidFlags: `+tc.verb+`: parameter "label" entry `+strconv.Quote(bad)+` must be key=value`)
				if data := toolErrorData(t, resp); data.ErrName != "ErrInvalidFlags" {
					t.Errorf("label %q: err_name = %q; want ErrInvalidFlags", labels, data.ErrName)
				}
			}
			if ids := rowIDs(t, d); len(ids) != 0 {
				t.Errorf("rows after the refusals = %v; want none", ids)
			}
			if tpls, _ := os.ReadDir(filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "templates")); len(tpls) != 0 {
				t.Errorf("templates after the refusals = %v; want none", tpls)
			}

			args["label"] = []string{"foo=bar"}
			tc.check(t, client, advCLICall(t, d, params))
		})
	}
}

// TestAdviceFollow_I6_OlderThanDurationForm: I6 "expire: parameter "older_than"
// value %q must be a Go duration like "12h" or trailing-d days like "7d" up to
// "106751d", with no sign", ErrInvalidFlags with nothing deleted (b.anw, b.hxn,
// b.sgw, b.c4n). The row ended past the default retention, so only "106751d" keeps it.
func TestAdviceFollow_I6_OlderThanDurationForm(t *testing.T) {
	follows := []struct{ value, wantIDs string }{
		{"12h", `["` + expireMCPID + `"]`},
		{"7d", `["` + expireMCPID + `"]`},
		{"106751d", `[]`},
	}
	for _, follow := range follows {
		t.Run(follow.value, func(t *testing.T) {
			d, rec, storePath := newExpireMCPServer(t, false)
			for _, bad := range []string{"soon", "7days", "-2h", "+12h", "+7d", "106752d", "365000d"} {
				data := toolErrorData(t, callTool(t, d, "expire", paramJSON(t, map[string]any{"older_than": bad})))
				if want := olderThanRefusal(bad); data.ErrName != "ErrInvalidFlags" || data.ErrDescription != want {
					t.Errorf("older_than %q = %s: %q\nwant ErrInvalidFlags: %q", bad, data.ErrName, data.ErrDescription, want)
				}
			}
			if _, err := apitest.ReadSpawnColumns(storePath, expireMCPID); err != nil {
				t.Fatalf("row after the refusals: %v; want it kept", err)
			}
			if n := len(rec.SocketCalls()) + len(rec.Calls()); n != 0 {
				t.Errorf("tmux calls after the refusals = %d; want none", n)
			}

			obj := expireToolResult(t, callTool(t, d, "expire", paramJSON(t, map[string]any{"older_than": follow.value})))
			if got := string(obj["ids"]); got != follow.wantIDs {
				t.Errorf("expire older_than %q ids = %s; want %s", follow.value, got, follow.wantIDs)
			}
		})
	}
}

// advUseName is the name an old-key refusal tells the caller to use: the
// quoted name after "(no longer accepted; use ".
func advUseName(t *testing.T, advice string) string {
	t.Helper()
	_, rest, ok := strings.Cut(advice, "(no longer accepted; use ")
	end := strings.Index(rest, ")")
	if !ok || end < 0 {
		t.Fatalf("advice %q names no key after %q", advice, "(no longer accepted; use ")
	}
	name, err := strconv.Unquote(rest[:end])
	if err != nil {
		t.Fatalf("advice %q: the name to use is not quoted: %v", advice, err)
	}
	return name
}

// advOldKeyFollow sends tool the arguments with the old key, requires the
// refusal to name a new key, then re-sends the arguments with the key renamed
// as the text says and returns that call's response.
func advOldKeyFollow(t *testing.T, d mcp.Dispatcher, tool, old string, args map[string]any) *mcp.Response {
	t.Helper()
	data := toolErrorData(t, callTool(t, d, tool, paramJSON(t, args)))
	if data.ErrName != "ErrInvalidFlags" || !strings.Contains(data.ErrDescription, `unknown parameter "`+old+`" (no longer accepted; use "`) {
		t.Fatalf("refusal = %s: %q; want ErrInvalidFlags naming the key to use for %q", data.ErrName, data.ErrDescription, old)
	}
	args[advUseName(t, data.ErrDescription)] = args[old]
	delete(args, old)
	return callTool(t, d, tool, paramJSON(t, args))
}

// TestAdviceFollow_I7_OldInstanceIDKeyUseInstanceID: I7 "unknown parameter
// "claude_instance_id" (no longer accepted; use "instance_id")", the name
// copied literally into the arguments in place of the old key: spawn launches
// the row under that id, get and status answer for the seeded row.
func TestAdviceFollow_I7_OldInstanceIDKeyUseInstanceID(t *testing.T) {
	for _, tool := range []string{"spawn", "get", "status"} {
		t.Run(tool, func(t *testing.T) {
			d, client := advCLIServer(t, "i7-row")
			id, args := "i7-row", map[string]any{}
			if tool == "spawn" {
				id, args["cwd"], args["agent"] = "i7-new", t.TempDir(), "claude"
			}
			args["claude_instance_id"] = id

			resp := advOldKeyFollow(t, d, tool, "claude_instance_id", args)

			if _, err := client.Get(id); err != nil {
				t.Errorf("row %s after the followed %s: %v", id, tool, err)
			}
			if resp.Error != nil {
				t.Fatalf("followed %s refused: %+v", tool, resp.Error)
			}
			if tool == "get" {
				if got := string(toolResult(t, resp)["instance_id"]); got != `"`+id+`"` {
					t.Errorf("get instance_id = %s; want %q", got, id)
				}
			}
		})
	}
}

// TestAdviceFollow_I8_OldArgsKeyUseAgentArgs: I8 "unknown parameter
// "claude_args" (no longer accepted; use "agent_args")", the name copied
// literally into the arguments in place of the old key: spawn stores the args
// on its row, make_template writes them to the template.
func TestAdviceFollow_I8_OldArgsKeyUseAgentArgs(t *testing.T) {
	agentArgs := []string{"--model", "opus"}
	t.Run("spawn", func(t *testing.T) {
		d, client := advCLIServer(t)
		args := map[string]any{"cwd": t.TempDir(), "agent": "claude", "instance_id": "i8-new", "claude_args": agentArgs}

		advCLIResult(t, advOldKeyFollow(t, d, "spawn", "claude_args", args), &api.SpawnResult{})

		row, err := client.Get("i8-new")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !slices.Equal(row.AgentArgs, agentArgs) {
			t.Errorf("row agent_args = %q; want %q", row.AgentArgs, agentArgs)
		}
	})
	t.Run("make_template", func(t *testing.T) {
		d, _ := advCLIServer(t)
		args := map[string]any{"name": "i8-tpl", "agent": "claude", "claude_args": agentArgs}

		var res api.MakeTemplateResult
		advCLIResult(t, advOldKeyFollow(t, d, "make_template", "claude_args", args), &res)

		body, err := os.ReadFile(res.Path)
		if err != nil {
			t.Fatalf("read template: %v", err)
		}
		if !strings.Contains(string(body), "agent_args") || !strings.Contains(string(body), "opus") {
			t.Errorf("template %q lacks agent_args = the sent args", body)
		}
	})
}
