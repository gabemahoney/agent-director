package mcp_test

// param_test.go pins b.c4u's and b.7or's MCP side: every manifest param of
// every MCP tool is decoded under its one (underscore) name, every tool
// refuses a key that is not one of its params with ErrInvalidFlags, in one
// wording, and runs nothing, spawn honours relay_mode, extra_env,
// no_pre_trust and tmux_session_name and list
// tmux_session_name as the CLI does, tools/list advertises exactly that, and
// get_permission answers.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// paramRow is the ended row each refusal case seeds, so a verb that takes an
// id has one to act on; paramToken is the request token those cases send.
const (
	paramRow   = "mcp-param-row"
	paramToken = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
)

// paramJSON marshals a tool's arguments.
func paramJSON(t *testing.T, args map[string]any) string {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(b)
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

// paramRequiredArgs seeds paramRow and returns v's required params with
// values that let the call run.
func paramRequiredArgs(t *testing.T, e *ptEnv, v manifest.VerbDef) map[string]any {
	t.Helper()
	seedFinished(t, e, paramRow, store.StateEnded)
	args := map[string]any{}
	for _, p := range v.Params {
		if !p.Required {
			continue
		}
		switch p.Name {
		case "cwd":
			args[p.Name] = t.TempDir()
		case "claude_instance_id":
			args[p.Name] = paramRow
			if p.Type == "[]string" {
				args[p.Name] = []string{paramRow}
			}
		case "request_token":
			args[p.Name] = paramToken
		case "decision":
			args[p.Name] = "allow"
		case "text":
			args[p.Name] = "hi"
		case "name":
			args[p.Name] = "mcp-param-tpl"
		default:
			t.Fatalf("%s: no sample value for required param %q", v.Name, p.Name)
		}
	}
	return args
}

// paramState is what a call that runs nothing leaves as it was: paramRow, the
// store's ids, the tmux calls and the templates directory.
type paramState struct {
	row       apitest.SpawnColumns
	rowErr    string
	ids       []string
	tmuxCalls int
	templates []string
}

// readParamState reads e's paramState.
func readParamState(t *testing.T, e *ptEnv) paramState {
	t.Helper()
	s := paramState{tmuxCalls: len(e.rec.Calls()) + len(e.rec.SocketCalls())}
	var err error
	if s.row, err = apitest.ReadSpawnColumns(e.storePath, paramRow); err != nil {
		s.rowErr = err.Error()
	}
	s.ids = rowIDs(t, e.d)
	slices.Sort(s.ids)
	entries, _ := os.ReadDir(filepath.Join(os.Getenv("HOME"), ".agent-director", "templates"))
	for _, en := range entries {
		s.templates = append(s.templates, en.Name())
	}
	return s
}

// paramWrongValue is a JSON value of the wrong type for a param of manifest
// type typ: a string for a bool or int, else a number.
func paramWrongValue(typ string) any {
	if typ == "bool" || typ == "int" {
		return "not-a-" + typ
	}
	return 7
}

// TestMCPParamParity: every manifest param of every MCP tool is decoded: a
// wrong-typed value is a decode error naming the param, and nothing runs (b.7or).
func TestMCPParamParity(t *testing.T) {
	for _, v := range exposedVerbs() {
		for _, p := range v.Params {
			t.Run(v.Name+"/"+p.Name, func(t *testing.T) {
				e, _ := newReuseParamEnv(t)
				seedFinished(t, e, paramRow, store.StateEnded)
				before := readParamState(t, e)

				resp := callTool(t, e.d, mcp.ToolName(v.Name), paramJSON(t, map[string]any{p.Name: paramWrongValue(p.Type)}))

				data := toolErrorData(t, resp)
				if !strings.Contains(data.ErrDescription, "cannot unmarshal") || !strings.Contains(data.ErrDescription, p.Name) {
					t.Errorf("%s with a wrong-typed %s = %s: %q; want a decode error naming %s (the param is not decoded)",
						v.Name, p.Name, data.ErrName, data.ErrDescription, p.Name)
				}
				if after := readParamState(t, e); !reflect.DeepEqual(after, before) {
					t.Errorf("the refused call changed state:\n got %+v\nwant %+v", after, before)
				}
			})
		}
	}
}

// paramValidList is the refusal's list of v's valid params: their names in
// manifest order, or "none".
func paramValidList(v manifest.VerbDef) string {
	var names []string
	for _, p := range v.Params {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// TestMCPParamUnknownRefused: every MCP tool refuses a key that is not one of
// its params, the old dashed names included, with ErrInvalidFlags whose
// description names the verb (not the tool), quotes the key and lists the
// valid params in manifest order (so an old name's underscore replacement), or
// "none", and runs nothing (b.c4u).
func TestMCPParamUnknownRefused(t *testing.T) {
	type refusal struct {
		name, verb, key string
		value           any
	}
	var cases []refusal
	for _, v := range exposedVerbs() {
		cases = append(cases, refusal{v.Name + ", unknown key", v.Name, "bogus_param", 1})
	}
	cases = append(cases,
		refusal{"spawn, old name relay-mode", "spawn", "relay-mode", "on"},
		refusal{"spawn, old name extra-env", "spawn", "extra-env", map[string]string{"MCP_PARAM": "1"}},
		refusal{"spawn, old name no-pre-trust", "spawn", "no-pre-trust", true},
		refusal{"spawn, old name tmux-session-name", "spawn", "tmux-session-name", "mcp-param-name"},
		refusal{"spawn, old name reuse-finished", "spawn", "reuse-finished", true},
		refusal{"list, old name tmux-session-name", "list", "tmux-session-name", "mcp-param-name"},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newReuseParamEnv(t)
			v, ok := manifest.Lookup(tc.verb)
			if !ok {
				t.Fatalf("no %s verb in the manifest", tc.verb)
			}
			args := paramRequiredArgs(t, e, v)
			args[tc.key] = tc.value
			before := readParamState(t, e)
			want := "ErrInvalidFlags: " + tc.verb + ": unknown parameter " + strconv.Quote(tc.key) +
				"; valid parameters: " + paramValidList(v)

			data := toolErrorData(t, callTool(t, e.d, mcp.ToolName(tc.verb), paramJSON(t, args)))

			if data.ErrName != "ErrInvalidFlags" {
				t.Errorf("err_name = %q (%s); want ErrInvalidFlags", data.ErrName, data.ErrDescription)
			}
			if data.ErrDescription != want {
				t.Errorf("description = %q\nwant %q", data.ErrDescription, want)
			}
			if after := readParamState(t, e); !reflect.DeepEqual(after, before) {
				t.Errorf("the refused call changed state:\n got %+v\nwant %+v", after, before)
			}
		})
	}
}

// TestMCPParamUnknownRefusalText pins the refusal's wording past one unknown
// key (TestMCPParamUnknownRefused): several unknown keys are each quoted as
// sent, sorted, after "parameters"; keys match case included, so a param's
// case variant is unknown; arguments that are not an object are a decode
// error. Nothing runs (b.c4u).
func TestMCPParamUnknownRefusalText(t *testing.T) {
	cases := []struct {
		name, verb string
		extra      map[string]any // added to the verb's required args
		raw        string         // the arguments as sent instead, when set
		wantName   string
		want       string // the description; {valid} is the verb's params in manifest order
		prefix     bool   // want is only the description's start
	}{
		{name: "several keys, sorted", verb: "list", extra: map[string]any{"zeta": 1, "Alpha": 1, "tmux-session-name": "x"},
			wantName: "ErrInvalidFlags",
			want:     `ErrInvalidFlags: list: unknown parameters "Alpha", "tmux-session-name", "zeta"; valid parameters: {valid}`},
		{name: "a param's case variant", verb: "spawn", extra: map[string]any{"Reuse_Finished": true}, wantName: "ErrInvalidFlags",
			want: `ErrInvalidFlags: spawn: unknown parameter "Reuse_Finished"; valid parameters: {valid}`},
		// ErrInternal is today's class of any MCP decode error.
		{name: "not an object", verb: "spawn", raw: `["reuse_finished"]`, wantName: "ErrInternal",
			want: "decode spawn params: ", prefix: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newReuseParamEnv(t)
			v, ok := manifest.Lookup(tc.verb)
			if !ok {
				t.Fatalf("no %s verb in the manifest", tc.verb)
			}
			args := paramRequiredArgs(t, e, v)
			for k, val := range tc.extra {
				args[k] = val
			}
			body := tc.raw
			if body == "" {
				body = paramJSON(t, args)
			}
			want := strings.ReplaceAll(tc.want, "{valid}", paramValidList(v))
			before := readParamState(t, e)

			data := toolErrorData(t, callTool(t, e.d, mcp.ToolName(tc.verb), body))

			if data.ErrName != tc.wantName {
				t.Errorf("err_name = %q (%s); want %s", data.ErrName, data.ErrDescription, tc.wantName)
			}
			if got := data.ErrDescription; tc.prefix && !strings.HasPrefix(got, want) || !tc.prefix && got != want {
				t.Errorf("description = %q\nwant %q (prefix only: %v)", got, want, tc.prefix)
			}
			if after := readParamState(t, e); !reflect.DeepEqual(after, before) {
				t.Errorf("the refused call changed state:\n got %+v\nwant %+v", after, before)
			}
		})
	}
}

