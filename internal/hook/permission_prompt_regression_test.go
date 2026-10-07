package hook_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// TestPermissionRequestNotOverwrittenByNotification is the b.ozs regression:
// with relay_mode off, a Notification(permission_prompt) after a
// PermissionRequest is a soft refresh and leaves the row check_permission.
func TestPermissionRequestNotOverwrittenByNotification(t *testing.T) {
	const id = "reg-b-ozs"
	st, _ := seedAgentRow(t, id, store.StatePending)
	agent, env := agentParent(t, st, id), envHook(id, hook.RelayModeOff)
	for _, payload := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
		`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{}}`,
		`{"hook_event_name":"Notification","notification_type":"permission_prompt"}`,
	} {
		fireGate(t, st, id, agent, env, []byte(payload))
	}
	if state := mustGetSpawn(t, st, id).State; state != store.StateCheckPermission {
		t.Errorf("state = %q after the Notification; want %q", state, store.StateCheckPermission)
	}
}
