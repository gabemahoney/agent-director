package api_test

// b.w21: decided pre-v7 requests must stop holding a new launch once their
// asking agent is gone, including stores already stuck before the fix.

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const legacyRecoveryWindow = 86400 * time.Second

// legacyRecoveryRequest seeds a pre-v7 verdict; raw edits represent an
// already-upgraded store, without running any repairing lifecycle verb.
func legacyRecoveryRequest(t *testing.T, e *killEnv, id, decision, reason, proof string) string {
	t.Helper()
	seed, err := apitest.SeedPermissionRequest(e.dbPath, id, "Bash")
	if err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	if decision != "" {
		if ok, err := e.st.DecidePermissionRequest(id, seed.RequestToken, decision, reason, store.WriterProcessDecide); err != nil || !ok {
			t.Fatalf("DecidePermissionRequest = %v, %v", ok, err)
		}
	}
	var decidedAt, proofAt, proofHow any
	if decision != "" {
		decidedAt = e.clock.Now().Add(-9 * time.Minute).UTC().Format("2006-01-02 15:04:05")
	}
	if proof != "" {
		proofAt, proofHow = e.clock.Now().Add(-time.Minute).UnixMilli(), proof
	}
	legacyRecoveryRaw(t, e.dbPath, func(db *sql.DB) {
		if _, err := db.Exec(`UPDATE permission_requests SET created_at = ?, decided_at = ?, proven_gone_at = ?, proven_gone_how = ?
			WHERE instance_id = ? AND request_token = ?`, e.clock.Now().Add(-10*time.Minute).UTC().Format("2006-01-02 15:04:05"),
			decidedAt, proofAt, proofHow, id, seed.RequestToken); err != nil {
			t.Fatal(err)
		}
	})
	pr := legacyRecoveryRead(t, e.st, id, seed.RequestToken)
	if !pr.PreV7() || pr.Hook != (store.ProcessIdentity{}) || !pr.DeliveredAt.IsZero() || pr.Closed() || pr.PaneAnswer != store.PaneAnswerNone {
		t.Fatalf("legacy request = %+v; want no hook, settle, ack or close, pane none", pr)
	}
	return seed.RequestToken
}

func legacyRecoveryRaw(t *testing.T, path string, f func(*sql.DB)) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f(db)
}

func legacyRecoveryRead(t *testing.T, s *store.Store, id, token string) store.PermissionRow {
	t.Helper()
	pr, err := s.GetPermissionRequest(id, token)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func legacyRecoverySend(e *killEnv, r killRow, now time.Time, token string) verbRun[api.SendKeysResult] {
	return runVerb(e, func() (api.SendKeysResult, error) {
		params := api.SendKeysParams{InstanceID: r.ID, Text: "continue", AllowPending: r.Spawn.State == store.StatePending}
		if token != "" {
			params.RequestToken, params.As, params.Key, params.Text = token, "allow", "1", ""
			params.ExpectPaneSHA256 = api.PaneSHA256("startup dialog\n")
		}
		return api.SendKeys(e.store, e.rec, api.SendKeysEnv{Relay: api.RelayView{
			Procs: e.pc, PIDNamespace: func() (string, bool) { return relayNS, true },
			Now: func() time.Time { return now }, Window: legacyRecoveryWindow}}, params)
	})
}

func legacyRecoveryAssertTyped(t *testing.T, e *killEnv, r killRow, run verbRun[api.SendKeysResult]) {
	t.Helper()
	if run.err != nil {
		t.Fatalf("b.w21 plain send must recover automatically: %v (ErrSendKeysWhileRelayed=%v ErrRelayFallenBack=%v)",
			run.err, errors.Is(run.err, api.ErrSendKeysWhileRelayed), errors.Is(run.err, api.ErrRelayFallenBack))
	}
	if !slices.Equal(paKinds(run.calls), paneSendCalls) {
		t.Fatalf("plain send calls = %v; want %v", paKinds(run.calls), paneSendCalls)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "continue")
}

func legacyRecoveryAssertVerdict(t *testing.T, before, after store.PermissionRow) {
	t.Helper()
	if after.Decision != before.Decision || after.DecisionReason != before.DecisionReason || !after.DecidedAt.Equal(before.DecidedAt) ||
		!after.DeliveredAt.Equal(before.DeliveredAt) || after.PaneAnswer != before.PaneAnswer {
		t.Errorf("legacy verdict changed: before %+v, after %+v", before, after)
	}
}

func TestLegacyRecoveryAlreadyStuckStore(t *testing.T) {
	t.Parallel()
	verdicts := []struct{ name, decision, reason string }{
		{"allow", "allow", ""}, {"operator_deny", "deny", store.DecisionReasonOperator},
		{"find_missing_deny", "deny", store.DecisionReasonFindMissing}, {"ended_deny", "deny", store.DecisionReasonEnded},
	}
	for _, state := range []string{store.StatePending, store.StateWaiting} {
		for _, v := range verdicts {
			t.Run(state+"/"+v.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{State: state, RelayOn: true})
				token := legacyRecoveryRequest(t, e, r.ID, v.decision, v.reason, store.ProvenGoneAgentGone)
				before := legacyRecoveryRead(t, e.st, r.ID, token)
				legacyRecoveryAssertTyped(t, e, r, legacyRecoverySend(e, r, e.clock.Now(), ""))
				legacyRecoveryAssertVerdict(t, before, legacyRecoveryRead(t, e.st, r.ID, token))
			})
		}
	}
}

