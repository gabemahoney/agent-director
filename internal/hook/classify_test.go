package hook

import (
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// TestClassifyEventSRDTable pins the SRD §5.2 event → state table, b.svb (the
// main agent's idle prompt), b.pmn (only a known terminal SessionEnd cause, in
// reason, matcher or endReason, ends the row; none or an unknown one is a soft
// refresh) and the SRD test rig's legacy event_name field.
func TestClassifyEventSRDTable(t *testing.T) {
	cases := []struct {
		name, raw   string
		wantState   string
		wantSoft    bool
		wantUnknown bool
		wantIdle    bool // WaitingIfWorking (b.svb)
	}{
		{name: "SessionStart", raw: `{"hook_event_name":"SessionStart"}`, wantState: store.StateWaiting},
		{name: "legacy event_name field", raw: `{"event_name":"SessionStart"}`, wantState: store.StateWaiting},
		{name: "UserPromptSubmit", raw: `{"hook_event_name":"UserPromptSubmit"}`, wantState: store.StateWorking},
		{name: "PreToolUse AskUserQuestion", raw: `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`, wantState: store.StateAskUser},
		{name: "PreToolUse Bash", raw: `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, wantState: store.StateWorking},
		{name: "PostToolUse", raw: `{"hook_event_name":"PostToolUse"}`, wantState: store.StateWorking},
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
		{name: "SessionEnd unknown reason", raw: `{"hook_event_name":"SessionEnd","reason":"future_value"}`, wantSoft: true},
		{name: "SessionEnd no cause", raw: `{"hook_event_name":"SessionEnd"}`, wantSoft: true},
		{name: "SessionEnd matcher logout", raw: `{"hook_event_name":"SessionEnd","matcher":"logout"}`, wantState: store.StateEnded},
		{name: "SessionEnd endReason logout", raw: `{"hook_event_name":"SessionEnd","endReason":"logout"}`, wantState: store.StateEnded},
		{name: "SessionEnd matcher compact", raw: `{"hook_event_name":"SessionEnd","matcher":"compact"}`, wantSoft: true},
		{name: "unknown event", raw: `{"hook_event_name":"BrandNewEvent"}`, wantSoft: true, wantUnknown: true},
		{name: "empty payload", raw: ``, wantSoft: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ClassifyEvent(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("ClassifyEvent: %v", err)
			}
			if res.NewState != tc.wantState || res.SoftRefresh != tc.wantSoft || res.UnknownEvent != tc.wantUnknown || res.WaitingIfWorking != tc.wantIdle {
				t.Errorf("state/soft/unknown/idle = %q/%v/%v/%v; want %q/%v/%v/%v", res.NewState, res.SoftRefresh,
					res.UnknownEvent, res.WaitingIfWorking, tc.wantState, tc.wantSoft, tc.wantUnknown, tc.wantIdle)
			}
		})
	}
	if _, err := ClassifyEvent(json.RawMessage("not json")); err == nil {
		t.Error("ClassifyEvent(malformed JSON) = nil error; want the parse error")
	}
}

// TestClassifyEventTranscriptPath pins SR-9.1/SR-22.9: every event carries the
// hook-reported transcript_path verbatim and its basename without extension as
// the session id; a garbage path ("...", "/") yields no session id but is
// still carried verbatim.
func TestClassifyEventTranscriptPath(t *testing.T) {
	const uuid = "12345678-1234-1234-1234-123456789012"
	cases := []struct {
		name, raw, wantPath, wantSessID string
	}{
		{"SessionStart", `{"hook_event_name":"SessionStart","transcript_path":"~/p/` + uuid + `.jsonl"}`, "~/p/" + uuid + ".jsonl", uuid},
		{"SessionStart, legacy event_name", `{"event_name":"SessionStart","transcript_path":"~/x/y/abc.jsonl"}`, "~/x/y/abc.jsonl", "abc"},
		{"SessionStart, no path", `{"hook_event_name":"SessionStart"}`, "", ""},
		{"SessionStart, empty path", `{"hook_event_name":"SessionStart","transcript_path":""}`, "", ""},
		{"SessionStart, path dots", `{"hook_event_name":"SessionStart","transcript_path":"..."}`, "...", ""},
		{"SessionStart, path slash", `{"hook_event_name":"SessionStart","transcript_path":"/"}`, "/", ""},
		{"SessionStart, other extension", `{"hook_event_name":"SessionStart","transcript_path":"sessionid.txt"}`, "sessionid.txt", "sessionid"},
		{"UserPromptSubmit", `{"hook_event_name":"UserPromptSubmit","transcript_path":"/abs/abc.jsonl"}`, "/abs/abc.jsonl", "abc"},
		{"Stop", `{"hook_event_name":"Stop","transcript_path":"~/x/abc.jsonl"}`, "~/x/abc.jsonl", "abc"},
		{"Stop, no path", `{"hook_event_name":"Stop"}`, "", ""},
		{"SessionEnd", `{"hook_event_name":"SessionEnd","reason":"logout","transcript_path":"~/x/abc.jsonl"}`, "~/x/abc.jsonl", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ClassifyEvent(json.RawMessage(tc.raw))
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

// classifyMap marshals payload and classifies it, failing the test on error.
func classifyMap(t *testing.T, payload map[string]any) ClassifyResult {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	res, err := ClassifyEvent(raw)
	if err != nil {
		t.Fatalf("ClassifyEvent: %v", err)
	}
	return res
}
