package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// TestClassifyEventSRDTable pins the SRD §5.2 event → state table of Claude (an
// empty agent is Claude: a hook entry without --agent) and SR-6.2's of Codex
// (agent "codex"), b.svb (the
// main agent's idle prompt), b.pmn (only a known terminal SessionEnd cause, in
// reason, matcher or endReason, ends the row; none or an unknown one is a soft
// refresh, and so is `other`, the reason a killed agent's SessionEnd carries,
// N9, so it closes no subagent record) and the SRD test rig's legacy event_name field.
func TestClassifyEventSRDTable(t *testing.T) {
	cases := []struct {
		name, agent, raw string
		wantState        string
		wantSoft         bool
		wantUnknown      bool
		wantIdle         bool // WaitingIfWorking (b.svb)
		wantEnd          bool // EndReport (SR-6.5)
	}{
		{name: "SessionStart", raw: `{"hook_event_name":"SessionStart"}`, wantState: store.StateWaiting},
		{name: "legacy event_name field", raw: `{"event_name":"SessionStart"}`, wantState: store.StateWaiting},
		{name: "UserPromptSubmit", raw: `{"hook_event_name":"UserPromptSubmit"}`, wantState: store.StateWorking},
		{name: "PreToolUse AskUserQuestion", raw: `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`, wantState: store.StateAskUser},
		{name: "PreToolUse Bash", raw: `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, wantState: store.StateWorking},
		{name: "PostToolUse", raw: `{"hook_event_name":"PostToolUse"}`, wantState: store.StateWorking},
		{name: "PostToolUseFailure", raw: `{"hook_event_name":"PostToolUseFailure"}`, wantState: store.StateWorking},
		{name: "Stop", raw: `{"hook_event_name":"Stop"}`, wantState: store.StateWaiting},
		{name: "Notification", raw: `{"hook_event_name":"Notification"}`, wantSoft: true},
		{name: "Notification idle_prompt", raw: `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, wantSoft: true, wantIdle: true},
		{name: "Notification idle_prompt from a subagent", raw: `{"hook_event_name":"Notification","notification_type":"idle_prompt","agent_id":"a1"}`, wantSoft: true},
		{name: "Notification permission_prompt", raw: `{"hook_event_name":"Notification","notification_type":"permission_prompt"}`, wantSoft: true},
		{name: "PermissionRequest", raw: `{"hook_event_name":"PermissionRequest"}`, wantState: store.StateCheckPermission},
		{name: "SessionEnd clear", raw: `{"hook_event_name":"SessionEnd","reason":"clear"}`, wantSoft: true},
		{name: "SessionEnd compact", raw: `{"hook_event_name":"SessionEnd","reason":"compact"}`, wantSoft: true},
		{name: "SessionEnd logout", raw: `{"hook_event_name":"SessionEnd","reason":"logout"}`, wantState: store.StateEnded},
		{name: "SessionEnd prompt_input_exit", raw: `{"hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`, wantState: store.StateEnded},
		{name: "SessionEnd exit", raw: `{"hook_event_name":"SessionEnd","reason":"exit"}`, wantState: store.StateEnded},
		{name: "SessionEnd other", raw: `{"hook_event_name":"SessionEnd","reason":"other"}`, wantSoft: true},
		{name: "SessionEnd matcher other", raw: `{"hook_event_name":"SessionEnd","matcher":"other"}`, wantSoft: true},
		{name: "SessionEnd unknown reason", raw: `{"hook_event_name":"SessionEnd","reason":"future_value"}`, wantSoft: true},
		{name: "SessionEnd no cause", raw: `{"hook_event_name":"SessionEnd"}`, wantSoft: true},
		{name: "SessionEnd matcher logout", raw: `{"hook_event_name":"SessionEnd","matcher":"logout"}`, wantState: store.StateEnded},
		{name: "SessionEnd endReason logout", raw: `{"hook_event_name":"SessionEnd","endReason":"logout"}`, wantState: store.StateEnded},
		{name: "SessionEnd matcher compact", raw: `{"hook_event_name":"SessionEnd","matcher":"compact"}`, wantSoft: true},
		{name: "SubagentStart", raw: `{"hook_event_name":"SubagentStart"}`, wantSoft: true},
		{name: "SubagentStart with agent_id", raw: `{"hook_event_name":"SubagentStart","agent_id":"a1","agent_type":"general-purpose"}`, wantSoft: true},
		{name: "SubagentStop", raw: `{"hook_event_name":"SubagentStop"}`, wantSoft: true},
		{name: "SubagentStop with agent_id", raw: `{"hook_event_name":"SubagentStop","agent_id":"a1","stop_hook_active":false}`, wantSoft: true},
		{name: "unknown event", raw: `{"hook_event_name":"BrandNewEvent"}`, wantSoft: true, wantUnknown: true},
		{name: "empty payload", raw: ``, wantSoft: true},
		// Codex (SR-6.2, MC-6): no idle path, no ask_user, no ended; SessionEnd is the end report.
		{name: "codex SessionStart startup", agent: "codex", raw: `{"hook_event_name":"SessionStart","source":"startup"}`, wantState: store.StateWaiting},
		{name: "codex SessionStart clear", agent: "codex", raw: `{"hook_event_name":"SessionStart","source":"clear"}`, wantState: store.StateWaiting},
		{name: "codex SessionStart resume", agent: "codex", raw: `{"hook_event_name":"SessionStart","source":"resume"}`, wantState: store.StateWaiting},
		{name: "codex SessionStart fork", agent: "codex", raw: `{"hook_event_name":"SessionStart","source":"fork"}`, wantState: store.StateWaiting},
		{name: "codex SessionStart compact", agent: "codex", raw: `{"hook_event_name":"SessionStart","source":"compact"}`, wantState: store.StateWaiting},
		{name: "codex UserPromptSubmit", agent: "codex", raw: `{"hook_event_name":"UserPromptSubmit"}`, wantState: store.StateWorking},
		{name: "codex PreToolUse Bash", agent: "codex", raw: `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, wantState: store.StateWorking},
		{name: "codex PreToolUse request_user_input", agent: "codex", raw: `{"hook_event_name":"PreToolUse","tool_name":"request_user_input"}`, wantState: store.StateWorking},
		{name: "codex PreToolUse request_user_input_async", agent: "codex", raw: `{"hook_event_name":"PreToolUse","tool_name":"request_user_input_async"}`, wantState: store.StateWorking},
		{name: "codex PreToolUse AskUserQuestion", agent: "codex", raw: `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`, wantState: store.StateWorking},
		{name: "codex PostToolUse", agent: "codex", raw: `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`, wantState: store.StateWorking},
		{name: "codex PostToolUse request_user_input", agent: "codex", raw: `{"hook_event_name":"PostToolUse","tool_name":"request_user_input"}`, wantState: store.StateWorking},
		{name: "codex Stop", agent: "codex", raw: `{"hook_event_name":"Stop"}`, wantState: store.StateWaiting},
		{name: "codex Interrupt", agent: "codex", raw: `{"hook_event_name":"Interrupt"}`, wantState: store.StateWaiting},
		{name: "codex Interrupt with agent_id", agent: "codex", raw: `{"hook_event_name":"Interrupt","agent_id":"a1"}`, wantSoft: true},
		{name: "codex PermissionRequest", agent: "codex", raw: `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`, wantState: store.StateCheckPermission},
		{name: "codex Notification", agent: "codex", raw: `{"hook_event_name":"Notification"}`, wantSoft: true},
		{name: "codex Notification idle_prompt is no idle path", agent: "codex", raw: `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, wantSoft: true},
		{name: "codex PostToolUseFailure", agent: "codex", raw: `{"hook_event_name":"PostToolUseFailure"}`, wantSoft: true},
		{name: "codex SubagentStart", agent: "codex", raw: `{"hook_event_name":"SubagentStart","agent_id":"a1"}`, wantSoft: true},
		{name: "codex SubagentStop", agent: "codex", raw: `{"hook_event_name":"SubagentStop","agent_id":"a1"}`, wantSoft: true},
		{name: "codex SessionEnd other", agent: "codex", raw: `{"hook_event_name":"SessionEnd","reason":"other"}`, wantSoft: true, wantEnd: true},
		{name: "codex SessionEnd with a reason Claude ends the row on", agent: "codex", raw: `{"hook_event_name":"SessionEnd","reason":"logout"}`, wantSoft: true, wantEnd: true},
		{name: "codex SessionEnd no cause", agent: "codex", raw: `{"hook_event_name":"SessionEnd"}`, wantSoft: true, wantEnd: true},
		{name: "codex SessionEnd with agent_id", agent: "codex", raw: `{"hook_event_name":"SessionEnd","reason":"other","agent_id":"a1"}`, wantSoft: true},
		{name: "codex unknown event", agent: "codex", raw: `{"hook_event_name":"BrandNewEvent"}`, wantSoft: true, wantUnknown: true},
		{name: "codex empty payload", agent: "codex", raw: ``, wantSoft: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ClassifyEvent(tc.agent, json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("ClassifyEvent: %v", err)
			}
			if res.NewState != tc.wantState || res.SoftRefresh != tc.wantSoft || res.UnknownEvent != tc.wantUnknown ||
				res.WaitingIfWorking != tc.wantIdle || res.EndReport != tc.wantEnd {
				t.Errorf("state/soft/unknown/idle/end = %q/%v/%v/%v/%v; want %q/%v/%v/%v/%v", res.NewState, res.SoftRefresh,
					res.UnknownEvent, res.WaitingIfWorking, res.EndReport, tc.wantState, tc.wantSoft, tc.wantUnknown, tc.wantIdle, tc.wantEnd)
			}
		})
	}
	for _, agent := range []string{"claude", "codex"} {
		if _, err := ClassifyEvent(agent, json.RawMessage("not json")); err == nil {
			t.Errorf("ClassifyEvent(%s, malformed JSON) = nil error; want the parse error", agent)
		}
	}
}

