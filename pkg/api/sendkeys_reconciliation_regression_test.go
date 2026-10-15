package api_test

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestSendKeysUnresolvedOwnerBlocksDifferentRequests(t *testing.T) {
	t.Parallel()
	for _, partial := range []bool{false, true} {
		for _, reopened := range []bool{false, true} {
			for _, request := range []string{"fresh ID", "different payload", "fresh ID and payload"} {
				t.Run(testName(partial, reopened, request), func(t *testing.T) {
					e := newSubmissionEnv(t)
					r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
					e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) { return nil, nil }
					if partial {
						e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallSendText)
					}
					p := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true, DeliveryRequestID: "original"}
					_, err := e.send(p)
					outcome, enters := "submission_unconfirmed", 1
					if partial {
						outcome, enters = "partial_input_unconfirmed", 0
					}
					assertDeliveryError(t, err, outcome, len(skpText), !partial)
					if len(e.rec.SocketCallsOf(tmux.CallSendText)) != 1 || len(ordinaryEnters(e.killEnv)) != enters {
						t.Fatal("initial unresolved attempt replayed input")
					}
					owner := cloneAttempt(e.b.current)
					if reopened {
						e.b = &deliveryBackend{current: cloneAttempt(owner), history: map[string]*api.InputDeliveryAttempt{owner.RequestID: cloneAttempt(owner)}, confirm: e.b.confirm}
						e.env.Input = e.b
					}
					if request != "different payload" {
						p.DeliveryRequestID = "fresh"
					}
					if request != "fresh ID" {
						p.Text = "different ordinary payload"
					}
					_, err = e.send(p)
					if request == "different payload" {
						assertDeliveryError(t, err, "not_started", 0, false)
					} else {
						assertDeliveryError(t, err, outcome, len(skpText), !partial)
					}
					if !reflect.DeepEqual(e.b.current, owner) || len(e.b.history) != 1 || !reflect.DeepEqual(e.b.history["original"], owner) {
						t.Fatalf("new request replaced unresolved owner: current=%+v history=%+v", e.b.current, e.b.history)
					}
					if len(e.rec.SocketCallsOf(tmux.CallSendText)) != 1 || len(ordinaryEnters(e.killEnv)) != enters {
						t.Fatal("different request replayed unresolved input")
					}
				})
			}
		}
	}
}

func testName(partial, reopened bool, request string) string {
	phase, backend := "after Enter", "same backend"
	if partial {
		phase = "partial text"
	}
	if reopened {
		backend = "reopened backend"
	}
	return phase + "/" + backend + "/" + request
}

func TestSendKeysPassiveReconciliationNeedsSameLaunchOnly(t *testing.T) {
	t.Parallel()
	for _, stored := range []bool{false, true} {
		for _, state := range []string{store.StateEnded, store.StateMissing, "gone pane", "changed token", "changed life", "changed session"} {
			name := "late proof/" + state
			if stored {
				name = "stored proof/" + state
			}
			t.Run(name, func(t *testing.T) {
				e := newSubmissionEnv(t)
				r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
				p := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true, DeliveryRequestID: "original"}
				if !stored {
					e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) { return nil, nil }
				}
				_, err := e.send(p)
				if stored && err != nil {
					t.Fatal(err)
				}
				if !stored {
					assertDeliveryError(t, err, "submission_unconfirmed", len(skpText), true)
				}
				assertOrdinaryWrites(t, e.killEnv, r, skpText)
				e.b.confirm = nil
				alter := func(row api.Spawn) api.Spawn {
					switch state {
					case store.StateEnded, store.StateMissing:
						row.State = state
					case "changed token":
						row.Identity.Token = "different-launch"
					case "changed life":
						row.LifeNumber++
					case "changed session":
						row.SessionID = "different-native-session"
					}
					return row
				}
				if state != "changed life" {
					e.s.get = alter
				}
				e.s.read = func(row api.Spawn) (api.Spawn, error) { return alter(row), nil }
				if err := e.rec.KillSessionID(r.Socket, r.Session.ID); err != nil {
					t.Fatal(err)
				}
				before := len(e.rec.SocketCalls())
				result, err := e.send(p)
				changed := state == "changed token" || state == "changed life" || state == "changed session"
				if changed {
					if err == nil || result.Outcome == "submitted" {
						t.Fatalf("foreign launch reconciled: %+v %v", result, err)
					}
				} else if err != nil || result.RequestID != "original" || result.Outcome != "submitted" || result.RecordID != "new-user-record" {
					t.Fatalf("passive proof=%+v %v", result, err)
				}
				if len(e.rec.SocketCalls()) != before {
					t.Fatal("passive reconciliation touched tmux")
				}
			})
		}
	}
}
