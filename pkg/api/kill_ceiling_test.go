package api_test

// kill_ceiling_test.go proves kill's SR-13.2 ceilings in virtual time (the
// Recorder charges every call its full timeout), on a live row and on a
// finished row with the opt-in (SR-6.2), and the SR-20.6 kill_exit_wait_ms
// case through api.New. Nothing waits in real time.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
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
// 3Q + 2A, never both, at the defaults and with Q raised above 2A.
func TestKillCeilingVirtualTime(t *testing.T) {
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
	kills := []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession}
	paths := []struct {
		name      string
		spec      killRowSpec
		checkable bool
	}{
		{"path i, agent alive through the wait", killRowSpec{}, true},
		{"path i, agent and a teammate alive through the wait", killRowSpec{Teammates: 1}, true},
		{"path ii, agent unreadable", killRowSpec{Agent: agentUnreadable}, false},
	}
	for _, qc := range []struct {
		name string
		q    time.Duration
	}{{"default Q", q}, {"Q above 2A", raised}} {
		for _, pc := range paths {
			t.Run(qc.name+"/"+pc.name, func(t *testing.T) {
				e := newKillEnv(t)
				e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: qc.q})
				r := e.seedRow(t, pc.spec)
				e.seedBystander(t, r.Socket)
				slept := ceilSleeps(e)
				start := e.clock.Now()
				res, err := e.kill(r.ID)
				elapsed := e.clock.Now().Sub(start)
				if pc.checkable {
					if want := 2*qc.q + 2*a + ex; elapsed != want {
						t.Errorf("virtual time = %v; want 2Q + 2A + E = %v", elapsed, want)
					}
					if *slept != ex {
						t.Errorf("waited %v; want the whole kill exit wait %v", *slept, ex)
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
				if want := 3*qc.q + 2*a; elapsed != want {
					t.Errorf("virtual time = %v; want 3Q + 2A = %v", elapsed, want)
				}
				if *slept != 0 {
					t.Errorf("waited %v on the follow-up path; want no wait", *slept)
				}
				if err != nil || !res.KillSent {
					t.Fatalf("Kill = %+v, %v; want kill_sent true, nil", res, err)
				}
				e.assertKillCalls(t, append(kills, tmux.CallLookup)...)
			})
		}
	}
}

// TestKillIncludeFinishedCeilingVirtualTime: with the opt-in, an ended or
// missing row's reported-in session past both is charged 2Q + 2A + E on path (i), 3Q + 2A on path (ii), never both.
func TestKillIncludeFinishedCeilingVirtualTime(t *testing.T) {
	q, a, ex := ceilDefaults()
	kills := []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession}
	paths := []struct {
		name  string
		agent agentState
	}{{"path i, agent alive through the wait", agentAlive}, {"path ii, agent unreadable", agentUnreadable}}
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		for _, p := range paths {
			t.Run(state+"/"+p.name, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedStarting(t, startingRow{state: state, endedAgo: defWindow, agent: p.agent, age: defWindow + defBound})
				slept := ceilSleeps(e)
				start := e.clock.Now()
				res, err := e.killOptIn(r.ID)
				elapsed := e.clock.Now().Sub(start)
				if p.agent == agentAlive {
					if want := 2*q + 2*a + ex; elapsed != want || *slept != ex {
						t.Errorf("virtual time = %v, waited %v; want 2Q + 2A + E = %v with the whole wait %v", elapsed, *slept, want, ex)
					}
					assertOneName(t, err, "ErrTmuxKillFailed")
					apitest.AssertDescription(t, err.Error(), apitest.DescKillWaitExpired(apitest.KillWaitExpired{
						InstanceID: r.ID, Name: r.Name, Sent: apitest.KillSent{Pane: true, Session: true},
						ExitWait: ex, AgentPID: r.AgentPID}))
					e.assertKillCalls(t, kills...)
					return
				}
				if want := 3*q + 2*a; elapsed != want || *slept != 0 {
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