// TestClassifyEventAgentSelection (SR-6.1): the agent picks the table and the
// result's flags: an absent --agent is Claude; only Claude's hooks feed subagent
// capture, only a Codex hook without an agent_id heals a transcript path;
// Cursor is accepted, and an unknown agent is ErrInvalidAgent.
func TestClassifyEventAgentSelection(t *testing.T) {
	cases := []struct {
		name, agent, raw string
		wantAgent        string
		wantCapture      bool
		wantHeals        bool
	}{
		{"absent is claude", "", `{"hook_event_name":"Stop"}`, "claude", true, false},
		{"claude", "claude", `{"hook_event_name":"Stop"}`, "claude", true, false},
		{"claude, empty payload", "claude", ``, "claude", true, false},
		{"codex", "codex", `{"hook_event_name":"Stop"}`, "codex", false, true},
		{"codex with agent_id", "codex", `{"hook_event_name":"PreToolUse","agent_id":"a1"}`, "codex", false, false},
		{"codex, empty payload", "codex", ``, "codex", false, false},
		{"cursor", "cursor", `{"hook_event_name":"Stop","origin":"main"}`, "cursor", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ClassifyEvent(tc.agent, json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("ClassifyEvent: %v", err)
			}
			if res.Agent != tc.wantAgent || res.SubagentCapture != tc.wantCapture || res.HealsTranscript != tc.wantHeals {
				t.Errorf("Agent/SubagentCapture/HealsTranscript = %q/%v/%v; want %q/%v/%v",
					res.Agent, res.SubagentCapture, res.HealsTranscript, tc.wantAgent, tc.wantCapture, tc.wantHeals)
			}
		})
	}
	for _, agent := range []string{"gemini", "Codex", " codex", "(no value)"} {
		t.Run("invalid "+agent, func(t *testing.T) {
			_, err := ClassifyEvent(agent, json.RawMessage(`{"hook_event_name":"Stop"}`))
			if !errors.Is(err, ErrInvalidAgent) {
				t.Errorf("ClassifyEvent(%q) error = %v; want ErrInvalidAgent", agent, err)
			}
			if want := fmt.Sprintf("hook: invalid --agent: %q (want claude, codex or cursor)", agent); err == nil || err.Error() != want {
				t.Errorf("ClassifyEvent(%q) error = %v; want %q", agent, err, want)
			}
		})
	}
}

