package mcp_test

// helpers_test.go — the package's shared fixtures: the JSON-RPC driver over a
// fake or live dispatcher, readers for tools/list and tools/call results, and
// mcpEnv, the one live-dispatcher environment.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// fakeDispatcher records every call and returns a programmable result/error.
type fakeDispatcher struct {
	calls  []dispatchedCall
	result any
	err    error
}

type dispatchedCall struct {
	name string
	args json.RawMessage
}

func (f *fakeDispatcher) Call(_ context.Context, name string, args json.RawMessage) (any, error) {
	f.calls = append(f.calls, dispatchedCall{name: name, args: args})
	return f.result, f.err
}

// runOne serves one JSON-RPC request and returns the parsed response (nil
// when the request produced none).
func runOne(t *testing.T, d mcp.Dispatcher, req mcp.Request) *mcp.Response {
	t.Helper()
	in, out := &bytes.Buffer{}, &bytes.Buffer{}
	if err := json.NewEncoder(in).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if err := mcp.New(d, nil).Serve(context.Background(), in, out); err != nil && !errors.Is(err, io.EOF) {
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

// callTool sends one tools/call request with raw JSON args.
func callTool(t *testing.T, d mcp.Dispatcher, tool, args string) *mcp.Response {
	t.Helper()
	return runOne(t, d, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"` + tool + `","arguments":` + args + `}`)})
}

// toolResult fails unless resp is a success with one text content part, and
// returns that part's JSON object, each field raw.
func toolResult(t *testing.T, resp *mcp.Response) map[string]json.RawMessage {
	t.Helper()
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/call failed: %+v", resp)
	}
	body, _ := json.Marshal(resp.Result)
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Content) != 1 || env.Content[0].Type != "text" {
		t.Fatalf("tools/call result = %s; want one text content part", body)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(env.Content[0].Text), &obj); err != nil {
		t.Fatalf("parse result text %q: %v", env.Content[0].Text, err)
	}
	return obj
}

// callToolText sends one tools/call, requires success and returns its JSON object.
func callToolText(t *testing.T, d mcp.Dispatcher, tool, args string) map[string]json.RawMessage {
	t.Helper()
	return toolResult(t, callTool(t, d, tool, args))
}

// toolErrorData fails unless resp is a JSON-RPC error, and returns its data.
func toolErrorData(t *testing.T, resp *mcp.Response) mcp.ToolErrorData {
	t.Helper()
	if resp == nil || resp.Error == nil {
		t.Fatalf("tools/call succeeded: %+v; want an error", resp)
	}
	body, _ := json.Marshal(resp.Error.Data)
	var data mcp.ToolErrorData
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("parse error data %s: %v", body, err)
	}
	return data
}

// paramJSON marshals a tool's arguments.
func paramJSON(t *testing.T, args map[string]any) string {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(b)
}

// listedTool is one tools/list entry.
type listedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Properties           map[string]propSchema `json:"properties"`
		Required             []string              `json:"required"`
		AdditionalProperties any                   `json:"additionalProperties"`
	} `json:"inputSchema"`
}

// toolsList returns tools/list's tools in order and its raw result body.
func toolsList(t *testing.T) ([]listedTool, string) {
	t.Helper()
	resp := runOne(t, &fakeDispatcher{}, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"})
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	body, _ := json.Marshal(resp.Result)
	var got struct {
		Tools []listedTool `json:"tools"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}
	return got.Tools, string(body)
}

// exposedVerbs is every manifest verb MCP serves as a tool.
func exposedVerbs() []manifest.VerbDef {
	var out []manifest.VerbDef
	for _, v := range manifest.Verbs {
		if mcp.ExposedVerb(v.Name) {
			out = append(out, v)
		}
	}
	return out
}

// mcpEnv is a live dispatcher over a Client on a temp store, with a
// non-existent config file and a tmuxfix.Recorder; socket is the default
// socket under the per-test TMUX_TMPDIR, whose per-user directory exists.
type mcpEnv struct {
	d         mcp.Dispatcher
	rec       *tmuxfix.Recorder
	storePath string
	socket    string
}

// newEnv builds an mcpEnv under a temp HOME, with TMUX unset and no caller
// instance id.
func newEnv(t *testing.T) *mcpEnv {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	t.Setenv("TMUX", "")
	tmpdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", tmpdir)
	sockDir := filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	dir := t.TempDir()
	e := &mcpEnv{rec: tmuxfix.NewRecorder(), storePath: filepath.Join(dir, "state.db"), socket: filepath.Join(sockDir, "default")}
	client, err := api.New(api.Options{StorePath: e.storePath, ConfigPath: filepath.Join(dir, "absent", "config.toml"),
		CreateIfMissing: true, TmuxClient: e.rec})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	e.d = mcp.NewLiveDispatcher(client)
	return e
}

// newReuseParamEnv is newEnv plus a per-test CLAUDE_CONFIG_DIR (with a
// .claude.json) for a spawn's extra_env.
func newReuseParamEnv(t *testing.T) (*mcpEnv, ptConfig) {
	t.Helper()
	return newEnv(t), ptSeedConfig(t, true)
}

// seed seeds id in state (a temp cwd, relay off, no session id, SeedSpawn's socket) with opts.
func (e *mcpEnv) seed(t *testing.T, id, state string, opts ...apitest.SpawnOption) {
	t.Helper()
	if _, err := apitest.SeedSpawn(e.storePath, id, state, t.TempDir(), "off", "", false, opts...); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
}

// seedFinished seeds id's row in state on e's socket, with no session id and
// opts, and returns its columns as stored.
func seedFinished(t *testing.T, e *mcpEnv, id, state string, opts ...apitest.SpawnOption) apitest.SpawnColumns {
	t.Helper()
	e.seed(t, id, state, append([]apitest.SpawnOption{apitest.WithTmuxSocket(e.socket)}, opts...)...)
	return readColumns(t, e.storePath, id)
}

// readColumns reads id's row straight from the store at dbPath.
func readColumns(t *testing.T, dbPath, id string) apitest.SpawnColumns {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return cols
}

// rowIDs returns the ids of every row, read through the list tool.
func rowIDs(t *testing.T, d mcp.Dispatcher) []string {
	t.Helper()
	var rows []struct {
		ID string `json:"claude_instance_id"`
	}
	if err := json.Unmarshal(callToolText(t, d, "list", `{}`)["spawns"], &rows); err != nil {
		t.Fatalf("parse list spawns: %v", err)
	}
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// assertNothingCreated checks the store has no row and tmux saw no call.
func assertNothingCreated(t *testing.T, d mcp.Dispatcher, rec *tmuxfix.Recorder) {
	t.Helper()
	if ids := rowIDs(t, d); len(ids) != 0 {
		t.Errorf("store rows = %v; want none", ids)
	}
	if n, s := len(rec.Calls()), len(rec.SocketCalls()); n != 0 || s != 0 {
		t.Errorf("tmux calls = %d name-based, %d socket; want none", n, s)
	}
}

// assertCreates checks the Recorder saw exactly one create, so the launch ran.
func assertCreates(t *testing.T, rec *tmuxfix.Recorder) {
	t.Helper()
	if n := len(rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
		t.Errorf("creates = %d; want 1", n)
	}
}

// spawnArgs marshals spawn's arguments: cwd, the config dir's extra_env, then
// extra (an explicit id, reuse_finished) on top.
func spawnArgs(t *testing.T, cwd string, c ptConfig, extra map[string]any) string {
	t.Helper()
	args := map[string]any{"cwd": cwd, "extra_env": c.env()}
	for k, v := range extra {
		args[k] = v
	}
	return paramJSON(t, args)
}
