package hook_test

// hook_codex_relay_test.go — the permission relay on Codex rows through
// hook.Handle against a real store (E4-T4): a relayed Codex PermissionRequest
// recorded and answered as Claude's is, the timeout deny's relay window, the
// fail-closed deny, which Codex hooks prove a request's dialog gone and which
// prove or close nothing, and the Escape pane answer that ends in an Interrupt.
// The Claude rows of these rules are relay_test.go's, timeout_writes_test.go's,
// request_proof_test.go's and tool_ran_test.go's.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// cxRelayParent is the hook parent of id's row for agent: Codex's own process
// for "codex", Claude's for anything else.
func cxRelayParent(t *testing.T, st *store.Store, id, agent string) hookParent {
	t.Helper()
	if agent == store.AgentCodex {
		return cxOwn(t, st, id)
	}
	return agentParent(t, st, id)
}

// cxRelayRow seeds id as a row of agent in state with relay on.
func cxRelayRow(t *testing.T, id, agent, state string) (*store.Store, string) {
	t.Helper()
	return seedAgentRow(t, id, state, apitest.WithAgent(agent), apitest.WithRelayMode("on"))
}

// cxToolInput is the raw tool_input of Codex fixture file.
func cxToolInput(t *testing.T, file string) string {
	t.Helper()
	var p struct {
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(readPayloadFixture(t, "codex/"+file), &p); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return string(p.ToolInput)
}

// cxSeedRequest records one relayed request of row id through the relay's own
// first write: no tool_use_id, and agent_id when agentID is set.
func cxSeedRequest(t *testing.T, st *store.Store, id, agentID string) store.PermissionRow {
	t.Helper()
	storefix.SeedRelayRequest(t, st, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
		ToolInput: `{"command":"x"}`, AgentID: agentID, Hook: relayHookSelf, SettledAt: time.Now().Add(time.Hour)})
	return onlyRequest(t, st, id)
}

// TestCodexRelayRecordsAndAnswers: a relayed Codex PermissionRequest records
// tool_name and tool_input verbatim, the agent_id it carries and no tool_use_id
// (NULL), with the row in check_permission while the hook polls, and prints the
// allow or deny envelope Claude's relay prints, byte for byte.
func TestCodexRelayRecordsAndAnswers(t *testing.T) {
	cases := []struct {
		agent, fixture, tool, verdict, agentID string
	}{
		{"codex", "permission-request-bash.json", "Bash", "allow", ""},
		{"codex", "permission-request-bash.json", "Bash", "deny", ""},
		{"codex", "permission-request-bash-outside-sandbox.json", "Bash", "allow", ""},
		{"codex", "permission-request-apply-patch-outside-roots.json", "apply_patch", "allow", ""},
		{"codex", "permission-request-write-stdin.json", "write_stdin", "allow", ""},
		{"codex", "permission-request-mcp.json", "mcp__ad__list", "allow", ""},
		{"codex", "permission-request-network-denied-escalation.json", "Bash", "allow", ""},
		{"codex", "permission-request-network-access.json", "Bash", "deny", ""},
		{"codex", "permission-request-subagent.json", "Bash", "allow", cxSubagentID},
		{"codex", "permission-request-subagent.json", "Bash", "deny", cxSubagentID},
	}
	for i, tc := range cases {
		t.Run(tc.verdict+" "+tc.fixture, func(t *testing.T) {
			id := "cx-relay-" + string(rune('a'+i))
			st, dbPath := cxRelayRow(t, id, tc.agent, store.StateWorking)
			hc := relayHC(id, cxRelayParent(t, st, id, tc.agent), 10*time.Second)
			hc.Agent = tc.agent
			var mid store.Spawn
			var midReq store.PermissionRow
			atSleep(t, hc, 1, func() {
				mid, midReq = mustGetSpawn(t, st, id), onlyRequest(t, st, id)
				decideOnly(t, st, id, tc.verdict)
			})

			out, _ := fireRelay(t, st, hc, string(readPayloadFixture(t, "codex/"+tc.fixture)))

			reason := ""
			if tc.verdict == "deny" {
				reason = store.DecisionReasonOperator
			}
			if want := hook.EncodeDecision(hook.EventNamePermissionRequest, tc.verdict, reason) + "\n"; out != want {
				t.Errorf("stdout = %q; want %q", out, want)
			}
			if mid.State != store.StateCheckPermission {
				t.Errorf("state while polling = %q; want check_permission", mid.State)
			}
			if midReq.ToolName != tc.tool || midReq.ToolInput != cxToolInput(t, tc.fixture) || midReq.AgentID != tc.agentID {
				t.Errorf("request = tool %q, input %s, agent_id %q; want %q, %s, %q",
					midReq.ToolName, midReq.ToolInput, midReq.AgentID, tc.tool, cxToolInput(t, tc.fixture), tc.agentID)
			}
			raw, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatalf("open raw: %v", err)
			}
			defer raw.Close()
			var useIDNull bool
			if err := raw.QueryRow(`SELECT tool_use_id IS NULL FROM permission_requests WHERE instance_id = ?`, id).Scan(&useIDNull); err != nil || !useIDNull {
				t.Errorf("tool_use_id IS NULL = %v, %v; want true (Codex sends none)", useIDNull, err)
			}
			if got := onlyRequest(t, st, id); got.DeliveredAt.IsZero() || got.Decision != tc.verdict {
				t.Errorf("request after the hook = decision %q, delivered_at %v; want %q, acked", got.Decision, got.DeliveredAt, tc.verdict)
			}
		})
	}
}