// TestMCPParamSpawnRelayModeAndExtraEnv: an MCP spawn's relay_mode and
// extra_env, under their underscore names, reach the row (b.c4u).
func TestMCPParamSpawnRelayModeAndExtraEnv(t *testing.T) {
	e, c := newReuseParamEnv(t)
	id := "mcp-renamed-" + uuid.NewString()[:8]
	env := c.env()
	env["MCP_PARAM_ENV"] = "set"

	callToolText(t, e.d, "spawn", paramJSON(t, map[string]any{
		"cwd": t.TempDir(), "claude_instance_id": id, "relay_mode": "on", "extra_env": env}))

	cols := readColumns(t, e.storePath, id)
	if cols.RelayMode != "on" {
		t.Errorf("row relay_mode = %v; want on (the default is off)", cols.RelayMode)
	}
	var stored map[string]string
	if s, _ := cols.ExtraEnv.(string); json.Unmarshal([]byte(s), &stored) != nil || !reflect.DeepEqual(stored, env) {
		t.Errorf("row extra_env = %v; want %v", cols.ExtraEnv, env)
	}
}

// TestMCPParamSpawnNoPreTrust: an MCP spawn with no_pre_trust reports pre_trust
// skipped, leaves .claude.json byte-identical and records the opt-out, so a
// later MCP resume of the row reports skipped too (b.7or).
func TestMCPParamSpawnNoPreTrust(t *testing.T) {
	e, c := newReuseParamEnv(t)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	id := "mcp-npt-" + uuid.NewString()[:8]

	obj := callToolText(t, e.d, "spawn", paramJSON(t, map[string]any{
		"cwd": cwd, "extra_env": c.env(), "claude_instance_id": id, "no_pre_trust": true}))

	assertPreTrust(t, obj, "skipped")
	c.check(t, cwd, false)
	if npt := readColumns(t, e.storePath, id).NoPreTrust; fmt.Sprint(npt) != "1" {
		t.Errorf("row no_pre_trust = %v; want 1 (the opt-out recorded)", npt)
	}
	// The agent reports in with a transcript, then exits with its session.
	session := "sess-" + id
	jsonl := apitest.SeedJsonlUnder(t, c.dir, cwd, session)
	apitest.ApplyAgentHook(t, e.storePath, id, "SessionStart", session, apitest.HookTranscript(jsonl, true))
	apitest.ApplyAgentHook(t, e.storePath, id, "SessionEnd", session)
	for _, s := range e.rec.Sessions(e.socket) {
		if err := e.rec.KillSessionID(e.socket, s.ID); err != nil {
			t.Fatalf("KillSessionID(%s): %v", s.ID, err)
		}
	}
	if st := readColumns(t, e.storePath, id).State; st != store.StateEnded {
		t.Fatalf("row state = %v; want ended before the resume", st)
	}

	obj = callToolText(t, e.d, "resume", paramJSON(t, map[string]any{"claude_instance_id": id}))

	assertPreTrust(t, obj, "skipped")
	c.check(t, cwd, false)
}

