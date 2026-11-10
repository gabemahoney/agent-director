package api_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// The merged fallback must retain the SQLite admission across the paste call.
func TestSendKeysCodexPasteFencesEnterAfterAdmissionLoss(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancelled caller", "changed row revision"} {
		t.Run(mode, func(t *testing.T) {
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.env.Context = ctx
			if mode == "cancelled caller" {
				e.rec.AfterCall(tmux.CallSendText, func(tmuxfix.SocketCall, error) { cancel() })
			} else {
				e.rowWriteAfter(t, tmux.CallSendText, r)
			}
			const text = "first\n\t界"
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: text})
			d := assertDeliveryError(t, err, "partial_input_unconfirmed", len(text), false)
			if d.Phase != "text_attempted" || !d.KeysSent {
				t.Fatalf("lost paste progress: %+v", d)
			}
			if strings.Contains(err.Error(), "no input was attempted") {
				t.Fatalf("admission refusal denied the preceding paste: %v", err)
			}
			calls := e.rec.SocketCallsOf(tmux.CallSendText)
			if len(calls) != 1 || !calls[0].Paste || calls[0].Text != text {
				t.Fatalf("paste calls=%+v", calls)
			}
			e.assertNoCalls(t, tmux.CallSendEnter, tmux.CallSendKey)
			if e.waits != 0 {
				t.Fatal("polled after admission loss")
			}
		})
	}
}

// Model a cancellation during buffer preparation followed by failed cleanup.
type refusedPasteCleanupTmux struct {
	*tmuxfix.Recorder
	cancel context.CancelFunc
}

func (t refusedPasteCleanupTmux) PasteKeysPaneGuarded(socket, pane, text string, beforeWrite func(bool) error) error {
	t.cancel()
	return errors.Join(t.Recorder.PasteKeysPaneGuarded(socket, pane, text, beforeWrite), errors.New("private buffer cleanup failed"))
}

func TestSendKeysCodexPasteAdmissionRefusalRetainsCleanupError(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.env.Context = ctx
	_, err := api.SendKeys(e.s, refusedPasteCleanupTmux{e.rec, cancel}, e.env, api.SendKeysParams{InstanceID: r.ID, Text: "hello"})
	assertDeliveryError(t, err, "not_started", 0, false)
	if !strings.Contains(err.Error(), "private buffer cleanup failed") {
		t.Fatalf("lost cleanup diagnostic: %v", err)
	}
	e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendEnter, tmux.CallSendKey)
}

// Fallback preserves the existing manual-interference record, including a
// refusal when that intent's commit is not confirmed. It never replaces an
// unresolved native attempt; the capability-loss regressions cover that guard.
func TestSendKeysCodexPasteRecordsManualIntentBeforeInput(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "unconfirmed commit"}[fail], func(t *testing.T) {
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
			e.env.Input = &capabilityDeliveryBackend{deliveryBackend: e.b, supported: false}
			old, err := inputdelivery.NewAttempt(sqliteInputIdentity(r.Spawn, e.storeID), "old", nil)
			if err != nil {
				t.Fatal(err)
			}
			old.Phase = "not_started"
			e.b.current = old
			e.b.save = func(a *api.InputDeliveryAttempt) error {
				if a.Phase != "manual_input" || !a.ManualInterference {
					t.Fatalf("fallback intent=%+v", a)
				}
				if fail {
					return inputdelivery.ErrUnconfirmed
				}
				return nil
			}
			e.rec.AfterCall(tmux.CallSendText, func(tmuxfix.SocketCall, error) {
				if e.b.current.Phase != "manual_input" || !e.b.current.ManualInterference {
					t.Fatal("paste preceded durable manual intent")
				}
			})
			_, err = e.send(api.SendKeysParams{InstanceID: r.ID, Text: "first\tsecond"})
			if fail {
				assertDeliveryError(t, err, "not_started", 0, false)
				e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendEnter, tmux.CallSendKey)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertInputWrites(t, e.killEnv, r, "first\tsecond")
			}
		})
	}
}

func TestSendKeysCodexPasteRequiresJournalAdmissionCapability(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
	legacy := struct {
		api.SendKeysTmux
		api.PasteKeysTmux
	}{e.rec, e.rec}
	_, err := api.SendKeys(e.s, legacy, e.env, api.SendKeysParams{InstanceID: r.ID, Text: "hello"})
	if !errors.Is(err, api.ErrSubmissionUnconfirmed) {
		t.Fatalf("missing guarded capability: %v", err)
	}
	assertDeliveryError(t, err, "not_started", 0, false)
	e.assertNoTmuxCall(t)
}