// TestClassifyEventTranscriptPath pins SR-9.1/SR-22.9: every event carries the
// hook-reported transcript_path verbatim and its basename without extension as
// the session id; a garbage path ("...", "/") yields no session id but is
// still carried verbatim. A Codex hook's session id is its payload's session_id
// (SR-6.3), never the rollout's file name, and a Codex hook with an agent_id has none.
func TestClassifyEventTranscriptPath(t *testing.T) {
	const uuid = "12345678-1234-1234-1234-123456789012"
	cases := []struct {
		name, agent, raw, wantPath, wantSessID string
	}{
		{"SessionStart", "", `{"hook_event_name":"SessionStart","transcript_path":"~/p/` + uuid + `.jsonl"}`, "~/p/" + uuid + ".jsonl", uuid},
		{"SessionStart, legacy event_name", "", `{"event_name":"SessionStart","transcript_path":"~/x/y/abc.jsonl"}`, "~/x/y/abc.jsonl", "abc"},
		{"SessionStart, no path", "", `{"hook_event_name":"SessionStart"}`, "", ""},
		{"SessionStart, empty path", "", `{"hook_event_name":"SessionStart","transcript_path":""}`, "", ""},
		{"SessionStart, path dots", "", `{"hook_event_name":"SessionStart","transcript_path":"..."}`, "...", ""},
		{"SessionStart, path slash", "", `{"hook_event_name":"SessionStart","transcript_path":"/"}`, "/", ""},
		{"SessionStart, other extension", "", `{"hook_event_name":"SessionStart","transcript_path":"sessionid.txt"}`, "sessionid.txt", "sessionid"},
		{"UserPromptSubmit", "", `{"hook_event_name":"UserPromptSubmit","transcript_path":"/abs/abc.jsonl"}`, "/abs/abc.jsonl", "abc"},
		{"Stop", "", `{"hook_event_name":"Stop","transcript_path":"~/x/abc.jsonl"}`, "~/x/abc.jsonl", "abc"},
		{"Stop, no path", "", `{"hook_event_name":"Stop"}`, "", ""},
		{"SessionEnd", "", `{"hook_event_name":"SessionEnd","reason":"logout","transcript_path":"~/x/abc.jsonl"}`, "~/x/abc.jsonl", "abc"},
		{"claude ignores session_id", "claude", `{"hook_event_name":"Stop","session_id":"payload-id","transcript_path":"~/x/abc.jsonl"}`, "~/x/abc.jsonl", "abc"},
		{"codex session_id, rollout of another name", "codex", `{"hook_event_name":"SessionStart","session_id":"` + uuid + `","transcript_path":"~/s/rollout-2026-01-15T10-00-00-` + uuid + `.jsonl"}`,
			"~/s/rollout-2026-01-15T10-00-00-" + uuid + ".jsonl", uuid},
		{"codex session_id, path of another session", "codex", `{"hook_event_name":"UserPromptSubmit","session_id":"main","transcript_path":"~/s/rollout-2026-01-15T10-00-00-review.jsonl"}`,
			"~/s/rollout-2026-01-15T10-00-00-review.jsonl", "main"},
		{"codex, no session_id", "codex", `{"hook_event_name":"Stop","transcript_path":"~/x/abc.jsonl"}`, "~/x/abc.jsonl", ""},
		{"codex, no path", "codex", `{"hook_event_name":"Stop","session_id":"main"}`, "", "main"},
		{"codex with agent_id: no session id", "codex", `{"hook_event_name":"PreToolUse","agent_id":"a1","session_id":"main","transcript_path":"~/x/abc.jsonl"}`, "~/x/abc.jsonl", ""},
		{"codex SessionEnd", "codex", `{"hook_event_name":"SessionEnd","reason":"other","session_id":"main","transcript_path":"~/x/abc.jsonl"}`, "~/x/abc.jsonl", "main"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ClassifyEvent(tc.agent, json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("ClassifyEvent: %v", err)
			}
			if res.TranscriptPath != tc.wantPath || res.SessionID != tc.wantSessID {
				t.Errorf("TranscriptPath/SessionID = %q/%q; want %q/%q", res.TranscriptPath, res.SessionID, tc.wantPath, tc.wantSessID)
			}
		})
	}
}

