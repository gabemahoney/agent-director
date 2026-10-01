package mcp_test

// spawn_reuse_param_test.go pins the MCP side of spawn's reuse-finished opt-in
// (SR-10.1, AC-REUSE-13): tools/list advertises it on spawn only, the spawn
// decoder knows the field, and without the opt-in a finished row still
// collides. make_template ignores it. The opt-in's effect on a finished row is
// Task 3's; no case here relies on tmux-session-name or no-pre-trust (b.7or).

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// newReuseParamEnv is a pre-trust env under a temp HOME, plus a per-test
// CLAUDE_CONFIG_DIR (with a .claude.json) for the spawn's extra-env.
func newReuseParamEnv(t *testing.T) (*ptEnv, ptConfig) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return newPreTrustEnv(t), ptSeedConfig(t, true)
}

// spawnArgs marshals spawn's arguments: cwd, the config dir's extra-env, then
// extra (an explicit id, reuse-finished) on top.
func spawnArgs(t *testing.T, cwd string, c ptConfig, extra map[string]any) string {
	t.Helper()
	args := map[string]any{"cwd": cwd, "extra-env": c.env()}
	for k, v := range extra {
		args[k] = v
	}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(b)
}

// callTool sends one tools/call request with raw JSON args.
func callTool(t *testing.T, d mcp.Dispatcher, tool, args string) *mcp.Response {
	t.Helper()
	return runOne(t, d, mcp.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"` + tool + `","arguments":` + args + `}`),
	})
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

// seedFinished seeds id's row in state and returns its columns as stored.
func seedFinished(t *testing.T, e *ptEnv, id, state string) apitest.SpawnColumns {
	t.Helper()
	if _, err := apitest.SeedSpawn(e.storePath, id, state, t.TempDir(), "off", "", false,
		apitest.WithTmuxSocket(e.socket)); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
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

// TestToolsListAdvertisesReuseFinished: spawn's schema has an optional boolean
// reuse-finished; make_template's has no reuse property under either spelling.
func TestToolsListAdvertisesReuseFinished(t *testing.T) {
	resp := runOne(t, &fakeDispatcher{}, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"})
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	var got struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
				Required []string `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	body, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}
	seen := map[string]bool{}
	for _, tool := range got.Tools {
		props := tool.InputSchema.Properties
		switch tool.Name {
		case "spawn":
			seen["spawn"] = true
			if p, ok := props["reuse-finished"]; !ok || p.Type != "boolean" {
				t.Errorf("spawn reuse-finished = %+v (present=%v); want type boolean", p, ok)
			}
			for _, r := range tool.InputSchema.Required {
				if r == "reuse-finished" {
					t.Error("spawn schema requires reuse-finished; want optional")
				}
			}
		case mcp.ToolName("make-template"):
			seen["make-template"] = true
			for _, name := range []string{"reuse-finished", "reuse_finished"} {
				if _, ok := props[name]; ok {
					t.Errorf("make_template schema has property %q; want none", name)
				}
			}
		}
	}
	if !seen["spawn"] || !seen["make-template"] {
		t.Fatalf("tools/list lacks spawn or make_template: %s", body)
	}
}

// TestMCPSpawnReuseFinishedWithoutIDIsFreshSpawn: with no explicit id the
// opt-in has no effect: a minted id, one new row, one create, others untouched.
func TestMCPSpawnReuseFinishedWithoutIDIsFreshSpawn(t *testing.T) {
	e, c := newReuseParamEnv(t)
	other := seedFinished(t, e, "mcp-reuse-other", store.StateEnded)

	obj := callToolText(t, e.d, "spawn", spawnArgs(t, t.TempDir(), c, map[string]any{"reuse-finished": true}))

	var minted string
	if err := json.Unmarshal(obj["claude_instance_id"], &minted); err != nil || minted == "" || minted == "mcp-reuse-other" {
		t.Fatalf("claude_instance_id = %s (%v); want a newly minted id", obj["claude_instance_id"], err)
	}
	if ids := rowIDs(t, e.d); len(ids) != 2 {
		t.Errorf("store rows = %v; want the seeded row and %s", ids, minted)
	}
	if cols := readColumns(t, e.storePath, minted); cols.State != store.StatePending {
		t.Errorf("new row state = %v; want pending", cols.State)
	}
	assertCreates(t, e.rec)
	if got := readColumns(t, e.storePath, "mcp-reuse-other"); !reflect.DeepEqual(got, other) {
		t.Errorf("other finished row changed:\n got %+v\nwant %+v", got, other)
	}
}

