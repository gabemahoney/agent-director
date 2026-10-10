package api_test

// kill_check_test.go covers kill's check after a sent kill (SR-6.1 step 4,
// SR-3.8, SR-6.4): the process wait (zombie, survivors, an unreadable later
// poll), the one follow-up lookup when the agent cannot be checked, never
// both on the current label (this id's own abandoned launch waits after its
// follow-up, b.myx: kill_optin_abandoned_test.go), the pane identity chosen
// over a disagreeing SessionStart pid, and that kill never signals the agent
// process.

import (
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kcRun is one kill call's observations: what kill slept, the virtual time
// it took, and the row's columns before it.
type kcRun struct {
	sleeps  []time.Duration
	elapsed time.Duration
	before  apitest.SpawnColumns
	res     api.KillResult
	err     error
}

// kcKill runs api.Kill on r as e.kill does but with pc as the start-time
// reader and every pause recorded, and fails if the row changed.
func kcKill(t *testing.T, e *killEnv, r killRow, pc api.ProcChecker) kcRun {
	t.Helper()
	run := kcRun{before: e.columns(t, r.ID)}
	prev := e.sleep
	e.sleep = func(d time.Duration) { run.sleeps = append(run.sleeps, d); prev(d) }
	start := e.clock.Now()
	run.res, run.err = api.Kill(e.store, e.rec, pc, e.cfg.EffectiveStartingSession(), e.cfg.EffectiveStoppingWindow(),
		e.cfg.EffectiveKillExitWait(), e.clock.Now, e.sleep, api.KillParams{ClaudeInstanceID: r.ID})
	run.elapsed = e.clock.Now().Sub(start)
	e.assertRowUnchanged(t, r.ID, run.before)
	return run
}

// kcTrail returns r's one ad.kill.called record.
func kcTrail(t *testing.T, id string) map[string]any {
	t.Helper()
	recs := killCalled(t, id)
	if len(recs) != 1 {
		t.Fatalf("ad.kill.called records for %s = %d; want 1", id, len(recs))
	}
	return recs[0]
}

// kcInts is a trail's JSON number list as ints.
func kcInts(v any) []int {
	var out []int
	list, _ := v.([]any)
	for _, n := range list {
		f, _ := n.(float64)
		out = append(out, int(f))
	}
	return out
}

// kcAssertErr checks err's one name and description, or that err is nil when want is nil.
func kcAssertErr(t *testing.T, err error, name string, want *apitest.DescCase) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatalf("kill: %v; want success", err)
		}
		return
	}
	assertOneName(t, err, name)
	apitest.AssertDescription(t, err.Error(), *want)
}

// kcAssertPolls fails unless sleeps is exactly polls pauses of api.KillPollInterval.
func kcAssertPolls(t *testing.T, sleeps []time.Duration, polls int) {
	t.Helper()
	if len(sleeps) != polls || slices.ContainsFunc(sleeps, func(d time.Duration) bool { return d != api.KillPollInterval }) {
		t.Errorf("sleeps = %v; want %d of %v", sleeps, polls, api.KillPollInterval)
	}
}

