package main_test

// dialog_hold_cli_test.go — b.146 step 2c through the built CLI and its real
// hooks: a relayed request's own ack is no proof that its dialog is gone, so
// plain send-keys is held (ErrDialogMaybeOpen, err_details on the envelope)
// until a hook of the agent proves it gone. The hooks run as children of this
// test process, the row's recorded pane process (hook_gate_cli_test.go).

import (
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// TestCLIDialogHeldUntilAHookProvesItGone (b.146 step 2c rules 1, 2, 4): a
// relayed PermissionRequest ends with its timeout deny, acked (delivered);
// plain send-keys is then ErrDialogMaybeOpen naming it (delivery delivered,
// unproven_since set) and get lists it in unproven_requests. A PostToolUse
// with another tool_use_id proves nothing; the main agent's Stop proves it
// gone (turn_end): get-permission gives the proof and get lists none.
func TestCLIDialogHeldUntilAHookProvesItGone(t *testing.T) {
	h := newGateHome(t)
	const id = "id-cli-hold"
	h.seed(t, id, hook.RelayModeOn, withTestProcessPane(t))
	writeRelayTimeout(t, h.home)
	deny := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"
	if out := h.hook(t, id, hook.RelayModeOn,
		`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"},"tool_use_id":"toolu_cli_hold"}`); out != deny {
		t.Fatalf("relay hook stdout = %q; want its timeout deny %q", out, deny)
	}
	assertOneTimeoutDeny(t, h.home, id)

	env := cliErrorEnvelope(t, h.home, "send-keys", "--claude-instance-id", id, "--text", "hi")

	var details map[string]any
	if string(env["err_name"]) != `"ErrDialogMaybeOpen"` || json.Unmarshal(env["err_details"], &details) != nil {
		t.Fatalf("send-keys envelope = %v; want ErrDialogMaybeOpen with an err_details object", env)
	}
	token, _ := details["request_token"].(string)
	if token == "" || details["delivery"] != "delivered" || details["tool_use_id"] != "toolu_cli_hold" || details["unproven_since"] == nil ||
		details["proven_gone_at"] != nil || details["state"] != store.StateCheckPermission {
		t.Errorf("err_details = %v; want the request delivered, unproven since its ack, state check_permission", details)
	}
	if reqs, ok := details["unproven_requests"].([]any); !ok || len(reqs) != 0 {
		t.Errorf("err_details.unproven_requests = %#v; want []", details["unproven_requests"])
	}
	assertUnproven(t, h.home, id, token)

	h.hook(t, id, hook.RelayModeOn, `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{},"tool_use_id":"toolu_other"}`)
	assertUnproven(t, h.home, id, token)

	h.hook(t, id, hook.RelayModeOn, `{"hook_event_name":"Stop"}`)
	perm := cliJSON(t, h.home, "get-permission", "--request-token", token)
	if perm["proven_gone_how"] != store.ProvenGoneTurnEnd || perm["proven_gone_at"] == nil || perm["unproven_since"] != nil {
		t.Errorf("get-permission after the Stop = proven_gone_how %v, proven_gone_at %v, unproven_since %v; want turn_end, set, null",
			perm["proven_gone_how"], perm["proven_gone_at"], perm["unproven_since"])
	}
	assertUnproven(t, h.home, id)
}

// assertUnproven fails unless get's unproven_requests for id are exactly tokens.
func assertUnproven(t *testing.T, home, id string, tokens ...string) {
	t.Helper()
	reqs, ok := cliJSON(t, home, "get", "--claude-instance-id", id)["unproven_requests"].([]any)
	if !ok || len(reqs) != len(tokens) {
		t.Fatalf("get unproven_requests = %v; want %v", reqs, tokens)
	}
	for i, r := range reqs {
		if m, _ := r.(map[string]any); m["request_token"] != tokens[i] {
			t.Errorf("get unproven_requests[%d] = %v; want %s", i, r, tokens[i])
		}
	}
}
