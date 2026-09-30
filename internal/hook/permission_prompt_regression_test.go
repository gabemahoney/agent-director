package hook_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// TestPermissionRequestNotOverwrittenByNotification is the regression test for
// b.ozs: a Notification(permission_prompt) following a PermissionRequest must
// not stomp the check_permission state.
//
// Sequence under test (relay_mode=off), every hook from the row's own agent
// (SR-22.9: its recorded pane process):
//  1. PreToolUse(Bash)        → state: working
//  2. PermissionRequest(Bash) → state: check_permission
//  3. Notification(permission_prompt) → soft-refresh only, state stays check_permission
func TestPermissionRequestNotOverwrittenByNotification(t *testing.T) {
	const instanceID = "reg-b-ozs"
	// SR-22.9: a pending row with a recorded pane (was InsertPending with none, which no hook can move now).
	st, _ := seedAgentRow(t, instanceID, store.StatePending)
	hc := hookConfig(envHook(instanceID, hook.RelayModeOff), agentParent(t, st, instanceID))

	fire := func(payloadJSON string) {
		t.Helper()
		if err := hook.Handle(context.Background(), strings.NewReader(payloadJSON), io.Discard, st, hc, nil); err != nil {
			t.Fatalf("Handle(%s): %v", payloadJSON, err)
		}
	}

	fire(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	fire(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{}}`)
	fire(`{"hook_event_name":"Notification","notification_type":"permission_prompt"}`)

	state, err := st.GetSpawnState(instanceID)
	if err != nil {
		t.Fatalf("GetSpawnState: %v", err)
	}
	if state != store.StateCheckPermission {
		t.Errorf("state = %q after Notification; want %q (Notification must not overwrite PermissionRequest state)", state, store.StateCheckPermission)
	}
}