// TestClassifyEventAgentID pins SR-22.9: only a non-empty agent_id marks a subagent
// SessionStart/SessionEnd; agent_id and agent_type never change the classification itself.
func TestClassifyEventAgentID(t *testing.T) {
	cases := []struct {
		name          string
		payload       map[string]any
		wantAgentID   string
		wantLifecycle bool
	}{
		{"SessionStart agent_id", map[string]any{"hook_event_name": "SessionStart", "agent_id": "a1"}, "a1", true},
		{"SessionStart legacy event_name agent_id", map[string]any{"event_name": "SessionStart", "agent_id": "a1"}, "a1", true},
		{"SessionEnd terminal agent_id", map[string]any{"hook_event_name": "SessionEnd", "reason": "prompt_input_exit", "agent_id": "a1"}, "a1", true},
		{"SessionEnd soft agent_id", map[string]any{"hook_event_name": "SessionEnd", "reason": "clear", "agent_id": "a1"}, "a1", true},
		{"PreToolUse agent_id", map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "agent_id": "a1"}, "a1", false},
		{"SessionStart agent_id empty", map[string]any{"hook_event_name": "SessionStart", "agent_id": ""}, "", false},
		{"SessionStart agent_id absent", map[string]any{"hook_event_name": "SessionStart"}, "", false},
		{"SessionStart agent_type only", map[string]any{"hook_event_name": "SessionStart", "agent_type": "reviewer"}, "", false},
		{"PreToolUse agent_type only", map[string]any{"hook_event_name": "PreToolUse", "tool_name": "AskUserQuestion", "agent_type": "reviewer"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.payload["transcript_path"] = "~/x/abc.jsonl"
			res := classifyMap(t, tc.payload)
			if res.AgentID != tc.wantAgentID || res.SubagentLifecycle() != tc.wantLifecycle {
				t.Errorf("AgentID/SubagentLifecycle = %q/%v; want %q/%v", res.AgentID, res.SubagentLifecycle(), tc.wantAgentID, tc.wantLifecycle)
			}
			// Apart from AgentID, the result equals the same payload without agent fields.
			plain := map[string]any{}
			for k, v := range tc.payload {
				if k != "agent_id" && k != "agent_type" {
					plain[k] = v
				}
			}
			want := classifyMap(t, plain)
			want.AgentID = res.AgentID
			if res != want {
				t.Errorf("result = %+v; want %+v", res, want)
			}
		})
	}
}