// TestCodexRelayTimeoutDeny: a Codex relay hook with no verdict denies by
// timeout (decision deny, reason timeout, acked, row left in check_permission)
// when its relay window less the reserve and the deny lead is spent, and
// records the settle instant at its kill plus the 2 s reserve; with no
// --timeout the window is [relay] timeout_seconds.
func TestCodexRelayTimeoutDeny(t *testing.T) {
	cases := []struct {
		name    string
		agent   string
		timeout time.Duration // --timeout; 0 = none given
		window  int           // relay.timeout_seconds
		kill    time.Duration // the hook's kill after its start
	}{
		{"--timeout given", "codex", 10 * time.Second, 0, 10 * time.Second},
		{"no --timeout: the relay window", "codex", 0, 20, 20 * time.Second},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "cx-relay-timeout-" + string(rune('a'+i))
			st, _ := cxRelayRow(t, id, tc.agent, store.StateWorking)
			hc := relayHC(id, cxRelayParent(t, st, id, tc.agent), tc.timeout)
			hc.Agent = tc.agent
			hc.Cfg = config.Relay{TimeoutSeconds: tc.window}
			start := hc.Now()

			out, _ := fireRelay(t, st, hc, string(readPayloadFixture(t, "codex/permission-request-bash.json")))

			if want := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"; out != want {
				t.Errorf("stdout = %q; want %q", out, want)
			}
			req := onlyRequest(t, st, id)
			if req.Decision != "deny" || req.DecisionReason != store.DecisionReasonTimeout || req.DeliveredAt.IsZero() {
				t.Errorf("request = decision %q, reason %q, delivered_at %v; want deny, timeout, acked", req.Decision, req.DecisionReason, req.DeliveredAt)
			}
			if want := start.Add(tc.kill + 2*time.Second).UnixMilli(); req.SettledAt.UnixMilli() != want {
				t.Errorf("settled_at = %v; want the kill plus the 2 s reserve (%d ms)", req.SettledAt, want)
			}
			if spent := hc.Now().Sub(start); spent < tc.kill-3*time.Second || spent > tc.kill-2*time.Second {
				t.Errorf("the hook polled for %v; want between %v and %v, its reserve and deny lead before its %v kill",
					spent, tc.kill-3*time.Second, tc.kill-2*time.Second, tc.kill)
			}
			if got := mustGetSpawn(t, st, id).State; got != store.StateCheckPermission {
				t.Errorf("state = %q; want check_permission until the agent's next hook", got)
			}
		})
	}
}