// TestKillCheckWait covers the checkable path: every reading after the kills
// polls every KillPollInterval up to the exit wait, zombie = gone, no follow-up.
func TestKillCheckWait(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		teammates int
		setup     func(e *killEnv, r killRow)
		polls     int  // pauses taken; -1 for the whole exit wait
		agentRuns bool // the error names the agent
		survivors bool // the error names every teammate
		check     string
	}{
		{"gone at the first reading", 0, func(e *killEnv, r killRow) {
			e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
		}, 0, false, false, "gone"},
		{"zombie at the first reading", 0, func(e *killEnv, r killRow) {
			e.setAfterCall(tmux.CallKillPane, procfix.Zombie(), r.AgentPID)
		}, 0, false, false, "gone"},
		{"gone after three polls", 0, func(e *killEnv, r killRow) {
			e.setAfterWaiting(3*api.KillPollInterval, procfix.Gone(), r.AgentPID)
		}, 3, false, false, "gone"},
		{"zombie during the wait", 0, func(e *killEnv, r killRow) {
			e.setAfterWaiting(2*api.KillPollInterval, procfix.Zombie(), r.AgentPID)
		}, 2, false, false, "gone"},
		{"still running for the whole wait", 0, func(*killEnv, killRow) {}, -1, true, false, "alive"},
		{"unreadable poll after a readable first reading", 0, func(e *killEnv, r killRow) {
			e.setAfterWaiting(api.KillPollInterval, procfix.Unreadable(), r.AgentPID)
		}, -1, true, false, "alive"},
		{"survivor alive through the wait", 1, func(e *killEnv, r killRow) {
			e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
		}, -1, false, true, "gone"},
		{"agent and two survivors alive through the wait", 2, func(*killEnv, killRow) {}, -1, true, true, "alive"},
		{"survivor gone during the wait", 1, func(e *killEnv, r killRow) {
			e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
			e.setAfterWaiting(2*api.KillPollInterval, procfix.Gone(), r.TeammatePIDs...)
		}, 2, false, false, "gone"},
		{"unreadable survivor not waited for", 1, func(e *killEnv, r killRow) {
			e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
			e.pc.Set(r.TeammatePIDs[0], procfix.Unreadable())
		}, 0, false, false, "gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{Teammates: tc.teammates})
			tc.setup(e, r)
			run := kcKill(t, e, r, e.pc)

			exitWait := e.cfg.EffectiveKillExitWait()
			polls := tc.polls
			if polls < 0 {
				polls = int(exitWait / api.KillPollInterval)
			}
			var want *apitest.DescCase
			if tc.agentRuns || tc.survivors {
				p := apitest.KillWaitExpired{InstanceID: r.ID, Name: r.Name,
					Sent: apitest.KillSent{Pane: true, Session: true}, ExitWait: exitWait}
				if tc.agentRuns {
					p.AgentPID = r.AgentPID
				}
				if tc.survivors {
					p.SurvivorPIDs = r.TeammatePIDs
				}
				c := apitest.DescKillWaitExpired(p)
				want = &c
			}
			kcAssertErr(t, run.err, "ErrTmuxKillFailed", want)
			if want == nil && !run.res.KillSent {
				t.Error("KillSent = false; want true")
			}
			e.assertKillCalls(t, killOursCalls...)
			kcAssertPolls(t, run.sleeps, polls)
			q, a := e.cfg.EffectiveQueryTimeout(), e.cfg.EffectiveActionTimeout()
			if wantElapsed := 2*q + 2*a + time.Duration(polls)*api.KillPollInterval; run.elapsed != wantElapsed {
				t.Errorf("virtual time = %v; want %v", run.elapsed, wantElapsed)
			}
			rec := kcTrail(t, r.ID)
			var survivors []int
			if tc.survivors {
				survivors = r.TeammatePIDs
			}
			if rec["process_check"] != tc.check || rec["followup_outcome"] != tmux.TokenNotRun ||
				rec["kill_sent"] != true || !slices.Equal(kcInts(rec["survivor_pids"]), survivors) {
				t.Errorf("trail = %v; want process_check %s, followup_outcome not_run, kill_sent true, survivor_pids %v",
					rec, tc.check, survivors)
			}
		})
	}
}