// TestMCPSpawnRejectsNonBoolReuseFinished: the decoder knows the field, so a
// non-boolean value is an error with no row and no tmux call.
func TestMCPSpawnRejectsNonBoolReuseFinished(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"string", "true"},
		{"number", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			toolErrorData(t, callTool(t, e.d, "spawn", spawnArgs(t, t.TempDir(), c, map[string]any{"reuse-finished": tc.value})))
			assertNothingCreated(t, e.d, e.rec)
		})
	}
}

// TestMCPSpawnReuseFinishedControlCharacterID: Epic 3's check still applies
// with the opt-in: ErrInvalidFlags, no row, no tmux call (SR-9.1).
func TestMCPSpawnReuseFinishedControlCharacterID(t *testing.T) {
	for _, tc := range []struct{ name, id string }{
		{"newline", "mcp-reuse\nid"},
		{"tab", "mcp-reuse\tid"},
		{"del", "mcp-reuse\x7fid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			args := spawnArgs(t, t.TempDir(), c, map[string]any{"claude_instance_id": tc.id, "reuse-finished": true})
			if data := toolErrorData(t, callTool(t, e.d, "spawn", args)); data.ErrName != "ErrInvalidFlags" {
				t.Errorf("err_name = %q (%s); want ErrInvalidFlags", data.ErrName, data.ErrDescription)
			}
			assertNothingCreated(t, e.d, e.rec)
		})
	}
}

// TestMCPSpawnFinishedRowCollidesWithoutOptIn: with reuse-finished absent or
// false, an ended or missing row's id collides; the row is unchanged and no
// session is created.
func TestMCPSpawnFinishedRowCollidesWithoutOptIn(t *testing.T) {
	const id = "mcp-reuse-finished-row"
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		for _, form := range []struct {
			name  string
			extra map[string]any
		}{
			{"absent", map[string]any{"claude_instance_id": id}},
			{"false", map[string]any{"claude_instance_id": id, "reuse-finished": false}},
		} {
			t.Run(state+"/"+form.name, func(t *testing.T) {
				e, c := newReuseParamEnv(t)
				before := seedFinished(t, e, id, state)

				data := toolErrorData(t, callTool(t, e.d, "spawn", spawnArgs(t, t.TempDir(), c, form.extra)))
				if data.ErrName != "ErrInstanceIdCollision" {
					t.Errorf("err_name = %q (%s); want ErrInstanceIdCollision", data.ErrName, data.ErrDescription)
				}
				if got := readColumns(t, e.storePath, id); !reflect.DeepEqual(got, before) {
					t.Errorf("row changed:\n got %+v\nwant %+v", got, before)
				}
				if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
					t.Errorf("creates = %d; want 0", n)
				}
			})
		}
	}
}

// TestMCPMakeTemplateIgnoresReuseFinished: make_template accepts either
// spelling, writes no reuse key, and the template loads cleanly.
func TestMCPMakeTemplateIgnoresReuseFinished(t *testing.T) {
	for _, key := range []string{"reuse-finished", "reuse_finished"} {
		t.Run(key, func(t *testing.T) {
			e, _ := newReuseParamEnv(t)
			args, err := json.Marshal(map[string]any{"name": "reuse-tpl", "cwd": t.TempDir(), key: true})
			if err != nil {
				t.Fatalf("marshal args: %v", err)
			}

			obj := callToolText(t, e.d, mcp.ToolName("make-template"), string(args))
			var path string
			if err := json.Unmarshal(obj["path"], &path); err != nil || path == "" {
				t.Fatalf("make_template path = %s (%v); want the written file", obj["path"], err)
			}
			var keys map[string]any
			if _, err := toml.DecodeFile(path, &keys); err != nil {
				t.Fatalf("decode template %s: %v", path, err)
			}
			for k := range keys {
				if strings.Contains(strings.ToLower(k), "reuse") {
					t.Errorf("template %s records key %q; want no reuse key", path, k)
				}
			}
			if _, err := config.LoadTemplate("reuse-tpl"); err != nil {
				t.Errorf("LoadTemplate: %v", err)
			}
		})
	}
}
