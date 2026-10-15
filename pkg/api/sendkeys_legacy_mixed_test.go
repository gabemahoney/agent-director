package api_test

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
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// b.w21: recovering a legacy row must preserve a modern hook's independent
// hold after its deny acknowledgement, including plain sends with a pane hash.
func TestLegacyRecoveryMixedModernDelivery(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StatePending, store.StateCheckPermission} {
		for _, hook := range []struct {
			name  string
			acked bool
			alive string
			held  bool
		}{
			{"acked_live", true, "live", true},
			{"unacked_unknown_before_confirm", false, "unknown", true},
			{"acked_unknown", true, "unknown", false},
			{"acked_gone", true, "gone", false},
		} {
			for _, send := range []string{"plain", "valid_hash", "stale_hash", "legacy_token"} {
				t.Run(state+"/"+hook.name+"/"+send, func(t *testing.T) {
					t.Parallel()
					e := newKillEnv(t)
					r := e.seedRow(t, killRowSpec{State: state, RelayOn: true})
					restorePending := func() {
						if state == store.StatePending {
							legacyRecoveryRaw(t, e.dbPath, func(db *sql.DB) {
								if _, err := db.Exec(`UPDATE spawns SET state = ?, launch_started_at = ? WHERE instance_id = ?`,
									state, r.Spawn.LaunchStartedAtMillis, r.ID); err != nil {
									t.Fatal(err)
								}
							})
						}
					}
					old := legacyRecoveryRequest(t, e, r.ID, "deny", store.DecisionReasonEnded, store.ProvenGoneAgentGone)
					restorePending()
					legacy := legacyRecoveryRead(t, e.st, r.ID, old)
					if legacy.Decision != "deny" || !legacy.ProvenGone() || legacy.ProvenGoneHow != store.ProvenGoneAgentGone {
						t.Fatalf("legacy request = %+v; want decided agent_gone row", legacy)
					}
					p := &paEnv{killEnv: e, r: r, now: e.clock.Now(), sks: e.store}
					p.setPane("startup dialog\n")
					view := p.view()
					view.Window = legacyRecoveryWindow
					sendKeys := func(params api.SendKeysParams) verbRun[api.SendKeysResult] {
						params.AllowPending = state == store.StatePending
						return runVerb(e, func() (api.SendKeysResult, error) {
							pollTime := p.now
							pollView := view
							pollView.Now = func() time.Time { return pollTime }
							return api.SendKeys(e.store, e.rec, api.SendKeysEnv{Relay: pollView, Input: e.delivery(r.ID), Sleep: func(d time.Duration) { pollTime = pollTime.Add(d) }}, params)
						})
					}
					// The legacy row alone releases plain keys before its 24-hour window ends.
					legacyRecoveryAssertTyped(t, e, r, sendKeys(p.plain("continue")))
					e.rec.Reset()
					current := storefix.TestRequestTokenB
					p.seedRequest(t, current, "toolu_current", relayEnvHook)
					if ok, err := e.st.DecideRelayRequest(r.ID, current, "deny", store.DecisionReasonOperator,
						store.WriterProcessDecide, time.Time{}, store.DefaultLockWait); err != nil || !ok {
						t.Fatalf("current deny = %v, %v", ok, err)
					}
					if hook.acked {
						if _, _, ok, err := e.st.AckRelayDecision(r.ID, current, p.now, store.DefaultLockWait, nil); err != nil || !ok {
							t.Fatalf("current ack = %v, %v", ok, err)
						}
					}
					// Relay insertion moves to check_permission; retain a stored request
					// beside pending to exercise the guard before SessionStart as well.
					restorePending()
					spawn, err := e.st.GetSpawn(r.ID)
					if err != nil || spawn.State != state || (state == store.StatePending && spawn.LaunchStartedAtMillis == 0) {
						t.Fatalf("spawn state = %s, %v; want %s", spawn.State, err, state)
					}
					switch hook.alive {
					case "unknown":
						e.pc.Set(relayEnvHook.PID, procfix.Unreadable())
					case "live":
						e.pc.Set(relayEnvHook.PID, procfix.Alive(relayEnvHook.Starttime))
					default:
						e.pc.Set(relayEnvHook.PID, procfix.Gone())
					}
					row := legacyRecoveryRead(t, e.st, r.ID, current)
					if row.PreV7() || row.Closed() || row.ProvenGone() || row.Decision != "deny" || row.DeliveredAt.IsZero() == hook.acked {
						t.Fatalf("current request = %+v; want modern unclosed deny, ack=%v", row, hook.acked)
					}
					facts, err := api.GetPermission(e.st, view, api.GetPermissionParams{RequestToken: current})
					wantDelivery := api.DeliveryNotConfirmed
					if hook.acked {
						wantDelivery = api.DeliveryDelivered
					}
					if err != nil || facts.Delivery != wantDelivery || !p.now.Before(facts.ConfirmBy) ||
						(facts.HookAlive == nil) != (hook.alive == "unknown") ||
						(hook.alive != "unknown" && *facts.HookAlive != (hook.alive == "live")) {
						t.Fatalf("current facts = %+v, %v; want %s, hook alive %v, before confirm_by", facts, err, wantDelivery, hook.alive)
					}
					params := p.plain("continue")
					var want error
					var calls []tmux.Call
					switch send {
					case "plain":
						params.DeliveryRequestID = "after-modern-hook"
						want = api.ErrDialogMaybeOpen
					case "valid_hash":
						params.ExpectPaneSHA256 = p.hash()
						calls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture, tmux.CallSendText, tmux.CallSendEnter}
					case "stale_hash":
						params.ExpectPaneSHA256 = api.PaneSHA256("an earlier dialog\n")
						want, calls = api.ErrPaneChanged, paneReadCalls
					case "legacy_token":
						params = p.answer(old, "allow", "1")
						want = store.ErrNoOpenPermissionRequest
					}
					if hook.held {
						want, calls = api.ErrSendKeysWhileRelayed, nil
					}
					run := sendKeys(params)
					if (want == nil && run.err != nil) || (want != nil && !errors.Is(run.err, want)) {
						t.Fatalf("send-keys = %v; want %v", run.err, want)
					}
					if !slices.Equal(paKinds(run.calls), calls) {
						t.Fatalf("pane calls = %v; want %v", paKinds(run.calls), calls)
					}
					if hook.held && (!strings.Contains(errText(run.err), current) || strings.Contains(errText(run.err), old)) {
						t.Fatalf("relay refusal = %v; want current token %s only", run.err, current)
					}
					if want == api.ErrDialogMaybeOpen {
						assertDialogMaybeOpen(t, run.err, current, state)
					}
					if want == nil {
						e.assertTextSent(t, r.Socket, r.Spawn.Identity.PaneID, "continue")
						e.assertEnterSent(t, r.Socket, r.Spawn.Identity.PaneID)
					}
					legacyRecoveryAssertVerdict(t, legacy, legacyRecoveryRead(t, e.st, r.ID, old))
					legacyRecoveryAssertVerdict(t, row, legacyRecoveryRead(t, e.st, r.ID, current))
				})
			}
		}
	}
}
