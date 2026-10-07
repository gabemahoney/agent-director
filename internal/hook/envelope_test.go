package hook_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
)

// TestEncodeDecision pins the envelope's byte-level structure per SRD §6.3
// (Claude Code reads hookSpecificOutput.decision; any other nesting silently
// falls back to the native dialog): an empty deny reason becomes "Denied by
// orchestrator", an empty allow reason omits message, and hookEventName is the
// caller's event name verbatim (b.45p).
func TestEncodeDecision(t *testing.T) {
	cases := []struct {
		event, behavior, reason string
		wantMessage             string // "" = message absent
	}{
		{hook.EventNamePermissionRequest, "allow", "looks good", "looks good"},
		{hook.EventNamePermissionRequest, "allow", "", ""},
		{hook.EventNamePermissionRequest, "deny", "", "Denied by orchestrator"},
		{hook.EventNamePermissionRequest, "deny", "policy block", "policy block"},
		{"PreToolUse", "deny", "x", "x"},
		{"", "deny", "x", "x"},
	}
	for _, tc := range cases {
		got := hook.EncodeDecision(tc.event, tc.behavior, tc.reason)
		decision := map[string]any{"behavior": tc.behavior}
		if tc.wantMessage != "" {
			decision["message"] = tc.wantMessage
		}
		want := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": tc.event, "decision": decision}}
		var env map[string]any
		if err := json.Unmarshal([]byte(got), &env); err != nil || !reflect.DeepEqual(env, want) {
			t.Errorf("EncodeDecision(%q, %q, %q) = %s (%v); want %v", tc.event, tc.behavior, tc.reason, got, err, want)
		}
	}
}