// TestKillCheckFollowUp covers the not-checkable path: exactly one follow-up
// lookup after the kills, no wait (a running teammate included), and its verdict decides.
func TestKillCheckFollowUp(t *testing.T) {
	t.Parallel()
	scriptFollowUp := func(f tmux.Failure) func(*testing.T, *killEnv, killRow) {
		return func(_ *testing.T, e *killEnv, r killRow) {
			e.rec.AfterCall(tmux.CallKillSession, func(tmuxfix.SocketCall, error) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: f}, tmux.CallLookup)
			})
		}
	}
	bystander := func(t *testing.T, e *killEnv, r killRow) { e.seedBystander(t, r.Socket) }
	cases := []struct {
		name      string
		agent     agentState
		teammates int // still running through the call
		setup     func(t *testing.T, e *killEnv, r killRow)
		followup  string
		errName   string
		want      func(e *killEnv, r killRow) apitest.DescCase
	}{
		{"unreadable agent, follow-up gone", agentUnreadable, 0, bystander, "gone", "", nil},
		// On the current label the follow-up alone decides: no wait for the teammate (b.myx).
		{"unreadable agent, a teammate outlives the kills, follow-up gone", agentUnreadable, 1, bystander, "gone", "", nil},
		{"no agent recorded, follow-up gone", agentNotRecorded, 0, func(_ *testing.T, e *killEnv, r killRow) {
			e.serverExitsWhenEmpty(r.Socket)
		}, "gone", "", nil},
		{"follow-up leftover", agentUnreadable, 0, func(_ *testing.T, e *killEnv, r killRow) {
			e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "old-" + uuid.NewString()[:8], Label: r.old()})
		}, "leftover", "", nil},
		// SR-20.6: formerly TestKillSwallowsTmuxFailure; a failed kill is left to
		// the follow-up: Ours is ErrTmuxKillFailed, Gone (the next row) success.
		{"follow-up ours, label still there", agentUnreadable, 0, func(_ *testing.T, e *killEnv, r killRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallKillPane, tmux.CallKillSession)
		}, "ours", "ErrTmuxKillFailed", func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescKillUncheckable(r.ID, r.Name, apitest.KillSent{Pane: true, Session: true})
		}},
		{"kills report failure but take effect, follow-up gone", agentUnreadable, 0, func(t *testing.T, e *killEnv, r killRow) {
			bystander(t, e, r)
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Applied: true},
				tmux.CallKillPane, tmux.CallKillSession)
		}, "gone", "", nil},
		{"follow-up unreadable", agentUnreadable, 0, func(t *testing.T, e *killEnv, r killRow) {
			bystander(t, e, r)
			scriptFollowUp(tmux.FailTimeout)(t, e, r)
		}, "cant_tell", "ErrTmuxUnresponsive", func(e *killEnv, r killRow) apitest.DescCase {
			return apitest.DescKillFollowUpUnresponsive(apitest.KillFollowUp{Name: r.Name, Timeout: e.cfg.EffectiveQueryTimeout()})
		}},
		{"follow-up tmux not run", agentUnreadable, 0, scriptFollowUp(tmux.FailUnavailable), "tmux_unavailable",
			"ErrTmuxNotAvailable", func(*killEnv, killRow) apitest.DescCase { return apitest.DescTmuxNotRun().AfterKillSent() }},
		{"follow-up socket permission", agentUnreadable, 0, scriptFollowUp(tmux.FailSocketDenied), "tmux_unavailable",
			"ErrTmuxNotAvailable", func(_ *killEnv, r killRow) apitest.DescCase {
				return apitest.DescSocketPermission(r.Socket).AfterKillSent()
			}},
		// b.47f: the recorded server stays up with no session (tmux's exit-empty off).
		{"follow-up gone, the recorded server left with no session", agentUnreadable, 0,
			func(*testing.T, *killEnv, killRow) {}, "gone", "", nil},
		{"follow-up different server, the socket rebound to an empty server", agentUnreadable, 0,
			func(_ *testing.T, e *killEnv, r killRow) {
				e.rec.AfterCall(tmux.CallKillSession, func(tmuxfix.SocketCall, error) {
					e.rec.RebindServer(r.Socket, tmuxfix.Server{})
					e.syncServers()
				})
			}, "different_server", "ErrTmuxNotAvailable",
			func(_ *killEnv, r killRow) apitest.DescCase { return apitest.DescDifferentServer(r.ID).AfterKillSent() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{Agent: tc.agent, Teammates: tc.teammates})
			tc.setup(t, e, r)
			run := kcKill(t, e, r, e.pc)

			var want *apitest.DescCase
			if tc.want != nil {
				c := tc.want(e, r)
				want = &c
			}
			kcAssertErr(t, run.err, tc.errName, want)
			if want == nil && !run.res.KillSent {
				t.Error("KillSent = false; want true")
			}
			// A row with no pane pid recorded has no pane of its own to kill: the session kill only.
			calls, kills, check := append(slices.Clone(killOursCalls), tmux.CallLookup), 2, "unreadable"
			if tc.agent == agentNotRecorded {
				calls, kills, check = slices.Delete(calls, 2, 3), 1, "not_recorded"
			}
			e.assertKillCalls(t, calls...)
			q, a := e.cfg.EffectiveQueryTimeout(), e.cfg.EffectiveActionTimeout()
			if wantElapsed := 3*q + time.Duration(kills)*a; len(run.sleeps) != 0 || run.elapsed != wantElapsed {
				t.Errorf("sleeps = %v, virtual time = %v; want no sleep and %v", run.sleeps, run.elapsed, wantElapsed)
			}
			if rec := kcTrail(t, r.ID); rec["process_check"] != check || rec["followup_outcome"] != tc.followup ||
				rec["kill_sent"] != true || len(kcInts(rec["survivor_pids"])) != 0 {
				t.Errorf("trail = %v; want process_check %s, followup_outcome %s, kill_sent true, no survivor_pids",
					rec, check, tc.followup)
			}
		})
	}
}