// TestMCPParamSpawnTmuxSessionName: an MCP spawn's tmux_session_name names the
// created session and is stored; supplied empty or invalid it is refused before
// anything is created, the empty refusal naming the param as MCP spells it (b.ro3);
// absent or null gives the default <basename(cwd)>-<id[:8]> (b.7or).
func TestMCPParamSpawnTmuxSessionName(t *testing.T) {
	const dir = "mcpwork"
	cases := []struct {
		name     string
		absent   bool
		value    any
		wantErr  string // the refusal; "" for a launch
		wantDesc string // a phrase the refusal's description carries
		want     string // the session name; "" for the default
	}{
		{name: "explicit", value: "mcp-named-session", want: "mcp-named-session"},
		{name: "empty", value: "", wantErr: "ErrTmuxSessionNameEmpty",
			wantDesc: "tmux_session_name (--tmux-session-name on the CLI) was supplied with an empty value"},
		{name: "invalid", value: "mcp:bad", wantErr: "ErrTmuxSessionNameInvalid"},
		{name: "absent", absent: true},
		{name: "null", value: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			cwd := filepath.Join(t.TempDir(), dir)
			if err := os.Mkdir(cwd, 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", cwd, err)
			}
			id := "tsn-" + uuid.NewString()[:8]
			args := map[string]any{"cwd": cwd, "extra_env": c.env(), "claude_instance_id": id}
			if !tc.absent {
				args["tmux_session_name"] = tc.value
			}

			if tc.wantErr != "" {
				data := toolErrorData(t, callTool(t, e.d, "spawn", paramJSON(t, args)))
				if data.ErrName != tc.wantErr {
					t.Errorf("err_name = %q (%s); want %s", data.ErrName, data.ErrDescription, tc.wantErr)
				}
				if !strings.Contains(data.ErrDescription, tc.wantDesc) {
					t.Errorf("err_description = %q; want it to carry %q", data.ErrDescription, tc.wantDesc)
				}
				assertNothingCreated(t, e.d, e.rec)
				return
			}
			callToolText(t, e.d, "spawn", paramJSON(t, args))

			want := tc.want
			if want == "" {
				want = dir + "-" + id[:8]
			}
			if creates := e.rec.SocketCallsOf(tmux.CallCreate); len(creates) != 1 || creates[0].Target != want {
				t.Errorf("creates = %+v; want one, of session %q", creates, want)
			}
			if got := fmt.Sprint(readColumns(t, e.storePath, id).TmuxSessionName); got != want {
				t.Errorf("row tmux_session_name = %q; want %q", got, want)
			}
		})
	}
}

