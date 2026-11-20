package api_test

// b.o7x: an admitted child's response survives main-agent state intervals;
// delivery remains distinct from proof that the child's dialog is gone.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestSubagentEpochDecideAndReaders(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, agent, verdict string
		beforeBump, later    bool
	}{
		{name: "Claude Stop then deny", agent: "claude", verdict: "deny"},
		{name: "Claude verdict then Stop then ack", agent: "claude", verdict: "allow", beforeBump: true},
		{name: "Claude later main request", agent: "claude", verdict: "allow", later: true},
		{name: "Codex Interrupt then deny", agent: "codex", verdict: "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPAEnvAgent(t, tc.agent, paSubagentID)
			p.pc.Set(relayEnvHook.PID, procfix.Alive(relayEnvHook.Starttime))
			token := storefix.TestRequestTokenA
			admitted := p.request(t, token)
			params := api.DecideParams{InstanceID: p.r.ID, RequestToken: token, Decision: tc.verdict}
			decide := func(ack bool) api.DecideResult {
				t.Helper()
				res, err := api.DecideWithSleep(p.st, p.view(), func(d time.Duration) {
					p.now = p.now.Add(d)
					if ack {
						if _, _, ok, err := p.st.AckRelayDecision(p.r.ID, token, p.now, store.DefaultLockWait, nil); err != nil || !ok {
							t.Fatalf("hook ack after epoch bump = %v, %v", ok, err)
						}
					}
				}, params)
				if err != nil {
					t.Fatalf("Decide child across main state change: %v", err)
				}
				return res
			}
			if tc.beforeBump {
				if res := decide(false); res.Delivery != api.DeliveryNotConfirmed {
					t.Fatalf("verdict before ack = %+v; want not_confirmed", res)
				}
			}
			p.mainAgentStop(t)
			wantState := store.StateWaiting
			if tc.later {
				// The unanswered child still holds working transitions, even
				// after Stop. A later main permission has its own token.
				apitest.ApplyAgentHook(t, p.dbPath, p.r.ID, "UserPromptSubmit", "")
				if got := p.codexState(t); got != store.StateWaiting {
					t.Fatalf("working hold lost: %s", got)
				}
				p.agentID = ""
				p.seedRequest(t, storefix.TestRequestTokenB, "", relayEnvHook)
				wantState = store.StateCheckPermission
			}
			row, err := p.st.GetSpawn(p.r.ID)
			if err != nil || row.PermissionEpoch <= admitted.OwnerPermissionEpoch {
				t.Fatalf("no real epoch bump: row=%+v, err=%v, admission=%d", row, err, admitted.OwnerPermissionEpoch)
			}
			c, _ := p.client(t)
			api.SetClockForTest(c, func() time.Time { return p.now })
			api.SetSelfPIDNSForTest(c, func() (string, bool) { return relayNS, true })
			checkReads := func(delivery string) {
				t.Helper()
				one, err := api.Get(p.st, p.view(), p.r.ID)
				if err != nil {
					t.Fatal(err)
				}
				many, err := c.GetMany([]string{p.r.ID})
				if err != nil || len(many.Spawns) != 1 || len(many.NotFound) != 0 {
					t.Fatalf("GetMany = %+v, %v", many, err)
				}
				for _, got := range []api.SpawnRow{one, many.Spawns[0]} {
					if got.State != wantState || (wantState == store.StateWaiting && len(got.PermissionRequests) != 0) {
						t.Errorf("reader changed raw state or exposed permission_requests: %+v", got)
					}
					var child *api.PermissionRequestInfo
					for i := range got.UnprovenRequests {
						if got.UnprovenRequests[i].RequestToken == token {
							child = &got.UnprovenRequests[i]
						}
					}
					if child == nil || child.Delivery != delivery || child.ProvenGoneAt != nil || child.ProvenGoneHow != nil || aliveIs(child.HookAlive) != "true" {
						t.Errorf("child in unproven_requests = %+v; want %s, live, no proof", child, delivery)
					}
				}
				pr, err := api.GetPermission(p.st, p.view(), api.GetPermissionParams{RequestToken: token})
				if err != nil || pr.Delivery != delivery || pr.ProvenGoneAt != nil || aliveIs(pr.HookAlive) != "true" {
					t.Errorf("GetPermission = %+v, %v; want %s with no proof", pr, err, delivery)
				}
			}
			checkReads(api.DeliveryNotConfirmed)
			if tc.beforeBump {
				if _, _, ok, err := p.st.AckRelayDecision(p.r.ID, token, p.now, store.DefaultLockWait, nil); err != nil || !ok {
					t.Fatalf("ack original verdict after Stop = %v, %v", ok, err)
				}
			} else if res := decide(true); res.Delivery != api.DeliveryDelivered {
				t.Fatalf("Decide with hook ack = %+v; want delivered", res)
			}
			checkReads(api.DeliveryDelivered)
			if got := p.request(t, token); got.OwnerPermissionEpoch != admitted.OwnerPermissionEpoch || got.Decision != tc.verdict || got.DecisionOrigin != store.DecisionOriginCaller || got.ProvenGone() || !got.ClosedAt.IsZero() {
				t.Errorf("delivery rewrote ownership or proof: %+v", got)
			}
			if tc.later {
				if other := p.request(t, storefix.TestRequestTokenB); other.Decision != "" || !other.DeliveredAt.IsZero() {
					t.Errorf("child verdict affected next main request: %+v", other)
				}
			}
			_, err = api.Decide(p.st, p.view(), params)
			assertOneSentinel(t, err, api.ErrAlreadyDecided)
			if _, _, ok, err := p.st.AckRelayDecision(p.r.ID, token, p.now, store.DefaultLockWait, nil); err != nil || ok {
				t.Errorf("second ack = %v, %v; want false", ok, err)
			}
		})
	}
}

func TestSubagentEpochMainStopStillRefuses(t *testing.T) {
	t.Parallel()
	p := newPAEnv(t)
	p.pc.Set(relayEnvHook.PID, procfix.Alive(relayEnvHook.Starttime))
	p.mainAgentStop(t)
	if pr := p.request(t, storefix.TestRequestTokenA); pr.ProvenGoneHow != store.ProvenGoneTurnEnd {
		t.Fatalf("main Stop did not prove main request gone: %+v", pr)
	}
	_, err := api.Decide(p.st, p.view(), api.DecideParams{InstanceID: p.r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})
	assertOneSentinel(t, err, api.ErrNoOpenPermissionRequest)
}