// TestKillCheckPaneIdentityChosen: when the SessionStart pid differs from the
// pane pid, the pane process is waited for and no disagree record is written.
func TestKillCheckPaneIdentityChosen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		paneGone  bool // the pane process goes at the session kill
		sessStart procfix.Process
	}{
		{"SessionStart pid alive, pane pid gone", true, procfix.Alive(apitest.LinuxProcStarttime)},
		{"SessionStart pid gone, pane pid alive", false, procfix.Gone()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			ssPID := e.newPID()
			r := e.seedRow(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithPID(ssPID)}})
			panePID := r.Spawn.Identity.PanePID
			e.pc.Set(ssPID, tc.sessStart)
			e.pc.Set(panePID, procfix.Alive(r.Spawn.Identity.PaneStarttime))
			if tc.paneGone {
				e.setAfterCall(tmux.CallKillSession, procfix.Gone(), panePID)
			}
			run := kcKill(t, e, r, e.pc)

			var want *apitest.DescCase
			if !tc.paneGone {
				c := apitest.DescKillWaitExpired(apitest.KillWaitExpired{InstanceID: r.ID, Name: r.Name,
					Sent: apitest.KillSent{Pane: true, Session: true}, ExitWait: e.cfg.EffectiveKillExitWait(),
					AgentPID: panePID})
				c.Forbid = append(c.Forbid, "pid "+strconv.Itoa(ssPID))
				want = &c
			}
			kcAssertErr(t, run.err, "ErrTmuxKillFailed", want)
			e.assertKillCalls(t, killOursCalls...)
			if rec := kcTrail(t, r.ID); rec["agent_pid"] != float64(panePID) {
				t.Errorf("trail agent_pid = %v; want the pane pid %d", rec["agent_pid"], panePID)
			}
			if d := killDisagrees(t, r.ID); len(d) != 0 {
				t.Errorf("ad.provenance.disagree records = %v; want none", d)
			}
		})
	}
}

// TestKillCheckNeverSignals: with the production start-time reader and a real
// child as the agent, a Recorder kill ends in ErrTmuxKillFailed and the child still runs.
func TestKillCheckNeverSignals(t *testing.T) {
	t.Parallel()
	pc := probe.NewProcChecker()
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})
	pid := child.Process.Pid
	start, alive, known := pc.StartTime(pid)
	if !alive || !known {
		t.Skipf("the start-time reader cannot read a child here: (%q, %v, %v)", start, alive, known)
	}
	e := newKillEnv(t)
	li := store.LaunchIdentity{Token: strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		Socket: apitest.TestSocket, ServerPID: killServerPID, ServerStart: killServerStart,
		ServerStarttime: apitest.LinuxProcStarttime, PaneID: apitest.TestPaneID, PanePID: pid, PaneStarttime: start}
	r := e.seedRow(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithLaunchIdentity(li),
		apitest.WithPID(pid), apitest.WithProcStarttime(start)}})
	run := kcKill(t, e, r, pc)

	want := apitest.DescKillWaitExpired(apitest.KillWaitExpired{InstanceID: r.ID, Name: r.Name,
		Sent: apitest.KillSent{Pane: true, Session: true}, ExitWait: e.cfg.EffectiveKillExitWait(), AgentPID: pid})
	kcAssertErr(t, run.err, "ErrTmuxKillFailed", &want)
	if s, a, k := pc.StartTime(pid); s != start || !a || !k {
		t.Errorf("child after kill: StartTime = (%q, %v, %v); want still running with %q", s, a, k, start)
	}
}
