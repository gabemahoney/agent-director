package hook_test

// b.o7x: exercise the real relay consumer after unrelated main-agent hooks,
// including a verdict committed before the bump and the hook's timeout deny.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const epochChildPermission = `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"},"agent_id":"epoch-child"}`
const epochIdle = `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`
const epochAsk = `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`

// epochMainHook shares the relay's injected clock and parent evidence, with
// a start beyond the request's real whole-second proof cutoff.
func epochMainHook(t *testing.T, st *store.Store, hc hook.HandleConfig, payload string) {
	t.Helper()
	hc.Start = onlyRequest(t, st, hc.Env("AGENT_DIRECTOR_INSTANCE_ID")).CreatedAt.Add(time.Second)
	hc.RelayHookGone = func(store.PermissionRow) bool { return false }
	if out, _ := fireRelay(t, st, hc, payload); out != "" {
		t.Fatalf("main hook output = %q; want empty", out)
	}
}

func epochView(hc hook.HandleConfig) api.RelayView {
	pc := procfix.New()
	pc.Set(relayHookSelf.PID, procfix.Alive(relayHookSelf.Starttime))
	return api.RelayView{Procs: pc, PIDNamespace: func() (string, bool) { return relayHookSelf.PIDNamespace, true }, Now: hc.Now, Window: time.Hour}
}

func epochDecide(t *testing.T, st *store.Store, hc hook.HandleConfig, id, token, verdict string) {
	t.Helper()
	// The caller returns before this same-thread relay resumes polling.
	// A zero wait bound avoids a real sleep while preserving the public API.
	var noWait int64
	res, err := api.Decide(st, epochView(hc),
		api.DecideParams{InstanceID: id, RequestToken: token, Decision: verdict, MaxWaitMs: &noWait})
	if err != nil || res.Delivery != api.DeliveryNotConfirmed {
		t.Fatalf("caller verdict before hook ack = %+v, %v; want not_confirmed", res, err)
	}
}

