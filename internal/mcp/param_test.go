package mcp_test

// param_test.go pins b.c4u's, b.7or's and b.ewa's MCP side: every manifest
// param of every MCP tool is decoded under its one (underscore) name; every
// tool refuses a key that is not one of its params, a value of the wrong shape
// and arguments that are not an object with ErrInvalidFlags, in one wording
// each, and runs nothing; tools/list advertises exactly the params; spawn's
// tmux_session_name and list's filters reach the verb; get_permission answers.

import (
	"bytes"
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

// paramRequiredArgs seeds paramRow and returns v's required params with
// values that let the call run.
func paramRequiredArgs(t *testing.T, e *mcpEnv, v manifest.VerbDef) map[string]any {
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
// store's ids, the tmux calls, the templates directory and the trail.
type paramState struct {
	row        apitest.SpawnColumns
	rowErr     string
	ids        []string
	tmuxCalls  int
	templates  []string
	trailLines int
}

// readParamState reads e's paramState.
func readParamState(t *testing.T, e *mcpEnv) paramState {
	t.Helper()
	s := paramState{tmuxCalls: len(e.rec.Calls()) + len(e.rec.SocketCalls())}
	var err error
	if s.row, err = apitest.ReadSpawnColumns(e.storePath, paramRow); err != nil {
		s.rowErr = err.Error()
	}
	s.ids = rowIDs(t, e.d) // list writes no trail line
	slices.Sort(s.ids)
	entries, _ := os.ReadDir(filepath.Join(os.Getenv("HOME"), ".agent-director", "templates"))
	for _, en := range entries {
		s.templates = append(s.templates, en.Name())
	}
	trail, _ := os.ReadFile(mcpTrailPath)
	s.trailLines = bytes.Count(trail, []byte("\n"))
	return s
}

// paramWrongValues are JSON values of the wrong shape for a param of manifest
// type typ, a wrong element or object value among them.
func paramWrongValues(typ string) []any {
	switch typ {
	case "bool":
		return []any{"yes", 1}
	case "int":
		return []any{"5", 1.5}
	case "[]string":
		return []any{7, "k=v", []any{"k=v", 1}}
	case "map[string]string":
		return []any{7, []string{"FOO=bar"}, map[string]any{"FOO": 1}}
	}
	return []any{7, []string{"k=v"}}
}

// paramRefusalPrefix is how MCP's refusal of v's param p's value begins, in
// every wording (b.ewa, b.anw). paramShapeRefusal and paramShapeRefused both
// build on it, so a rewording TestMCPParamParity catches updates
// TestMCPParamTypesAgree too.
func paramRefusalPrefix(v manifest.VerbDef, p manifest.ParamDef) string {
	return fmt.Sprintf("ErrInvalidFlags: %s: parameter %q ", v.Name, p.Name)
}

// paramShapeRefusal is MCP's refusal of a value of the wrong JSON shape for
// v's param p: ErrInvalidFlags naming the verb, p as sent and p's expected
// value (b.ewa).
func paramShapeRefusal(v manifest.VerbDef, p manifest.ParamDef) string {
	return paramRefusalPrefix(v, p) + "must be " + paramShapes[p.Type].want
}

// paramShapeRefused reports whether data is ErrInvalidFlags refusing v's param
// p's value in any wording (paramShapeRefusal, the "has the wrong type"
// fallback, a label entry's or duration's form).
func paramShapeRefused(data mcp.ToolErrorData, v manifest.VerbDef, p manifest.ParamDef) bool {
	return data.ErrName == "ErrInvalidFlags" && strings.HasPrefix(data.ErrDescription, paramRefusalPrefix(v, p))
}

// TestMCPParamParity: every manifest param of every MCP tool is decoded: a
// value of the wrong shape, sent with the verb's required params, is refused
// with ErrInvalidFlags naming the verb, the param and its expected value, and
// nothing runs (b.7or, b.ewa).
func TestMCPParamParity(t *testing.T) {
	for _, v := range exposedVerbs() {
		for _, p := range v.Params {
			t.Run(v.Name+"/"+p.Name, func(t *testing.T) {
				e, _ := newReuseParamEnv(t)
				args := paramRequiredArgs(t, e, v)
				before := readParamState(t, e)
				want := paramShapeRefusal(v, p)

				for _, wrong := range paramWrongValues(p.Type) {
					args[p.Name] = wrong
					body := paramJSON(t, args)
					data := toolErrorData(t, callTool(t, e.d, mcp.ToolName(v.Name), body))
					if data.ErrName != "ErrInvalidFlags" || data.ErrDescription != want {
						t.Errorf("%s %s = %s: %q\nwant ErrInvalidFlags: %q", v.Name, body, data.ErrName, data.ErrDescription, want)
					}
				}
				if after := readParamState(t, e); !reflect.DeepEqual(after, before) {
					t.Errorf("the refused calls changed state:\n got %+v\nwant %+v", after, before)
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
// "none", and runs nothing: no row, tmux call, template or trail line (b.c4u).
// Kill's former finished-row opt-in, in any spelling, is such a key (SR-6.8,
// SR-6.6), as are the per-invocation params make_template's description says
// it refuses.
func TestMCPParamUnknownRefused(t *testing.T) {
	type refusal struct {
		name, verb, key string
		value           any
	}
	var cases []refusal
	for _, v := range exposedVerbs() {
		cases = append(cases, refusal{v.Name + ", unknown key", v.Name, "bogus_param", 1})
	}
	for _, old := range []struct {
		verb, key string
		value     any
	}{
		{"spawn", "relay-mode", "on"}, {"spawn", "extra-env", map[string]string{"MCP_PARAM": "1"}},
		{"spawn", "no-pre-trust", true}, {"spawn", "tmux-session-name", "mcp-param-name"}, {"spawn", "reuse-finished", true},
		{"list", "tmux-session-name", "mcp-param-name"},
		{"kill", "include-finished", true}, {"kill", "include_finished", true}, {"kill", "IncludeFinished", true}, {"kill", "includefinished", true},
		{"make-template", "template", "tpl"}, {"make-template", "claude_instance_id", "tpl-id"}, {"make-template", "tmux_session_name", "tpl-session"},
		{"make-template", "reuse_finished", true}, {"make-template", "reuse-finished", true},
	} {
		cases = append(cases, refusal{old.verb + ", " + old.key, old.verb, old.key, old.value})
	}
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

			if data.ErrName != "ErrInvalidFlags" || data.ErrDescription != want {
				t.Errorf("refusal = %s: %q\nwant ErrInvalidFlags: %q", data.ErrName, data.ErrDescription, want)
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
// case variant is unknown (b.c4u). Arguments that are not an object are
// ErrInvalidFlags on every tool (b.ewa). Nothing runs.
func TestMCPParamUnknownRefusalText(t *testing.T) {
	type refusal struct {
		name, verb string
		extra      map[string]any // added to the verb's required args
		raw        string         // the arguments as sent instead, when set
		want       string         // the description; {valid} is the verb's params in manifest order
	}
	cases := []refusal{
		{name: "several keys, sorted", verb: "list", extra: map[string]any{"zeta": 1, "Alpha": 1, "tmux-session-name": "x"},
			want: `ErrInvalidFlags: list: unknown parameters "Alpha", "tmux-session-name", "zeta"; valid parameters: {valid}`},
		{name: "a param's case variant", verb: "spawn", extra: map[string]any{"Reuse_Finished": true},
			want: `ErrInvalidFlags: spawn: unknown parameter "Reuse_Finished"; valid parameters: {valid}`},
		{name: "spawn, a string", verb: "spawn", raw: `"reuse_finished"`, want: "ErrInvalidFlags: spawn: arguments must be a JSON object"},
		{name: "spawn, a number", verb: "spawn", raw: `7`, want: "ErrInvalidFlags: spawn: arguments must be a JSON object"},
		{name: "spawn, a boolean", verb: "spawn", raw: `true`, want: "ErrInvalidFlags: spawn: arguments must be a JSON object"},
	}
	for _, v := range exposedVerbs() {
		cases = append(cases, refusal{name: v.Name + ", an array", verb: v.Name, raw: `["reuse_finished"]`,
			want: "ErrInvalidFlags: " + v.Name + ": arguments must be a JSON object"})
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

			if data.ErrName != "ErrInvalidFlags" || data.ErrDescription != want {
				t.Errorf("refusal = %s: %q\nwant ErrInvalidFlags: %q", data.ErrName, data.ErrDescription, want)
			}
			if after := readParamState(t, e); !reflect.DeepEqual(after, before) {
				t.Errorf("the refused call changed state:\n got %+v\nwant %+v", after, before)
			}
		})
	}
}

// TestMCPParamToolsListNames: each tool's schema properties are exactly its
// manifest params (TestManifestParamNamesHaveNoDash keeps those dash-free), its
// required list the required ones, and additionalProperties is false, as the
// server enforces (b.c4u). make_template's description names the
// per-invocation params TestMCPParamUnknownRefused shows it refuses.
func TestMCPParamToolsListNames(t *testing.T) {
	tools, _ := toolsList(t)
	byName := map[string]listedTool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
		if tool.InputSchema.AdditionalProperties != false {
			t.Errorf("%s additionalProperties = %v; want false", tool.Name, tool.InputSchema.AdditionalProperties)
		}
	}
	for _, v := range exposedVerbs() {
		tool, ok := byName[mcp.ToolName(v.Name)]
		props, want, required, wantRequired := []string{}, []string{}, append([]string{}, tool.InputSchema.Required...), []string{}
		for name := range tool.InputSchema.Properties {
			props = append(props, name)
		}
		for _, p := range v.Params {
			want = append(want, p.Name)
			if p.Required {
				wantRequired = append(wantRequired, p.Name)
			}
		}
		slices.Sort(props)
		slices.Sort(want)
		if !ok || !reflect.DeepEqual(props, want) || !slices.Equal(required, wantRequired) {
			t.Errorf("%s properties = %v, required %v (listed %v); want the manifest params %v, required %v",
				tool.Name, props, required, ok, want, wantRequired)
		}
	}
	const refused = "Per-invocation params (template, claude_instance_id, tmux_session_name, reuse_finished) are refused."
	if desc := byName[mcp.ToolName("make-template")].Description; !strings.Contains(desc, refused) {
		t.Errorf("make_template description %q lacks %q", desc, refused)
	}
}

// TestMCPParamSpawnTmuxSessionName: an MCP spawn's tmux_session_name names the
// created session and is stored; supplied empty it is refused before anything
// is created, naming the param as MCP spells it (b.ro3); null is not supplied,
// so the default <basename(cwd)>-<id[:8]> applies (b.7or).
func TestMCPParamSpawnTmuxSessionName(t *testing.T) {
	const dir = "mcpwork"
	cases := []struct {
		name  string
		value any
		want  string // the session name; "" for a refusal
	}{
		{name: "explicit", value: "mcp-named-session", want: "mcp-named-session"},
		{name: "empty", value: ""},
		{name: "null", value: nil, want: dir + "-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			cwd := filepath.Join(t.TempDir(), dir)
			if err := os.Mkdir(cwd, 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", cwd, err)
			}
			id := "tsn-" + uuid.NewString()[:8]
			args := spawnArgs(t, cwd, c, map[string]any{"claude_instance_id": id, "tmux_session_name": tc.value})

			if tc.want == "" {
				data := toolErrorData(t, callTool(t, e.d, "spawn", args))
				const phrase = "tmux_session_name (--tmux-session-name on the CLI) was supplied with an empty value"
				if data.ErrName != "ErrTmuxSessionNameEmpty" || !strings.Contains(data.ErrDescription, phrase) {
					t.Errorf("refusal = %s: %q; want ErrTmuxSessionNameEmpty carrying %q", data.ErrName, data.ErrDescription, phrase)
				}
				assertNothingCreated(t, e.d, e.rec)
				return
			}
			callToolText(t, e.d, "spawn", args)
			want := tc.want
			if want == dir+"-" {
				want += id[:8]
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

// TestMCPParamListFilters: list's tmux_session_name and state reach the
// filter together: of three rows, only the waiting one using the name is
// listed (b.7or).
func TestMCPParamListFilters(t *testing.T) {
	e, _ := newReuseParamEnv(t)
	seedFinished(t, e, "mcp-list-a", store.StateWaiting, apitest.WithTmuxSessionName("alpha"))
	seedFinished(t, e, "mcp-list-b1", store.StateEnded, apitest.WithTmuxSessionName("beta"))
	seedFinished(t, e, "mcp-list-b2", store.StateWaiting, apitest.WithTmuxSessionName("beta"))
	var rows []struct {
		ID string `json:"claude_instance_id"`
	}
	obj := callToolText(t, e.d, "list", `{"tmux_session_name":"beta","state":["waiting"]}`)
	if err := json.Unmarshal(obj["spawns"], &rows); err != nil || len(rows) != 1 || rows[0].ID != "mcp-list-b2" {
		t.Errorf("list = %s (%v); want only mcp-list-b2", obj["spawns"], err)
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

	obj := callToolText(t, e.d, "get_permission", paramJSON(t, map[string]any{"request_token": seed.RequestToken}))

	for key, want := range map[string]string{"request_token": strconv.Quote(seed.RequestToken),
		"request_id": strconv.FormatInt(seed.RequestID, 10), "tool_name": `"Bash"`, "decision": "null"} {
		if got := string(obj[key]); got != want {
			t.Errorf("get_permission %s = %s; want %s", key, got, want)
		}
	}
	if data := toolErrorData(t, callTool(t, e.d, "get_permission", paramJSON(t, map[string]any{"request_token": paramToken}))); data.ErrName != "ErrPermissionRequestNotFound" {
		t.Errorf("unknown token: err_name = %q (%s); want ErrPermissionRequestNotFound", data.ErrName, data.ErrDescription)
	}
}
