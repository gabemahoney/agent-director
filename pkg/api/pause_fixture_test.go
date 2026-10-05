package api_test

// pause_fixture_test.go extends the kill and pane-verb fixtures for pause
// (SR-20.2, SR-20.3): its invocations, its call sequences (the line cleared
// before /exit, b.9o4), the wait's poll seam, the /exit assertions, the
// SessionEnd that ends the row after Enter or at the wait's first sleep, the
// failing state read and pause's disagree reader. It holds no tests. A later
// verb that acts on the agent's pane must extend this fixture and
// pane_verb_fixture_test.go, not copy them.

import (
	"context"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killStore is pause's store too.
var _ api.PauseStore = (*killStore)(nil)

// GetSpawnState counts the read, then returns failStateReads' error, else
// delegates.
func (w *killStore) GetSpawnState(id string) (string, error) {
	w.stateReads++
	if w.stateErr != nil {
		return "", w.stateErr
	}
	return w.st.GetSpawnState(id)
}

// failStateReads makes every later state read (pause's wait polls) return
// err (errInjectedStore when nil).
func (w *killStore) failStateReads(err error) { w.stateErr = orInjected(err) }

// exitText is what pause types into the agent's pane.
const exitText = "/exit"

// pause's call sequences (b.9o4): it clears the agent's input line
// (callClearLine) before typing /exit, so a delivery is lookup, listing,
// line clear, /exit and Enter, a failed line clear types nothing after it,
// and a failed /exit call makes no Enter.
var (
	pauseSendCalls  = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, callClearLine, tmux.CallSendText, tmux.CallSendEnter}
	pauseTextCalls  = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, callClearLine, tmux.CallSendText}
	pauseClearCalls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, callClearLine}
)

// pauseActions are pause's keys calls a failure can hit, with the calls made
// up to and including it: the line clear (nothing typed after it), the /exit
// call (no Enter), or Enter after it.
var pauseActions = []struct {
	call  tmux.Call
	calls []tmux.Call
}{
	{tmux.CallSendKey, pauseClearCalls},
	{tmux.CallSendText, pauseTextCalls},
	{tmux.CallSendEnter, pauseSendCalls},
}

// pauseTimeoutSeconds is the wait e.pause allows: a row nothing ends stays
// waiting and the wait ends in ErrPauseTimeout after this real-time second.
const pauseTimeoutSeconds = 1

// fastPausePolls makes the wait poll every millisecond of real sleep
// (api.SetPauseTestKnobs) until cleanup restores the cadence; not for parallel tests.
func fastPausePolls(t *testing.T) {
	t.Helper()
	interval, sleep := api.PauseTestKnobs()
	api.SetPauseTestKnobs(time.Millisecond, time.Sleep)
	t.Cleanup(func() { api.SetPauseTestKnobs(interval, sleep) })
}

// pauseParams is pause on r.
func pauseParams(r killRow) api.PauseParams { return api.PauseParams{ClaudeInstanceID: r.ID} }

// pause runs the exported api.Pause with a background context, e.store,
// e.rec, e.pc (its server and pane checks) and pauseTimeoutSeconds.
func (e *killEnv) pause(p api.PauseParams) (api.PauseResult, error) {
	return e.pauseWithin(context.Background(), pauseTimeoutSeconds, p)
}

// pauseWithin is pause with ctx and the wait's timeout given.
func (e *killEnv) pauseWithin(ctx context.Context, timeoutSeconds int, p api.PauseParams) (api.PauseResult, error) {
	return api.Pause(ctx, e.store, e.rec, e.pc, timeoutSeconds, p)
}

// pauseRun runs pause with p as a verbRun.
func (e *killEnv) pauseRun(p api.PauseParams) verbRun[api.PauseResult] {
	return runVerb(e, func() (api.PauseResult, error) { return e.pause(p) })
}

// pauseClient runs Client.Pause (background context, config's pause timeout)
// on a new e.client, for the trail or [tmux] config cases; logs is its log.
func (e *killEnv) pauseClient(t *testing.T, p api.PauseParams, settings ...apitest.TmuxSetting) (res api.PauseResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	res, err = c.Pause(context.Background(), p)
	return res, buf.String(), err
}

