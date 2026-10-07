package mcp_test

// param_type_test.go pins b.pti: every MCP tool param's manifest Type, its
// tools/list schema and the MCP decode agree, so a caller sending the declared
// shape is not refused for its shape; spawn's relay_mode, label, claude_args
// and extra_env in those shapes reach the launch. (TestManifestSpawnTemplateParamsOneType
// pins spawn's and make-template's shared params to one Type.)

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/tmux"
	api "github.com/gabemahoney/agent-director/pkg/api"
)

// propSchema is the shape part of a tools/list property schema.
type propSchema struct {
	Type                 string      `json:"type"`
	Items                *propSchema `json:"items,omitempty"`
	AdditionalProperties *propSchema `json:"additionalProperties,omitempty"`
}

// String renders s as JSON, so failures show the nested shape.
func (s propSchema) String() string {
	b, _ := json.Marshal(s)
	return string(b)
}

// paramShapes maps each manifest param Type an MCP tool may take to the
// tools/list schema it must get, a well-formed value of that shape and the
// expected value a refusal of another shape states (b.ewa).
var paramShapes = map[string]struct {
	schema propSchema
	sample any
	want   string
}{
	"string":            {propSchema{Type: "string"}, "k=v", "a string"},
	"duration":          {propSchema{Type: "string"}, "1h", "a string holding " + api.OlderThanForm},
	"bool":              {propSchema{Type: "boolean"}, true, "a boolean"},
	"int":               {propSchema{Type: "integer"}, 1, "an integer"},
	"[]string":          {propSchema{Type: "array", Items: &propSchema{Type: "string"}}, []string{"k=v"}, "an array of strings"},
	"map[string]string": {propSchema{Type: "object", AdditionalProperties: &propSchema{Type: "string"}}, map[string]string{"MCP_SAMPLE": "v"}, "an object with string values"},
}

// TestMCPParamTypesAgree: every param of every MCP tool has a manifest Type
// with a JSON Schema shape, tools/list gives it that shape, and the MCP decode
// accepts a value of it (b.pti). TestMCPParamParity is the wrong-shape half.
func TestMCPParamTypesAgree(t *testing.T) {
	tools, _ := toolsList(t)
	schemas := map[string]map[string]propSchema{}
	for _, tool := range tools {
		schemas[tool.Name] = tool.InputSchema.Properties
	}
	e, _ := newReuseParamEnv(t)
	for _, v := range exposedVerbs() {
		for _, p := range v.Params {
			t.Run(v.Name+"/"+p.Name, func(t *testing.T) {
				shape, ok := paramShapes[p.Type]
				if !ok {
					t.Fatalf("manifest Type %q has no JSON Schema shape; tools/list gives %s %v", p.Type, p.Name, schemas[mcp.ToolName(v.Name)][p.Name])
				}
				if got := schemas[mcp.ToolName(v.Name)][p.Name]; !reflect.DeepEqual(got, shape.schema) {
					t.Errorf("tools/list schema = %v; want %v for manifest Type %q", got, shape.schema, p.Type)
				}

				args := paramJSON(t, map[string]any{p.Name: shape.sample})

				// The call may still be refused (a required param is absent); only a shape refusal fails here.
				if resp := callTool(t, e.d, mcp.ToolName(v.Name), args); resp.Error != nil {
					if data := toolErrorData(t, resp); paramShapeRefused(data, v, p) {
						t.Errorf("%s %s = %s: %q; want the declared %s shape decoded", v.Name, args, data.ErrName, data.ErrDescription, p.Type)
					}
				}
			})
		}
	}
}

// TestMCPSpawnDeclaredShapesReachLaunch: an MCP spawn sending relay_mode,
// label, claude_args and extra_env in their declared shapes launches with
// them: the labels and variables on the session env (CLAUDE_CONFIG_DIR
// steering pre-trust), the args after claude's own, and all four on the row
// (b.pti, b.c4u).
func TestMCPSpawnDeclaredShapesReachLaunch(t *testing.T) {
	e, c := newReuseParamEnv(t)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	id := "mcp-shapes-" + uuid.NewString()[:8]
	env := c.env()
	env["MCP_SHAPE_ENV"] = "set"
	args := []string{"--model", "opus"}

	obj := callToolText(t, e.d, "spawn", paramJSON(t, map[string]any{"cwd": cwd, "claude_instance_id": id, "relay_mode": "on",
		"label": []string{"role=worker", "team=core"}, "claude_args": args, "extra_env": env}))

	assertPreTrust(t, obj, "ok")
	c.check(t, cwd, true)
	creates := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(creates) != 1 {
		t.Fatalf("creates = %+v; want one", creates)
	}
	for k, want := range map[string]string{"CLAUDE_CONFIG_DIR": c.dir, "MCP_SHAPE_ENV": "set",
		"AGENT_DIRECTOR_LABEL_ROLE": "worker", "AGENT_DIRECTOR_LABEL_TEAM": "core"} {
		if got, ok := creates[0].Envs[k]; !ok || got != want {
			t.Errorf("session env %s = %q (present=%v); want %q", k, got, ok, want)
		}
	}
	if cmd := creates[0].Command; len(cmd) < len(args) || !slices.Equal(cmd[len(cmd)-len(args):], args) {
		t.Errorf("command = %q; want it to end with claude_args %q", cmd, args)
	}
	type stored struct {
		Labels     map[string]string
		ClaudeArgs []string
		ExtraEnv   map[string]string
	}
	var row stored
	cols := readColumns(t, e.storePath, id)
	for _, col := range []struct{ raw, into any }{
		{cols.Labels, &row.Labels}, {cols.ClaudeArgs, &row.ClaudeArgs}, {cols.ExtraEnv, &row.ExtraEnv},
	} {
		if s, _ := col.raw.(string); json.Unmarshal([]byte(s), col.into) != nil {
			t.Errorf("row column %v is not JSON", col.raw)
		}
	}
	if want := (stored{map[string]string{"role": "worker", "team": "core"}, args, env}); !reflect.DeepEqual(row, want) {
		t.Errorf("row labels, claude_args, extra_env = %+v; want %+v", row, want)
	}
	if cols.RelayMode != "on" {
		t.Errorf("row relay_mode = %v; want on (the default is off)", cols.RelayMode)
	}
}