func TestSubagentEpochRelayAcrossMainHooks(t *testing.T) {
	for _, tc := range []struct {
		name, agent, event, state string
		bumps                     int64
		later                     bool
	}{
		{name: "Claude Stop", agent: "claude", event: `{"hook_event_name":"Stop"}`, state: store.StateWaiting, bumps: 1},
		{name: "Claude AskUserQuestion", agent: "claude", event: epochAsk, state: store.StateAskUser, bumps: 1},
		{name: "Claude idle held", agent: "claude", event: epochIdle, state: store.StateCheckPermission},
		{name: "Claude later main request", agent: "claude", event: `{"hook_event_name":"Stop"}`, state: store.StateCheckPermission, bumps: 2, later: true},
		{name: "Codex Stop", agent: "codex", event: `{"hook_event_name":"Stop"}`, state: store.StateWaiting, bumps: 1},
		{name: "Codex Interrupt", agent: "codex", event: `{"hook_event_name":"Interrupt"}`, state: store.StateWaiting, bumps: 1},
	} {
		for _, mode := range []string{"deny after bump", "allow before bump", "timeout"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				const id = "epoch-relay"
				st, _ := cxRelayRow(t, id, tc.agent, store.StateWorking)
				hc := relayHC(id, cxRelayParent(t, st, id, tc.agent), 15*time.Second)
				hc.Agent = tc.agent
				clock := hookClock(t, hc)
				var admitted store.PermissionRow
				atSleep(t, hc, 1, func() {
					admitted = onlyRequest(t, st, id)
					clock.Advance(time.Second) // turn-end must cover the request
					if mode == "allow before bump" {
						epochDecide(t, st, hc, id, admitted.RequestToken, "allow")
					}
					epochMainHook(t, st, hc, tc.event)
					if tc.later {
						epochMainHook(t, st, hc, `{"hook_event_name":"UserPromptSubmit"}`)
						if got := mustGetSpawn(t, st, id).State; got != store.StateWaiting {
							t.Fatalf("unacked child no longer holds working: %s", got)
						}
						storefix.SeedRelayRequest(t, st, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenB,
							ToolName: "Bash", ToolInput: `{}`, Hook: relayHookSelf, SettledAt: hc.Now().Add(time.Hour)})
					}
					row := mustGetSpawn(t, st, id)
					if row.State != tc.state || row.PermissionEpoch != admitted.OwnerPermissionEpoch+tc.bumps {
						t.Fatalf("main hooks left state=%s epoch=%d; want %s, admission %d + %d", row.State, row.PermissionEpoch, tc.state, admitted.OwnerPermissionEpoch, tc.bumps)
					}
					pr, err := st.GetPermissionRequest(id, admitted.RequestToken)
					if err != nil || pr.ProvenGone() || !pr.ClosedAt.IsZero() {
						t.Fatalf("main hooks retired child: %+v, %v", pr, err)
					}
					if mode == "deny after bump" {
						epochDecide(t, st, hc, id, admitted.RequestToken, "deny")
					}
				})
				before := len(readTrailLines(t, trailFile()))
				out, _ := fireRelay(t, st, hc, epochChildPermission)
				verdict, reason, origin := "deny", store.DecisionReasonOperator, store.DecisionOriginCaller
				if mode == "allow before bump" {
					verdict, reason = "allow", ""
				} else if mode == "timeout" {
					reason, origin = "", store.DecisionOriginTimeout
				}
				if want := hook.EncodeDecision(hook.EventNamePermissionRequest, verdict, reason) + "\n"; out != want {
					t.Errorf("exact relay output = %q; want one envelope %q", out, want)
				}
				pr, err := st.GetPermissionRequest(id, admitted.RequestToken)
				wantReason := reason
				if mode == "timeout" {
					wantReason = store.DecisionReasonTimeout
				}
				if err != nil || pr.Decision != verdict || pr.DecisionReason != wantReason || pr.DecisionOrigin != origin || pr.ResponseState != store.ResponseRelayClaimed || pr.DeliveredAt.IsZero() || pr.ProvenGone() || !pr.ClosedAt.IsZero() || pr.OwnerPermissionEpoch != admitted.OwnerPermissionEpoch {
					t.Errorf("durable response = %+v, %v; want %s/%s/%s, acked, original owner and no proof", pr, err, verdict, wantReason, origin)
				}
				gp, err := api.GetPermission(st, epochView(hc), api.GetPermissionParams{RequestToken: admitted.RequestToken})
				if err != nil || gp.Delivery != api.DeliveryDelivered || gp.ProvenGoneAt != nil {
					t.Errorf("GetPermission after emission = %+v, %v", gp, err)
				}
				if n := len(linesAfter(t, before, "ad.resume.observed", id)); n != 1 {
					t.Errorf("resume emissions = %d; want exactly one", n)
				}
				if tc.later {
					other, err := st.GetPermissionRequest(id, storefix.TestRequestTokenB)
					if err != nil || other.Decision != "" || !other.DeliveredAt.IsZero() {
						t.Errorf("earlier child affected next main request: %+v, %v", other, err)
					}
				}
				if _, _, ok, err := st.AckRelayDecision(id, pr.RequestToken, hc.Now(), store.DefaultLockWait, nil); err != nil || ok {
					t.Errorf("repeated ack = %v, %v; want false", ok, err)
				}
			})
		}
	}
}

// b.m3k: a slow real insert must not invalidate the main Stop proof fixture.
func TestSubagentEpochMainStopProofAfterLateInsert(t *testing.T) {
	const id = "epoch-late-insert"
	st, _ := cxRelayRow(t, id, store.AgentClaude, store.StateWorking)
	parent := agentParent(t, st, id)

	// SQLite uses the real clock. Put the relay clock behind it so the real
	// insert lands beyond the old virtual Stop cutoff without waiting.
	hc := relayHC(id, parent, 10*time.Second)
	clock := hookClock(t, hc)
	clock.Advance(-2 * time.Second)
	base := hc.Now()

	atSleep(t, hc, 1, func() {
		admitted := onlyRequest(t, st, id)
		clock.Advance(time.Second)
		oldCutoff := hc.Now().Truncate(time.Second)
		if !admitted.CreatedAt.After(base.Truncate(time.Second)) || admitted.CreatedAt.Before(oldCutoff) {
			t.Fatalf("late insert did not reproduce clock mismatch: base=%s created_at=%s old_cutoff=%s", base, admitted.CreatedAt, oldCutoff)
		}
		epochMainHook(t, st, hc, `{"hook_event_name":"Stop"}`)
		pr := onlyRequest(t, st, id)
		if !pr.ProvenGone() || pr.ProvenGoneHow != store.ProvenGoneTurnEnd {
			t.Fatalf("Stop did not prove late-inserted main request gone: %+v", pr)
		}
	})
	if out, _ := fireRelay(t, st, hc, relayPayload); out != "" {
		t.Errorf("retired request emitted %q; want no bytes", out)
	}
	if pr := onlyRequest(t, st, id); pr.ProvenGoneHow != store.ProvenGoneTurnEnd {
		t.Fatalf("relay did not retain turn_end proof: %+v", pr)
	}
}

