package api_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// AC-CFG-02 (SR-2.4, SR-4.1, SR-13.1, SR-20.6): api.New hands the [tmux]
// timeouts and pipe-close wait to the production client, driven against
// test/fake-tmux with per-socket hang and pipe-holding injections.

const (
	// callDeadline bounds every client call, so a wiring bug cannot hang the run.
	callDeadline = 15 * time.Second
	// timingSlack is the stated tolerance above an expected duration.
	timingSlack = time.Second
	// pipeHold is how long a pipe-holding child keeps the pipes open: well
	// past every bound asserted here.
	pipeHold = 5 * time.Second
)

// timeoutClass is one representative call of a timeout class.
type timeoutClass struct {
	name string
	call tmux.Call
	key  config.TmuxKey
	def  time.Duration
	run  func(c *tmux.Client, socket string) error
}

// timeoutClasses returns the query, action and create representatives.
func timeoutClasses() []timeoutClass {
	d := config.Tmux{}
	return []timeoutClass{
		{"query", tmux.CallLookup, config.TmuxQueryTimeoutMs, d.EffectiveQueryTimeout(),
			func(c *tmux.Client, s string) error { _, err := c.Lookup(s); return err }},
		{"action", tmux.CallKillSession, config.TmuxActionTimeoutMs, d.EffectiveActionTimeout(),
			func(c *tmux.Client, s string) error { return c.KillSessionID(s, "$0") }},
		{"create", tmux.CallCreate, config.TmuxCreateTimeoutMs, d.EffectiveCreateTimeout(),
			func(c *tmux.Client, s string) error {
				_, err := c.NewSession(s, "w4", os.TempDir(), nil, []string{"true"}, "tok", "w4-id", "sid")
				return err
			}},
	}
}

// newFakeTmuxClient builds a Client with api.New from a config written with
// settings, against the shared fake, and returns its production tmux client.
func newFakeTmuxClient(t *testing.T, settings ...apitest.TmuxSetting) *tmux.Client {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath, settings...)
	client, err := api.New(api.Options{
		ConfigPath:      cfgPath,
		StorePath:       filepath.Join(dir, "store", "state.db"),
		CreateIfMissing: true,
		TmuxCommand:     faketmuxfix.Binary(t),
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	tc, ok := api.TmuxClientOf(client).(*tmux.Client)
	if !ok {
		t.Fatalf("api.New built tmux client %T, want *tmux.Client", api.TmuxClientOf(client))
	}
	return tc
}

// timedCall runs call under callDeadline and returns its duration and error.
func timedCall(t *testing.T, call func() error) (time.Duration, error) {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- call() }()
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(callDeadline):
		t.Fatalf("call did not return within %v", callDeadline)
		return 0, nil
	}
}

// seconds renders d as the timeout error states it ("0.3 s").
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + " s"
}