// TestClassifyEventSubagentStartStop (SR-2.2, SR-13.1): SubagentStart and
// SubagentStop, with and without agent_id, are known events that carry the
// agent_id verbatim, and are none of a subagent SessionStart/SessionEnd (so
// never ignored as subagent_event), the main agent's end of turn or a tool that
// ran.
func TestClassifyEventSubagentStartStop(t *testing.T) {
	for _, event := range []string{"SubagentStart", "SubagentStop"} {
		for _, agentID := range []string{"a1", ""} {
			name := event + "/agent_id=" + agentID
			t.Run(name, func(t *testing.T) {
				p := map[string]any{"hook_event_name": event, "tool_use_id": "toolu_01", "agent_id": agentID}
				res := classifyMap(t, p)
				if res.EventName != event || res.UnknownEvent || !res.SoftRefresh || res.NewState != "" {
					t.Errorf("EventName/UnknownEvent/SoftRefresh/NewState = %q/%v/%v/%q; want %q/false/true/empty",
						res.EventName, res.UnknownEvent, res.SoftRefresh, res.NewState, event)
				}
				if res.AgentID != agentID {
					t.Errorf("AgentID = %q; want %q", res.AgentID, agentID)
				}
				if res.SubagentLifecycle() || res.TurnEnded() || res.ToolRan() || res.WaitingIfWorking {
					t.Errorf("SubagentLifecycle/TurnEnded/ToolRan/WaitingIfWorking = %v/%v/%v/%v; want all false",
						res.SubagentLifecycle(), res.TurnEnded(), res.ToolRan(), res.WaitingIfWorking)
				}
			})
		}
	}
}

