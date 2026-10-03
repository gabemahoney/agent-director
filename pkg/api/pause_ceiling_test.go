package api_test

// pause_ceiling_test.go proves pause's SR-13.2 ceiling in virtual time (the
// Recorder charges every call its full timeout and no pipe-close wait W, so
// a ceiling's W terms drop out here; the configured wait is excluded). The
// line clear before /exit (b.9o4) is one more action call, so pause's
// longest path is 3Q + 3A (3Q + 3A + 6W with W), an /exit timeout 2Q + 2A,
// an Enter timeout 2Q + 3A, and none enters the wait. Nothing waits in real time.

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

// TestPauseCeilingVirtualTime: a failed Enter whose follow-up lookup times out
// charges 3Q + 3A in six calls; the timeouts charge less, with no follow-up.
func TestPauseCeilingVirtualTime(t *testing.T) {
	q, a, _ := ceilDefaults()
	if got := 3*q + 3*a; got != 10500*time.Millisecond {
		t.Fatalf("3Q + 3A at the defaults = %v; want 10.5 s without W", got)
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
		name     string
		call     tmux.Call
		failure  tmux.Failure
		followUp func(*testing.T, *killEnv, killRow, tmux.Call) // nil: the follow-up lookup answers from the table
		calls    []tmux.Call
		want     func(q, a time.Duration) time.Duration
		desc     func(a time.Duration) apitest.DescCase
	}{
		{"Enter fails, follow-up lookup times out", tmux.CallSendEnter, tmux.FailUnrecognized,
			skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailTimeout}), withFollowUp(pauseSendCalls),
			func(q, a time.Duration) time.Duration { return 3*q + 3*a },
			func(time.Duration) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallSendEnter, "").AfterEnterFailed(apitest.PanePause)
			}},
		{"exit text times out", tmux.CallSendText, tmux.FailTimeout, nil, pauseTextCalls,
			func(q, a time.Duration) time.Duration { return 2*q + 2*a },
			func(a time.Duration) apitest.DescCase {
				return apitest.DescKeysTimeout(apitest.PanePause, tmux.CallSendText, a)
			}},
		{"Enter times out", tmux.CallSendEnter, tmux.FailTimeout, nil, pauseSendCalls,
			func(q, a time.Duration) time.Duration { return 2*q + 3*a },
			func(a time.Duration) apitest.DescCase {
				return apitest.DescKeysTimeout(apitest.PanePause, tmux.CallSendEnter, a)
			}},
	}
	for _, sc := range settings {
		for _, pc := range paths {
			t.Run(sc.name+"/"+pc.name, func(t *testing.T) {
				e := newKillEnv(t)
				e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: sc.q, Action: sc.a})
				r := e.seedRow(t, killRowSpec{})
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: pc.failure}, pc.call)
				if pc.followUp != nil {
					pc.followUp(t, e, r, pc.call)
				}
				start := e.clock.Now()
				res, _, err := e.pauseClient(t, pauseParams(r), sc.config...)
				elapsed, want := e.clock.Now().Sub(start), pc.want(sc.q, sc.a)
				if elapsed != want {
					t.Errorf("virtual time = %v; want %v", elapsed, want)
				}
				if ceiling := 3*sc.q + 3*sc.a; elapsed > ceiling {
					t.Errorf("virtual time = %v; above the 3Q + 3A ceiling %v", elapsed, ceiling)
				}
				// The wait would poll a row nothing ends until ErrPauseTimeout.
				if !errors.Is(err, api.ErrTmuxUnresponsive) {
					t.Fatalf("Pause = %+v, %v; want ErrTmuxUnresponsive", res, err)
				}
				apitest.AssertDescription(t, err.Error(), pc.desc(sc.a), r.Token, r.StoreID)
				e.assertPaneCalls(t, pc.calls...)
				e.assertExitTyped(t, r.Socket, r.Spawn.Identity.PaneID)
			})
		}
	}
}
