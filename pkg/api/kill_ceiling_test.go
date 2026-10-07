package api_test

// kill_ceiling_test.go proves the SR-13.2 ceilings in virtual time (the
// Recorder charges every call its full timeout and no pipe-close wait W, so a
// ceiling's W terms drop out here): kill's on a live row and on a finished row
// with the opt-in (SR-6.2), with the SR-20.6 kill_exit_wait_ms case through
// api.New; read-pane's 3Q + A (SR-7.5), send-keys' 3Q + 2A and pause's 3Q + 3A
// (its line clear before /exit is one more action, b.9o4; no path here enters
// its wait). Nothing waits in real time.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ceilDefaults returns the default query timeout Q, action timeout A and kill exit wait E.
func ceilDefaults() (q, a, e time.Duration) {
	d := config.Default().Tmux
	return d.EffectiveQueryTimeout(), d.EffectiveActionTimeout(), d.EffectiveKillExitWait()
}

// ceilSleeps wraps e.sleep and returns the total of every pause kill takes.
func ceilSleeps(e *killEnv) *time.Duration {
	total, prev := new(time.Duration), e.sleep
	e.sleep = func(d time.Duration) { *total += d; prev(d) }
	return total
}

// TestKillCeilingVirtualTime: path (i) charges 2Q + 2A + E and path (ii)
// 3Q + 2A, never both, at the defaults and with Q raised above 2A, on a live
// row and with the opt-in on an ended row's reported-in session past both.
func TestKillCeilingVirtualTime(t *testing.T) {
	t.Parallel()
	q, a, ex := ceilDefaults()
	if got := 2*q + 2*a + ex; got != 12*time.Second {
		t.Fatalf("2Q + 2A + E at the defaults = %v; SR-13.2 says 12 s", got)
	}
	if got := 3*q + 2*a; got != 8500*time.Millisecond {
		t.Fatalf("3Q + 2A at the defaults = %v; SR-13.2 says 8.5 s", got)
	}
	raised := 2*a + ex // above 2A and above E, so path (ii) is the larger
	if 3*raised+2*a <= 2*raised+2*a+ex {
		t.Fatalf("Q = %v does not make path (ii) the larger", raised)
	}
	finished := func(a agentState) *startingRow {
		return &startingRow{state: store.StateEnded, endedAgo: defWindow, agent: a, age: defWindow + defBound}
	}
	paths := []struct {
		name      string
		spec      killRowSpec
		optIn     *startingRow // the opt-in on this finished row
		checkable bool
	}{
		{"path i, agent alive through the wait", killRowSpec{}, nil, true},
		{"path ii, agent unreadable", killRowSpec{Agent: agentUnreadable}, nil, false},
		{"opt-in, path i, agent alive through the wait", killRowSpec{}, finished(agentAlive), true},
		{"opt-in, path ii, agent unreadable", killRowSpec{}, finished(agentUnreadable), false},
	}
	kills := []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession}
	for _, qc := range []struct {
		name string
		q    time.Duration
	}{{"default Q", q}, {"Q above 2A", raised}} {
		for _, pc := range paths {
			t.Run(qc.name+"/"+pc.name, func(t *testing.T) {
				e := newKillEnv(t)
				e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: qc.q})
				kill := e.kill
				var r killRow
				if pc.optIn != nil {
					r, kill = e.seedStarting(t, *pc.optIn).killRow, e.killOptIn
				} else {
					r = e.seedRow(t, pc.spec)
					e.seedBystander(t, r.Socket)
				}
				slept := ceilSleeps(e)
				start := e.clock.Now()
				res, err := kill(r.ID)
				elapsed := e.clock.Now().Sub(start)
				if pc.checkable {
					if want := 2*qc.q + 2*a + ex; elapsed != want || *slept != ex {
						t.Errorf("virtual time = %v, waited %v; want 2Q + 2A + E = %v with the whole kill exit wait %v",
							elapsed, *slept, want, ex)
					}
					if !errors.Is(err, api.ErrTmuxKillFailed) {
						t.Fatalf("err = %v; want ErrTmuxKillFailed", err)
					}
					apitest.AssertDescription(t, err.Error(), apitest.DescKillWaitExpired(apitest.KillWaitExpired{
						InstanceID: r.ID, Name: r.Name, Sent: apitest.KillSent{Pane: true, Session: true},
						ExitWait: ex, AgentPID: r.AgentPID, SurvivorPIDs: r.TeammatePIDs}))
					e.assertKillCalls(t, kills...)
					return
				}
				if want := 3*qc.q + 2*a; elapsed != want || *slept != 0 {
					t.Errorf("virtual time = %v, waited %v; want 3Q + 2A = %v with no wait", elapsed, *slept, want)
				}
				if err != nil || !res.KillSent {
					t.Fatalf("Kill = %+v, %v; want kill_sent true, nil", res, err)
				}
				e.assertKillCalls(t, append(kills, tmux.CallLookup)...)
			})
		}
	}
}