// TestMCPParamListTmuxSessionName: list's tmux_session_name filters by exact
// session name, live and ended rows alike, and together with state (b.7or).
func TestMCPParamListTmuxSessionName(t *testing.T) {
	e, _ := newReuseParamEnv(t)
	for _, r := range []struct{ id, state, name string }{
		{"mcp-list-a", store.StateEnded, "alpha"},
		{"mcp-list-b1", store.StateEnded, "beta"},
		{"mcp-list-b2", store.StateWaiting, "beta"},
	} {
		seedFinished(t, e, r.id, r.state, apitest.WithTmuxSessionName(r.name))
	}
	for _, tc := range []struct {
		name  string
		state []string
		want  []string
	}{
		{"beta", nil, []string{"mcp-list-b1", "mcp-list-b2"}},
		{"beta", []string{store.StateWaiting}, []string{"mcp-list-b2"}},
		{"alpha", nil, []string{"mcp-list-a"}},
		{"alpha", []string{store.StateWaiting}, []string{}},
		{"none-such", nil, []string{}},
	} {
		t.Run(tc.name+"/"+strings.Join(tc.state, ","), func(t *testing.T) {
			var rows []struct {
				ID string `json:"claude_instance_id"`
			}
			args := map[string]any{"tmux_session_name": tc.name}
			if tc.state != nil {
				args["state"] = tc.state
			}
			obj := callToolText(t, e.d, "list", paramJSON(t, args))
			if err := json.Unmarshal(obj["spawns"], &rows); err != nil {
				t.Fatalf("parse list spawns: %v", err)
			}
			got := []string{}
			for _, r := range rows {
				got = append(got, r.ID)
			}
			slices.Sort(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("list tmux_session_name=%q state=%v = %v; want %v", tc.name, tc.state, got, tc.want)
			}
		})
	}
}