// TestCodexRelayFailsClosed: a relayed Codex PermissionRequest that fails
// before it is recorded prints the deny envelope: its first write, one gated
// transaction with the move to check_permission, dying at the insert leaves
// neither the request nor the move; an invalid instance id writes nothing.
func TestCodexRelayFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		agent   string
		trigger bool // the insert dies
		badID   bool // the env carries an invalid instance id
	}{
		{name: "the insert dies", agent: "codex", trigger: true},
		{name: "invalid instance id", agent: "codex", badID: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "cx-relay-closed-" + string(rune('a'+i))
			st, dbPath := cxRelayRow(t, id, tc.agent, store.StateWorking)
			hc := relayHC(id, cxRelayParent(t, st, id, tc.agent), 10*time.Second)
			hc.Agent = tc.agent
			if tc.badID {
				hc.Env = envWith("id/with/slash")
			}
			before := mustGetSpawn(t, st, id)
			if tc.trigger {
				raw, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatalf("open raw: %v", err)
				}
				defer raw.Close()
				if _, err := raw.Exec(`CREATE TRIGGER ad_test_cx_dies_at_insert BEFORE INSERT ON permission_requests
					BEGIN SELECT RAISE(ABORT, 'test: the hook dies at its insert'); END`); err != nil {
					t.Fatalf("install trigger: %v", err)
				}
			}

			out, _ := fireRelay(t, st, hc, string(readPayloadFixture(t, "codex/permission-request-bash.json")))

			assertDenyEnvelope(t, bytes.NewBufferString(out))
			assertRowUnchanged(t, st, id, before)
			assertNoRequests(t, st, id)
		})
	}
}

// TestCodexHookProvesRequestsGone: later main-agent turn ends prove main-agent
// requests gone; subagent lifecycle hooks prove neither request origin gone.
func TestCodexHookProvesRequestsGone(t *testing.T) {
	cases := []struct {
		name       string
		agent      string // the row's agent and the hook's --agent
		fixture    string
		agentID    string // the request's agent_id
		first      bool   // the hook fires before the request is recorded
		sameSecond bool   // the hook started in the second the request was recorded
		relayOff   bool
		foreign    bool
		want       string // proven_gone_how; "" unproven
		wantState  string
	}{
		{name: "Interrupt after a main-agent request", agent: "codex", fixture: "interrupt.json",
			want: store.ProvenGoneTurnEnd, wantState: store.StateWaiting},
		{name: "Stop after a main-agent request", agent: "codex", fixture: "stop.json",
			want: store.ProvenGoneTurnEnd, wantState: store.StateWaiting},
		{name: "Interrupt before the request", agent: "codex", fixture: "interrupt.json", first: true, wantState: store.StateCheckPermission},
		{name: "Stop before the request", agent: "codex", fixture: "stop.json", first: true, wantState: store.StateCheckPermission},
		{name: "Interrupt started in the request's second", agent: "codex", fixture: "interrupt.json", sameSecond: true,
			wantState: store.StateWaiting},
		{name: "Stop started in the request's second", agent: "codex", fixture: "stop.json", sameSecond: true,
			wantState: store.StateWaiting},
		{name: "marked Interrupt", agent: "codex", fixture: "interrupt-subagent.json", wantState: store.StateCheckPermission},
		{name: "Interrupt after a subagent's request", agent: "codex", fixture: "interrupt.json", agentID: cxSubagentID,
			wantState: store.StateWaiting},
		{name: "Stop after a subagent's request", agent: "codex", fixture: "stop.json", agentID: cxSubagentID,
			wantState: store.StateWaiting},
		{name: "SubagentStart after a main-agent request", agent: "codex", fixture: "subagent-start.json", wantState: store.StateCheckPermission},
		{name: "SubagentStop after a main-agent request", agent: "codex", fixture: "subagent-stop.json", wantState: store.StateCheckPermission},
		{name: "SubagentStart after a subagent request", agent: "codex", fixture: "subagent-start.json", agentID: cxSubagentID, wantState: store.StateCheckPermission},
		{name: "SubagentStop after a subagent request", agent: "codex", fixture: "subagent-stop.json", agentID: cxSubagentID, wantState: store.StateCheckPermission},
		{name: "review thread PreToolUse", agent: "codex", fixture: "pre-tool-use-review-thread.json", wantState: store.StateCheckPermission},
		{name: "review thread UserPromptSubmit", agent: "codex", fixture: "user-prompt-submit-review-thread.json",
			wantState: store.StateCheckPermission},
		{name: "plain PreToolUse", agent: "codex", fixture: "pre-tool-use-bash.json", wantState: store.StateCheckPermission},
		{name: "plain UserPromptSubmit", agent: "codex", fixture: "user-prompt-submit.json", wantState: store.StateCheckPermission},
		{name: "Interrupt, relay off", agent: "codex", fixture: "interrupt.json", relayOff: true, wantState: store.StateWaiting},
		{name: "Stop, relay off", agent: "codex", fixture: "stop.json", relayOff: true, wantState: store.StateWaiting},
		{name: "Interrupt from another process", agent: "codex", fixture: "interrupt.json", foreign: true, wantState: store.StateCheckPermission},
		{name: "Stop from another process", agent: "codex", fixture: "stop.json", foreign: true, wantState: store.StateCheckPermission},
		{name: "Interrupt under the Claude flag", agent: "claude", fixture: "interrupt.json", wantState: store.StateCheckPermission},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "cx-proof-" + string(rune('a'+i))
			st, _ := cxRelayRow(t, id, tc.agent, store.StateWorking)
			parent := cxRelayParent(t, st, id, tc.agent)
			if tc.foreign {
				parent = codexParent(foreignParent(t, st, id))
			}
			env := envWith(id)
			if tc.relayOff {
				env = envHook(id, "")
			}
			hc := hookConfig(env, parent)
			hc.RelayHookGone = func(store.PermissionRow) bool { return false } // its relay hook may still answer
			var req store.PermissionRow
			if !tc.first {
				req = cxSeedRequest(t, st, id, tc.agentID)
				hc.Start = req.CreatedAt.Add(time.Second)
				if tc.sameSecond {
					hc.Start = req.CreatedAt.Truncate(time.Second).Add(999 * time.Millisecond)
				}
			}

			ssgHandleWith(t, st, hc, string(readPayloadFixture(t, "codex/"+tc.fixture)), newSilentLogger())

			if tc.first {
				req = cxSeedRequest(t, st, id, tc.agentID)
			}
			pr := onlyRequest(t, st, id)
			if pr.ProvenGoneHow != tc.want || pr.ProvenGone() != (tc.want != "") {
				t.Fatalf("proven_gone_how = %q (proven %v); want %q", pr.ProvenGoneHow, pr.ProvenGone(), tc.want)
			}
			if tc.want != "" && pr.ProvenGoneAt.UnixMilli() != hc.Now().UnixMilli() {
				t.Errorf("proven_gone_at = %v; want the hook's clock %v", pr.ProvenGoneAt, hc.Now())
			}
			if pr.PaneAnswer != store.PaneAnswerNone || pr.Decision != "" {
				t.Errorf("request = pane_answer %q, decision %q; want it as recorded (the proof closes nothing)", pr.PaneAnswer, pr.Decision)
			}
			if got := mustGetSpawn(t, st, id).State; got != tc.wantState {
				t.Errorf("state = %q; want %q", got, tc.wantState)
			}
		})
	}
}

