//go:build linux

package realtmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// b.283: a Cursor turn's end releases an undecided relay request's input
// hold before and after confirm_by, without a verdict or pane answer.
func TestCursorTurnEndReleasesUndeliveredRelayInput(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane turn-end relay release")
	}
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	for _, phase := range []string{"before", "after"} {
		for _, operation := range []string{"plain", "inspected", "pause"} {
			t.Run(phase+"/"+operation, func(t *testing.T) {
				const session = "turn-end-release-session"
				script := cursorfix.Scenario{SessionID: session, CompleteBeforeAnswers: true, Requests: []json.RawMessage{relayFrame("release-request", "session/request_permission", cursorfix.Permission(session, "release-tool", "execute"))}}
				timeout := 20
				if phase == "after" {
					timeout = 1
				}
				f, peer, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: timeout})
				h := gatewayHold{f: f, peer: peer, launch: launch, relay: "on"}
				if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "end before answers"}); e != nil {
					t.Fatal(e)
				}
				h.req = relayRequests(t, f, launch.ID, 1)[0]
				waitFor(t, "turn ends to waiting", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, h.observe(t))
				peer.Configure(t, cursorfix.Scenario{SessionID: session})
				p, e := f.API.GetPermission(api.GetPermissionParams{RequestToken: h.req.RequestToken})
				if e != nil {
					t.Fatal(e)
				}
				if p.ProvenGoneHow == nil || *p.ProvenGoneHow != store.ProvenGoneTurnEnd || p.Decision != nil || p.DecidedAt != nil || p.Delivery == "delivered" || p.PaneAnswer != "none" {
					t.Fatalf("turn end must retire an undecided, undelivered request: %+v", p)
				}
				if phase == "after" {
					// The gateway/API use real process clocks. Wait only for this
					// request's short confirm_by to exercise the fallback guard.
					time.Sleep(time.Until(p.ConfirmBy) + 100*time.Millisecond)
				}
				p, e = f.API.GetPermission(api.GetPermissionParams{RequestToken: h.req.RequestToken})
				if e != nil {
					t.Fatal(e)
				}
				wantDelivery := "not_confirmed"
				if phase == "after" {
					wantDelivery = "fallen_back"
				}
				if p.Delivery != wantDelivery {
					t.Fatalf("%s confirm_by delivery: want %s, got %+v", phase, wantDelivery, p)
				}
				if phase == "before" && !time.Now().Before(p.ConfirmBy) {
					t.Fatal("setup exhausted the before-confirm_by window")
				}
				text := phase + " confirm_by " + operation
				switch operation {
				case "plain":
					_, e = f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: text})
				case "inspected":
					e = h.followUp(t, text)
				case "pause":
					_, e = f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID})
				}
				if e != nil {
					t.Fatalf("%s %s confirm_by on turn_end request: want success, got %v", operation, phase, e)
				}
				if phase == "before" && !time.Now().Before(p.ConfirmBy) {
					t.Fatal("input call crossed confirm_by")
				}
				if operation != "pause" {
					waitFor(t, "follow-up session/prompt reaches peer", func() bool { return h.prompted(t, text) }, h.observe(t))
				}
				before := relayRequests(t, f, launch.ID, 1)[0]
				wantRefusal := api.ErrNoOpenPermissionRequest
				// Pause already ended its row; only a live row takes the
				// documented late-decide fallback path after confirm_by.
				if phase == "after" && operation != "pause" {
					wantRefusal = api.ErrRelayFallenBack
				}
				if _, e := f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: h.req.RequestToken, Decision: "allow"}); !errors.Is(e, wantRefusal) {
					t.Fatalf("decide on retired request: want %v, got %v", wantRefusal, e)
				}
				after := relayRequests(t, f, launch.ID, 1)[0]
				// Pause closes the request with an ended denial before this
				// decide; its full snapshot must still remain unchanged below.
				if operation != "pause" && (after.Decision != "" || !after.DecidedAt.IsZero()) {
					t.Fatalf("late decide recorded a verdict: %+v", after)
				}
				if after.ResponseState != "" || !after.DeliveredAt.IsZero() || len(relayReplies(t, peer)) != 0 {
					t.Fatalf("late decide recorded a response: %+v replies=%v", after, relayReplies(t, peer))
				}
				if wantRefusal == api.ErrRelayFallenBack {
					// Only the attempted verdict and fallback observation may
					// change; proof, pane answer and all other facts stay intact.
					after.AttemptedDecision, after.AttemptedAt, after.HookGoneAt = before.AttemptedDecision, before.AttemptedAt, before.HookGoneAt
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("late decide changed retired request: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}
