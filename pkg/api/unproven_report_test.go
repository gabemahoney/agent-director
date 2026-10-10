package api_test

// unproven_report_test.go — b.146 step 2c's report (rule 4): get's
// unproven_requests, and proven_gone_at, proven_gone_how and unproven_since on
// get-permission and on every reported request. Fixture:
// pane_answer_fixture_test.go.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// unprovenTokens is the request tokens of infos, in order.
func unprovenTokens(infos []api.PermissionRequestInfo) []string {
	out := []string{}
	for _, i := range infos {
		out = append(out, i.RequestToken)
	}
	return out
}

// TestGetReportsUnprovenRequests (b.146 step 2c rule 4): with A acked, B open
// (its relay hook alive) and C acked and proven by its tool's PostToolUse, get
// lists A and B, oldest first, in unproven_requests (A with unproven_since its
// ack's instant, B with none: it still awaits an answer) and B alone in
// permission_requests, with the same facts in both lists (one judged read);
// get-permission gives C's proof (tool_ran, its instant,
// no unproven_since) and A's unproven_since; list's rows carry no
// unproven_requests. Once the row ends, every request is proven agent_gone and
// get lists none.
func TestGetReportsUnprovenRequests(t *testing.T) {
	t.Parallel()
	tokA, tokB, tokC := storefix.TestRequestTokenA, storefix.TestRequestTokenB, storefix.TestRequestTokenC
	p := newPAEnv(t)
	hookB := api.ProcessIdentity{PID: 54322, Starttime: relayEnvHook.Starttime, PIDNamespace: relayNS}
	p.pc.Set(hookB.PID, procfix.Alive(hookB.Starttime))
	p.seedRequest(t, tokB, "toolu_01B", hookB)
	p.seedRequest(t, tokC, "toolu_01C", relayEnvHook)
	r7Acked(t, p)
	if ok, err := p.st.DecideRelayRequest(p.r.ID, tokC, "allow", "", store.WriterProcessDecide, time.Time{}, store.DefaultLockWait); err != nil || !ok {
		t.Fatalf("decide C = %v, %v", ok, err)
	}
	if _, _, ok, err := p.st.AckRelayDecision(p.r.ID, tokC, p.now, store.DefaultLockWait, nil); err != nil || !ok {
		t.Fatalf("ack C = %v, %v", ok, err)
	}
	proof := toolRan("toolu_01C")
	proof.At = p.now.Add(time.Second)
	if n := p.prove(t, proof); n != 1 {
		t.Fatalf("C's PostToolUse proved %d; want C", n)
	}
	acked := p.now
	p.now = p.now.Add(time.Minute)

	row, err := api.Get(p.st, p.view(), p.r.ID)

	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := unprovenTokens(row.UnprovenRequests); len(got) != 2 || got[0] != tokA || got[1] != tokB {
		t.Fatalf("unproven_requests = %v; want [A B]", got)
	}
	a, b := row.UnprovenRequests[0], row.UnprovenRequests[1]
	if a.Delivery != api.DeliveryDelivered || a.UnprovenSince == nil || a.UnprovenSince.UnixMilli() != acked.UnixMilli() || a.ProvenGoneAt != nil {
		t.Errorf("unproven A = %s; want delivered, unproven_since its ack %v", jsonOf(t, a), acked)
	}
	if b.Delivery != api.DeliveryNotConfirmed || b.UnprovenSince != nil || b.ProvenGoneAt != nil || b.ProvenGoneHow != nil {
		t.Errorf("unproven B = %s; want not_confirmed, unproven_since null (it awaits an answer)", jsonOf(t, b))
	}
	if got := unprovenTokens(row.PermissionRequests); len(got) != 1 || got[0] != tokB {
		t.Errorf("permission_requests = %v; want [B], the open one", got)
	} else if open := jsonOf(t, row.PermissionRequests[0]); open != jsonOf(t, b) {
		t.Errorf("B in permission_requests = %s; want the same facts as in unproven_requests %s (one read)", open, jsonOf(t, b))
	}

	c, err := api.GetPermission(p.st, p.view(), api.GetPermissionParams{RequestToken: tokC})
	if err != nil || c.ProvenGoneAt == nil || c.ProvenGoneAt.UnixMilli() != proof.At.UnixMilli() || c.ProvenGoneHow == nil ||
		*c.ProvenGoneHow != store.ProvenGoneToolRan || c.UnprovenSince != nil {
		t.Errorf("get-permission C = %s, %v; want proven tool_ran at %v, unproven_since null", jsonOf(t, c), err, proof.At)
	}
	ga, err := api.GetPermission(p.st, p.view(), api.GetPermissionParams{RequestToken: tokA})
	if err != nil || ga.ProvenGoneAt != nil || ga.UnprovenSince == nil || ga.UnprovenSince.UnixMilli() != acked.UnixMilli() {
		t.Errorf("get-permission A = %s, %v; want unproven since %v", jsonOf(t, ga), err, acked)
	}

	listed, err := api.List(p.st, p.view(), api.ListParams{})
	if err != nil || len(listed.Spawns) != 1 {
		t.Fatalf("List = %+v, %v; want the one row", listed.Spawns, err)
	}
	var listRow map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonOf(t, listed.Spawns[0])), &listRow); err != nil {
		t.Fatalf("unmarshal list row: %v", err)
	}
	if _, has := listRow["unproven_requests"]; has {
		t.Errorf("list row = %s; want no unproven_requests (get only)", jsonOf(t, listed.Spawns[0]))
	}

	r7End(t, p)
	ended, err := api.Get(p.st, p.view(), p.r.ID)
	if err != nil || ended.UnprovenRequests == nil || len(ended.UnprovenRequests) != 0 {
		t.Errorf("Get of the ended row = unproven_requests %v, %v; want []", ended.UnprovenRequests, err)
	}
	for _, tok := range []string{tokA, tokB} {
		if pr := p.request(t, tok); pr.ProvenGoneHow != store.ProvenGoneAgentGone {
			t.Errorf("request %s after the row ended = proven_gone_how %q; want agent_gone", tok, pr.ProvenGoneHow)
		}
	}
}
