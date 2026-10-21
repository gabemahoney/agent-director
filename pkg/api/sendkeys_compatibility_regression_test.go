package api_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestSendKeysPlainLaunchRetainsEstablishedInput(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
		t.Run(state, func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{State: state})
			e.env.Input = nil // No native launch manifest: the pane is an ordinary stand-in.
			for _, p := range []api.SendKeysParams{
				{InstanceID: r.ID, Text: "manual prefix", NoEnter: true},
				{InstanceID: r.ID, Text: "first\r\n" + strings.Repeat("界", 300)},
				{InstanceID: r.ID, Text: "second prompt"},
			} {
				e.rec.Reset()
				result, err := e.send(p)
				if err != nil || result != (api.SendKeysResult{}) {
					t.Fatalf("plain input result=%+v err=%v; want established delivery without native proof", result, err)
				}
				if p.NoEnter {
					calls := e.rec.SocketCallsOf(tmux.CallSendText)
					if len(calls) != 1 || calls[0].Text != p.Text || calls[0].PressEnter || calls[0].Socket != r.Socket || calls[0].Target != r.Spawn.Identity.PaneID {
						t.Fatalf("manual text calls=%+v", calls)
					}
					e.assertNoCalls(t, tmux.CallSendEnter, tmux.CallSendKey)
				} else {
					assertTextSent(t, e.rec, r.Socket, r.Spawn.Identity.PaneID, strings.ReplaceAll(p.Text, "\r", ""))
					assertEnterSent(t, e.rec, r.Socket, r.Spawn.Identity.PaneID)
				}
			}
		})
	}
}

type unavailableDeliveryBackend struct{ opened bool }

func (b *unavailableDeliveryBackend) Open(api.InputDeliveryIdentity) (api.InputDeliverySession, error) {
	b.opened = true
	return nil, errors.New("native journal unavailable")
}

func TestSendKeysEstablishedGuardsPrecedeNativeJournal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state string
		relay skRelay
		want  error
	}{
		{"ended", store.StateEnded, skRelayOff, api.ErrSpawnNotInteractive},
		{"pending without opt in", store.StatePending, skRelayOff, api.ErrSpawnNotInteractive},
		{"relay held", store.StateCheckPermission, skRelayHeld, api.ErrSendKeysWhileRelayed},
		{"relay fallen back", store.StateCheckPermission, skRelayFallenBack, api.ErrRelayFallenBack},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{State: tc.state, RelayOn: tc.relay != skRelayOff})
			if tc.relay != skRelayOff {
				storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
			}
			if tc.relay == skRelayFallenBack {
				storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA, 2*sendKeysWindow())
			}
			e.env.Relay.Now = time.Now
			backend := &unavailableDeliveryBackend{}
			e.env.Input = backend
			_, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: "ordinary input"})
			if !errors.Is(err, tc.want) || backend.opened {
				t.Fatalf("guard=%v native opened=%v; want %v before native I/O", err, backend.opened, tc.want)
			}
			e.assertNoTmuxCall(t)
		})
	}
}

func TestSendKeysDialogGuardPrecedesNativeJournal(t *testing.T) {
	t.Parallel()
	p := newPAEnv(t)
	r7Acked(t, p)
	backend := &unavailableDeliveryBackend{}
	_, err := api.SendKeys(p.sks, p.rec, api.SendKeysEnv{Relay: p.view(), Input: backend}, p.plain("continue"))
	if !errors.Is(err, api.ErrDialogMaybeOpen) || backend.opened {
		t.Fatalf("guard=%v native opened=%v; want dialog hold before native I/O", err, backend.opened)
	}
	p.assertNothingSent(t)
}

type capabilityDeliveryBackend struct {
	*deliveryBackend
	supported bool
}

func (b *capabilityDeliveryBackend) Open(id api.InputDeliveryIdentity) (api.InputDeliverySession, error) {
	s, err := b.deliveryBackend.Open(id)
	if err != nil {
		return nil, err
	}
	return &capabilityDeliverySession{InputDeliverySession: s, supported: b.supported}, nil
}

type capabilityDeliverySession struct {
	api.InputDeliverySession
	supported bool
}

func (s *capabilityDeliverySession) Supports(api.InputDeliveryIdentity) bool { return s.supported }

func TestSendKeysUnsupportedNativeCapabilityKeepsEstablishedInput(t *testing.T) {
	t.Parallel()
	e := newNativeSubmissionEnv(t)
	r := e.seedRow(t, killRowSpec{State: store.StateWaiting})
	e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
	e.b.ready = func() (bool, error) { t.Fatal("unsupported launch polled native readiness"); return false, nil }
	e.b.queue = func() (int, error) { t.Fatal("unsupported launch polled native queue"); return 0, nil }
	e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) {
		t.Fatal("unsupported launch read native proof")
		return nil, nil
	}
	payload := strings.Repeat("界", 300)
	result, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: payload})
	if err != nil || result != (api.SendKeysResult{}) || e.b.current != nil {
		t.Fatalf("unsupported native result=%+v err=%v attempt=%+v", result, err, e.b.current)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, payload)
}

func TestSendKeysCapabilityLossNeverReplaysNativeAttempt(t *testing.T) {
	t.Parallel()
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "after Enter", true: "partial text"}[partial], func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			e.b.confirm = func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) { return nil, nil }
			if partial {
				e.b.queue = func() (int, error) {
					if e.b.current != nil && e.b.current.BytesAttempted > 0 {
						return 1, nil
					}
					return 0, nil
				}
			}
			params := api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true, DeliveryRequestID: "owner"}
			_, err := e.send(params)
			outcome := "submission_unconfirmed"
			if partial {
				outcome = "partial_input_unconfirmed"
			}
			assertDeliveryError(t, err, outcome, len(skpText), !partial)
			owner := cloneAttempt(e.b.current)
			texts, enters := len(e.rec.SocketCallsOf(tmux.CallSendText)), len(ordinaryEnters(e.killEnv))
			e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
			for _, id := range []string{"owner", "fresh"} {
				params.DeliveryRequestID = id
				_, err = e.send(params)
				assertDeliveryError(t, err, outcome, len(skpText), !partial)
				if !reflect.DeepEqual(owner, e.b.current) || len(e.rec.SocketCallsOf(tmux.CallSendText)) != texts || len(ordinaryEnters(e.killEnv)) != enters {
					t.Fatal("capability fallback replaced or replayed the unresolved native owner")
				}
			}
		})
	}
}

func TestSendKeysPendingCodexFallbackKeepsBoundedHookCheck(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StatePending, store.StateWorking, store.StateEnded} {
		t.Run(state, func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			e.env.Input = nil
			e.rec.AfterCall(tmux.CallSendEnter, func(_ tmuxfix.SocketCall, _ error) { e.s.sent = true })
			e.s.read = func(row api.Spawn) (api.Spawn, error) { row.State = state; return row, nil }
			result, err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			if state == store.StateWorking {
				if err != nil || result != (api.SendKeysResult{}) {
					t.Fatalf("hook activity result=%+v err=%v", result, err)
				}
			} else {
				assertOneName(t, err, "ErrSubmissionUnconfirmed")
				d, ok := api.ErrDetails(err).(api.SubmissionUnconfirmedDetails)
				if !ok || !d.KeysSent || d.SubmissionConfirmed || d.RowState != state {
					t.Fatalf("legacy uncertainty details=%+v err=%v", api.ErrDetails(err), err)
				}
			}
			if state == store.StatePending && e.slept != 2*time.Second {
				t.Fatalf("hook observation bound=%v; want 2s", e.slept)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText)
		})
	}
}
