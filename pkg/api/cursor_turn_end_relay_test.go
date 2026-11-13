package api_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// b.283: Cursor's turn-end proof releases both relay holds, including an
// unacknowledged verdict; an unproven request or another agent still holds input.
func TestCursorTurnEndRelayInput(t *testing.T) {
	t.Parallel()
	type relayCase struct {
		name    string
		env     func(*testing.T) *paEnv
		proven  bool
		decided bool
		expired bool
		sibling bool
		want    error
	}
	cursorEnv := func(t *testing.T) *paEnv {
		p := newPAEnvAgent(t, store.AgentCursor, "")
		// Cursor has no relay-hook process; only confirm_by bounds delivery.
		cursorReadSQL(t, p.dbPath, `UPDATE permission_requests SET hook_pid=NULL,
		 hook_starttime=NULL,hook_pidns=NULL WHERE instance_id=?`, p.r.ID)
		return p
	}
	cases := []relayCase{
		{name: "Cursor/undecided/before-confirm-by", env: cursorEnv, proven: true},
		{name: "Cursor/undecided/after-confirm-by", env: cursorEnv, proven: true, expired: true},
		{name: "Cursor/decided/before-confirm-by", env: cursorEnv, proven: true, decided: true},
		{name: "Cursor/unproven", env: cursorEnv, want: api.ErrSendKeysWhileRelayed},
		{name: "Cursor/retired-A-live-B/before-confirm-by", env: cursorEnv, proven: true,
			sibling: true, want: api.ErrSendKeysWhileRelayed},
		{name: "Cursor/retired-A-live-B/after-confirm-by", env: cursorEnv, proven: true,
			sibling: true, expired: true, want: api.ErrRelayFallenBack},
	}
	for _, ag := range paAgents {
		cases = append(cases, relayCase{name: ag.name + "/turn-end-hook-may-answer", env: ag.env,
			proven: true, want: api.ErrSendKeysWhileRelayed})
	}
	for _, tc := range cases {
		for _, verb := range []string{"plain-send-keys", "inspected-send-keys", "pause"} {
			t.Run(tc.name+"/"+verb, func(t *testing.T) {
				t.Parallel()
				p := tc.env(t)
				token := storefix.TestRequestTokenA
				// Non-Cursor controls keep a hook that may still deliver a verdict.
				p.pc.Set(relayEnvHook.PID, procfix.Alive(relayEnvHook.Starttime))
				c, _ := p.client(t)
				api.SetClockForTest(c, func() time.Time { return p.now })
				api.SetSelfPIDNSForTest(c, func() (string, bool) { return relayNS, true })
				api.SetSleepForTest(c, func(d time.Duration) { p.now = p.now.Add(d) })
				if tc.decided {
					p.withPermissionWriter(t, func() {
						res, err := c.Decide(api.DecideParams{InstanceID: p.r.ID, RequestToken: token, Decision: "allow"})
						if err != nil || res.Delivery != api.DeliveryNotConfirmed {
							t.Fatalf("decide = %+v, %v; want an unacknowledged verdict", res, err)
						}
					})
				}
				if tc.proven {
					if p.agent == store.AgentCursor {
						// Reuse the complete synthetic binding; this tests the API guard,
						// not gateway authentication or real ACP turn completion.
						if err := storefix.WithSeedPane(p.dbPath, p.r.ID, func(gate store.HookGate) error {
							return p.st.ProveCursorTurnEnded(*gate.Cursor, []string{token}, time.Now().Add(time.Second),
								func(store.Spawn) error { return nil })
						}); err != nil {
							t.Fatal(err)
						}
					} else if n := p.prove(t, turnEnd()); n != 1 {
						t.Fatalf("turn end proved %d requests; want one", n)
					}
				}
				holding := token
				if tc.sibling {
					// A retires before the later request B is admitted (b.283 round 1).
					// B has no hook identity, as on a real Cursor request.
					holding = storefix.TestRequestTokenB
					p.seedRequest(t, holding, "toolu_02NEXT", api.ProcessIdentity{})
				}
				if err := seedAgentState(p.st, p.dbPath, p.r.ID, store.StateWaiting); err != nil {
					t.Fatal(err)
				}
				if p.agent == store.AgentCursor && (!tc.proven || tc.sibling) {
					// A real Cursor waiting report cannot override an unproven request.
					// Seed that snapshot explicitly to isolate Pause's relay guard from
					// its earlier state guard, as the non-Cursor controls do naturally.
					cursorReadSQL(t, p.dbPath, `UPDATE spawns SET state='waiting' WHERE instance_id=?`, p.r.ID)
				}
				pr := p.request(t, token)
				if pr.ProvenGone() != tc.proven || tc.proven && pr.ProvenGoneHow != store.ProvenGoneTurnEnd ||
					!pr.DeliveredAt.IsZero() || pr.Closed() || pr.PaneAnswered() || (pr.Decision != "") != tc.decided {
					t.Fatalf("unexpected request setup: %+v", pr)
				}
				if tc.sibling {
					b := p.request(t, holding)
					if b.RequestID <= pr.RequestID || b.ProvenGone() || !b.DeliveredAt.IsZero() ||
						b.Decision != "" || b.Closed() || b.PaneAnswered() {
						t.Fatalf("request B = %+v; want a later, unproven, unacknowledged request", b)
					}
					pr = b
				}
				if tc.expired {
					p.now = pr.SettledAt.Add(time.Millisecond)
				}
				res, err := c.GetPermission(api.GetPermissionParams{RequestToken: holding})
				wantDelivery := api.DeliveryNotConfirmed
				if tc.expired {
					wantDelivery = api.DeliveryFallenBack
				}
				if err != nil || res.Delivery != wantDelivery {
					t.Fatalf("get-permission = %+v, %v; want %s", res, err, wantDelivery)
				}
				if verb == "pause" && tc.want == nil {
					p.rec.AfterCall(callEnterKey, func(tmuxfix.SocketCall, error) {
						if err := seedAgentState(p.st, p.dbPath, p.r.ID, store.StateEnded); err != nil {
							t.Fatal(err)
						}
					})
				}

				if verb == "pause" {
					_, err = c.Pause(context.Background(), pauseParams(p.r))
				} else {
					params := p.plain("next prompt")
					if verb == "inspected-send-keys" {
						params.ExpectPaneSHA256 = p.hash()
					}
					_, err = c.SendKeys(params)
				}
				if tc.want != nil {
					assertOneSentinel(t, err, tc.want)
					if tc.sibling {
						adviceAssertPhrase(t, err, "request "+holding)
						if tc.expired {
							assertFallenBackDetails(t, err, holding, store.StateWaiting)
						}
					}
					p.assertNoTmuxCall(t)
					return
				}
				if errors.Is(err, api.ErrSendKeysWhileRelayed) || errors.Is(err, api.ErrRelayFallenBack) {
					t.Fatalf("%s refused by relay guard after Cursor turn end: %v", verb, err)
				}
				if err != nil {
					t.Fatalf("%s = %v; want success after Cursor turn end", verb, err)
				}
			})
		}
	}
}
