package main_test

import (
	"strconv"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The hook verb's --agent flag through the built CLI (SR-6.1): the argument
// parser and an invalid value are tested only here. What each agent's table
// does with each event is internal/hook's. Rows are working rows whose pane
// process is this test process, so every hook passes the gate; helpers are
// hook_gate_cli_test.go's.

// seedAgentRow seeds a working row of agent whose pane process is this test
// process.
func (h gateHome) seedAgentRow(t *testing.T, id, agent, relayMode string) {
	t.Helper()
	if _, err := apitest.SeedSpawn(stateDB(h.home), id, store.StateWorking, "", relayMode, "", true,
		withTestProcessPane(t), apitest.WithAgent(agent)); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
}

// codexInterrupt and codexSessionEnd are Codex hooks of the verified process;
// claudeInterrupt is the same event Claude's table does not know.
const (
	codexInterrupt  = `{"hook_event_name":"Interrupt","session_id":"codex-session"}`
	codexSessionEnd = `{"hook_event_name":"SessionEnd","session_id":"codex-session","reason":"other"}`
	claudeInterrupt = `{"hook_event_name":"Interrupt"}`
)

// TestHookCLIAgentSelectsItsTable: --agent codex classifies by Codex's table
// (an Interrupt is a turn end, a SessionEnd an end report that keeps the
// state); no flag and --agent claude classify by Claude's (an Interrupt is
// unknown: a soft refresh).
func TestHookCLIAgentSelectsItsTable(t *testing.T) {
	cases := []struct {
		name      string
		agent     string // the row's agent
		args      []string
		payload   string
		wantState string
		wantEnd   bool // the end report is recorded
	}{
		{name: "codex_interrupt_is_a_turn_end", agent: "codex", args: []string{"--agent", "codex"}, payload: codexInterrupt, wantState: store.StateWaiting},
		{name: "codex_session_end_is_an_end_report", agent: "codex", args: []string{"--agent", "codex"}, payload: codexSessionEnd, wantState: store.StateWorking, wantEnd: true},
		{name: "no_flag_is_claude", agent: "claude", payload: claudeInterrupt, wantState: store.StateWorking},
		{name: "claude_flag", agent: "claude", args: []string{"--agent", "claude"}, payload: claudeInterrupt, wantState: store.StateWorking},
	}
	fakeDir := buildFakeTmux(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := gateHome{home: t.TempDir(), fakeDir: fakeDir}
			const id = "id-agent-table"
			h.seedAgentRow(t, id, tc.agent, "off")

			if out := h.hookArgs(t, id, "", tc.payload, tc.args...); out != "" {
				t.Errorf("stdout = %q; want empty", out)
			}
			after := h.row(t, id)
			if after.State != tc.wantState || (after.EndReportedAtMillis > 0) != tc.wantEnd {
				t.Errorf("state = %q, end_reported_at = %d; want %q, end report recorded = %v",
					after.State, after.EndReportedAtMillis, tc.wantState, tc.wantEnd)
			}
		})
	}
}

// TestHookCLIAgentParsedAmongOtherArguments: --agent codex keeps its value
// whatever else the line holds: --timeout before or after it, the --agent=codex
// form, an unknown flag or a stray positional argument on either side.
func TestHookCLIAgentParsedAmongOtherArguments(t *testing.T) {
	cases := map[string][]string{
		"timeout_first":            {"--timeout", "4", "--agent", "codex"},
		"timeout_last":             {"--agent", "codex", "--timeout", "4"},
		"equals_form":              {"--agent=codex"},
		"unknown_flag_before":      {"--bogus", "--agent", "codex"},
		"unknown_flag_with_equals": {"--bogus=1", "--agent", "codex"},
		"unknown_flag_after":       {"--agent", "codex", "--bogus"},
		"positional_before":        {"stray", "--agent", "codex"},
		"positional_between":       {"--timeout", "4", "stray", "--agent", "codex"},
		"positional_after":         {"--agent", "codex", "stray"},
		// Malformed flags (the flag package's "bad flag syntax") fail without
		// consuming the argument: the parser must skip them and still advance.
		"malformed_triple_dash":        {"---x", "--agent", "codex"},
		"malformed_dash_equals":        {"-=x", "--agent", "codex"},
		"malformed_double_dash_equals": {"--=codex", "--agent", "codex"},
		"malformed_bare_triple_dash":   {"---", "--agent", "codex"},
		"malformed_after_agent":        {"--agent", "codex", "---x"},
		"single_dash_form":             {"-agent", "codex"},
		"timeout_unparseable_value":    {"--timeout", "soon", "--agent", "codex"},
	}
	fakeDir := buildFakeTmux(t)
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			h := gateHome{home: t.TempDir(), fakeDir: fakeDir}
			const id = "id-agent-parse"
			h.seedAgentRow(t, id, "codex", "off")

			h.hookArgs(t, id, "", codexInterrupt, args...)
			if got := h.row(t, id).State; got != store.StateWaiting {
				t.Errorf("state = %q; want %q (Codex's Interrupt: --agent codex kept its value)", got, store.StateWaiting)
			}
		})
	}
}

