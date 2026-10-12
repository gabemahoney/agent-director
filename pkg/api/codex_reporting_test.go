package api_test

// codex_reporting_test.go — SR-8.1's reporting side on Codex rows (E4-T4): the
// rows' relay requests as get-permission, get and list show them, and the
// agent dimension (relayAgents) the decide delivery tests share. The delivery
// contract is Claude's, unchanged: no Codex-specific delivery is tested.

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// relayAgents are the agents whose rows the delivery and decide tests run on.
var relayAgents = []string{store.AgentClaude, store.AgentCodex}

// agentLabel is a subtest name for a case on agent's rows: name for Claude (the
// pre-Codex name), "codex/"+name otherwise.
func agentLabel(agent, name string) string {
	if agent == store.AgentClaude {
		return name
	}
	return agent + "/" + name
}

// codexKinds are the five kinds of Codex PermissionRequest (RN-7): the tool_name
// Codex sends and its tool_input, as the relay hook stores it (spaced, not
// canonical JSON, so any re-serialisation shows).
var codexKinds = []struct {
	name, toolName, toolInput string
}{
	{"shell command outside the sandbox", "Bash", `{ "command": "touch /work/outside/shell.txt", "description": "fixture" }`},
	{"apply_patch outside the writable roots", "apply_patch",
		`{ "command": "*** Begin Patch\n*** Add File: /work/outside/patch.txt\n+fixture\n*** End Patch" }`},
	{"write_stdin", "write_stdin", `{ "additional_permissions": null, "approval_id": "call_mock_4_0", "chars": "hello-fixture\n", "cwd": "file:///work/demo", ` +
		`"environment_id": "local", "parent_call_id": "call_mock_1_0", "sandbox_permissions": "require_escalated", "session_id": 1001, "tty": true }`},
	{"MCP tool", "mcp__ad__list", `{ "limit": 1 }`},
	{"network access", "Bash", `{ "command": "curl -sS -m 8 -o /dev/null -w fixture-np-%{http_code} http://example.com/", "description": "network-access http://example.com:80" }`},
}

// TestCodexRequestsReportedByteForByte: get-permission, get and list show a
// Codex request's tool_name and tool_input exactly as recorded (one case per
// RN-7 kind), its tool_use_id null, on the wire too; its agent_id is recorded
// as sent, a subagent's included (pkg/api shows no agent_id, so the store's
// row is the witness).
func TestCodexRequestsReportedByteForByte(t *testing.T) {
	t.Parallel()
	for _, k := range codexKinds {
		for _, agentID := range []string{"", paSubagentID} {
			name := k.name
			if agentID != "" {
				name += "/subagent"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				s, dbPath := apitest.SeedDecideFixtureAgent(t, "on", store.AgentCodex)
				now := time.Now().UTC().Truncate(time.Millisecond)
				e := &relayEnv{s: s, dbPath: dbPath, id: "id-d-1", pc: procfix.New(), ns: relayNS, now: now, settled: now.Add(time.Hour)}
				storefix.SeedRelayRequest(t, s, e.id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: k.toolName,
					ToolInput: k.toolInput, AgentID: agentID, Hook: relayEnvHook, SettledAt: e.settled})
				e.pc.Set(relayEnvHook.PID, procfix.Alive(relayEnvHook.Starttime))

				res := e.getPermission(t, e.s)
				fromGet, fromList := e.listed(t)

				if res.ToolName != k.toolName || res.ToolInput != k.toolInput || res.ToolUseID != nil {
					t.Errorf("get-permission = tool_name %q, tool_input %q, tool_use_id %v; want %q, %q, null", res.ToolName, res.ToolInput, res.ToolUseID, k.toolName, k.toolInput)
				}
				for _, got := range []struct {
					verb string
					info *api.PermissionRequestInfo
				}{{"get", fromGet}, {"list", fromList}} {
					if got.info == nil {
						t.Fatalf("%s lists no permission request", got.verb)
					}
					if got.info.ToolName != k.toolName || got.info.ToolInput != k.toolInput || got.info.ToolUseID != nil {
						t.Errorf("%s = tool_name %q, tool_input %q, tool_use_id %v; want %q, %q, null", got.verb, got.info.ToolName, got.info.ToolInput, got.info.ToolUseID, k.toolName, k.toolInput)
					}
				}
				if wire := jsonOf(t, res); !strings.Contains(wire, `"tool_use_id":null`) {
					t.Errorf("get-permission on the wire = %s; want tool_use_id null", wire)
				}
				if pr := e.request(t); pr.AgentID != agentID || pr.ToolUseID != "" {
					t.Errorf("stored request = agent_id %q, tool_use_id %q; want %q, none", pr.AgentID, pr.ToolUseID, agentID)
				}
			})
		}
	}
}

// TestCodexGetReportsTurnEndProof (b.146 step 2c rule 4 on a Codex row): with
// request A acked, get lists it in unproven_requests and get-permission gives
// its unproven_since and no proof; a PostToolUse (no tool_use_id to match)
// proves nothing; the main agent's Interrupt proves it turn_end, after which
// get-permission gives proven_gone_how turn_end, unproven_since null and get
// lists no unproven request.
func TestCodexGetReportsTurnEndProof(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newCodexPAEnv(t)
	r7Acked(t, p)
	acked := p.now
	p.now = p.now.Add(time.Minute)
	p.prove(t, toolRan(paToolUseID))

	row, err := api.Get(p.st, p.view(), p.r.ID)
	if err != nil || len(row.UnprovenRequests) != 1 || row.UnprovenRequests[0].RequestToken != tok {
		t.Fatalf("Get = unproven_requests %v, %v; want [A]", unprovenTokens(row.UnprovenRequests), err)
	}
	if u := row.UnprovenRequests[0]; u.ToolUseID != nil || u.UnprovenSince == nil || u.UnprovenSince.UnixMilli() != acked.UnixMilli() || u.ProvenGoneAt != nil {
		t.Errorf("unproven A = %s; want tool_use_id null, unproven since its ack %v, no proof", jsonOf(t, u), acked)
	}

	p.mainAgentStop(t)

	res, err := api.GetPermission(p.st, p.view(), api.GetPermissionParams{RequestToken: tok})
	if err != nil || res.ProvenGoneAt == nil || res.ProvenGoneHow == nil || *res.ProvenGoneHow != store.ProvenGoneTurnEnd || res.UnprovenSince != nil {
		t.Errorf("get-permission = %s, %v; want proven turn_end, unproven_since null", jsonOf(t, res), err)
	}
	row, err = api.Get(p.st, p.view(), p.r.ID)
	if err != nil || len(row.UnprovenRequests) != 0 {
		t.Errorf("Get after the Interrupt = unproven_requests %v, %v; want none", unprovenTokens(row.UnprovenRequests), err)
	}
}
