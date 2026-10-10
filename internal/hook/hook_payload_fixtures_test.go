// Hook payload fixtures (SR-20.6, AC-HOOK-01): testdata/hook-payloads holds
// one payload per event agent-director registers (hookEvents in
// internal/spawn/settings.go): SessionStart, UserPromptSubmit, PreToolUse,
// PostToolUse, PostToolUseFailure, Stop, Notification, SessionEnd and
// PermissionRequest. The
// *-subagent fixtures carry agent_id (a subagent or in-process teammate,
// SR-22.9, AC-HOOK-02); session-start-agent-type-only carries only
// agent_type (a session started with --agent).
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
