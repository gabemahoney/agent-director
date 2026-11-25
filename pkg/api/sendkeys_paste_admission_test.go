package api_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.cqr: a rollout append changes no SQLite revision. Check it again before
// paste and Enter, including adapters that supply no native input journal.
func TestSendKeysCodexTurnErrorDuringPastePreparation(t *testing.T) {
	t.Parallel()
	for _, noJournal := range []bool{false, true} {
		for _, afterText := range []bool{false, true} {
			t.Run(fmt.Sprintf("no_journal=%v/after_text=%v", noJournal, afterText), func(t *testing.T) {
				e := newSubmissionEnv(t)
				turn := codexfix.ErroredTurn
				path := codexfix.TempRolloutPath(t, turn.SessionID, true, turn.Started())
				spec := codexSpec(store.StateWaiting, agentAlive, false)
				spec.SessionID = turn.SessionID
				spec.Opts = append(spec.Opts, apitest.WithJsonlPath(path))
				if noJournal {
					e.env.Input = noJournalDeliveryBackend{}
				}
				r := e.seedRow(t, spec)
				at := tmux.CallListPanes
				if afterText {
					at = tmux.CallSendText
				}
				e.rec.AfterCall(at, func(tmuxfix.SocketCall, error) {
					if err := os.WriteFile(path, turn.Ended(), 0600); err != nil {
						t.Fatal(err)
					}
				})
				err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: "2"})
				if afterText {
					assertDeliveryError(t, err, "partial_input_unconfirmed", 1, false)
					if strings.Contains(err.Error(), "nothing was sent") {
						t.Fatalf("refusal denied the preceding paste: %v", err)
					}
					if calls := e.rec.SocketCallsOf(tmux.CallSendText); len(calls) != 1 || !calls[0].Paste {
						t.Fatalf("paste calls=%+v; want one", calls)
					}
				} else {
					assertOneName(t, err, "ErrCodexTurnErrored")
					e.assertNothingSent(t)
				}
				e.assertNoCalls(t, tmux.CallSendEnter, tmux.CallSendKey)
			})
		}
	}
}

// An injected adapter may supply no journal; keep the normal owned tmux target.
type noJournalDeliveryBackend struct{}

func (noJournalDeliveryBackend) Open(api.InputDeliveryIdentity) (api.InputDeliverySession, error) {
	return nil, nil
}

// b.cqr: tokenless rows cannot own a target session, but an observed error
// still refuses ordinary text before lookup or any input-adapter call.
func TestSendKeysCodexTurnErroredTokenlessRow(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	turn := codexfix.ErroredTurn
	path := codexfix.TempRolloutPath(t, turn.SessionID, true, turn.Ended())
	spec := codexSpec(store.StateWaiting, agentAlive, false)
	spec.SessionID, spec.NoSession = turn.SessionID, true
	spec.Opts = append(spec.Opts, apitest.WithJsonlPath(path), apitest.WithNoLaunchToken())
	r := e.seedRow(t, spec)
	assertOneName(t, e.send(api.SendKeysParams{InstanceID: r.ID, Text: "2"}), "ErrCodexTurnErrored")
	e.assertNothingSent(t)
	e.assertNoCalls(t, tmux.CallLookup)
}

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
