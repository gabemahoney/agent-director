package hook

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// TestPermissionRequestNotOverwrittenByNotification is the regression test for
// b.ozs: a Notification(permission_prompt) following a PermissionRequest must
// not stomp the check_permission state.
//
// Sequence under test (relay_mode=off):
//  1. PreToolUse(Bash)        → state: working
//  2. PermissionRequest(Bash) → state: check_permission
//  3. Notification(permission_prompt) → soft-refresh only, state stays check_permission
func TestPermissionRequestNotOverwrittenByNotification(t *testing.T) {
	st, err := store.OpenOrInit(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("OpenOrInit: %v", err)
	}
	defer st.Close()

	const instanceID = "reg-b-ozs"
	if err := st.InsertPending(store.Spawn{
		ClaudeInstanceID: instanceID,
		State:            store.StatePending,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-test",
		RelayMode:        "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	env := func(key string) string {
		switch key {
		case envInstanceID:
			return instanceID
		case EnvRelayMode:
			return RelayModeOff
		}
		return ""
	}
	hc := HandleConfig{Env: env, Cfg: config.Relay{}}

	fire := func(payloadJSON string) {
		t.Helper()
		if err := Handle(context.Background(), strings.NewReader(payloadJSON), io.Discard, st, hc, nil); err != nil {
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
