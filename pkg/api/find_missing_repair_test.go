package api_test

// find_missing_repair_test.go — find-missing's repair of a stale
// check_permission row (b.146 rule 9) and the remembered idle-prompt
// Notification (problem 3): after a delivered timeout deny with the turn's
// Stop lost, the idle Notification moves the row to waiting and records
// idle_since, and the repair picks waiting when idle_since is set and working
// when it is not; a row whose request still awaits an answer, or whose relay
// hook may still run, is left alone, but a request find-missing's mark closed
// is not judged.

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// relayOtherNS is a pid namespace other than the reader's (relayNS): a hook
// recorded in it can't be checked.
const relayOtherNS = "pid:[4026532000]"

// relayHookB is the relay hook process request B records, not request A's
// (relayEnvHook); no test puts it in its process fake, so it reads gone.
var relayHookB = store.ProcessIdentity{PID: relayEnvHook.PID + 1, Starttime: storefix.SeedPaneStarttime, PIDNamespace: relayNS}

// idleNotification applies the main agent's idle-prompt Notification to id's
// row, as its own agent.
func idleNotification(t *testing.T, s *store.Store, dbPath, id string) {
	t.Helper()
	if err := storefix.WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		gate.Event = "Notification"
		_, _, err := s.ApplyHookWaitingIfWorking(id, gate, "Notification", "", false)
		return err
	}); err != nil {
		t.Fatalf("idle Notification on %s: %v", id, err)
	}
}

// timeoutDeny is request token's relay hook making its timeout deny, delivered.
func timeoutDeny(t *testing.T, s *store.Store, id, token string) {
	t.Helper()
	if ok, err := s.DenyRelayTimeout(id, token, time.Now(), store.DefaultLockWait, nil); err != nil || !ok {
		t.Fatalf("timeout deny of %s = %v, %v", token, ok, err)
	}
}

// relayRequestB records request B on id by relayHookB, settling at settled.
func relayRequestB(t *testing.T, s *store.Store, id string, settled time.Time) {
	t.Helper()
	storefix.SeedRelayRequest(t, s, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenB, ToolName: "Bash",
		ToolInput: `{}`, Hook: relayHookB, SettledAt: settled})
}

// TestRepairCheckPermission (rules 9, 12, problem 3): the repair moves a
// relay-on check_permission row out only when no request awaits an answer and
// no relay hook may still run (alive, or can't tell before its confirm_by): to
// waiting when the idle Notification came last, to working otherwise. A
// request the mark closed in the Spawn's earlier life does not hold it, its
// hook gone, alive or not checkable.
func TestRepairCheckPermission(t *testing.T) {
	t.Parallel()
	closedThenB := []string{"decide", "mark", "resume", "relayB", "denyB"}
	cases := []struct {
		name      string
		steps     []string // "idle" (the idle Notification), "deny" (A's timeout deny, delivered), "decide" (a verdict on A, not acked), "mark" (find-missing's mark), "resume" (the row resumed), "relayB" (request B recorded by relayHookB), "denyB" (its timeout deny, delivered)
		hook      string   // A's hook at the repair: "gone", "alive", "can't tell" (recorded in another pid namespace); B's is gone
		past      bool     // the repair runs at the requests' confirm_by
		wantState string
		wantIdle  bool
	}{
		{"timeout deny delivered, then the idle Notification", []string{"deny", "idle"}, "gone", false, store.StateWaiting, true},
		{"idle Notification while it awaited, then delivered", []string{"idle", "deny"}, "gone", false, store.StateWaiting, true},
		{"delivered, no idle Notification", []string{"deny"}, "gone", false, store.StateWorking, false},
		{"delivered, its hook still running", []string{"deny"}, "alive", false, store.StateCheckPermission, false},
		{"delivered, its hook can't be checked, before confirm_by", []string{"deny"}, "can't tell", false, store.StateCheckPermission, false},
		{"delivered, its hook can't be checked, at confirm_by", []string{"deny"}, "can't tell", true, store.StateWorking, false},
		{"a verdict not acked, its hook gone", []string{"decide"}, "gone", false, store.StateCheckPermission, false},
		{"undecided, its hook gone", nil, "gone", false, store.StateCheckPermission, false},
		{"a verdict closed by the mark, then resumed and B delivered", closedThenB, "gone", false, store.StateWorking, false},
		{"closed, its hook can't be checked, before confirm_by; resumed and B delivered", closedThenB, "can't tell", false,
			store.StateWorking, false},
		{"closed, its hook still running; resumed and B delivered", closedThenB, "alive", false, store.StateWorking, false},
		{"closed, its hook can't be checked; resumed, idle Notification while B awaited, then B delivered",
			[]string{"decide", "mark", "resume", "relayB", "idle", "denyB"}, "can't tell", false, store.StateWaiting, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hookA := relayEnvHook
			if tc.hook == "can't tell" {
				hookA.PIDNamespace = relayOtherNS
			}
			e := newRelayEnv(t, hookA)
			for _, step := range tc.steps {
				switch step {
				case "idle":
					idleNotification(t, e.s, e.dbPath, e.id)
				case "deny":
					timeoutDeny(t, e.s, e.id, storefix.TestRequestTokenA)
				case "decide":
					if _, _, err := e.decideWith(t, e.s, "allow", ptr(int64(0)), nil); err != nil {
						t.Fatalf("decide: %v", err)
					}
				case "mark":
					closeMark(t, e)
				case "resume":
					closeResume(t, e)
				case "relayB":
					relayRequestB(t, e.s, e.id, e.settled)
				case "denyB":
					timeoutDeny(t, e.s, e.id, storefix.TestRequestTokenB)
				}
			}
			if tc.hook == "gone" {
				e.pc.Set(hookA.PID, procfix.Gone())
			}
			if tc.past {
				e.now = e.settled
			}

			api.RepairCheckPermission(e.s, e.view(), nil)

			sp, err := e.s.GetSpawn(e.id)
			if err != nil {
				t.Fatalf("GetSpawn: %v", err)
			}
			if sp.State != tc.wantState || (sp.IdleSince != "") != tc.wantIdle {
				t.Errorf("row = state %q, idle_since %q; want %q, idle_since set %v", sp.State, sp.IdleSince, tc.wantState, tc.wantIdle)
			}
		})
	}
}

