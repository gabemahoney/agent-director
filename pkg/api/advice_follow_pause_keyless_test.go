package api_test

// advice_follow_pause_keyless_test.go: literal-follow tests (b.fji) for
// keyless pause's ErrSpawnNotPausable advice (SR-6.4), on pause_keyless_test.go's
// covered-row fixture. They replace keyless pause's process-wide signal hook,
// so all are serial.

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestAdviceFollow_E29_PauseSignalUncertainInspectRetry: E29 "inspect with get
// before retrying" after a signal call failed though the agent got it: get
// reads the row, and the retried pause finds the agent gone and succeeds
// without a second signal.
func TestAdviceFollow_E29_PauseSignalUncertainInspectRetry(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	e := newKeylessEnv(t, killRowSpec{})
	e.answer = func(s kpSignal) error {
		e.pc.Set(s.pid, procfix.Gone())
		return errors.New("operation not permitted")
	}
	err := e.pause(context.Background(), nil, 0)
	adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "inspect with get before retrying")

	if _, err := e.c.Get(e.r.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := e.pause(context.Background(), nil, 0); err != nil {
		t.Fatalf("retried pause: %v; want success", err)
	}
	e.assertSignals(t, 1)
}

// TestAdviceFollow_E30_PauseExitUncertainInspectGet: E30 "inspect with get
// before more input" after the agent did not exit within the budget: get
// reads the row still live, with its Messages still Held.
func TestAdviceFollow_E30_PauseExitUncertainInspectGet(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	e := newKeylessEnv(t, killRowSpec{})
	e.answer = func(kpSignal) error { return nil }
	held := e.held(t)
	err := e.pause(context.Background(), nil, 0)
	adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "inspect with get before more input")

	row, err := e.c.Get(e.r.ID)
	if err != nil || row.State != store.StateWaiting {
		t.Fatalf("get = %+v, %v; want the row still waiting", row, err)
	}
	e.assertKept(t, held)
}

// TestAdviceFollow_E31_PauseIssuerRunningGetRetryOnceExited: E31 "inspect
// with get, and retry pause once that process has exited" after the agent
// exited but an owned helper still runs (Main Agent decision 16): get reads
// the row, and once the helper has exited the retried pause succeeds with no
// second signal.
func TestAdviceFollow_E31_PauseIssuerRunningGetRetryOnceExited(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	e := newKeylessEnv(t, killRowSpec{})
	e.issueHelper(t, kpHelper)
	err := e.pause(context.Background(), nil, 0)
	adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "inspect with get, and retry pause once that process has exited "+
		"(an agent-director hook or delivery job ends on its own within its job budget)")

	if _, err := e.c.Get(e.r.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	e.pc.Set(kpHelper.PID, procfix.Gone())
	if err := e.pause(context.Background(), nil, 0); err != nil {
		t.Fatalf("pause once the helper exited: %v; want success", err)
	}
	e.assertSignals(t, 1)
}

// TestAdviceFollow_E32_PauseLaunchChangedRetry: E32 "nothing was signalled;
// retry pause" after the Launch was replaced just before the signal: the
// retried pause signals the replacement's own agent and succeeds.
func TestAdviceFollow_E32_PauseLaunchChangedRetry(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	e := newKeylessEnv(t, killRowSpec{})
	pid := e.newPID()
	e.pc.Set(pid, procfix.Alive(apitest.LinuxProcStarttime))
	pane := store.LaunchIdentity{Socket: apitest.TestSocket, PaneID: "%4242", PanePID: pid, PaneStarttime: apitest.LinuxProcStarttime}
	st := &kpHookedStore{Store: e.st, once: func() { e.replace(t, pane) }}
	err := e.pause(context.Background(), st, 0)
	adviceAssertAdvice(t, err, api.ErrSpawnNotPausable, "nothing was signalled; retry pause")

	if err := e.pause(context.Background(), nil, 0); err != nil {
		t.Fatalf("retried pause: %v; want success", err)
	}
	if want := []kpSignal{{pid, apitest.LinuxProcStarttime}}; !reflect.DeepEqual(e.sent, want) {
		t.Errorf("signals = %+v; want only the replacement's agent %+v", e.sent, want)
	}
}