// TestKillCeilingExitWaitSetting: kill_exit_wait_ms 300 from a config file
// through api.New bounds the wait (SR-20.6; AC-CFG-02).
func TestKillCeilingExitWaitSetting(t *testing.T) {
	t.Parallel()
	const exitWaitMs = 300
	exitWait := exitWaitMs * time.Millisecond
	q, a, _ := ceilDefaults()
	cases := []struct {
		name   string
		goneAt time.Duration // 0: the agent outlives the wait
	}{
		{"agent outlives its pane kill", 0},
		{"agent exits at 0.2 s", 200 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			c, _ := e.client(t, apitest.TmuxInt(config.TmuxKillExitWaitMs, exitWaitMs))
			slept := ceilSleeps(e)
			wantWait := exitWait
			if tc.goneAt > 0 {
				e.setAfterWaiting(tc.goneAt, procfix.Gone(), r.AgentPID)
				wantWait = tc.goneAt
			}
			start := e.clock.Now()
			res, err := c.Kill(api.KillParams{ClaudeInstanceID: r.ID})
			if *slept != wantWait {
				t.Errorf("waited %v; want %v", *slept, wantWait)
			}
			if elapsed, want := e.clock.Now().Sub(start), 2*q+2*a+wantWait; elapsed != want {
				t.Errorf("virtual time = %v; want 2Q + 2A + %v = %v", elapsed, wantWait, want)
			}
			e.assertKillCalls(t, tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession)
			if tc.goneAt > 0 {
				if err != nil || !res.KillSent {
					t.Fatalf("Kill = %+v, %v; want kill_sent true, nil", res, err)
				}
				return
			}
			if !errors.Is(err, api.ErrTmuxKillFailed) {
				t.Fatalf("err = %v; want ErrTmuxKillFailed", err)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescKillWaitExpired(apitest.KillWaitExpired{
				InstanceID: r.ID, Name: r.Name, Sent: apitest.KillSent{Pane: true, Session: true},
				ExitWait: exitWait, AgentPID: r.AgentPID}))
		})
	}
}

