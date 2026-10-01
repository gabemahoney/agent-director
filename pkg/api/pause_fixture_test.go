package api_test

// pause_fixture_test.go extends the kill and pane-verb fixtures for pause
// (SR-20.2, SR-20.3): its invocations, the wait's poll seam, the /exit
// assertions, the SessionEnd that ends the row after Enter, the failing state
// read and pause's disagree reader. It holds no tests. A later verb that
// acts on the agent's pane must extend this fixture and
// pane_verb_fixture_test.go, not copy them.

import (
	"context"
	"testing"
	"time"

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

// endAfterEnter ends r's row as its own agent (a SessionEnd through
// apitest.ApplyAgentHook, the row's claude_session_id; SR-22.9) when the
// first Enter call returns. The row must record its pane by then (seeded or
// adopted). The test fails unless it ran and applied.
func (e *killEnv) endAfterEnter(t *testing.T, r killRow) {
	t.Helper()
	e.onceAfter(t, tmux.CallSendEnter, "SessionEnd", func() (bool, string) {
		row, err := e.st.GetSpawn(r.ID)
		if err != nil {
			return false, err.Error()
		}
		a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", row.ClaudeSessionID)
		return a.Applied, a.Reason
	})
}

// assertExitDelivered fails unless the calls were exactly paneSendCalls:
// exitText typed into paneID on socket, then one Enter to paneID.
func (e *killEnv) assertExitDelivered(t *testing.T, socket, paneID string) {
	t.Helper()
	e.assertDelivered(t, socket, paneID, exitText)
}

// assertExitTyped fails unless exactly one text call typed exitText into
// paneID on socket, with Enter asked for (the failed-action cases).
func (e *killEnv) assertExitTyped(t *testing.T, socket, paneID string) {
	t.Helper()
	e.assertTextSent(t, socket, paneID, exitText)
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
