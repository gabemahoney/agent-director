// Hook payload fixtures (SR-20.6, AC-HOOK-01): testdata/hook-payloads holds
// one payload per event agent-director registers (hookEvents in
// internal/spawn/settings.go): SessionStart, UserPromptSubmit, PreToolUse,
// PostToolUse, PostToolUseFailure, Stop, Notification, SessionEnd,
// PermissionRequest, SubagentStart and SubagentStop (eleven events). The
// *-subagent fixtures carry agent_id (a subagent or in-process teammate,
// SR-22.9, AC-HOOK-02); session-start-agent-type-only carries only
// agent_type (a session started with --agent). The subagent-*, *-agent* and
// stop-background-tasks fixtures are b.xjn's B1 to B11 (SRD Appendix B), from
// Claude Code 2.1.280's probe logs with fixture ids and no content field.
// Shapes follow Claude Code's hooks reference, https://code.claude.com/docs/en/hooks
// (read 2026-09-30; the page has no version stamp, the newest version it cites is v2.1.274).
package hook_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
)

// payloadFixtures lists every fixture with its event, the event's own
// field that names the case (value "" = the field need only be present)
// and the agent_id it carries ("" = none).
var payloadFixtures = []struct {
	file, event, field, value, agentID string
}{
	{"session-start-startup.json", "SessionStart", "source", "startup", ""},
	{"session-start-resume.json", "SessionStart", "source", "resume", ""},
	{"session-start-clear.json", "SessionStart", "source", "clear", ""},
	{"session-start-compact.json", "SessionStart", "source", "compact", ""},
	{"session-start-subagent.json", "SessionStart", "source", "startup", "a7c41e9b2d5f8036"},
	{"session-start-agent-type-only.json", "SessionStart", "agent_type", "security-reviewer", ""},
	{"user-prompt-submit.json", "UserPromptSubmit", "prompt", "", ""},
	{"pre-tool-use-ask-user-question.json", "PreToolUse", "tool_name", "AskUserQuestion", ""},
	{"pre-tool-use-bash.json", "PreToolUse", "tool_name", "Bash", ""},
	{"pre-tool-use-subagent.json", "PreToolUse", "tool_name", "Grep", "a5b92f0e7d3c6184"},
	{"post-tool-use.json", "PostToolUse", "tool_response", "", ""},
	{"post-tool-use-failure.json", "PostToolUseFailure", "error", "", ""},
	{"stop.json", "Stop", "last_assistant_message", "", ""},
	{"notification.json", "Notification", "notification_type", "idle_prompt", ""},
	{"session-end-prompt-input-exit.json", "SessionEnd", "reason", "prompt_input_exit", ""},
	{"session-end-clear.json", "SessionEnd", "reason", "clear", ""},
	{"session-end-subagent.json", "SessionEnd", "reason", "prompt_input_exit", "a3e08d6f1c9b4275"},
	{"permission-request.json", "PermissionRequest", "tool_name", "Bash", ""},
	// b.xjn, SRD Appendix B (B1 to B11). B6's agent_id is its launching
	// parent's (B1's).
	{"subagent-start.json", "SubagentStart", "agent_type", "general-purpose", "a3f9c2e81b7d4056"},
	{"subagent-start-teammate.json", "SubagentStart", "agent_type", "helper", "ahelper-5c1e9a7b3d2f8406"},
	{"subagent-stop.json", "SubagentStop", "background_tasks", "", "a3f9c2e81b7d4056"},
	{"subagent-stop-internal.json", "SubagentStop", "agent_type", "", "a2b6d9f04e8c1735"},
	{"post-tool-use-agent.json", "PostToolUse", "tool_name", "Agent", ""},
	{"post-tool-use-agent-nested.json", "PostToolUse", "tool_name", "Agent", "a3f9c2e81b7d4056"},
	{"post-tool-use-agent-teammate.json", "PostToolUse", "tool_name", "Agent", ""},
	{"stop-background-tasks.json", "Stop", "background_tasks", "", ""},
	{"pre-tool-use-agent.json", "PreToolUse", "tool_name", "Agent", ""},
	{"pre-tool-use-agent-teammate.json", "PreToolUse", "tool_name", "Agent", ""},
	{"post-tool-use-agent-completed.json", "PostToolUse", "tool_name", "Agent", ""},
}

// readPayloadFixture returns the raw bytes of testdata/hook-payloads/<name>.
func readPayloadFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "hook-payloads", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

func TestPayloadFixtureParses(t *testing.T) {
	for _, tc := range payloadFixtures {
		t.Run(tc.file, func(t *testing.T) {
			raw := readPayloadFixture(t, tc.file)
			var p map[string]any
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := hook.PeekEventName(raw); got != tc.event {
				t.Errorf("PeekEventName = %q; want %q", got, tc.event)
			}
			sid, _ := p["session_id"].(string)
			path, _ := p["transcript_path"].(string)
			if sid == "" || path == "" {
				t.Fatalf("session_id %q / transcript_path %q: both must be set", sid, path)
			}
			if got := hook.ExtractSessionID(path); got != sid {
				t.Errorf("ExtractSessionID(%q) = %q; want session_id %q", path, got, sid)
			}
			own, ok := p[tc.field]
			if !ok {
				t.Fatalf("event field %q missing", tc.field)
			}
			if tc.value != "" && own != tc.value {
				t.Errorf("%s = %v; want %q", tc.field, own, tc.value)
			}
			if _, has := p["agent_id"]; has != (tc.agentID != "") {
				t.Errorf("agent_id present = %v; want %v", has, tc.agentID != "")
			}
			res, err := hook.ClassifyEvent(raw)
			if err != nil {
				t.Fatalf("ClassifyEvent: %v", err)
			}
			if res.AgentID != tc.agentID {
				t.Errorf("ClassifyEvent AgentID = %q; want %q", res.AgentID, tc.agentID)
			}
		})
	}
}