// TestPaneVerbsCeilingVirtualTime: each verb's longest path (a failed action
// whose follow-up lookup answers or times out) charges its ceiling, and its
// timeouts charge less with no follow-up, at the defaults and with Q raised
// above 2A through api.New's config.
func TestPaneVerbsCeilingVirtualTime(t *testing.T) {
	t.Parallel()
	q, a, _ := ceilDefaults()
	ceilings := map[string]func(q, a time.Duration) time.Duration{
		"read-pane": func(q, a time.Duration) time.Duration { return 3*q + a },
		"send-keys": func(q, a time.Duration) time.Duration { return 3*q + 2*a },
		"pause":     func(q, a time.Duration) time.Duration { return 3*q + 3*a },
	}
	for verb, want := range map[string]time.Duration{"read-pane": 6500 * time.Millisecond,
		"send-keys": 8500 * time.Millisecond, "pause": 10500 * time.Millisecond} {
		if got := ceilings[verb](q, a); got != want {
			t.Fatalf("%s's ceiling at the defaults = %v; want %v", verb, got, want)
		}
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
	followUpTimesOut := skaFollowUpLookup(tmuxfix.Script{Failure: tmux.FailTimeout})
	paths := []struct {
		verb, name string
		call       tmux.Call
		failure    tmux.Failure
		followUp   func(*testing.T, *killEnv, killRow, tmux.Call) // nil: the follow-up lookup answers from the table
		calls      []tmux.Call
		want       func(q, a time.Duration) time.Duration
		desc       func(a time.Duration) apitest.DescCase
	}{
		{"read-pane", "capture fails, follow-up finds Ours", tmux.CallCapture, tmux.FailUnrecognized, nil,
			withFollowUp(paneReadCalls), ceilings["read-pane"],
			func(time.Duration) apitest.DescCase { return apitest.DescUnrecognisedReply(tmux.CallCapture, "") }},
		{"read-pane", "capture times out", tmux.CallCapture, tmux.FailTimeout, nil, paneReadCalls,
			func(q, a time.Duration) time.Duration { return 2*q + a },
			func(a time.Duration) apitest.DescCase { return apitest.DescCallTimeout(tmux.CallCapture, a) }},
		{"send-keys", "Enter fails, follow-up finds Ours", tmux.CallSendEnter, tmux.FailUnrecognized, nil,
			withFollowUp(paneSendCalls), ceilings["send-keys"], func(time.Duration) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallSendEnter, "").AfterEnterFailed(apitest.PaneSendKeys)
			}},
		{"send-keys", "text times out", tmux.CallSendText, tmux.FailTimeout, nil, paneTextCalls,
			func(q, a time.Duration) time.Duration { return 2*q + a },
			func(a time.Duration) apitest.DescCase {
				return apitest.DescKeysTimeout(apitest.PaneSendKeys, tmux.CallSendText, a)
			}},
		{"send-keys", "Enter times out", tmux.CallSendEnter, tmux.FailTimeout, nil, paneSendCalls,
			func(q, a time.Duration) time.Duration { return 2*q + 2*a },
			func(a time.Duration) apitest.DescCase {
				return apitest.DescKeysTimeout(apitest.PaneSendKeys, tmux.CallSendEnter, a)
			}},
		{"pause", "Enter fails, follow-up lookup times out", tmux.CallSendEnter, tmux.FailUnrecognized, followUpTimesOut,
			withFollowUp(pauseSendCalls), ceilings["pause"], func(time.Duration) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallSendEnter, "").AfterEnterFailed(apitest.PanePause)
			}},
		{"pause", "exit text times out", tmux.CallSendText, tmux.FailTimeout, nil, pauseTextCalls,
			func(q, a time.Duration) time.Duration { return 2*q + 2*a },
			func(a time.Duration) apitest.DescCase {
				return apitest.DescKeysTimeout(apitest.PanePause, tmux.CallSendText, a)
			}},
		{"pause", "Enter times out", tmux.CallSendEnter, tmux.FailTimeout, nil, pauseSendCalls,
			func(q, a time.Duration) time.Duration { return 2*q + 3*a },
			func(a time.Duration) apitest.DescCase {
				return apitest.DescKeysTimeout(apitest.PanePause, tmux.CallSendEnter, a)
			}},
	}
	for _, sc := range settings {
		for _, pc := range paths {
			t.Run(sc.name+"/"+pc.verb+"/"+pc.name, func(t *testing.T) {
				e := newKillEnv(t)
				e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: sc.q, Action: sc.a})
				r := e.seedRow(t, killRowSpec{})
				pane := r.Spawn.Identity.PaneID
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: pc.failure}, pc.call)
				if pc.followUp != nil {
					pc.followUp(t, e, r, pc.call)
				}
				start := e.clock.Now()
				var err error
				switch pc.verb {
				case "read-pane":
					_, err = e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID}, sc.config...)
				case "send-keys":
					_, _, err = e.sendKeysClient(t, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hello"}, sc.config...)
				default:
					_, _, err = e.pauseClient(t, pauseParams(r), sc.config...)
				}
				elapsed, want := e.clock.Now().Sub(start), pc.want(sc.q, sc.a)
				if elapsed != want {
					t.Errorf("virtual time = %v; want %v", elapsed, want)
				}
				if ceiling := ceilings[pc.verb](sc.q, sc.a); elapsed > ceiling {
					t.Errorf("virtual time = %v; above the ceiling %v", elapsed, ceiling)
				}
				if !errors.Is(err, api.ErrTmuxUnresponsive) {
					t.Fatalf("%s = %v; want ErrTmuxUnresponsive", pc.verb, err)
				}
				apitest.AssertDescription(t, err.Error(), pc.desc(sc.a), r.Token, r.StoreID)
				e.assertPaneCalls(t, pc.calls...)
				switch pc.verb {
				case "read-pane":
					e.assertCaptured(t, pane, api.DefaultReadPaneLines, false)
				case "send-keys":
					e.assertTextSent(t, r.Socket, pane, "hello")
				default:
					e.assertExitTyped(t, r.Socket, pane)
				}
			})
		}
	}
}
