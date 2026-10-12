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
//
// testdata/codex-hook-payloads holds Codex's (SR-14.3): Epic 1's 0.162.1 captures
// with home paths and every id replaced by a fixture value, the relations kept.
// A Codex fixture is named "codex/<file>" to readPayloadFixture; fixtureName
// builds that from the table's agent column.
package hook_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
)

// cxSubagentID is the agent_id of the Codex subagent fixtures.
const cxSubagentID = "019f0000-0000-7000-8000-000000000013"

// rolloutName is the file name shape of a Codex transcript: rollout-<time>-<session id>.jsonl.
var rolloutName = regexp.MustCompile(`^rollout-\d{4}-\d\d-\d\dT\d\d-\d\d-\d\d-[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}\.jsonl$`)

// payloadFixtures lists every fixture with its agent ("claude" or "codex"; the
// directory it is in), its event, the event's own field that names the case
// (value "" = the field need only be present) and the agent_id it carries
// ("" = none).
var payloadFixtures = []struct {
	agent, file, event, field, value, agentID string
}{
	{"claude", "session-start-startup.json", "SessionStart", "source", "startup", ""},
	{"claude", "session-start-resume.json", "SessionStart", "source", "resume", ""},
	{"claude", "session-start-clear.json", "SessionStart", "source", "clear", ""},
	{"claude", "session-start-compact.json", "SessionStart", "source", "compact", ""},
	{"claude", "session-start-subagent.json", "SessionStart", "source", "startup", "a7c41e9b2d5f8036"},
	{"claude", "session-start-agent-type-only.json", "SessionStart", "agent_type", "security-reviewer", ""},
	{"claude", "user-prompt-submit.json", "UserPromptSubmit", "prompt", "", ""},
	{"claude", "pre-tool-use-ask-user-question.json", "PreToolUse", "tool_name", "AskUserQuestion", ""},
	{"claude", "pre-tool-use-bash.json", "PreToolUse", "tool_name", "Bash", ""},
	{"claude", "pre-tool-use-subagent.json", "PreToolUse", "tool_name", "Grep", "a5b92f0e7d3c6184"},
	{"claude", "post-tool-use.json", "PostToolUse", "tool_response", "", ""},
	{"claude", "post-tool-use-failure.json", "PostToolUseFailure", "error", "", ""},
	{"claude", "stop.json", "Stop", "last_assistant_message", "", ""},
	{"claude", "notification.json", "Notification", "notification_type", "idle_prompt", ""},
	{"claude", "session-end-prompt-input-exit.json", "SessionEnd", "reason", "prompt_input_exit", ""},
	{"claude", "session-end-clear.json", "SessionEnd", "reason", "clear", ""},
	{"claude", "session-end-subagent.json", "SessionEnd", "reason", "prompt_input_exit", "a3e08d6f1c9b4275"},
	{"claude", "permission-request.json", "PermissionRequest", "tool_name", "Bash", ""},
	// b.xjn, SRD Appendix B (B1 to B11). B6's agent_id is its launching
	// parent's (B1's).
	{"claude", "subagent-start.json", "SubagentStart", "agent_type", "general-purpose", "a3f9c2e81b7d4056"},
	{"claude", "subagent-start-teammate.json", "SubagentStart", "agent_type", "helper", "ahelper-5c1e9a7b3d2f8406"},
	{"claude", "subagent-stop.json", "SubagentStop", "background_tasks", "", "a3f9c2e81b7d4056"},
	{"claude", "subagent-stop-internal.json", "SubagentStop", "agent_type", "", "a2b6d9f04e8c1735"},
	{"claude", "post-tool-use-agent.json", "PostToolUse", "tool_name", "Agent", ""},
	{"claude", "post-tool-use-agent-nested.json", "PostToolUse", "tool_name", "Agent", "a3f9c2e81b7d4056"},
	{"claude", "post-tool-use-agent-teammate.json", "PostToolUse", "tool_name", "Agent", ""},
	{"claude", "stop-background-tasks.json", "Stop", "background_tasks", "", ""},
	{"claude", "pre-tool-use-agent.json", "PreToolUse", "tool_name", "Agent", ""},
	{"claude", "pre-tool-use-agent-teammate.json", "PreToolUse", "tool_name", "Agent", ""},
	{"claude", "post-tool-use-agent-completed.json", "PostToolUse", "tool_name", "Agent", ""},
	// Codex's, from Epic 1's 0.162.1 captures, scrubbed (codex-hook-payloads/). Every id
	// is a fixture value with the captures' relations: a subagent's and the /review
	// thread's hooks carry the main session id and their own rollout; resume carries the
	// resumed session's own id; clear and fork carry new ones.
	{"codex", "session-start-startup.json", "SessionStart", "source", "startup", ""},
	{"codex", "session-start-clear.json", "SessionStart", "source", "clear", ""},
	{"codex", "session-start-resume.json", "SessionStart", "source", "resume", ""},
	{"codex", "session-start-fork.json", "SessionStart", "source", "fork", ""},
	{"codex", "session-start-compact.json", "SessionStart", "source", "compact", ""},
	{"codex", "user-prompt-submit.json", "UserPromptSubmit", "prompt", "", ""},
	{"codex", "user-prompt-submit-review-thread.json", "UserPromptSubmit", "prompt", "", ""},
	{"codex", "pre-tool-use-bash.json", "PreToolUse", "tool_name", "Bash", ""},
	{"codex", "pre-tool-use-request-user-input.json", "PreToolUse", "tool_name", "request_user_input", ""},
	{"codex", "pre-tool-use-request-user-input-async.json", "PreToolUse", "tool_name", "request_user_input_async", ""},
	{"codex", "pre-tool-use-subagent.json", "PreToolUse", "tool_name", "Bash", cxSubagentID},
	{"codex", "pre-tool-use-review-thread.json", "PreToolUse", "tool_name", "Bash", ""},
	{"codex", "post-tool-use-bash.json", "PostToolUse", "tool_response", "", ""},
	{"codex", "post-tool-use-subagent.json", "PostToolUse", "tool_response", "", cxSubagentID},
	{"codex", "permission-request-bash.json", "PermissionRequest", "tool_name", "Bash", ""},
	{"codex", "permission-request-subagent.json", "PermissionRequest", "tool_name", "Bash", cxSubagentID},
	{"codex", "stop.json", "Stop", "last_assistant_message", "", ""},
	{"codex", "interrupt.json", "Interrupt", "turn_id", "", ""},
	// Built from Codex's source shape, not captured (Q3): Codex never sends it.
	{"codex", "interrupt-subagent.json", "Interrupt", "fixture_note", "", cxSubagentID},
	{"codex", "session-end-other.json", "SessionEnd", "reason", "other", ""},
	{"codex", "subagent-start.json", "SubagentStart", "agent_type", "default", cxSubagentID},
	{"codex", "subagent-stop.json", "SubagentStop", "agent_transcript_path", "", cxSubagentID},
}