// TestClassifyEventToolRan (b.146 rule 13): only a PostToolUse or
// PostToolUseFailure carrying a tool_use_id says its tool ran; every event
// carries its tool_use_id verbatim.
func TestClassifyEventToolRan(t *testing.T) {
	cases := []struct {
		agent, event, toolUseID string
		want                    bool
	}{
		{"claude", "PostToolUse", "toolu_01", true},
		{"claude", "PostToolUseFailure", "toolu_01", true},
		{"claude", "PostToolUse", "", false},
		{"claude", "PostToolUseFailure", "", false},
		{"claude", "PreToolUse", "toolu_01", false},
		{"claude", "PermissionRequest", "toolu_01", false},
		{"codex", "PostToolUse", "call_01", false},
		{"codex", "PostToolUseFailure", "call_01", false},
		{"codex", "PreToolUse", "call_01", false},
	}
	for _, tc := range cases {
		t.Run(tc.agent+"/"+tc.event+"/"+tc.toolUseID, func(t *testing.T) {
			p := map[string]any{"hook_event_name": tc.event, "tool_name": "Bash"}
			if tc.toolUseID != "" {
				p["tool_use_id"] = tc.toolUseID
			}
			res := classifyAgentMap(t, tc.agent, p)
			if res.ToolRan() != tc.want || res.ToolUseID != tc.toolUseID {
				t.Errorf("ToolRan/ToolUseID = %v/%q; want %v/%q", res.ToolRan(), res.ToolUseID, tc.want, tc.toolUseID)
			}
		})
	}
}

// TestClassifyCodexMainThreadTurn (b.x72): a Codex hook is from the row's main
// thread only with no agent_id and a transcript file name carrying its
// session id; only then does it carry its turn_id, and only one of at most
// 256 bytes. A /review thread's hook (the main session id, its own rollout), a
// subagent's and a Claude hook are not main-thread and carry no turn.
func TestClassifyCodexMainThreadTurn(t *testing.T) {
	cases := []struct {
		name, agent, fixture string
		extra                map[string]any
		wantMain             bool
		wantTurn             string
	}{
		{"main-thread PreToolUse", "codex", "pre-tool-use-bash.json", nil, true, "019f0000-0000-7000-8000-000000000006"},
		{"main-thread UserPromptSubmit", "codex", "user-prompt-submit.json", nil, true, "019f0000-0000-7000-8000-000000000006"},
		{"main-thread Stop with no turn_id", "codex", "stop.json", map[string]any{"turn_id": nil}, true, ""},
		{"agent_id on a main-thread payload", "codex", "pre-tool-use-bash.json", map[string]any{"agent_id": "a1"}, false, ""},
		{"subagent PreToolUse", "codex", "pre-tool-use-subagent.json", nil, false, ""},
		{"review thread PreToolUse", "codex", "pre-tool-use-review-thread.json", nil, false, ""},
		{"no session_id", "codex", "pre-tool-use-bash.json", map[string]any{"session_id": nil}, false, ""},
		{"256-byte turn_id kept", "codex", "pre-tool-use-bash.json", map[string]any{"turn_id": strings.Repeat("t", 256)}, true, strings.Repeat("t", 256)},
		{"257-byte turn_id dropped", "codex", "pre-tool-use-bash.json", map[string]any{"turn_id": strings.Repeat("t", 257)}, true, ""},
		{"Claude hook", "claude", "pre-tool-use-bash.json", nil, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "codex-hook-payloads", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			var p map[string]any
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatalf("fixture %s: %v", tc.fixture, err)
			}
			for k, v := range tc.extra {
				if v == nil {
					delete(p, k)
					continue
				}
				p[k] = v
			}
			res := classifyAgentMap(t, tc.agent, p)
			if res.MainThread != tc.wantMain || res.TurnID != tc.wantTurn {
				t.Errorf("MainThread/TurnID = %v/%q; want %v/%q", res.MainThread, res.TurnID, tc.wantMain, tc.wantTurn)
			}
		})
	}
}

// classifyMap marshals payload and classifies it as a Claude hook, failing the test on error.
func classifyMap(t *testing.T, payload map[string]any) ClassifyResult {
	t.Helper()
	return classifyAgentMap(t, "claude", payload)
}

// classifyAgentMap marshals payload and classifies it as a hook of agent, failing the test on error.
func classifyAgentMap(t *testing.T, agent string, payload map[string]any) ClassifyResult {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	res, err := ClassifyEvent(agent, raw)
	if err != nil {
		t.Fatalf("ClassifyEvent: %v", err)
	}
	return res
}