func TestLegacyRecoveryExpiredVerdicts(t *testing.T) {
	t.Parallel()
	for _, v := range []struct{ name, decision, reason string }{
		{"find_missing_deny", "deny", store.DecisionReasonFindMissing},
		{"ended_deny", "deny", store.DecisionReasonEnded}, {"ordinary_allow_control", "allow", ""},
	} {
		for _, beyond := range []time.Duration{0, time.Second} {
			t.Run(v.name+"/beyond_"+beyond.String(), func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{State: store.StateWaiting, RelayOn: true})
				token := legacyRecoveryRequest(t, e, r.ID, v.decision, v.reason, store.ProvenGoneAgentGone)
				before := legacyRecoveryRead(t, e.st, r.ID, token)
				now := before.CreatedAt.Add(legacyRecoveryWindow + 2*time.Second + beyond)
				legacyRecoveryAssertTyped(t, e, r, legacyRecoverySend(e, r, now, ""))
				legacyRecoveryAssertVerdict(t, before, legacyRecoveryRead(t, e.st, r.ID, token))
			})
		}
	}
}

func TestLegacyRecoveryTokenCannotAnswerGoneAgent(t *testing.T) {
	t.Parallel()
	for _, v := range []struct {
		name, decision, reason string
		want                   error
	}{
		{"allow", "allow", "", store.ErrNoOpenPermissionRequest},
		{"ended", "deny", store.DecisionReasonEnded, store.ErrNoOpenPermissionRequest},
		{"find_missing", "deny", store.DecisionReasonFindMissing, store.ErrAlreadyDecided},
	} {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: store.StateWaiting, RelayOn: true})
			token := legacyRecoveryRequest(t, e, r.ID, v.decision, v.reason, store.ProvenGoneAgentGone)
			before := legacyRecoveryRead(t, e.st, r.ID, token)
			run := legacyRecoverySend(e, r, before.CreatedAt.Add(legacyRecoveryWindow+3*time.Second), token)
			if !errors.Is(run.err, v.want) || len(run.calls) != 0 {
				t.Fatalf("gone-agent token send = %v, calls %v; want %v, zero keys", run.err, paKinds(run.calls), v.want)
			}
			legacyRecoveryAssertVerdict(t, before, legacyRecoveryRead(t, e.st, r.ID, token))
		})
	}
}

func TestLegacyRecoveryKeepsCurrentRequestHeld(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"legacy_unproven", "pending_legacy_unproven", "legacy_turn_end_only", "legacy_undecided_agent_gone", "modern_live_turn_end", "modern_live_tool_ran", "modern_unknown_before_settle", "stale_legacy_plus_current"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			state := store.StateWaiting
			if name == "pending_legacy_unproven" {
				state = store.StatePending
			}
			r := e.seedRow(t, killRowSpec{State: state, RelayOn: true})
			var token string
			switch name {
			case "legacy_unproven", "pending_legacy_unproven", "legacy_turn_end_only", "legacy_undecided_agent_gone":
				decision, proof := "allow", ""
				if name == "legacy_turn_end_only" {
					proof = store.ProvenGoneTurnEnd
				}
				if name == "legacy_undecided_agent_gone" {
					decision, proof = "", store.ProvenGoneAgentGone
				}
				token = legacyRecoveryRequest(t, e, r.ID, decision, "", proof)
			default:
				if name == "stale_legacy_plus_current" {
					legacyRecoveryRequest(t, e, r.ID, "deny", store.DecisionReasonEnded, store.ProvenGoneAgentGone)
				}
				token = storefix.TestRequestTokenB
				storefix.SeedRelayRequest(t, e.st, r.ID, store.RelayRequest{RequestToken: token,
					ToolName: "Bash", ToolInput: `{}`, ToolUseID: "toolu_current", Hook: relayEnvHook, SettledAt: e.clock.Now().Add(time.Hour)})
				if name == "modern_unknown_before_settle" {
					e.pc.Set(relayEnvHook.PID, procfix.Unreadable())
				} else {
					e.pc.Set(relayEnvHook.PID, procfix.Alive(relayEnvHook.Starttime))
				}
				if name == "modern_live_turn_end" || name == "modern_live_tool_ran" {
					proof := turnEnd
					if name == "modern_live_tool_ran" {
						proof = toolRan("toolu_current")
					}
					if n := proveGone(t, e, r.ID, proof); n != 1 {
						t.Fatalf("modern %s proof covered %d requests; want current request", proof.How, n)
					}
				}
			}
			for _, named := range []bool{false, true} {
				sendToken := ""
				if named {
					sendToken = token
				}
				run := legacyRecoverySend(e, r, e.clock.Now(), sendToken)
				if !errors.Is(run.err, api.ErrSendKeysWhileRelayed) || !strings.Contains(errText(run.err), token) || len(run.calls) != 0 {
					t.Fatalf("current request named=%v: %v, calls %v; want ErrSendKeysWhileRelayed naming %s, zero keys", named, run.err, paKinds(run.calls), token)
				}
			}
		})
	}
}