// TestCodexPostToolUseClosesNothing: a Codex PostToolUse (or PostToolUseFailure)
// carrying a tool_use_id proves no request gone and closes none, whether the
// request has a null tool_use_id or the same one, is the main agent's or a
// subagent's, or has fallen back (its relay hook gone, answered at the pane
// outside agent-director): it stays open and holds the row in check_permission.
func TestCodexPostToolUseClosesNothing(t *testing.T) {
	const useID = "call_fixture_1" // post-tool-use-bash.json's tool_use_id
	cases := []struct {
		name       string
		agent      string
		event      string // hook_event_name; "" = the fixture's PostToolUse
		requestUse string // the request's tool_use_id; "" = NULL
		agentID    string
		fallenBack bool // RelayHookGone answers true
	}{
		{name: "PostToolUse, a null tool_use_id", agent: "codex"},
		{name: "PostToolUse, the same tool_use_id", agent: "codex", requestUse: useID},
		{name: "PostToolUse, a subagent's request", agent: "codex", requestUse: useID, agentID: cxSubagentID},
		{name: "PostToolUse, a fallen-back request", agent: "codex", requestUse: useID, fallenBack: true},
		{name: "PostToolUse, a fallen-back request with a null tool_use_id", agent: "codex", fallenBack: true},
		{name: "PostToolUseFailure, the same tool_use_id", agent: "codex", event: "PostToolUseFailure", requestUse: useID},
		{name: "PostToolUseFailure, a fallen-back request", agent: "codex", event: "PostToolUseFailure", requestUse: useID, fallenBack: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "cx-post-" + string(rune('a'+i))
			st, _ := cxRelayRow(t, id, tc.agent, store.StateWorking)
			storefix.SeedRelayRequest(t, st, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
				ToolInput: `{}`, ToolUseID: tc.requestUse, AgentID: tc.agentID, Hook: relayHookSelf, SettledAt: time.Now().Add(-time.Hour)})
			fields := map[string]any{}
			if tc.event != "" {
				fields["hook_event_name"] = tc.event
			}
			payload := payloadWith(t, "codex/post-tool-use-bash.json", fields)
			hc := hookConfig(envWith(id), cxRelayParent(t, st, id, tc.agent))
			hc.RelayHookGone = func(store.PermissionRow) bool { return tc.fallenBack }
			hc.Start = onlyRequest(t, st, id).CreatedAt.Add(time.Second)
			before := len(readTrailLines(t, trailFile()))
			var calls []string

			ssgHandleWith(t, orderStore{Store: st, calls: &calls}, hc, string(payload), newSilentLogger())

			pr := onlyRequest(t, st, id)
			if pr.ProvenGone() || pr.PaneAnswer != store.PaneAnswerNone || pr.Decision != "" || !pr.HookGoneAt.IsZero() || !pr.AwaitsAnswer() {
				t.Errorf("request = proven %v, pane_answer %q, decision %q, hook_gone_at %v; want it open as recorded",
					pr.ProvenGone(), pr.PaneAnswer, pr.Decision, pr.HookGoneAt)
			}
			if got := mustGetSpawn(t, st, id).State; got != store.StateCheckPermission {
				t.Errorf("state = %q; want check_permission (the open request holds the row)", got)
			}
			for _, c := range calls {
				if c == callProve || c == callToolRan {
					t.Errorf("hook called %q; want neither proof nor close for a Codex PostToolUse (calls %v)", c, calls)
				}
			}
			if committed := linesAfter(t, before, "ad.row_mutation.committed", id); len(committed) != 0 {
				t.Errorf("ad.row_mutation.committed = %v; want none", committed)
			}
		})
	}
}

