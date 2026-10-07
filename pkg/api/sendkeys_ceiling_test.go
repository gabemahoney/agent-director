package api_test

// sendkeys_ceiling_test.go proves send-keys' SR-13.2 ceiling in virtual time
// (the Recorder charges every call its full timeout): the longest path is
// 3Q + 2A, a text timeout 2Q + A, an Enter timeout 2Q + 2A. Nothing waits in real time.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSendKeysCeilingVirtualTime: a failed Enter whose follow-up finds Ours
// charges 3Q + 2A in five calls; the timeouts charge less, with no follow-up.
func TestSendKeysCeilingVirtualTime(t *testing.T) {
	t.Parallel()
	q, a, _ := ceilDefaults()
	if got := 3*q + 2*a; got != 8500*time.Millisecond {
		t.Fatalf("3Q + 2A at the defaults = %v; SR-13.2 says 8.5 s", got)
	}
	raised := config.Tmux{QueryTimeoutMs: 2*config.DefaultActionTimeoutMs + config.DefaultQueryTimeoutMs,
		ActionTimeoutMs: config.DefaultActionTimeoutMs}
	settings := []struct {
		name   string
		q, a   time.Duration
		config []apitest.TmuxSetting // also written for api.New: a config with Q above 2A loads
	}{
		{"default Q", q, a, nil},
		{"Q above 2A", raised.EffectiveQueryTimeout(), raised.EffectiveActionTimeout(), []apitest.TmuxSetting{
			apitest.TmuxInt(config.TmuxQueryTimeoutMs, raised.QueryTimeoutMs),
			apitest.TmuxInt(config.TmuxActionTimeoutMs, raised.ActionTimeoutMs)}},
	}
	if s := settings[1]; s.q <= 2*s.a {
		t.Fatalf("raised Q = %v; want above 2A = %v", s.q, 2*s.a)
	}
	paths := []struct {
		name    string
		call    tmux.Call
		failure tmux.Failure
		calls   []tmux.Call
		want    func(q, a time.Duration) time.Duration
		desc    func(a time.Duration) apitest.DescCase
	}{
		{"Enter fails, follow-up finds Ours", tmux.CallSendEnter, tmux.FailUnrecognized, withFollowUp(paneSendCalls),
			func(q, a time.Duration) time.Duration { return 3*q + 2*a },
			func(time.Duration) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallSendEnter, "").AfterEnterFailed(apitest.PaneSendKeys)
			}},
		{"text times out", tmux.CallSendText, tmux.FailTimeout, paneTextCalls,
			func(q, a time.Duration) time.Duration { return 2*q + a },
			func(a time.Duration) apitest.DescCase {
				return apitest.DescKeysTimeout(apitest.PaneSendKeys, tmux.CallSendText, a)
			}},
		{"Enter times out", tmux.CallSendEnter, tmux.FailTimeout, paneSendCalls,
			func(q, a time.Duration) time.Duration { return 2*q + 2*a },
			func(a time.Duration) apitest.DescCase {
				return apitest.DescKeysTimeout(apitest.PaneSendKeys, tmux.CallSendEnter, a)
			}},
	}
	for _, sc := range settings {
		for _, pc := range paths {
			t.Run(sc.name+"/"+pc.name, func(t *testing.T) {
				e := newKillEnv(t)
				e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: sc.q, Action: sc.a})
				r := e.seedRow(t, killRowSpec{})
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: pc.failure}, pc.call)
				start := e.clock.Now()
				res, _, err := e.sendKeysClient(t, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hello"}, sc.config...)
				elapsed, want := e.clock.Now().Sub(start), pc.want(sc.q, sc.a)
				if elapsed != want {
					t.Errorf("virtual time = %v; want %v", elapsed, want)
				}
				if ceiling := 3*sc.q + 2*sc.a; elapsed > ceiling {
					t.Errorf("virtual time = %v; above the 3Q + 2A ceiling %v", elapsed, ceiling)
				}
				if !errors.Is(err, api.ErrTmuxUnresponsive) {
					t.Fatalf("SendKeys = %+v, %v; want ErrTmuxUnresponsive", res, err)
				}
				apitest.AssertDescription(t, err.Error(), pc.desc(sc.a))
				e.assertPaneCalls(t, pc.calls...)
				e.assertTextSent(t, r.Socket, r.Spawn.Identity.PaneID, "hello")
			})
		}
	}
}
