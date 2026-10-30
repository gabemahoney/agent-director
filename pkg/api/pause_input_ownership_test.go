package api_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestPauseRecordsManualInterferenceOnlyBeforeInput(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"relay refusal", "missing pane", "lock held", "journal fails", "success"} {
		t.Run(mode, func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
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
			if mode == "lock held" || mode == "journal fails" {
				assertOneName(t, err, "ErrInternal")
			}
			for _, c := range e.rec.SocketCalls()[before:] {
				if c.Call == tmux.CallSendText || c.Call == tmux.CallSendKey || c.Call == tmux.CallSendEnter {
					t.Fatalf("refused pause sent input: %+v", c)
				}
			}
		})
	}
}

func TestPauseEstablishedGuardsPrecedeNativeJournal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state string
		relay bool
		want  string
	}{
		{"working", store.StateWorking, false, "ErrSpawnNotPausable"},
		{"pending", store.StatePending, false, "ErrSpawnNotPausable"},
		{"relay refusal", store.StateWaiting, true, "ErrSendKeysWhileRelayed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, killRowSpec{State: tc.state, RelayOn: tc.relay})
			if tc.relay {
				storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
			}
			backend := &unavailableDeliveryBackend{}
			_, err := api.Pause(context.Background(), e.store, e.rec, api.PauseEnv{Input: backend, Relay: e.env.Relay}, 0, pauseParams(r))
			assertOneName(t, err, tc.want)
			if backend.opened {
				t.Fatal("pause opened native journal before its established guard")
			}
			e.assertNoTmuxCall(t)
		})
	}
}

func TestPauseSQLiteRetainsUnresolvedOrdinaryOwner(t *testing.T) {
	t.Parallel()
	e, r, _ := sqliteSubmission(t)
	if a := storefix.ApplyAgentHook(t, e.st, r.ID, "SessionStart", r.Spawn.SessionID); !a.Applied {
		t.Fatalf("waiting hook: %+v", a)
	}
	row, err := e.st.GetSpawn(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Spawn = row
	p := api.SendKeysParams{InstanceID: r.ID, Text: "uncertain original", DeliveryRequestID: "original"}
	_, err = e.send(p)
	assertDeliveryError(t, err, "submission_unconfirmed", len(p.Text), true)
	_, owner := sqliteAttempt(t, e, r)
	cleared := false
	e.rec.AfterCall(tmux.CallSendKey, func(c tmuxfix.SocketCall, _ error) {
		if c.Key != "C-u" {
			return
		}
		cleared = true
		_, a := sqliteAttempt(t, e, r)
		if a.RequestID != owner.RequestID || !a.ManualInterference || a.BytesAttempted != owner.BytesAttempted || a.EnterAttempted != owner.EnterAttempted {
			t.Fatalf("pause erased owner: %+v", a)
		}
	})
	_, err = api.Pause(context.Background(), e.st, e.rec, api.PauseEnv{Relay: e.env.Relay}, 0, pauseParams(r))
	assertOneName(t, err, "ErrPauseTimeout")
	if !cleared {
		t.Fatal("pause did not issue its authorized input")
	}
	_, after := sqliteAttempt(t, e, r)
	if !after.ManualInterference || after.RequestID != owner.RequestID {
		t.Fatalf("lost interference: %+v", after)
	}
}

// t2.7tz.tb.ib R5: cleanup failure after /exit retains the dispatch facts and
// the unresolved ordinary owner; cleanup never hides a pre-input refusal.
func TestPauseSQLiteCloseFailureRetainsDispatch(t *testing.T) {
	t.Parallel()
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused=%t", refused), func(t *testing.T) {
			e, r, _ := sqliteSubmission(t)
			if a := storefix.ApplyAgentHook(t, e.st, r.ID, "SessionStart", r.Spawn.SessionID); !a.Applied {
				t.Fatalf("waiting hook: %+v", a)
			}
			row, err := e.st.GetSpawn(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			r.Spawn = row
			_, err = e.send(api.SendKeysParams{InstanceID: r.ID, Text: "uncertain original", DeliveryRequestID: "original"})
			assertDeliveryError(t, err, "submission_unconfirmed", len("uncertain original"), true)
			_, owner := sqliteAttempt(t, e, r)
			if refused {
				if err := e.rec.KillSessionID(r.Socket, r.Session.ID); err != nil {
					t.Fatal(err)
				}
			}
			e.rec.Reset()
			closed := 0
			cleanup := errors.New("injected cleanup acknowledgement loss")
			e.env.Input.(*sqliteDeliveryBackend).afterClose = func() error {
				closed++
				return cleanup
			}
			_, err = api.Pause(context.Background(), e.st, e.rec, api.PauseEnv{Input: e.env.Input, Relay: e.env.Relay}, 0, pauseParams(r))
			if closed != 1 {
				t.Fatalf("close count=%d; want 1", closed)
			}
			if !errors.Is(err, cleanup) {
				t.Errorf("cleanup failure is not observable: %v", err)
			}
			_, after := sqliteAttempt(t, e, r)
			if refused {
				assertOneName(t, err, "ErrTmuxSendKeys")
				e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendKey, tmux.CallSendEnter)
				if !reflect.DeepEqual(owner, after) {
					t.Fatal("refused pause changed ordinary owner")
				}
				return
			}
			assertOneName(t, err, "ErrSubmissionUnconfirmed")
			want := api.SubmissionUnconfirmedDetails{Outcome: "submission_unconfirmed", Phase: "enter_attempted",
				BytesAttempted: len("/exit"), EnterMayHaveBeenSent: true, KeysSent: true,
				RowState: row.State, Reason: "input writer cleanup failed: " + cleanup.Error()}
			if got := api.ErrDetails(err); got != want {
				t.Errorf("details=%+v; want %+v", got, want)
			}
			assertInputWrites(t, e.killEnv, r, "/exit")
			keys := e.rec.SocketCallsOf(tmux.CallSendKey)
			if len(keys) != 2 || keys[0].Key != "C-u" || keys[0].Socket != r.Socket || keys[0].Target != row.Identity.PaneID || keys[0].Repeat != 0 || keys[1].Key != "Enter" {
				t.Fatalf("pause keys=%+v; want one C-u and one Enter", keys)
			}
			if after == nil || !after.ManualInterference {
				t.Fatalf("cleanup lost interference: %+v", after)
			}
			owner.ManualInterference, owner.Revision, owner.Confirmation = true, after.Revision, after.Confirmation
			if !reflect.DeepEqual(owner, after) {
				t.Fatalf("cleanup changed original attempt: before=%+v after=%+v", owner, after)
			}
		})
	}
}
