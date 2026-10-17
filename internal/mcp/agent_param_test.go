package mcp_test

// agent_param_test.go pins the MCP side of the agent param (SR-2.1): spawn's
// reaches the row and the get and list results, a make_template writes it, and
// agent_args are inherited from a template of the same agent. The refusals,
// the check order and the codex stop are pkg/api's and internal/spawn's; the
// wrong-shape refusal is TestMCPParamParity's and the advice the refusals give
// is TestAdviceFollow_I9 and I10's.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// agentSpawnArgs marshals spawn's arguments as spawnArgs does, but with no
// agent unless extra gives one.
func agentSpawnArgs(t *testing.T, cwd string, c ptConfig, extra map[string]any) string {
	t.Helper()
	args := map[string]any{"cwd": cwd, "extra_env": c.env()}
	for k, v := range extra {
		args[k] = v
	}
	return paramJSON(t, args)
}

// TestMCPSpawnAgentReachesRow: an MCP spawn's agent is recorded on its row, and
// the get and list tools show it; an agent that is not claude or codex is
// ErrAgentInvalid naming it, with nothing created.
func TestMCPSpawnAgentReachesRow(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "cursor"} {
		t.Run(agent, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			id := "agent-" + uuid.NewString()[:8]

			callToolText(t, e.d, "spawn", agentSpawnArgs(t, t.TempDir(), c, map[string]any{"instance_id": id, "agent": agent, "no_pre_trust": true}))

			if got := readColumns(t, e.storePath, id).Agent; got != agent {
				t.Errorf("row agent = %v; want claude", got)
			}
			if got := string(callToolText(t, e.d, "get", paramJSON(t, map[string]any{"instance_id": id}))["agent"]); got != `"`+agent+`"` {
				t.Errorf("get agent = %s; want \"claude\"", got)
			}
			var rows []struct {
				Agent string `json:"agent"`
			}
			if err := json.Unmarshal(callToolText(t, e.d, "list", `{}`)["spawns"], &rows); err != nil || len(rows) != 1 || rows[0].Agent != agent {
				t.Errorf("list spawns = %+v (%v); want one row of agent claude", rows, err)
			}
		})
	}
	t.Run("invalid", func(t *testing.T) {
		e, c := newReuseParamEnv(t)

		data := toolErrorData(t, callTool(t, e.d, "spawn", spawnArgs(t, t.TempDir(), c, map[string]any{"agent": "Claude"})))

		if want := `agent "Claude" (want claude, codex or cursor)`; data.ErrName != "ErrAgentInvalid" || !strings.Contains(data.ErrDescription, want) {
			t.Errorf("refusal = %s: %q; want ErrAgentInvalid carrying %q", data.ErrName, data.ErrDescription, want)
		}
		assertNothingCreated(t, e.d, e.rec)
	})
}

// TestMCPMakeTemplateWritesAgent: a make_template's agent is written to the
// template file, and a spawn naming the template and giving no agent of its own
// takes the template's agent and inherits its agent_args.
func TestMCPMakeTemplateWritesAgent(t *testing.T) {
	e, c := newReuseParamEnv(t)
	var made struct {
		Path string `json:"path"`
	}
	text := toolText(t, callTool(t, e.d, "make_template", paramJSON(t, map[string]any{
		"name": "agent-tpl", "agent": "claude", "agent_args": []string{"--model", "opus"}})))
	if err := json.Unmarshal([]byte(text), &made); err != nil {
		t.Fatalf("parse make_template result %q: %v", text, err)
	}
	if body, err := os.ReadFile(made.Path); err != nil || !strings.Contains(string(body), `agent = "claude"`) {
		t.Fatalf("template %s = %q (%v); want it to hold agent = \"claude\"", made.Path, body, err)
	}
	id := "agent-" + uuid.NewString()[:8]

	callToolText(t, e.d, "spawn", agentSpawnArgs(t, t.TempDir(), c, map[string]any{"template": "agent-tpl", "instance_id": id}))

	cols := readColumns(t, e.storePath, id)
	if cols.Agent != "claude" || cols.AgentArgs != `["--model","opus"]` {
		t.Errorf("row agent, agent_args = %v, %v; want claude, [\"--model\",\"opus\"]", cols.Agent, cols.AgentArgs)
	}
}

// TestMCPSpawnAgentArgsInheritance (SR-2.1): with a template of the agent it
// launches, an absent agent_args inherits the template's list and an explicitly
// empty one replaces it with none; with a template of another agent, an
// explicitly empty one is accepted and inherits nothing. (The refusal without
// the list is TestAdviceFollow_I10's.)
func TestMCPSpawnAgentArgsInheritance(t *testing.T) {
	cases := []struct {
		name          string
		templateAgent string
		agent         string
		agentArgs     any // the call's agent_args; nil for absent
		want          string
	}{
		{"absent inherits", config.AgentClaude, "claude", nil, `["--model","opus"]`},
		{"empty replaces", config.AgentClaude, "claude", []string{}, `[]`},
		{"empty against another agent's template inherits nothing", config.AgentCodex, "claude", []string{}, `[]`},
		{"Cursor absent inherits", "cursor", "cursor", nil, `["--model","opus"]`},
		{"Cursor empty replaces", "cursor", "cursor", []string{}, `[]`},
		{"Cursor foreign empty replaces", "claude", "cursor", []string{}, `[]`},
		{"Cursor nonempty exact replacement", "claude", "cursor", []string{"--mode=ask", "--", "--resume\n多行 🦋"}, `["--mode=ask","--","--resume\n多行 🦋"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			seedTemplate(t, "tpl", apitest.TemplateFields{Agent: tc.templateAgent, AgentArgs: []string{"--model", "opus"}})
			id := "agent-" + uuid.NewString()[:8]
			extra := map[string]any{"template": "tpl", "agent": tc.agent, "instance_id": id}
			if tc.agentArgs != nil {
				extra["agent_args"] = tc.agentArgs
			}

			callToolText(t, e.d, "spawn", agentSpawnArgs(t, t.TempDir(), c, extra))

			if got := readColumns(t, e.storePath, id).AgentArgs; got != tc.want {
				t.Errorf("row agent_args = %v; want %s", got, tc.want)
			}
		})
	}
}