// fixtureName is the name readPayloadFixture takes for file of agent: a Codex
// file lives in its own directory, and its name may match a Claude fixture's.
func fixtureName(agent, file string) string {
	if agent == "codex" {
		return "codex/" + file
	}
	return file
}

// readPayloadFixture returns the raw bytes of testdata/hook-payloads/<name>, or
// of testdata/codex-hook-payloads/<file> for the name "codex/<file>".
func readPayloadFixture(t *testing.T, name string) []byte {
	t.Helper()
	dir := "hook-payloads"
	if file, ok := strings.CutPrefix(name, "codex/"); ok {
		dir, name = "codex-hook-payloads", file
	}
	raw, err := os.ReadFile(filepath.Join("testdata", dir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// codexPayload returns the Codex fixture with extra keys set (a nil value removes
// the key) and its transcript moved into t.TempDir() under its own rollout file
// name: written when present, absent otherwise. It also returns the payload's
// session id and transcript path.
func codexPayload(t *testing.T, fixture string, present bool, extra map[string]any) (payload []byte, sessionID, path string) {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(readPayloadFixture(t, "codex/"+fixture), &p); err != nil {
		t.Fatalf("fixture %s: %v", fixture, err)
	}
	sessionID, _ = p["session_id"].(string)
	path = filepath.Join(t.TempDir(), filepath.Base(p["transcript_path"].(string)))
	if present {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write rollout %s: %v", path, err)
		}
	}
	p["transcript_path"] = path
	for k, v := range extra {
		if v == nil {
			delete(p, k)
			continue
		}
		p[k] = v
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw, sessionID, path
}

// TestPayloadFixtureParses: every fixture parses and its event, event field and
// agent_id match its row. A Claude session id is the transcript's basename; a
// Codex one is session_id, the transcript is a rollout of another name, and the
// classifier carries the path verbatim and no session id for a hook with an agent_id.
func TestPayloadFixtureParses(t *testing.T) {
	for _, tc := range payloadFixtures {
		t.Run(tc.agent+"/"+tc.file, func(t *testing.T) {
			raw := readPayloadFixture(t, fixtureName(tc.agent, tc.file))
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
			wantSession := sid
			if tc.agent == "codex" {
				if !rolloutName.MatchString(filepath.Base(path)) || hook.ExtractSessionID(path) == sid {
					t.Errorf("transcript_path %q: want a rollout-shaped name that is not the session id %q", path, sid)
				}
				if strings.Contains(string(raw), "/home/u/") {
					t.Errorf("fixture holds a probe home path")
				}
				if tc.agentID != "" {
					wantSession = ""
				}
			} else if got := hook.ExtractSessionID(path); got != sid {
				t.Errorf("ExtractSessionID(%q) = %q; want session_id %q", path, got, sid)
			}
			own, ok := p[tc.field]
			if !ok {
				t.Fatalf("event field %q missing", tc.field)
			}
			if tc.value != "" && own != tc.value {
				t.Errorf("%s = %v; want %q", tc.field, own, tc.value)
			}
			if got, has := p["agent_id"]; has != (tc.agentID != "") || (has && got != tc.agentID) {
				t.Errorf("agent_id = %v (present %v); want %q", got, has, tc.agentID)
			}
			res, err := hook.ClassifyEvent(tc.agent, raw)
			if err != nil {
				t.Fatalf("ClassifyEvent: %v", err)
			}
			if res.AgentID != tc.agentID || res.SessionID != wantSession || res.TranscriptPath != path {
				t.Errorf("ClassifyEvent AgentID/SessionID/TranscriptPath = %q/%q/%q; want %q/%q/%q",
					res.AgentID, res.SessionID, res.TranscriptPath, tc.agentID, wantSession, path)
			}
		})
	}
}

// TestPayloadFixtureEventsMatchRegistered: every event internal/spawn
// registers for Claude has a fixture, every Claude fixture's event is
// registered, and every fixture file, Codex's included, is in payloadFixtures.
// (The Codex registered-events check is E4-T2's.)
func TestPayloadFixtureEventsMatchRegistered(t *testing.T) {
	registered := registeredHookEvents(t)
	fixtureEvents, fixtureFiles, tableFiles := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for dir, agent := range map[string]string{"hook-payloads": "claude", "codex-hook-payloads": "codex"} {
		files, err := filepath.Glob(filepath.Join("testdata", dir, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("glob %s: %v (%d files)", dir, err, len(files))
		}
		for _, f := range files {
			name := fixtureName(agent, filepath.Base(f))
			fixtureFiles[name] = true
			if agent == "claude" {
				fixtureEvents[hook.PeekEventName(readPayloadFixture(t, name))] = true
			}
		}
	}
	for _, tc := range payloadFixtures {
		tableFiles[fixtureName(tc.agent, tc.file)] = true
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
// prompt, assistant message, error, tool output or usage: SR-5.2).
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
}