func TestLegacyRecoveryCloseResumeAndSessionStart(t *testing.T) {
	t.Parallel()
	for _, close := range []struct {
		name  string
		apply func(*testing.T, *paEnv)
	}{
		{"find_missing", r7Mark}, {"session_end", func(t *testing.T, p *paEnv) {
			if applied := apitest.ApplyAgentHook(t, p.dbPath, p.r.ID, "SessionEnd", ""); !applied.Applied {
				t.Fatalf("terminal SessionEnd = %+v; want applied", applied)
			}
		}},
	} {
		for _, priorProof := range []string{"", store.ProvenGoneTurnEnd} {
			t.Run(close.name+"/prior_"+priorProof, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				// Terminal writes use the store's wall clock; keep this history in the same relay window.
				e.clock.Advance(time.Now().UTC().Truncate(time.Second).Sub(e.clock.Now()))
				r := e.seedRow(t, killRowSpec{State: store.StateWaiting, RelayOn: true})
				token := legacyRecoveryRequest(t, e, r.ID, "deny", store.DecisionReasonOperator, priorProof)
				before := legacyRecoveryRead(t, e.st, r.ID, token)
				close.apply(t, &paEnv{killEnv: e, r: r})
				closed := legacyRecoveryRead(t, e.st, r.ID, token)
				if !closed.Closed() {
					t.Errorf("b.w21 terminal close left decided legacy request unclosed")
				}
				legacyRecoveryAssertVerdict(t, before, closed)
				if closed.Closed() {
					e.clock.Advance(closed.ClosedAt.Add(time.Second).Sub(e.clock.Now()))
				}
				sp, err := e.st.GetSpawn(r.ID)
				if err != nil {
					t.Fatal(err)
				}
				li := r.Spawn.Identity
				li.Token = newToken()
				res, version, err := e.st.MoveToPending(r.ID, sp.Snapshot, e.clock.Now().UnixMilli(), li.Token, r.Socket, "", store.LaunchOwner{})
				if err != nil || res != store.CondApplied {
					t.Fatalf("resume move = %v, %v", res, err)
				}
				if res, err := e.st.RecordLaunchIdentity(r.ID, version, li.Token, li); err != nil || res != store.CondApplied {
					t.Fatalf("new launch identity = %v, %v", res, err)
				}
				r.Token, r.Spawn.Identity = li.Token, li
				if err := e.rec.SetLabel(r.Socket, r.Session.ID, li.PaneID, li.Token, r.ID, r.StoreID); err != nil {
					t.Fatalf("label new launch: %v", err)
				}
				for _, state := range []string{store.StatePending, store.StateWaiting} {
					if state == store.StateWaiting {
						if applied := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionStart", "sess-"+newToken()); !applied.Applied {
							t.Fatalf("resumed SessionStart = %+v; want applied", applied)
						}
					}
					r.Spawn.State = state
					e.rec.Reset()
					legacyRecoveryAssertTyped(t, e, r, legacyRecoverySend(e, r, e.clock.Now(), ""))
					after := legacyRecoveryRead(t, e.st, r.ID, token)
					legacyRecoveryAssertVerdict(t, before, after)
					if !after.Closed() || !after.ClosedAt.Equal(closed.ClosedAt) {
						t.Errorf("closure did not persist across %s", state)
					}
				}
			})
		}
	}
}

func TestLegacyRecoveryPreservesCurrentFallback(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{State: store.StateWaiting, RelayOn: true})
	old := legacyRecoveryRequest(t, e, r.ID, "deny", store.DecisionReasonEnded, store.ProvenGoneAgentGone)
	current := storefix.TestRequestTokenB
	storefix.SeedRelayRequest(t, e.st, r.ID, store.RelayRequest{RequestToken: current,
		ToolName: "Bash", ToolInput: `{}`, Hook: relayEnvHook, SettledAt: e.clock.Now().Add(time.Hour)})
	e.pc.Set(relayEnvHook.PID, procfix.Gone())
	now := legacyRecoveryRead(t, e.st, r.ID, old).CreatedAt.Add(legacyRecoveryWindow + 2*time.Second)
	run := legacyRecoverySend(e, r, now, "")
	if !errors.Is(run.err, api.ErrRelayFallenBack) || len(run.calls) != 0 {
		t.Fatalf("current fallback = %v, calls %v; want ErrRelayFallenBack, zero keys", run.err, paKinds(run.calls))
	}
	details, ok := api.ErrDetails(run.err).(api.RelayFallenBackDetails)
	if !ok || details.RequestToken != current {
		t.Fatalf("current fallback details = %#v; want current token %s", api.ErrDetails(run.err), current)
	}
}