// TestCodexEscapeThenInterrupt: a request answered by an Escape at the pane
// (deny, key sent) holds the row in check_permission; the Interrupt Codex then
// sends proves it gone (turn_end) and the row reads waiting, and the next
// UserPromptSubmit moves it to working.
func TestCodexEscapeThenInterrupt(t *testing.T) {
	const id = "cx-escape"
	st, _ := cxRelayRow(t, id, store.AgentCodex, store.StateWorking)
	req := cxSeedRequest(t, st, id, "")
	intent, ok, err := st.RecordPaneIntent(id, req.RequestToken, "deny", relayHookSelf, nil, store.DefaultLockWait, nil)
	if err != nil || !ok {
		t.Fatalf("RecordPaneIntent = %v, %v", ok, err)
	}
	if ok, err := st.RecordPaneSent(id, req.RequestToken, intent, store.DefaultLockWait); err != nil || !ok {
		t.Fatalf("RecordPaneSent = %v, %v", ok, err)
	}
	fire := func(fixture string, start time.Time) {
		t.Helper()
		hc := hookConfig(envWith(id), cxOwn(t, st, id))
		hc.Start = start
		var out bytes.Buffer
		if err := hook.Handle(context.Background(), strings.NewReader(string(readPayloadFixture(t, "codex/"+fixture))), &out, st, hc, newSilentLogger()); err != nil {
			t.Fatalf("Handle(%s): %v", fixture, err)
		}
	}

	fire("interrupt.json", req.CreatedAt.Add(time.Second))

	if got := mustGetSpawn(t, st, id).State; got != store.StateWaiting {
		t.Errorf("state after the Interrupt = %q; want waiting", got)
	}
	if pr := onlyRequest(t, st, id); pr.ProvenGoneHow != store.ProvenGoneTurnEnd || pr.PaneAnswer != store.PaneAnswerSent || pr.Decision != "deny" {
		t.Errorf("request = proven %q, pane_answer %q, decision %q; want turn_end, sent, deny", pr.ProvenGoneHow, pr.PaneAnswer, pr.Decision)
	}

	fire("user-prompt-submit.json", time.Time{})

	if got := mustGetSpawn(t, st, id).State; got != store.StateWorking {
		t.Errorf("state after the next UserPromptSubmit = %q; want working", got)
	}
}