// endAsAgent ends r's row as its own agent (a SessionEnd through
// apitest.ApplyAgentHook, the row's claude_session_id; SR-22.9) and reports
// whether it applied, and why not. The row must record its pane by then.
func (e *killEnv) endAsAgent(t *testing.T, r killRow) (bool, string) {
	t.Helper()
	row, err := e.st.GetSpawn(r.ID)
	if err != nil {
		return false, err.Error()
	}
	a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", row.ClaudeSessionID)
	return a.Applied, a.Reason
}

// endAfterEnter ends r's row as its own agent (endAsAgent) when the first
// Enter call returns. The test fails unless it ran and applied.
func (e *killEnv) endAfterEnter(t *testing.T, r killRow) {
	t.Helper()
	e.onceAfter(t, tmux.CallSendEnter, "SessionEnd", func() (bool, string) { return e.endAsAgent(t, r) })
}

// endAtFirstWait ends r's row as its own agent (endAsAgent) at the pause
// wait's first sleep (api.SetPauseTestKnobs), so only a wait that sleeps sees
// it; cleanup restores the knobs and fails the test unless it ran and
// applied. Not for parallel tests.
func (e *killEnv) endAtFirstWait(t *testing.T, r killRow) {
	t.Helper()
	interval, sleep := api.PauseTestKnobs()
	ran := false
	api.SetPauseTestKnobs(time.Millisecond, func(d time.Duration) {
		if ran {
			time.Sleep(d)
			return
		}
		ran = true
		if applied, why := e.endAsAgent(t, r); !applied {
			t.Errorf("SessionEnd at the wait's first sleep not applied: %s", why)
		}
	})
	t.Cleanup(func() {
		api.SetPauseTestKnobs(interval, sleep)
		if !ran {
			t.Errorf("SessionEnd at the wait's first sleep never ran: the wait never slept")
		}
	})
}

// assertExitDelivered fails unless the calls were exactly pauseSendCalls:
// paneID's input line cleared on socket (assertLineCleared), exitText typed
// into it, then one Enter to it.
func (e *killEnv) assertExitDelivered(t *testing.T, socket, paneID string) {
	t.Helper()
	e.assertPaneCalls(t, pauseSendCalls...)
	e.assertExitTyped(t, socket, paneID)
	if got := e.rec.SocketCallsOf(tmux.CallSendEnter); len(got) != 1 || got[0].Socket != socket || got[0].Target != paneID {
		t.Errorf("Enter calls = %+v; want one to %s on %s", got, paneID, socket)
	}
}

// assertExitTyped fails unless paneID's input line was cleared on socket
// (assertLineCleared) and exactly one text call then typed exitText into
// it, with Enter asked for (the failed-action cases too).
func (e *killEnv) assertExitTyped(t *testing.T, socket, paneID string) {
	t.Helper()
	e.assertLineCleared(t, socket, paneID)
	e.assertTextSent(t, socket, paneID, exitText)
}

// assertLineCleared fails unless exactly one key send was made, paneClearKey
// to paneID on socket, and it came before every text call (b.9o4).
func (e *killEnv) assertLineCleared(t *testing.T, socket, paneID string) {
	t.Helper()
	var keys []tmuxfix.SocketCall
	textFirst := false
	for _, c := range e.rec.SocketCalls() {
		switch {
		case c.Key != "":
			keys = append(keys, c)
		case c.Call == tmux.CallSendText && len(keys) == 0:
			textFirst = true
		}
	}
	if len(keys) != 1 {
		t.Fatalf("key sends = %+v; want one, %s to %s on %s", keys, paneClearKey, paneID, socket)
	}
	if c := keys[0]; c.Key != paneClearKey || c.Socket != socket || c.Target != paneID {
		t.Errorf("key send %q to %q on %s; want %s to %s on %s", c.Key, c.Target, c.Socket, paneClearKey, paneID, socket)
	}
	if textFirst {
		t.Errorf("a text call came before the line was cleared; want %s first", paneClearKey)
	}
}

// pauseDisagrees returns id's ad.provenance.disagree records written by pause.
func pauseDisagrees(t *testing.T, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range pendTrail(t, "ad.provenance.disagree", id) {
		if l["verb"] == "pause" {
			out = append(out, l)
		}
	}
	return out
}
