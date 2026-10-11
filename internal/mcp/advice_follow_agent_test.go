package mcp_test

// advice_follow_agent_test.go copies, over MCP and literally, the agent advice
// of spawn's refusals (b.fji I9, I10): ErrAgentMissing's "pass agent claude or
// codex (--agent on the CLI), or put agent in the template" and
// ErrAgentArgsMismatch's "spawn agent <name>, or pass this call's own
// non-empty agent_args, which replace the template's". The
// word before the CLI spelling is the MCP param; its value is the agent the
// text names.

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestAdviceFollow_I9_AgentMissingPassAgent: I9 ErrAgentMissing's "pass agent claude or codex (--agent on the CLI),
// or put agent in the template": the param and agent it names copied literally into the same spawn, or the agent put in
// the template it names, launch the row with that agent.
func TestAdviceFollow_I9_AgentMissingPassAgent(t *testing.T) {
	const phrase = "pass agent claude or codex (--agent on the CLI), or put agent in the template"
	for _, tc := range []string{"pass agent", "put agent in the template"} {
		t.Run(tc, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			id := "i9-" + uuid.NewString()[:8]
			extra := map[string]any{"instance_id": id}
			if tc != "pass agent" {
				extra["template"] = "i9-tpl"
				seedTemplate(t, "i9-tpl", apitest.TemplateFields{RelayMode: "off"})
			}
			args := agentSpawnArgs(t, t.TempDir(), c, extra)

			data := toolErrorData(t, callTool(t, e.d, "spawn", args))

			if data.ErrName != "ErrAgentMissing" || !strings.Contains(data.ErrDescription, phrase) {
				t.Fatalf("refusal = %s: %q; want ErrAgentMissing carrying %q", data.ErrName, data.ErrDescription, phrase)
			}
			assertNothingCreated(t, e.d, e.rec)
			if tc == "pass agent" {
				_, rest, _ := strings.Cut(data.ErrDescription, "pass ")
				words := strings.Fields(rest) // agent claude or codex ...
				extra[words[0]] = words[1]
				args = agentSpawnArgs(t, t.TempDir(), c, extra)
			} else {
				seedTemplate(t, "i9-tpl", apitest.TemplateFields{Agent: config.AgentClaude, RelayMode: "off"})
			}

			callToolText(t, e.d, "spawn", args)

			assertCreates(t, e.rec)
			if got := readColumns(t, e.storePath, id).Agent; got != "claude" {
				t.Errorf("row agent = %v; want claude", got)
			}
		})
	}
}

// TestAdviceFollow_I10_AgentArgsMismatch: I10 ErrAgentArgsMismatch's "spawn agent <name>, or pass this call's own non-empty
// agent_args, which replace the template's", each way copied literally into the same spawn: the agent named inherits
// the template's agent_args; a non-empty agent_args of the call's own replaces them.
func TestAdviceFollow_I10_AgentArgsMismatch(t *testing.T) {
	const own = "pass this call's own non-empty agent_args, which replace the template's"
	cases := []struct {
		name          string
		templateAgent string
		callAgent     string
		follow        func(t *testing.T, advice string, extra map[string]any)
		want          string // the row's agent_args after the followed call
	}{
		{"spawn the named agent", config.AgentClaude, config.AgentCodex, func(t *testing.T, advice string, extra map[string]any) {
			_, rest, ok := strings.Cut(advice, "spawn agent ")
			if !ok {
				t.Fatalf("advice %q names no agent after \"spawn agent \"", advice)
			}
			extra["agent"] = strings.TrimSuffix(strings.Fields(rest)[0], ",")
		}, `["--model","opus"]`},
		{"pass non-empty agent_args", config.AgentCodex, config.AgentClaude, func(t *testing.T, advice string, extra map[string]any) {
			if !strings.Contains(advice, own) {
				t.Fatalf("advice %q lacks %q", advice, own)
			}
			extra["agent_args"] = []string{"--model", "sonnet"}
		}, `["--model","sonnet"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, c := newReuseParamEnv(t)
			seedTemplate(t, "i10-tpl", apitest.TemplateFields{Agent: tc.templateAgent, AgentArgs: []string{"--model", "opus"}})
			id := "i10-" + uuid.NewString()[:8]
			cwd := t.TempDir()
			extra := map[string]any{"template": "i10-tpl", "agent": tc.callAgent, "instance_id": id}

			data := toolErrorData(t, callTool(t, e.d, "spawn", agentSpawnArgs(t, cwd, c, extra)))

			if data.ErrName != "ErrAgentArgsMismatch" || !strings.Contains(data.ErrDescription, ", or "+own) {
				t.Fatalf("refusal = %s: %q; want ErrAgentArgsMismatch carrying %q", data.ErrName, data.ErrDescription, ", or "+own)
			}
			assertNothingCreated(t, e.d, e.rec)
			tc.follow(t, data.ErrDescription, extra)

			callToolText(t, e.d, "spawn", agentSpawnArgs(t, cwd, c, extra))

			assertCreates(t, e.rec)
			cols := readColumns(t, e.storePath, id)
			if cols.AgentArgs != tc.want || cols.Agent != "claude" {
				t.Errorf("row agent, agent_args = %v, %v; want claude, %s", cols.Agent, cols.AgentArgs, tc.want)
			}
		})
	}
}
