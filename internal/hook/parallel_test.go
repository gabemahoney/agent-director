package hook_test

// SR-9.2: two relayed PermissionRequests of one spawn, concurrently on a real
// store (real SQLite write-lock interleaving) and the real poll clock, key
// distinct rows by request_token: one's decision or timeout never reaches the
// other.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// relayPair is two concurrent relayed hooks: A asks for Bash, B for Read.
type relayPair struct {
	tokenA, tokenB string
	outA, outB     bytes.Buffer
	doneA, doneB   chan struct{}
}

// startRelayPair runs A and B from instanceID's own agent (SR-22.9) with relay
// timeouts timeoutA and timeoutB seconds, and waits for both open rows.
func startRelayPair(t *testing.T, st *store.Store, instanceID string, timeoutA, timeoutB int) *relayPair {
	t.Helper()
	p := &relayPair{doneA: make(chan struct{}), doneB: make(chan struct{})}
	agent := agentParent(t, st, instanceID)
	run := func(tool string, timeout int, out *bytes.Buffer, done chan struct{}) {
		defer close(done)
		hc := hookConfig(envWith(instanceID), agent)
		hc.Cfg, hc.Clock = config.Relay{TimeoutSeconds: timeout}, hook.DefaultPollClock()
		_ = hook.Handle(context.Background(), strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"`+tool+`","tool_input":{}}`),
			out, st, hc, nil)
	}
	go run("Bash", timeoutA, &p.outA, p.doneA)
	go run("Read", timeoutB, &p.outB, p.doneB)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (p.tokenA == "" || p.tokenB == "") {
		rows, _ := st.OpenPermissionRequestsForSpawn(instanceID)
		for _, r := range rows { // by tool: concurrent inserts may tie on created_at
			switch r.ToolName {
			case "Bash":
				p.tokenA = r.RequestToken
			case "Read":
				p.tokenB = r.RequestToken
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.tokenA == "" || p.tokenB == "" || p.tokenA == p.tokenB {
		t.Fatalf("open rows: Bash token %q, Read token %q; want two distinct within 5s", p.tokenA, p.tokenB)
	}
	return p
}

// waitDone fails the test unless done closes within 4s.
func waitDone(t *testing.T, done chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatalf("%s did not return within 4s", what)
	}
}

// decided reads id's request token's decision and reason.
func decided(t *testing.T, st *store.Store, id, token string) (string, string) {
	t.Helper()
	row, err := st.GetPermissionRequest(id, token)
	if err != nil {
		t.Fatalf("GetPermissionRequest(%s): %v", token, err)
	}
	return row.Decision, row.DecisionReason
}

// TestParallelHookOrdering: the spawn is check_permission while both rows are
// open; deciding A (allow) leaves B undecided; each hook prints its own verdict.
func TestParallelHookOrdering(t *testing.T) {
	const id = "parallel-hook-ord"
	st, _ := seedAgentRow(t, id, store.StateWorking)
	p := startRelayPair(t, st, id, 30, 30)

	if state, err := st.GetSpawnState(id); err != nil || state != store.StateCheckPermission {
		t.Errorf("state = %q (%v) before any row is decided; want check_permission", state, err)
	}
	if _, err := st.DecidePermissionRequest(id, p.tokenA, "allow", "", ""); err != nil {
		t.Fatalf("decide A: %v", err)
	}
	waitDone(t, p.doneA, "hook A")
	if d, _ := decided(t, st, id, p.tokenB); d != "" {
		t.Errorf("row B decided %q after deciding A only", d)
	}
	if _, err := st.DecidePermissionRequest(id, p.tokenB, "deny", "not allowed", ""); err != nil {
		t.Fatalf("decide B: %v", err)
	}
	waitDone(t, p.doneB, "hook B")

	wantA := hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "") + "\n"
	wantB := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "not allowed") + "\n"
	if a, b := p.outA.String(), p.outB.String(); a != wantA || b != wantB {
		t.Errorf("A printed %q, B printed %q; want %q, %q (each its own verdict)", a, b, wantA, wantB)
	}
}

// TestPerRowTimeoutIsolation: A's 1s relay timeout writes deny/timeout to A's
// row only; B stays open until its own allow, which carries no reason.
func TestPerRowTimeoutIsolation(t *testing.T) {
	const id = "per-row-timeout-iso"
	st, _ := seedAgentRow(t, id, store.StateWorking)
	p := startRelayPair(t, st, id, 1, 60)

	waitDone(t, p.doneA, "hook A (1s timeout)")
	if d, r := decided(t, st, id, p.tokenA); d != "deny" || r != store.DecisionReasonTimeout {
		t.Errorf("row A = %q/%q; want deny/%s", d, r, store.DecisionReasonTimeout)
	}
	if d, _ := decided(t, st, id, p.tokenB); d != "" {
		t.Errorf("row B decided %q after A's timeout; want open", d)
	}
	if _, err := st.DecidePermissionRequest(id, p.tokenB, "allow", "", ""); err != nil {
		t.Fatalf("decide B: %v", err)
	}
	waitDone(t, p.doneB, "hook B")
	if d, r := decided(t, st, id, p.tokenB); d != "allow" || r != "" {
		t.Errorf("row B = %q/%q; want allow with no reason", d, r)
	}
	assertDenyEnvelope(t, &p.outA)
	if !strings.Contains(p.outB.String(), `"behavior":"allow"`) {
		t.Errorf("hook B printed %q; want allow", p.outB.String())
	}
}