// TestTmuxTimeoutsApplied: a hung call reports its own class's configured
// timeout (or the default when unset), in the CallError and its message.
func TestTmuxTimeoutsApplied(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	withTempHome(t)
	cases := []struct {
		name string
		ms   map[config.TmuxKey]int64 // nil: no [tmux] settings
		only tmux.Call                // "" runs every class
	}{
		{name: "all 300", ms: map[config.TmuxKey]int64{
			config.TmuxQueryTimeoutMs: 300, config.TmuxActionTimeoutMs: 300, config.TmuxCreateTimeoutMs: 300}},
		{name: "distinct per class", ms: map[config.TmuxKey]int64{
			config.TmuxQueryTimeoutMs: 300, config.TmuxActionTimeoutMs: 400, config.TmuxCreateTimeoutMs: 500}},
		{name: "defaults", only: tmux.CallLookup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var settings []apitest.TmuxSetting
			for k, v := range tc.ms {
				settings = append(settings, apitest.TmuxInt(k, v))
			}
			client := newFakeTmuxClient(t, settings...)
			for _, cl := range timeoutClasses() {
				if tc.only != "" && cl.call != tc.only {
					continue
				}
				t.Run(cl.name, func(t *testing.T) {
					t.Parallel()
					want := cl.def
					ms, configured := tc.ms[cl.key]
					if configured {
						want = time.Duration(ms) * time.Millisecond
					}
					socket := filepath.Join(t.TempDir(), "sock")
					faketmuxfix.Tables{}.Inject(t, socket, faketmuxfix.Hang(cl.call))

					elapsed, err := timedCall(t, func() error { return cl.run(client, socket) })

					var ce *tmux.CallError
					if !errors.As(err, &ce) {
						t.Fatalf("err = %v (%T), want *tmux.CallError", err, err)
					}
					if ce.Failure != tmux.FailTimeout || ce.Call != cl.call || ce.Timeout != want {
						t.Errorf("CallError{Failure: %v, Call: %q, Timeout: %v}, want {%v, %q, %v}",
							ce.Failure, ce.Call, ce.Timeout, tmux.FailTimeout, cl.call, want)
					}
					wantMsg := "tmux " + string(cl.call) + " failed: no answer within " + seconds(want)
					if got := ce.Error(); got != wantMsg {
						t.Errorf("Error() = %q, want %q", got, wantMsg)
					}
					if elapsed < want || elapsed >= want+timingSlack {
						t.Errorf("returned after %v, want in [%v, %v)", elapsed, want, want+timingSlack)
					}
					if configured && elapsed >= cl.def {
						t.Errorf("returned after %v, want well before the %v default", elapsed, cl.def)
					}
				})
			}
		})
	}
}

// TestTmuxTimeoutsPipeCloseWait: an action call that exits 0 while a child
// holds its pipes returns after the configured pipe-close wait (SR-2.4).
func TestTmuxTimeoutsPipeCloseWait(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	withTempHome(t)
	const waitMs, actionMs = 300, 300
	wait := time.Duration(waitMs) * time.Millisecond
	upper := time.Duration(actionMs)*time.Millisecond + wait + timingSlack
	client := newFakeTmuxClient(t,
		apitest.TmuxInt(config.TmuxPipeCloseWaitMs, waitMs),
		apitest.TmuxInt(config.TmuxActionTimeoutMs, actionMs))

	cases := []struct {
		name string
		call tmux.Call
		run  func(c *tmux.Client, socket, sessionID, paneID string) error
		// wantFail is the SR-2.4 outcome: 0 (no Failure) for success (an action whose
		// output is unused), else the failure of a data call cut short.
		wantFail tmux.Failure
	}{
		{"session kill succeeds", tmux.CallKillSession,
			func(c *tmux.Client, s, sid, _ string) error { return c.KillSessionID(s, sid) }, 0},
		{"capture is cut short", tmux.CallCapture,
			func(c *tmux.Client, s, _, pid string) error { _, err := c.CapturePaneID(s, pid, 10, false); return err },
			tmux.FailUnrecognized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			socket := filepath.Join(t.TempDir(), "sock")
			pane := faketmuxfix.Pane{ID: "%0", PID: os.Getpid()}
			sess := faketmuxfix.Session{ID: "$0", Name: "w4", Created: time.Now().Unix(),
				Panes: []faketmuxfix.Pane{pane}}
			faketmuxfix.Tables{}.Write(t, socket, faketmuxfix.Table{
				Server:     &faketmuxfix.Server{PID: os.Getpid(), Start: time.Now().Unix()},
				Sessions:   []faketmuxfix.Session{sess},
				Injections: []faketmuxfix.Injection{faketmuxfix.HoldPipes(tc.call, pipeHold)},
			})

			elapsed, err := timedCall(t, func() error { return tc.run(client, socket, sess.ID, pane.ID) })

			if tc.wantFail == 0 {
				if err != nil {
					t.Errorf("err = %v, want nil (exit 0 is success for an action)", err)
				}
			} else {
				var ce *tmux.CallError
				if !errors.As(err, &ce) || ce.Failure != tc.wantFail || ce.Call != tc.call {
					t.Errorf("err = %v, want a %v %q CallError", err, tc.wantFail, tc.call)
				}
			}
			if elapsed < wait || elapsed > upper {
				t.Errorf("returned after %v, want in [%v, %v]", elapsed, wait, upper)
			}
		})
	}
}