// resumeOnPane resumes r's finished row as resume does: the move to pending,
// the identity write recording r's pane (alive in e.pc), then the new agent's
// first hook, to waiting.
func (e *killEnv) resumeOnPane(t *testing.T, r killRow) {
	t.Helper()
	sp, err := e.st.GetSpawn(r.ID)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	token := newToken()
	res, moved, err := e.st.MoveToPending(r.ID, sp.Snapshot, e.clock.Now().UnixMilli(), token, r.Spawn.Identity.Socket, "", store.LaunchOwner{})
	if err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending = %v, %v; want applied", res, err)
	}
	if res, err := e.st.RecordLaunchIdentity(r.ID, moved, token, r.Spawn.Identity); err != nil || res != store.CondApplied {
		t.Fatalf("RecordLaunchIdentity = %v, %v; want applied", res, err)
	}
	if err := seedAgentState(e.st, e.dbPath, r.ID, store.StateWaiting); err != nil {
		t.Fatalf("the resumed agent's first hook: %v", err)
	}
}

// TestFindMissingRepairsStaleCheckPermission: Client.FindMissing runs the
// repair after its sweep: a live relay-on row left in check_permission after
// its request's timeout deny was delivered and its hook exited reads working,
// or waiting when the idle Notification came, with one stale_check_permission
// tick, also when a request the mark closed before a resume has a hook that
// can't be checked; a relay-off row in check_permission is left as it is, with
// none. A repaired row is in neither of the result's lists.
func TestFindMissingRepairsStaleCheckPermission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		relay  bool
		idle   bool
		closed bool // A decided, closed by the mark with its hook in another pid namespace, then resumed and B delivered
		want   string
	}{
		{"relay on, no idle Notification", true, false, false, store.StateWorking},
		{"relay on, idle Notification while it awaited", true, true, false, store.StateWaiting},
		{"relay on, a closed request's hook can't be checked, B delivered", true, false, true, store.StateWorking},
		{"relay off", false, false, false, store.StateCheckPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: store.StateCheckPermission, RelayOn: tc.relay})
			if tc.relay {
				hookA, settled := relayEnvHook, e.clock.Now().Add(time.Hour)
				if tc.closed {
					hookA.PIDNamespace = relayOtherNS
				}
				storefix.SeedRelayRequest(t, e.st, r.ID, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
					ToolInput: `{}`, Hook: hookA, SettledAt: settled})
				if tc.idle {
					idleNotification(t, e.st, e.dbPath, r.ID)
				}
				if tc.closed {
					re := &relayEnv{s: e.st, id: r.ID} // the closeSteps read only the store and the id
					closeDecide(t, re)
					closeMark(t, re)
					e.resumeOnPane(t, r)
					relayRequestB(t, e.st, r.ID, settled)
					timeoutDeny(t, e.st, r.ID, storefix.TestRequestTokenB)
				} else {
					timeoutDeny(t, e.st, r.ID, storefix.TestRequestTokenA) // e.pc lists no hook process: it has exited
				}
			}
			c, _ := e.client(t)
			api.SetSelfPIDNSForTest(c, func() (string, bool) { return relayNS, true })
			mark := trailMark(t)

			res, err := c.FindMissing(context.Background())
			if err != nil {
				t.Fatalf("FindMissing: %v", err)
			}

			if sp, err := e.st.GetSpawn(r.ID); err != nil || sp.State != tc.want {
				t.Errorf("state after find-missing = %q, %v; want %q", sp.State, err, tc.want)
			}
			if slices.Contains(res.IDs, r.ID) || slices.Contains(res.UnverifiedIDs, r.ID) {
				t.Errorf("result = %+v; want %s in neither list", res, r.ID)
			}
			ticks := slices.DeleteFunc(ticksSince(t, mark, r.ID), func(tk map[string]any) bool {
				return tk["reconciliation_reason"] != "stale_check_permission"
			})
			if tc.want == store.StateCheckPermission {
				if len(ticks) != 0 {
					t.Errorf("stale_check_permission ticks = %v; want none", ticks)
				}
				return
			}
			if len(ticks) != 1 {
				t.Fatalf("stale_check_permission ticks = %d; want 1", len(ticks))
			}
			want := map[string]any{"event": "ad.find_missing.tick", "claude_instance_id": r.ID, "source": "ad_find_missing",
				"prior_state": store.StateCheckPermission, "new_state": tc.want, "reconciliation_reason": "stale_check_permission"}
			for k, v := range want {
				assertAPITrailStr(t, ticks[0], k, v.(string))
			}
			if _, ok := ticks[0]["ts"]; !ok || len(ticks[0]) != len(want)+1 {
				t.Errorf("tick = %v; want exactly ts and %v", ticks[0], want)
			}
		})
	}
}