func TestSubagentEpochRelayRetirementStillSilent(t *testing.T) {
	for _, retirement := range []string{"main Stop proof", "child session replacement", "child closure"} {
		for _, decided := range []bool{false, true} {
			name := retirement + "/timeout"
			if decided {
				name = retirement + "/verdict before retirement"
			}
			t.Run(name, func(t *testing.T) {
				const id = "epoch-retired"
				st, path := cxRelayRow(t, id, store.AgentClaude, store.StateWorking)
				hc := relayHC(id, agentParent(t, st, id), 10*time.Second)
				payload := epochChildPermission
				if retirement == "main Stop proof" {
					payload = relayPayload
				}
				var token string
				atSleep(t, hc, 1, func() {
					token = onlyRequest(t, st, id).RequestToken
					hookClock(t, hc).Advance(time.Second)
					if decided {
						epochDecide(t, st, hc, id, token, "allow")
					}
					epochMainHook(t, st, hc, `{"hook_event_name":"Stop"}`)
					switch retirement {
					case "child session replacement":
						if got := apitest.ApplyAgentHook(t, path, id, "SessionStart", "replacement"); !got.Applied {
							t.Fatalf("replace session: %+v", got)
						}
					case "child closure":
						row := mustGetSpawn(t, st, id)
						if _, result, err := st.MarkMissingIfSameLife(id, row.Snapshot); err != nil || result != store.CondApplied {
							t.Fatalf("close: %v, %v", result, err)
						}
					}
					pr, err := st.GetPermissionRequest(id, token)
					if err != nil {
						t.Fatal(err)
					}
					row := mustGetSpawn(t, st, id)
					if row.PermissionEpoch <= pr.OwnerPermissionEpoch {
						t.Fatalf("retirement did not advance epoch: row=%+v request=%+v", row, pr)
					}
					switch retirement {
					case "main Stop proof":
						if pr.ProvenGoneHow != store.ProvenGoneTurnEnd {
							t.Fatalf("Stop did not prove main request gone: %+v", pr)
						}
					case "child session replacement":
						if row.SessionID != "replacement" || row.SessionID == pr.OwnerSessionID {
							t.Fatalf("session did not replace request owner: row=%+v request=%+v", row, pr)
						}
					case "child closure":
						if pr.ClosedAt.IsZero() {
							t.Fatalf("request was not closed: %+v", pr)
						}
					}
				})
				before := len(readTrailLines(t, trailFile()))
				if out, _ := fireRelay(t, st, hc, payload); out != "" {
					t.Errorf("retired request emitted %q; want no bytes", out)
				}
				pr, err := st.GetPermissionRequest(id, token)
				if err != nil || !pr.DeliveredAt.IsZero() || pr.ResponseState != "" {
					t.Errorf("retirement granted an ack: %+v, %v", pr, err)
				}
				if n := len(linesAfter(t, before, "ad.resume.observed", id)); n != 0 {
					t.Errorf("retired request emitted %d resume events", n)
				}
			})
		}
	}
}

func TestSubagentEpochIdleNotificationAfterDelivery(t *testing.T) {
	const id = "epoch-idle"
	st, _ := cxRelayRow(t, id, store.AgentClaude, store.StateWorking)
	hc := relayHC(id, agentParent(t, st, id), 10*time.Second)
	atSleep(t, hc, 1, func() { decideOnly(t, st, id, "allow") })
	if out, _ := fireRelay(t, st, hc, epochChildPermission); out != hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "")+"\n" {
		t.Fatalf("initial delivery = %q", out)
	}
	admitted := onlyRequest(t, st, id)
	hookClock(t, hc).Advance(time.Second)
	epochMainHook(t, st, hc, `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	if got := mustGetSpawn(t, st, id).State; got != store.StateWorking {
		t.Fatalf("delivered child still holds working: %s", got)
	}
	epochMainHook(t, st, hc, epochIdle)
	row := mustGetSpawn(t, st, id)
	pr := onlyRequest(t, st, id)
	if row.State != store.StateWaiting || row.PermissionEpoch != admitted.OwnerPermissionEpoch+2 || pr.ProvenGone() || pr.OwnerPermissionEpoch != admitted.OwnerPermissionEpoch {
		t.Fatalf("idle Notification changed child proof/owner or did not bump state: row=%+v request=%+v", row, pr)
	}
}
