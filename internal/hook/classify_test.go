package hook

import (
	"encoding/json"
	"testing"

	"github.com/gabemahoney/claude-director/internal/store"
)

func TestClassifyEventSRDTable(t *testing.T) {
	cases := []struct {
		name        string
		payload     map[string]any
		wantState   string
		wantSoft    bool
		wantUnknown bool
	}{
		{
			name:      "SessionStart",
			payload:   map[string]any{"hook_event_name": "SessionStart"},
			wantState: store.StateWaiting,
		},
		{
			name:      "UserPromptSubmit",
			payload:   map[string]any{"hook_event_name": "UserPromptSubmit"},
			wantState: store.StateWorking,
		},
		{
			name:      "PreToolUse_AskUserQuestion",
			payload:   map[string]any{"hook_event_name": "PreToolUse", "tool_name": "AskUserQuestion"},
			wantState: store.StateAskUser,
		},
		{
			name:      "PreToolUse_Bash",
			payload:   map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash"},
			wantState: store.StateWorking,
		},
		{
			name:      "PreToolUse_Read",
			payload:   map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Read"},
			wantState: store.StateWorking,
		},
		{
			name:      "PostToolUse",
			payload:   map[string]any{"hook_event_name": "PostToolUse"},
			wantState: store.StateWorking,
		},
		{
			name:      "Stop",
			payload:   map[string]any{"hook_event_name": "Stop"},
			wantState: store.StateWaiting,
		},
		{
			name:      "Notification",
			payload:   map[string]any{"hook_event_name": "Notification"},
			wantState: store.StateWaiting,
		},
		{
			name:      "PermissionRequest",
			payload:   map[string]any{"hook_event_name": "PermissionRequest"},
			wantState: store.StateCheckPermission,
		},
		{
			name:     "SessionEnd_clear_soft",
			payload:  map[string]any{"hook_event_name": "SessionEnd", "reason": "clear"},
			wantSoft: true,
		},
		{
			name:     "SessionEnd_compact_soft",
			payload:  map[string]any{"hook_event_name": "SessionEnd", "reason": "compact"},
			wantSoft: true,
		},
		{
			name:      "SessionEnd_user_quit_ended",
			payload:   map[string]any{"hook_event_name": "SessionEnd", "reason": "user_quit"},
			wantState: store.StateEnded,
		},
		{
			name:      "SessionEnd_unknown_reason_ended",
			payload:   map[string]any{"hook_event_name": "SessionEnd", "reason": "future_value"},
			wantState: store.StateEnded,
		},
		{
			name:        "unknown_event_soft_and_flagged",
			payload:     map[string]any{"hook_event_name": "BrandNewEvent"},
			wantSoft:    true,
			wantUnknown: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			res, err := ClassifyEvent(raw)
			if err != nil {
				t.Fatalf("ClassifyEvent: %v", err)
			}
			if res.SoftRefresh != tc.wantSoft {
				t.Errorf("SoftRefresh = %v; want %v", res.SoftRefresh, tc.wantSoft)
			}
			if res.NewState != tc.wantState {
				t.Errorf("NewState = %q; want %q", res.NewState, tc.wantState)
			}
			if res.UnknownEvent != tc.wantUnknown {
				t.Errorf("UnknownEvent = %v; want %v", res.UnknownEvent, tc.wantUnknown)
			}
		})
	}
}

// TestClassifyEventAcceptsLegacyEventNameField pins the SRD-test-example
// compatibility: Claude Code emits `hook_event_name`, but the SRD test
// rig in subtask 4.4 uses `event_name`. The classifier accepts either.
func TestClassifyEventAcceptsLegacyEventNameField(t *testing.T) {
	raw := json.RawMessage(`{"event_name":"SessionStart","transcript_path":"~/x/y/abc.jsonl"}`)
	res, err := ClassifyEvent(raw)
	if err != nil {
		t.Fatalf("ClassifyEvent: %v", err)
	}
	if res.NewState != store.StateWaiting {
		t.Errorf("NewState = %q; want waiting", res.NewState)
	}
	if res.SessionID != "abc" {
		t.Errorf("SessionID = %q; want abc", res.SessionID)
	}
}

func TestClassifyEventSessionIDExtraction(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"~/.claude/projects/-tmp/12345678-1234-1234-1234-123456789012.jsonl",
			"12345678-1234-1234-1234-123456789012"},
		{"/abs/path/uuid.jsonl", "uuid"},
		{"sessionid.txt", "sessionid"},
		{"", ""},
		// "..." has its last '.' at index 2; base[:2] = ".." which the
		// classifier rejects as obviously-bogus -> "" (don't clobber the
		// known good session id with garbage).
		{"...", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := extractSessionID(tc.path)
			if got != tc.want {
				t.Errorf("extractSessionID(%q) = %q; want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassifyEventEmptyPayloadIsSoftRefresh(t *testing.T) {
	res, err := ClassifyEvent(nil)
	if err != nil {
		t.Fatalf("ClassifyEvent(nil): %v", err)
	}
	if !res.SoftRefresh {
		t.Errorf("empty payload should soft-refresh; res = %+v", res)
	}
}

func TestClassifyEventMalformedJSONReturnsError(t *testing.T) {
	_, err := ClassifyEvent(json.RawMessage("not json"))
	if err == nil {
		t.Fatalf("expected JSON parse error")
	}
}

func TestClassifyEventSessionEndMissingReasonIsEnded(t *testing.T) {
	// SessionEnd with no reason field — conservative: any reason not in
	// the closed soft set transitions to ended.
	raw := json.RawMessage(`{"hook_event_name":"SessionEnd"}`)
	res, err := ClassifyEvent(raw)
	if err != nil {
		t.Fatalf("ClassifyEvent: %v", err)
	}
	if res.NewState != store.StateEnded {
		t.Errorf("NewState = %q; want ended", res.NewState)
	}
}
