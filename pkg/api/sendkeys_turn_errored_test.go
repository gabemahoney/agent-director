package api_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// erroredTurnSendEnv adds a Codex row (waiting unless initialState is given)
// and its captured errored rollout, without any permission request.
func erroredTurnSendEnv(t *testing.T, relayOn bool, initialState ...string) (*killEnv, killRow, string) {
	t.Helper()
	path := codexfix.TempRolloutPath(t, codexfix.ErroredTurn.SessionID, true, codexfix.ErroredTurn.Ended())
	e := newKillEnv(t)
	e.rowAgent = store.AgentCodex
	state := store.StateWaiting
	if len(initialState) > 0 {
		state = initialState[0]
	}
	r := e.seedRow(t, killRowSpec{State: state, RelayOn: relayOn,
		SessionID: codexfix.ErroredTurn.SessionID, Opts: []apitest.SpawnOption{apitest.WithJsonlPath(path)}})
	return e, r, path
}

// b.cqr: an errored turn holds ordinary text before any input, regardless
// of relay mode or allow-pending, including an actual pending row.
func TestSendKeysCodexTurnErroredRefusesOrdinaryText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state                 string
		relayOn, allowPending bool
	}{
		{store.StateWaiting, false, false},
		{store.StateWaiting, false, true},
		{store.StateWaiting, true, false},
		{store.StateWaiting, true, true},
		{store.StatePending, false, true},
	} {
		t.Run(fmt.Sprintf("state=%s/relay_on=%v/allow_pending=%v", tc.state, tc.relayOn, tc.allowPending), func(t *testing.T) {
			t.Parallel()
			e, r, _ := erroredTurnSendEnv(t, tc.relayOn, tc.state)
			_, err := e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "2", AllowPending: tc.allowPending})
			if name, _ := errnames.Classify(err); name != "ErrCodexTurnErrored" {
				t.Errorf("err_name = %q (%v); want ErrCodexTurnErrored", name, err)
			}
			e.assertNothingSent(t)
		})
	}
}

// b.cqr: key, no-enter text and Enter-only input pass the error hold, send
// exactly the requested input, and leave ordinary text held and the mark true.
func TestSendKeysCodexTurnErroredManualInputDoesNotClear(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		params api.SendKeysParams
	}{
		{"literal key", api.SendKeysParams{Key: "2"}},
		{"no-enter text", api.SendKeysParams{Text: "draft", NoEnter: true}},
		{"Enter-only", api.SendKeysParams{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, r, _ := erroredTurnSendEnv(t, true)
			params := tc.params
			params.InstanceID = r.ID
			if _, err := e.sendKeys(params); err != nil {
				t.Fatalf("manual send: %v", err)
			}
			text := params.Text
			if params.Key != "" {
				text = params.Key
			}
			if text == "" {
				e.assertEnterOnly(t, r.Socket, r.Spawn.Identity.PaneID)
			} else {
				assertTextSent(t, e.rec, r.Socket, r.Spawn.Identity.PaneID, text)
				e.assertNoCalls(t, tmux.CallSendKey, tmux.CallSendEnter)
				if e.rec.SocketCallsOf(tmux.CallSendText)[0].Paste {
					t.Error("manual text was pasted; want literal input without Enter")
				}
			}

			c, _ := e.client(t)
			got, err := c.Get(r.ID)
			if err != nil || got.LastTurnErrored == nil || !*got.LastTurnErrored {
				t.Fatalf("after manual input: last_turn_errored=%v err=%v; want true", got.LastTurnErrored, err)
			}
			e.rec.Reset()
			_, err = e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "2"})
			if name, _ := errnames.Classify(err); name != "ErrCodexTurnErrored" {
				t.Errorf("ordinary send after manual input: err_name = %q (%v); want ErrCodexTurnErrored", name, err)
			}
			e.assertNothingSent(t)
		})
	}
}

// b.cqr: inspected intentional text passes the error hold; a stale pane hash
// retains ErrPaneChanged and dispatches no input.
func TestSendKeysCodexTurnErroredPaneHash(t *testing.T) {
	t.Parallel()
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%v", stale), func(t *testing.T) {
			t.Parallel()
			e, r, _ := erroredTurnSendEnv(t, true)
			e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "caller-inspected pane\n")
			pane, err := e.readPaneClient(t, api.ReadPaneParams{InstanceID: r.ID})
			if err != nil {
				t.Fatalf("read-pane: %v", err)
			}
			if stale {
				e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, "changed pane\n")
			}
			e.rec.Reset()
			_, err = e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "2", ExpectPaneSHA256: pane.PaneSHA256})
			if stale {
				if name, _ := errnames.Classify(err); name != "ErrPaneChanged" {
					t.Errorf("err_name = %q (%v); want ErrPaneChanged", name, err)
				}
				e.assertNothingSent(t)
				return
			}
			if err != nil {
				t.Fatalf("inspected text send: %v", err)
			}
			assertTextSent(t, e.rec, r.Socket, r.Spawn.Identity.PaneID, "2")
			assertEnterSent(t, e.rec, r.Socket, r.Spawn.Identity.PaneID)
			e.assertNoCalls(t, tmux.CallSendEnter, callClearLine)
		})
	}
}

// b.cqr: the next rollout task_started clears the hold without requiring a
// UserPromptSubmit hook; the same ordinary send is then delivered once.
func TestSendKeysCodexTurnErroredNextStartAdmitsText(t *testing.T) {
	t.Parallel()
	e, r, path := erroredTurnSendEnv(t, false)
	_, err := e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "2"})
	if name, _ := errnames.Classify(err); name != "ErrCodexTurnErrored" {
		t.Errorf("before next start: err_name = %q (%v); want ErrCodexTurnErrored", name, err)
	}
	e.assertNothingSent(t)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(nextTaskStarted(t))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append next task_started: write=%v close=%v", writeErr, closeErr)
	}
	e.rec.Reset()
	if _, err := e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "2"}); err != nil {
		t.Fatalf("ordinary send after next start: %v", err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "2")
}