// TestMCPParamToolsListNames: each tool's schema properties are exactly its
// manifest params (TestManifestParamNamesHaveNoDash keeps those dash-free),
// and additionalProperties is false, as the server enforces (b.c4u).
func TestMCPParamToolsListNames(t *testing.T) {
	resp := runOne(t, &fakeDispatcher{}, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"})
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}
	var got struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties           map[string]json.RawMessage `json:"properties"`
				AdditionalProperties any                        `json:"additionalProperties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	body, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}
	props := map[string][]string{}
	for _, tool := range got.Tools {
		names := []string{}
		for name := range tool.InputSchema.Properties {
			names = append(names, name)
		}
		slices.Sort(names)
		props[tool.Name] = names
		if tool.InputSchema.AdditionalProperties != false {
			t.Errorf("%s additionalProperties = %v; want false", tool.Name, tool.InputSchema.AdditionalProperties)
		}
	}
	for _, v := range exposedVerbs() {
		want := []string{}
		for _, p := range v.Params {
			want = append(want, p.Name)
		}
		slices.Sort(want)
		if have, ok := props[mcp.ToolName(v.Name)]; !ok || !reflect.DeepEqual(have, want) {
			t.Errorf("%s properties = %v (listed %v); want the manifest params %v", mcp.ToolName(v.Name), have, ok, want)
		}
	}
}

// TestMCPParamGetPermissionSeededRow: get_permission, which tools/list
// advertises, returns a seeded open request by its token, and an unknown
// token is ErrPermissionRequestNotFound.
func TestMCPParamGetPermissionSeededRow(t *testing.T) {
	e, _ := newReuseParamEnv(t)
	const id = "mcp-perm-row"
	if _, err := apitest.SeedSpawn(e.storePath, id, store.StateCheckPermission, t.TempDir(), "on", "", false); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
	seed, err := apitest.SeedPermissionRequest(e.storePath, id, "Bash")
	if err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}

	obj := callToolText(t, e.d, mcp.ToolName("get-permission"), paramJSON(t, map[string]any{"request_token": seed.RequestToken}))

	for key, want := range map[string]string{
		"request_token": strconv.Quote(seed.RequestToken),
		"request_id":    strconv.FormatInt(seed.RequestID, 10),
		"tool_name":     `"Bash"`,
		"decision":      "null",
	} {
		if got := string(obj[key]); got != want {
			t.Errorf("get_permission %s = %s; want %s", key, got, want)
		}
	}

	data := toolErrorData(t, callTool(t, e.d, mcp.ToolName("get-permission"), paramJSON(t, map[string]any{"request_token": paramToken})))
	if data.ErrName != "ErrPermissionRequestNotFound" {
		t.Errorf("get_permission of an unknown token: err_name = %q (%s); want ErrPermissionRequestNotFound",
			data.ErrName, data.ErrDescription)
	}
}
