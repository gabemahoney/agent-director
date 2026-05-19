package hook_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gabemahoney/claude-director/internal/hook"
)

// TestEncodeDecisionShape pins the byte-level structure per SRD §6.3.
// Claude Code 2.x parses the envelope by reading
// hookSpecificOutput.decision; any deviation in nesting or field
// names silently turns the relay into a fallback-to-native-dialog
// path.
func TestEncodeDecisionShape(t *testing.T) {
	got := hook.EncodeDecision("allow", "looks good")

	var env struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("envelope did not parse: %v\nraw=%s", err, got)
	}
	if env.HookSpecificOutput.HookEventName != "PermissionRequest" {
		t.Errorf("hookEventName = %q; want PermissionRequest", env.HookSpecificOutput.HookEventName)
	}
	if env.HookSpecificOutput.Decision.Behavior != "allow" {
		t.Errorf("behavior = %q; want allow", env.HookSpecificOutput.Decision.Behavior)
	}
	if env.HookSpecificOutput.Decision.Message != "looks good" {
		t.Errorf("message = %q; want \"looks good\"", env.HookSpecificOutput.Decision.Message)
	}
}

func TestEncodeDecisionDenyEmptyReasonDefaults(t *testing.T) {
	// SRD §6.3: empty reason on deny → "Denied by orchestrator".
	got := hook.EncodeDecision("deny", "")
	if !strings.Contains(got, `"behavior":"deny"`) {
		t.Errorf("missing behavior=deny in %q", got)
	}
	if !strings.Contains(got, `"message":"Denied by orchestrator"`) {
		t.Errorf("default deny message missing in %q", got)
	}
}

func TestEncodeDecisionAllowEmptyReasonOmitsMessage(t *testing.T) {
	// SRD §6.3: empty reason on allow → message field absent (Claude
	// Code drops empty messages silently; we mirror that to avoid
	// emitting noise the TUI might display).
	got := hook.EncodeDecision("allow", "")
	// Decode and inspect; "absent" means the message key doesn't
	// appear in decision.
	var env struct {
		HookSpecificOutput struct {
			Decision map[string]any `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("envelope parse: %v", err)
	}
	if _, ok := env.HookSpecificOutput.Decision["message"]; ok {
		t.Errorf("allow envelope unexpectedly contains message field: %s", got)
	}
}

func TestEncodeDecisionDenyWithReasonPreserves(t *testing.T) {
	got := hook.EncodeDecision("deny", "policy block")
	if !strings.Contains(got, `"message":"policy block"`) {
		t.Errorf("explicit deny reason lost: %q", got)
	}
}
