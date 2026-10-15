package api_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestPauseRecordsManualInterferenceOnlyBeforeInput(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"relay refusal", "missing pane", "lock held", "journal fails", "success"} {
		t.Run(mode, func(t *testing.T) {
			e := newSubmissionEnv(t)
			spec := codexSpec(store.StateWaiting, agentAlive, false)
			spec.RelayOn = mode == "relay refusal"
			r := e.seedRow(t, spec)
			e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) { return nil, nil }
			_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, DeliveryRequestID: "original"})
			assertDeliveryError(t, err, "submission_unconfirmed", len(skpText), true)
			owner := cloneAttempt(e.b.current)
			switch mode {
			case "relay refusal":
				e.store.failPermissionRequests(nil)
			case "missing pane":
				if err := e.rec.KillSessionID(r.Socket, r.Session.ID); err != nil {
					t.Fatal(err)
				}
			case "lock held":
				e.b.locked = true
			case "journal fails":
				e.b.save = func(*api.InputDeliveryAttempt) error { return fmt.Errorf("fsync failed") }
			}
			before := len(e.rec.SocketCalls())
			cleared := false
			e.rec.AfterCall(tmux.CallSendKey, func(c tmuxfix.SocketCall, _ error) {
				if c.Key != "C-u" {
					return
				}
				cleared = true
				if !e.b.current.ManualInterference || e.b.current.RequestID != owner.RequestID || !e.b.locked {
					t.Fatal("pause cleared composer before persisting original ownership under lock")
				}
			})
			_, err = api.Pause(context.Background(), e.store, e.rec, api.PauseEnv{Input: e.b, Relay: e.env.Relay}, 0, pauseParams(r))
			if mode == "success" {
				assertOneName(t, err, "ErrPauseTimeout")
				if !cleared || !e.b.current.ManualInterference || e.b.current.RequestID != owner.RequestID {
					t.Fatal("pause did not preserve manual interference ownership")
				}
				return
			}
			if err == nil || !reflect.DeepEqual(e.b.current, owner) {
				t.Fatalf("refused pause changed ownership: %+v %v", e.b.current, err)
			}
			for _, c := range e.rec.SocketCalls()[before:] {
				if c.Call == tmux.CallSendText || c.Call == tmux.CallSendKey || c.Call == tmux.CallSendEnter {
					t.Fatalf("refused pause sent input: %+v", c)
				}
			}
		})
	}
}
