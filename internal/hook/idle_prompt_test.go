package hook_test

// idle_prompt_test.go — b.svb through hook.Handle on a real store: the main
// agent's idle-prompt Notification returns a row left working (a background
// fork's PreToolUse after the turn's Stop) to waiting; every other
// Notification, and an idle prompt carrying agent_id, is a soft refresh; after
// a delivered timeout deny it also leaves check_permission (b.146 problem 3).
// The store-level columns, the gate and every prior state are in
// internal/store/hook_gate_test.go (TestHookGateWaitingIfWorking) and
// internal/store/relay_writes_test.go.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// notificationPayload is notification.json (an idle prompt) with
// notification_type set to ntype ("" removes it) and agent_id when non-empty.
func notificationPayload(t *testing.T, ntype, agentID string) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readPayloadFixture(t, "notification.json"), &m); err != nil {
		t.Fatalf("parse notification.json: %v", err)
	}
	delete(m, "notification_type")
	if ntype != "" {
		m["notification_type"] = ntype
	}
	if agentID != "" {
		m["agent_id"] = agentID
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal notification payload: %v", err)
	}
	return b
}

// assertNotificationApplied checks one applied Notification on id's row: one
// version advance, one prior -> want transition (soft unless the state moved),
// ad.hook.fired's upsert_outcome updated, and no ad.hook.ignored.
func assertNotificationApplied(t *testing.T, st *store.Store, id string, before int, prior store.Spawn, want string) {
	t.Helper()
	row := mustGetSpawn(t, st, id)
	if row.State != want {
		t.Errorf("State = %q; want %q", row.State, want)
	}
	if row.RowVersion != prior.RowVersion+1 {
		t.Errorf("RowVersion = %d; want %d (one applied write)", row.RowVersion, prior.RowVersion+1)
	}
	lines := linesAfter(t, before, "ad.spawn.state_transition", id)
	if len(lines) != 1 {
		t.Fatalf("ad.spawn.state_transition lines = %v; want exactly 1", lines)
	}
	assertStr(t, lines[0], "prior_state", prior.State)
	assertStr(t, lines[0], "new_state", want)
	assertStr(t, lines[0], "triggering_event_name", "Notification")
	if got, soft := lines[0]["soft_refresh"], want == prior.State; got != soft {
		t.Errorf("soft_refresh = %v; want %v", got, soft)
	}
	assertStr(t, oneFired(t, before, id), "upsert_outcome", string(store.UpsertUpdated))
	assertNoIgnored(t, before, id)
}

// TestIdlePromptReturnsStrayWorkingRowToWaiting is the b.svb regression,
// replaying CSCB live run 5: after the turn's Stop a background fork's
// PreToolUse (no PostToolUse, no Stop) left the row working at an idle prompt.
func TestIdlePromptReturnsStrayWorkingRowToWaiting(t *testing.T) {
	const id = "svb-cscb-replay"
	st, _ := seedAgentRow(t, id, store.StatePending)
	agent := agentParent(t, st, id)
	steps := []struct {
		name    string
		payload []byte
		want    string
	}{
		{"SessionStart", readPayloadFixture(t, "session-start-startup.json"), store.StateWaiting},
		{"UserPromptSubmit", readPayloadFixture(t, "user-prompt-submit.json"), store.StateWorking},
		{"Stop", readPayloadFixture(t, "stop.json"), store.StateWaiting},
		{"stray PreToolUse after Stop", readPayloadFixture(t, "pre-tool-use-bash.json"), store.StateWorking},
	}
	for _, s := range steps {
		fireGate(t, st, id, agent, nil, s.payload)
		if got := mustGetSpawn(t, st, id).State; got != s.want {
			t.Fatalf("after %s: State = %q; want %q", s.name, got, s.want)
		}
	}

	prior := mustGetSpawn(t, st, id)
	before := len(readTrailLines(t, trailFile()))
	fireGate(t, st, id, agent, nil, notificationPayload(t, "idle_prompt", ""))
	assertNotificationApplied(t, st, id, before, prior, store.StateWaiting)
}

// TestNotificationOnWorkingRow: from the row's own agent, every Notification
// but the main agent's idle prompt leaves a busy row working (soft refresh).
func TestNotificationOnWorkingRow(t *testing.T) {
	cases := []struct {
		name, ntype, agentID string
	}{
		{"idle_prompt from a subagent", "idle_prompt", "a5b92f0e7d3c6184"},
		{"permission_prompt", "permission_prompt", ""},
		{"auth_success", "auth_success", ""},
		{"elicitation_dialog", "elicitation_dialog", ""},
		{"unknown type", "future_notification_type", ""},
		{"no type", "", ""},
	}
	st, dbPath := storefix.OpenTempStore(t)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "svb-notify-" + string(rune('a'+i))
			seedGateRow(t, dbPath, id, store.StateWorking, "")
			prior := mustGetSpawn(t, st, id)
			before := len(readTrailLines(t, trailFile()))

			fireGate(t, st, id, agentParent(t, st, id), nil, notificationPayload(t, tc.ntype, tc.agentID))

			assertNotificationApplied(t, st, id, before, prior, store.StateWorking)
		})
	}
}

// TestIdlePromptAfterDeliveredTimeoutDeny replays b.146 problem 3: a relayed
// request's timeout deny is delivered (the row stays check_permission), the
// turn's Stop is lost, and the main agent's idle-prompt Notification moves the
// relay-on row to waiting and records idle_since; a subagent's idle prompt is a
// soft refresh that records none.
func TestIdlePromptAfterDeliveredTimeoutDeny(t *testing.T) {
	for i, tc := range []struct {
		name, agentID, want string
		idle                bool
	}{
		{"main agent", "", store.StateWaiting, true},
		{"subagent", "a5b92f0e7d3c6184", store.StateCheckPermission, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "p3-idle-after-deny-" + string(rune('a'+i))
			st, dbPath := storefix.OpenTempStore(t)
			if _, err := apitest.SeedSpawn(dbPath, id, store.StateWorking, "", "on", "", false); err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			agent := agentParent(t, st, id)
			if out, _ := fireRelay(t, st, relayHC(id, agent, 10*time.Second), relayPayload); !strings.Contains(out, `"deny"`) {
				t.Fatalf("relay stdout = %q; want its timeout deny", out)
			}
			prior := mustGetSpawn(t, st, id)
			if prior.State != store.StateCheckPermission {
				t.Fatalf("state after the delivered deny = %q; want check_permission", prior.State)
			}
			before := len(readTrailLines(t, trailFile()))

			fireGate(t, st, id, agent, nil, notificationPayload(t, "idle_prompt", tc.agentID))

			assertNotificationApplied(t, st, id, before, prior, tc.want)
			if got := mustGetSpawn(t, st, id).IdleSince != ""; got != tc.idle {
				t.Errorf("idle_since set = %v; want %v", got, tc.idle)
			}
		})
	}
}