// TestPayloadFixtureEventsMatchRegistered: every event internal/spawn
// registers has a fixture, every fixture's event is registered, and every
// fixture file is in payloadFixtures.
func TestPayloadFixtureEventsMatchRegistered(t *testing.T) {
	registered := registeredHookEvents(t)
	files, err := filepath.Glob(filepath.Join("testdata", "hook-payloads", "*.json"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	fixtureEvents, fixtureFiles, tableFiles := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, f := range files {
		fixtureFiles[filepath.Base(f)] = true
		fixtureEvents[hook.PeekEventName(readPayloadFixture(t, filepath.Base(f)))] = true
	}
	for _, tc := range payloadFixtures {
		tableFiles[tc.file] = true
	}
	for what, got := range map[string][]string{
		"registered events without a fixture":        missingFrom(registered, fixtureEvents),
		"fixture events not registered":              missingFrom(fixtureEvents, registered),
		"fixture files missing from payloadFixtures": missingFrom(fixtureFiles, tableFiles),
	} {
		if len(got) != 0 {
			t.Errorf("%s: %v", what, got)
		}
	}
}

// missingFrom returns the sorted keys of a that are absent from b.
func missingFrom(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// registeredHookEvents reads spawn's unexported hookEvents list (and the
// string constants it names) from internal/spawn/settings.go's source.
func registeredHookEvents(t *testing.T) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "spawn", "settings.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse spawn settings: %v", err)
	}
	consts := map[string]string{}
	var names []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				switch v := vs.Values[i].(type) {
				case *ast.BasicLit:
					if gd.Tok == token.CONST && v.Kind == token.STRING {
						consts[n.Name], _ = strconv.Unquote(v.Value)
					}
				case *ast.CompositeLit:
					if n.Name == "hookEvents" {
						for _, e := range v.Elts {
							if id, ok := e.(*ast.Ident); ok {
								names = append(names, id.Name)
							}
						}
					}
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("hookEvents not found in internal/spawn/settings.go")
	}
	events := map[string]bool{}
	for _, n := range names {
		v, ok := consts[n]
		if !ok {
			t.Fatalf("hookEvents names %s, which is not a string constant in settings.go", n)
		}
		events[v] = true
	}
	return events
}

// TestSubagentFixtureShapes: the b.xjn fixtures carry no content field (no
// prompt, assistant message, error, tool output or usage: SR-5.2), and the
// ids and texts the subagent tests name (the sub* constants) are the ones the
// files hold.
func TestSubagentFixtureShapes(t *testing.T) {
	files := []string{fxStart, fxStartTeammate, fxStop, fxStopInternal, fxResult, fxResultNested,
		fxResultTeammate, fxMainStop, fxRequest, fxRequestTeammate, fxCompleted}
	content := []string{"prompt", "last_assistant_message", "error", "tool_input.prompt",
		"tool_response.content", "tool_response.prompt", "tool_response.usage"}
	for _, f := range files {
		t.Run("no content/"+f, func(t *testing.T) {
			m := decodeFixture(t, f)
			for _, path := range content {
				if _, has := getKey(m, path); has {
					t.Errorf("%s holds %q; a b.xjn fixture holds no content field", f, path)
				}
			}
		})
	}
	for _, c := range []struct {
		fixture, path, want string
	}{
		{fxStart, "session_id", subSessionID},
		{fxStart, "agent_id", subAgentID},
		{fxStart, "agent_type", subType},
		{fxStartTeammate, "agent_id", subTeammateID},
		{fxStartTeammate, "agent_type", subTeammateType},
		{fxStop, "agent_id", subAgentID},
		{fxStop, "background_tasks", ""},
		{fxStopInternal, "agent_id", subInternalID},
		{fxResult, "tool_use_id", subToolUseID},
		{fxResult, "tool_response.agentId", subAgentID},
		{fxResult, "tool_response.resolvedModel", subModel},
		{fxResult, "tool_input.description", subLaunchDesc},
		{fxResult, "tool_input.subagent_type", subType},
		{fxResultNested, "agent_id", subAgentID},
		{fxResultNested, "tool_use_id", subNestedToolUseID},
		{fxResultNested, "tool_response.agentId", subChildID},
		{fxResultNested, "tool_input.description", subNestedDesc},
		{fxResultTeammate, "tool_use_id", subTeamToolUseID},
		{fxRequest, "tool_use_id", subToolUseID},
		{fxRequest, "tool_input.subagent_type", subType},
		{fxRequestTeammate, "tool_use_id", subTeamToolUseID},
		{fxCompleted, "tool_use_id", subToolUseID},
		{fxCompleted, "tool_response.agentId", subAgentID},
		{fxCompleted, "tool_response.status", "completed"},
	} {
		t.Run("constant/"+c.fixture+"/"+c.path, func(t *testing.T) {
			got := fixtureValue(t, c.fixture, c.path)
			if c.want != "" && got != c.want {
				t.Errorf("%s = %v; want %q", c.path, got, c.want)
			}
		})
	}
	if got := fixtureValue(t, fxResult, "tool_response.status"); got != "async_launched" {
		t.Errorf("B5 status = %v; want async_launched", got)
	}
	if got := fixtureValue(t, fxMainStop, "background_tasks").([]any); len(got) != 2 {
		t.Errorf("B8 background_tasks = %d entries; want 2", len(got))
	}
}