// TestHookCLITimeoutWithoutValueTakesNextArgument: --timeout takes the next
// argument as its value whatever it is (the flag package's rule), and an
// unparseable one leaves the timeout 0 with no parse error. So in
// "--timeout --agent codex" the "--agent" is --timeout's value, and the hook
// exits with the agent absent (claude's table): it is never read as a trailing
// --agent with no value, which would be an invalid agent.
func TestHookCLITimeoutWithoutValueTakesNextArgument(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	h := gateHome{home: t.TempDir(), fakeDir: fakeDir}
	const id = "id-agent-timeout-eats"
	h.seedAgentRow(t, id, "codex", "off")

	// Claude's table does not know Interrupt: a soft refresh, the state kept.
	h.hookArgs(t, id, "", codexInterrupt, "--timeout", "--agent", "codex")
	if got := h.row(t, id).State; got != store.StateWorking {
		t.Errorf("state = %q; want %q (--agent was --timeout's value: the hook ran as claude)", got, store.StateWorking)
	}
}

// TestHookCLIAgentWithTimeoutHonoursEitherOrder: a relayed Codex
// PermissionRequest ends with its timeout deny after the --timeout N the
// line carries, in either order and past an unknown argument. The home's relay
// window is a day, so a hook that lost N would outlive gateHookDeadline.
func TestHookCLIAgentWithTimeoutHonoursEitherOrder(t *testing.T) {
	const payload = `{"hook_event_name":"PermissionRequest","session_id":"codex-session","tool_name":"Bash","tool_input":{"command":"ls"}}`
	timeout := strconv.Itoa(gateRelayTimeoutSeconds)
	cases := map[string][]string{
		"agent_then_timeout":         {"--agent", "codex", "--timeout", timeout},
		"timeout_then_agent":         {"--timeout", timeout, "--agent", "codex"},
		"timeout_after_unknown_flag": {"--agent", "codex", "--bogus", "--timeout", timeout},
		"timeout_after_positional":   {"--agent", "codex", "stray", "--timeout", timeout},
	}
	fakeDir := buildFakeTmux(t)
	deny := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			h := gateHome{home: t.TempDir(), fakeDir: fakeDir}
			const id = "id-agent-timeout"
			h.seedAgentRow(t, id, "codex", hook.RelayModeOn)

			if out := h.hookArgs(t, id, hook.RelayModeOn, payload, args...); out != deny {
				t.Errorf("stdout = %q; want %q", out, deny)
			}
			if got := h.row(t, id).State; got != store.StateCheckPermission {
				t.Errorf("state = %q; want %q", got, store.StateCheckPermission)
			}
			assertOneTimeoutDeny(t, h.home, id)
		})
	}
}

// TestHookCLIInvalidAgentFailsOpen: an --agent value other than claude,
// codex or cursor (a wrong name, a wrong case, an empty value, no value) exits 0
// with nothing printed and writes nothing, and a relayed PermissionRequest
// gets the deny envelope, with no request recorded.
func TestHookCLIInvalidAgentFailsOpen(t *testing.T) {
	args := map[string][]string{
		"unknown_name":  {"--agent", "gemini"},
		"wrong_case":    {"--agent", "CODEX"},
		"empty_value":   {"--agent", ""},
		"equals_empty":  {"--agent="},
		"missing_value": {"--agent"},
		// A trailing --agent with no value left, past other arguments and in
		// the single-dash form, is the same invalid agent.
		"missing_value_single_dash":   {"-agent"},
		"missing_value_after_timeout": {"--timeout", "4", "--agent"},
		"missing_value_after_unknown": {"--bogus", "--agent"},
		"missing_value_after_stray":   {"stray", "--agent"},
		"missing_value_after_badflag": {"---x", "--agent"},
	}
	events := []struct {
		name      string
		relayMode string
		payload   string
		want      string
	}{
		{name: "stop", payload: `{"hook_event_name":"Stop"}`},
		{
			name:      "relayed_permission_request",
			relayMode: hook.RelayModeOn,
			payload:   `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`,
			want:      hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n",
		},
	}
	fakeDir := buildFakeTmux(t)
	for name, a := range args {
		for _, ev := range events {
			t.Run(name+"/"+ev.name, func(t *testing.T) {
				h := gateHome{home: t.TempDir(), fakeDir: fakeDir}
				const id = "id-agent-invalid"
				h.seedAgentRow(t, id, "claude", ev.relayMode)
				before := h.row(t, id)

				if out := h.hookArgs(t, id, ev.relayMode, ev.payload, a...); out != ev.want {
					t.Errorf("stdout = %q; want %q", out, ev.want)
				}
				after := h.row(t, id)
				if after.State != before.State || after.RowVersion != before.RowVersion || !after.LastSeenAt.Equal(before.LastSeenAt) {
					t.Errorf("row changed by a hook with an invalid agent:\nbefore=%+v\nafter =%+v", before, after)
				}
				st := openDBForRead(t, h.home)
				if reqs, err := st.PermissionRequestsForSpawn(id); err != nil || len(reqs) != 0 {
					t.Errorf("permission requests = %+v, %v; want none recorded", reqs, err)
				}
			})
		}
	}
}
