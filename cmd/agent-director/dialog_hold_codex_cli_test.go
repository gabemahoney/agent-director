package main_test

// dialog_hold_codex_cli_test.go — demo step 3 of b.ssz Epic 4 through the
// built CLI and its real hooks: b.146 step 2c's hold on an agent busy send
// doesn't cover (a covered Claude send is Held instead, never refused). Which
// hook proves what is internal/hook's and pkg/api's; this is the one flow
// whose facts the envelope carries.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.cqr: one errored Codex row through the built CLI's read and error envelopes.
func TestCLICodexTurnErroredReadAndSendEnvelopes(t *testing.T) {
	h := newGateHome(t)
	const id = "cli-turn-errored"
	turn := codexfix.ErroredTurn
	path := codexfix.TempRolloutPath(t, turn.SessionID, true, turn.Ended())
	if _, err := apitest.SeedSpawn(stateDB(h.home), id, store.StateWaiting, t.TempDir(), "on", turn.SessionID, true,
		withTestProcessPane(t), apitest.WithAgent(store.AgentCodex), apitest.WithJsonlPath(path)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"get", "--instance-id", id}, {"get-many", id}, {"list"}} {
		stdout, stderr, code := runSpawnCLI(t, h.home, h.fakeDir, args...)
		var row map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stdout), &row); err != nil || code != 0 || stderr != "" {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q err=%v", args, code, stdout, stderr, err)
		}
		if args[0] != "get" {
			var rows []map[string]json.RawMessage
			if err := json.Unmarshal(row["spawns"], &rows); err != nil || len(rows) != 1 {
				t.Fatalf("%s spawns=%s err=%v; want one row", args[0], row["spawns"], err)
			}
			row = rows[0]
		}
		if string(row["instance_id"]) != `"`+id+`"` || string(row["last_turn_errored"]) != "true" || string(row["state"]) != `"waiting"` {
			t.Errorf("%s: instance_id=%s last_turn_errored=%s state=%s; want %s, true, waiting",
				args[0], row["instance_id"], row["last_turn_errored"], row["state"], id)
		}
	}
	mark := len(fakeTmuxInvocations(t, h.home))
	stdout, stderr, code := runSpawnCLI(t, h.home, h.fakeDir, "send-keys", "--instance-id", id, "--text", "2")
	assertOnlyEnvelope(t, stdout, stderr, code, "ErrCodexTurnErrored")
	if calls := fakeTmuxInvocations(t, h.home)[mark:]; len(calls) != 0 {
		t.Errorf("refused send called tmux: %q; want no input or other tmux calls", calls)
	}
}

// TestCLICodexDialogHeldUntilAnInterruptProvesItGone: `hook --agent codex`
// relays a Codex PermissionRequest (tool_name and tool_input recorded, no
// tool_use_id) to its timeout deny, acked; plain send-keys is then
// ErrDialogMaybeOpen with tool_use_id null in err_details and a description
// that names Codex's proofs (Stop or Interrupt), not a PostToolUse; get lists
// the request unproven. A Codex PostToolUse with a tool_use_id proves nothing;
// the main agent's Interrupt proves it gone (turn_end): get-permission gives the
// proof and get lists none.
func TestCLICodexDialogHeldUntilAnInterruptProvesItGone(t *testing.T) {
	h := newGateHome(t)
	const id = "id-cli-hold-codex"
	h.seedAgentRow(t, id, "codex", hook.RelayModeOn)
	writeRelayTimeout(t, h.home)
	deny := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"
	if out := h.hookArgs(t, id, hook.RelayModeOn,
		`{"hook_event_name":"PermissionRequest","session_id":"codex-session","tool_name":"Bash","tool_input":{"command":"touch /work/outside/shell.txt"}}`,
		"--agent", "codex"); out != deny {
		t.Fatalf("relay hook stdout = %q; want its timeout deny %q", out, deny)
	}
	assertOneTimeoutDeny(t, h.home, id)

	env := cliErrorEnvelope(t, h.home, "send-keys", "--instance-id", id, "--text", "hi")

	var details map[string]any
	if string(env["err_name"]) != `"ErrDialogMaybeOpen"` || json.Unmarshal(env["err_details"], &details) != nil {
		t.Fatalf("send-keys envelope = %v; want ErrDialogMaybeOpen with an err_details object", env)
	}
	var description string
	if err := json.Unmarshal(env["err_description"], &description); err != nil {
		t.Fatalf("err_description = %s (%v); want a string", env["err_description"], err)
	}
	apitest.AssertDescription(t, description, apitest.DescDialogMaybeOpenCodex())
	token, _ := details["request_token"].(string)
	if v, present := details["tool_use_id"]; !present || v != nil {
		t.Errorf("err_details tool_use_id = %v (present %v); want null on a Codex request", v, present)
	}
	if token == "" || details["delivery"] != "delivered" || details["unproven_since"] == nil ||
		details["proven_gone_at"] != nil || details["state"] != store.StateCheckPermission {
		t.Errorf("err_details = %v; want the request delivered, unproven since its ack, state check_permission", details)
	}
	if perm := cliJSON(t, h.home, "get-permission", "--request-token", token); perm["tool_name"] != "Bash" ||
		!strings.Contains(perm["tool_input"].(string), "touch /work/outside/shell.txt") {
		t.Errorf("get-permission tool_name %v, tool_input %v; want Codex's tool name and input", perm["tool_name"], perm["tool_input"])
	}
	assertUnproven(t, h.home, id, token)

	h.hookArgs(t, id, hook.RelayModeOn,
		`{"hook_event_name":"PostToolUse","session_id":"codex-session","tool_name":"Bash","tool_input":{},"tool_response":"","tool_use_id":"call_fixture_1"}`,
		"--agent", "codex")
	assertUnproven(t, h.home, id, token)

	h.hookArgs(t, id, hook.RelayModeOn, codexInterrupt, "--agent", "codex")
	perm := cliJSON(t, h.home, "get-permission", "--request-token", token)
	if perm["proven_gone_how"] != store.ProvenGoneTurnEnd || perm["proven_gone_at"] == nil || perm["unproven_since"] != nil {
		t.Errorf("get-permission after the Interrupt = proven_gone_how %v, proven_gone_at %v, unproven_since %v; want turn_end, set, null",
			perm["proven_gone_how"], perm["proven_gone_at"], perm["unproven_since"])
	}
	assertUnproven(t, h.home, id)
}

// assertUnproven fails unless get's unproven_requests for id are exactly tokens.
func assertUnproven(t *testing.T, home, id string, tokens ...string) {
	t.Helper()
	reqs, ok := cliJSON(t, home, "get", "--instance-id", id)["unproven_requests"].([]any)
	if !ok || len(reqs) != len(tokens) {
		t.Fatalf("get unproven_requests = %v; want %v", reqs, tokens)
	}
	for i, r := range reqs {
		if m, _ := r.(map[string]any); m["request_token"] != tokens[i] {
			t.Errorf("get unproven_requests[%d] = %v; want %s", i, r, tokens[i])
		}
	}
}
