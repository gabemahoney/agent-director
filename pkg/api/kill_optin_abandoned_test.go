package api_test

// kill_optin_abandoned_test.go: kill's operator-only finished-row opt-in
// (SR-6.5) on this id's own abandoned launch (b.6sa): a finished row whose
// latest launch records a token but no tmux server or pane
// (recordsNoLaunchSession) beside sessions of earlier launches of its id, so
// the lookup is Leftover. While the youngest is younger than the
// starting-session bound the call gets resume's still-starting refusal and
// sends nothing; past it each session's agent pane (the one carrying its
// label's token, wherever it now is) and then the session are killed by id,
// lowest $N first, the youngest's agent is waited for with the other pane
// processes, or, when it cannot be checked, one follow-up lookup must find
// none of the sessions and then the other pane processes are waited for
// (b.myx). The row never changes. A
// finished row that records its launch's server or pane, or no token, keeps
// "never reported in"; a live row keeps kill's own Leftover refusal
// (TestKillPendingBesideLeftover). A real plain spawn's row is
// kill_optin_history_test.go's; following the advice is C9's and HO12's.

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kabSession is a session of an earlier launch of the row's id: its tmux id
// ("" takes the server's next $N), its name's prefix (names list in byte
// order), its age at the rule's instant, its agent pane's process
// (agentNotRecorded: alive, but the pane carries no token) and whether that
// pane moved into an unrelated session (its own one pane then a plain one).
type kabSession struct {
	id, name string
	age      time.Duration
	agent    agentState
	moved    bool
}

// kabSeeded is a kabSession as stored, with its agent pane's pid and id.
type kabSeeded struct {
	tmuxfix.SeedSession
	pid  int
	pane string
}

// seedAbandoned seeds a finished row in state that records a launch token but
// no tmux server, pane or process (recordsNoLaunchSession), a bystander on
// its socket and each of sessions, labelled for the row's id in this store
// with its own new token. Call the opt-in next: ages are at ruleInstant.
func (e *killEnv) seedAbandoned(t *testing.T, state string, sessions ...kabSession) (killRow, []kabSeeded) {
	t.Helper()
	r := e.seedRow(t, killRowSpec{State: state, NoSession: true, NoServerIdentity: true, NoPane: true,
		Agent: agentNotRecorded})
	e.ensureServer(&r)
	e.seedBystander(t, r.Socket)
	at := e.ruleInstant()
	var out []kabSeeded
	for _, s := range sessions {
		token, pid := newToken(), e.newPID()
		e.pc.Set(pid, s.agent.process(apitest.LinuxProcStarttime))
		pane := tmuxfix.SeedPane{PID: pid, AdPane: token}
		if s.agent == agentNotRecorded {
			pane.AdPane = ""
		}
		own, paneID := []tmuxfix.SeedPane{pane}, ""
		if s.moved { // an unlabelled session's second pane; a session keeps at least one pane, so a plain one stays
			pane.Index = 1
			elsewhere := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "elsewhere-" + uuid.NewString()[:8],
				Panes: []tmuxfix.SeedPane{{}, pane}})
			own, paneID = nil, elsewhere.Panes[1].ID
		}
		stored := e.seedOther(t, r.Socket, tmuxfix.SeedSession{ID: s.id, Name: s.name + "abandoned-" + uuid.NewString()[:8],
			Created: at.Add(-s.age).Unix(), Label: tmuxfix.Valid(token, r.ID, r.StoreID), Panes: own})
		if paneID == "" {
			paneID = stored.Panes[0].ID
		}
		out = append(out, kabSeeded{stored, pid, paneID})
	}
	e.syncServers()
	return r, out
}

// kabDesc names sessions as a description does.
func kabDesc(sessions ...kabSeeded) []apitest.DescSession {
	var out []apitest.DescSession
	for _, s := range sessions {
		out = append(out, apitest.DescSession{Name: s.Name, ID: s.ID})
	}
	return out
}

// kabStarting is the still-starting refusal naming sessions.
func kabStarting(e *killEnv, r killRow, sessions ...kabSeeded) apitest.DescCase {
	return apitest.DescAbandonedLaunch(apitest.AbandonedLaunch{InstanceID: r.ID, Sessions: kabDesc(sessions...),
		Bound: e.cfg.EffectiveStartingSession()})
}

// kabForbid is what no description of r's call may carry: the row's and the sessions' tokens, the store id.
func kabForbid(r killRow, sessions []kabSeeded) []string {
	out := []string{r.Token, r.StoreID}
	for _, s := range sessions {
		out = append(out, s.Label.Token)
	}
	return out
}

// kabAssertErr fails unless err is name's (nil when name is "") with desc's description.
func kabAssertErr(t *testing.T, err error, name string, desc apitest.DescCase, forbid []string) {
	t.Helper()
	if name == "" {
		if err != nil {
			t.Fatalf("kill-finished: %v; want success", err)
		}
		return
	}
	assertOneName(t, err, name)
	if err != nil {
		apitest.AssertDescription(t, err.Error(), desc, forbid...)
	}
}

// kabExitAtOwnPaneKill makes each session of sessions marked in exits end its
// pane's process when its own pane is killed.
func kabExitAtOwnPaneKill(e *killEnv, sessions []kabSeeded, exits []bool) {
	byPane := map[string]int{}
	for i, s := range sessions {
		if exits[i] {
			byPane[s.pane] = s.pid
		}
	}
	e.rec.AfterCall(tmux.CallKillPane, func(c tmuxfix.SocketCall, _ error) {
		if pid, ok := byPane[c.Target]; ok {
			e.pc.Set(pid, procfix.Gone())
		}
	})
}

// TestKillIncludeFinishedAbandonedLaunch: one session of an earlier launch,
// still starting, is refused with nothing sent; past the bound its agent pane
// and the session are killed by id, its agent waited for, or, unreadable or
// with no pane carrying its token, one follow-up lookup must find the session
// gone, then its pane processes are waited for; a pane listing that cannot
// answer sends nothing. The row never changes.
func TestKillIncludeFinishedAbandonedLaunch(t *testing.T) {
	t.Parallel()
	sent, sessionOnly := apitest.KillSent{Pane: true, Session: true}, apitest.KillSent{Session: true}
	followUp := append(slices.Clone(seqOurs), tmux.CallLookup)
	kills := func(e *killEnv, r killRow, _ kabSeeded) {
		e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallKillPane, tmux.CallKillSession)
	}
	laterLaunch := func(e *killEnv, r killRow, _ kabSeeded) {
		adviceOnceAfter(e.rec, tmux.CallKillSession, func() {
			e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "later-" + uuid.NewString()[:8],
				Label: tmuxfix.Valid(newToken(), r.ID, r.StoreID)})
		})
	}
	cases := []struct {
		name     string
		session  kabSession
		exitAt   tmux.Call                                // the call whose return ends its pane's process ("": none)
		world    func(e *killEnv, r killRow, s kabSeeded) // more of the world (nil: none)
		errName  string
		desc     func(e *killEnv, r killRow, s kabSeeded) apitest.DescCase
		calls    []tmux.Call
		stays    bool // the session is still listed after the call
		survivor bool // its pane's process is named still running (survivor_pids)
		trail    map[string]any
	}{
		{name: "younger than the bound by 1 s: still starting", session: kabSession{age: defBound - time.Second},
			errName: "ErrTmuxUnresponsive", calls: []tmux.Call{tmux.CallLookup}, stays: true,
			desc:  func(e *killEnv, r killRow, s kabSeeded) apitest.DescCase { return kabStarting(e, r, s) },
			trail: map[string]any{"kill_sent": false, "pane_killed": false, "process_check": "not_run"}},
		{name: "the bound old, its agent exits at its pane kill", session: kabSession{age: defBound},
			exitAt: tmux.CallKillPane, calls: seqOurs, trail: map[string]any{"kill_sent": true, "pane_killed": true,
				"process_check": "gone", "followup_outcome": "not_run"}},
		{name: "its agent outlives the kill exit wait", session: kabSession{age: defBound}, errName: "ErrTmuxKillFailed",
			desc: func(e *killEnv, r killRow, s kabSeeded) apitest.DescCase {
				return apitest.DescKillWaitExpired(apitest.KillWaitExpired{InstanceID: r.ID, Name: r.Name, Sent: sent,
					ExitWait: e.cfg.EffectiveKillExitWait(), AgentPID: s.pid})
			}, calls: seqOurs, trail: map[string]any{"kill_sent": true, "process_check": "alive"}},
		{name: "its agent unreadable, the session gone at the follow-up",
			session: kabSession{age: defBound, agent: agentUnreadable}, calls: followUp,
			trail: map[string]any{"kill_sent": true, "process_check": "unreadable", "followup_outcome": "gone"}},
		{name: "its agent unreadable, the kills time out: the session still listed",
			session: kabSession{age: defBound, agent: agentUnreadable}, world: kills, errName: "ErrTmuxKillFailed",
			desc: func(_ *killEnv, r killRow, _ kabSeeded) apitest.DescCase {
				return apitest.DescKillUncheckable(r.ID, r.Name, sent)
			}, calls: followUp, stays: true,
			trail: map[string]any{"kill_sent": true, "process_check": "unreadable", "followup_outcome": "leftover"}},
		{name: "its agent unreadable, only a later launch's session at the follow-up",
			session: kabSession{age: defBound, agent: agentUnreadable}, world: laterLaunch, calls: followUp,
			trail: map[string]any{"kill_sent": true, "process_check": "unreadable", "followup_outcome": "leftover"}},
		{name: "no pane carries its token, its pane's process exits at the session kill: the follow-up, then the wait",
			session: kabSession{age: defBound, agent: agentNotRecorded}, exitAt: tmux.CallKillSession, calls: seqFollowUp,
			trail: map[string]any{"kill_sent": true, "pane_killed": false, "process_check": "not_recorded",
				"followup_outcome": "gone"}},
		{name: "no pane carries its token, its pane's process outlives the wait (b.myx)",
			session: kabSession{age: defBound, agent: agentNotRecorded}, errName: "ErrTmuxKillFailed",
			desc: func(e *killEnv, r killRow, s kabSeeded) apitest.DescCase {
				return apitest.DescKillWaitExpired(apitest.KillWaitExpired{InstanceID: r.ID, Name: r.Name, Sent: sessionOnly,
					ExitWait: e.cfg.EffectiveKillExitWait(), SurvivorPIDs: []int{s.pid}})
			}, calls: seqFollowUp, survivor: true,
			trail: map[string]any{"kill_sent": true, "pane_killed": false, "process_check": "not_recorded",
				"followup_outcome": "gone"}},
		{name: "the pane listing times out: nothing sent", session: kabSession{age: defBound},
			world: func(e *killEnv, r killRow, _ kabSeeded) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallListPanes)
			}, errName: "ErrTmuxUnresponsive",
			desc: func(e *killEnv, _ killRow, _ kabSeeded) apitest.DescCase {
				return apitest.DescCallTimeout(tmux.CallListPanes, e.cfg.EffectiveQueryTimeout())
			}, calls: []tmux.Call{tmux.CallLookup, tmux.CallListPanes}, stays: true,
			trail: map[string]any{"kill_sent": false, "pane_killed": false, "process_check": "not_run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r, seeded := e.seedAbandoned(t, store.StateEnded, tc.session)
			s := seeded[0]
			if tc.exitAt != "" {
				e.setAfterCall(tc.exitAt, procfix.Gone(), s.pid)
			}
			if tc.world != nil {
				tc.world(e, r, s)
			}
			before, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)

			res, err := e.killOptIn(r.ID)

			var desc apitest.DescCase
			if tc.desc != nil {
				desc = tc.desc(e, r, s)
			}
			kabAssertErr(t, err, tc.errName, desc, kabForbid(r, seeded))
			if sentKill := tc.trail["kill_sent"] == true; tc.errName == "" && res.KillSent != sentKill {
				t.Errorf("kill_sent = %v; want %v", res.KillSent, sentKill)
			}
			e.assertKillCalls(t, tc.calls...)
			if slices.Contains(tc.calls, tmux.CallKillPane) && seqTarget(e, tmux.CallKillPane) != s.pane {
				t.Errorf("pane kill targets %q; want the pane carrying the session's token %q",
					seqTarget(e, tmux.CallKillPane), s.pane)
			}
			if slices.Contains(tc.calls, tmux.CallKillSession) && seqTarget(e, tmux.CallKillSession) != s.ID {
				t.Errorf("session kill targets %q; want %q", seqTarget(e, tmux.CallKillSession), s.ID)
			}
			if !slices.Contains(tc.calls, tmux.CallKillSession) {
				if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
					t.Errorf("sessions changed:\n got %+v\nwant %+v", got, sessions)
				}
			} else if seqHas(e, r.Socket, s.ID) != tc.stays {
				t.Errorf("session %s listed after the call: %t; want %t", s.ID, !tc.stays, tc.stays)
			}
			e.assertRowUnchanged(t, r.ID, before)
			want := map[string]any{"include_finished": true, "lookup_outcome": "leftover", "outcome": "ok"}
			if tc.errName != "" {
				want["outcome"] = tc.errName
			}
			if tc.trail["process_check"] == "alive" || tc.trail["process_check"] == "gone" {
				want["agent_pid"] = float64(s.pid)
			}
			for k, v := range tc.trail {
				want[k] = v
			}
			kolAssertCalled(t, r.ID, want)
			var survivors []int
			if tc.survivor {
				survivors = []int{s.pid}
			}
			if got := kcInts(kcTrail(t, r.ID)["survivor_pids"]); !slices.Equal(got, survivors) {
				t.Errorf("ad.kill.called survivor_pids = %v; want %v", got, survivors)
			}
		})
	}
}

// TestKillIncludeFinishedAbandonedLaunchSessions: sessions of several earlier
// launches are judged by the youngest; past the bound every one is ended,
// lowest $N first whatever the listing order, each agent pane before its
// session. The youngest's agent is the agent; the others' processes, an agent
// pane moved out of its session included, are waited for as other pane
// processes. When the youngest's agent cannot be checked (unreadable, or no
// pane carries its token) the follow-up lookup must find none of the sessions,
// and then those processes are still waited for (b.myx).
func TestKillIncludeFinishedAbandonedLaunchSessions(t *testing.T) {
	t.Parallel()
	// Listed by name they come highest $N first; $30 is the youngest.
	sessions := func(youngest time.Duration, agent agentState, moved bool) []kabSession {
		return []kabSession{{id: "$30", name: "a", age: youngest, agent: agent},
			{id: "$12", name: "b", age: defBound + time.Hour}, {id: "$9", name: "c", age: defBound + 2*time.Hour, moved: moved}}
	}
	sent := apitest.KillSent{Pane: true, Session: true}
	survivor9 := func(e *killEnv, r killRow, s []kabSeeded) apitest.DescCase {
		return apitest.DescKillWaitExpired(apitest.KillWaitExpired{InstanceID: r.ID, Name: r.Name, Sent: sent,
			ExitWait: e.cfg.EffectiveKillExitWait(), SurvivorPIDs: []int{s[2].pid}})
	}
	youngestHangsUp := func(e *killEnv, _ killRow, s []kabSeeded) { // $30's pane process exits at a session kill
		e.setAfterCall(tmux.CallKillSession, procfix.Gone(), s[0].pid)
	}
	cases := []struct {
		name     string
		youngest time.Duration
		agent    agentState // the youngest's agent
		moved    bool       // $9's agent pane moved into an unrelated session
		exits    []bool     // per session, its agent exits at its own pane kill
		world    func(e *killEnv, r killRow, s []kabSeeded)
		errName  string
		desc     func(e *killEnv, r killRow, s []kabSeeded) apitest.DescCase
		survivor bool   // $9's agent is named still running
		followUp string // the follow-up lookup's outcome ("": none made)
		stays    bool   // $9 is still listed after the call
		polls    int    // pauses of a wait that ends early (a survivor: the whole exit wait)
	}{
		{name: "all past the bound: each ended, lowest $N first", youngest: defBound, exits: []bool{true, true, true}},
		{name: "an older session's agent outlives the wait", youngest: defBound, exits: []bool{true, true, false},
			errName: "ErrTmuxKillFailed", survivor: true, desc: survivor9},
		{name: "an older session's agent pane moved into an unrelated session: its agent exits at its pane kill",
			youngest: defBound, moved: true, exits: []bool{true, true, true}},
		{name: "an older session's agent pane moved into an unrelated session: its agent outlives the wait",
			youngest: defBound, moved: true, exits: []bool{true, true, false}, errName: "ErrTmuxKillFailed", survivor: true,
			desc: survivor9},
		{name: "the youngest's agent unreadable, $9's kills time out: $9 still listed at the follow-up", youngest: defBound,
			agent: agentUnreadable, exits: []bool{false, true, false},
			world: func(e *killEnv, r killRow, _ []kabSeeded) { // the first pane kill and session kill, $9's
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallKillPane, tmux.CallKillSession)
			}, errName: "ErrTmuxKillFailed", followUp: "leftover", stays: true,
			desc: func(_ *killEnv, r killRow, _ []kabSeeded) apitest.DescCase {
				return apitest.DescKillUncheckable(r.ID, r.Name, sent)
			}},
		// b.myx: the follow-up finds none of the sessions, then $9's moved agent is still waited for.
		{name: "the youngest's agent unreadable, $9's agent pane moved: its agent exits at its pane kill",
			youngest: defBound, agent: agentUnreadable, moved: true, exits: []bool{false, true, true}, followUp: "gone"},
		{name: "the youngest's agent unreadable, $9's agent pane moved: its agent exits during the wait",
			youngest: defBound, agent: agentUnreadable, moved: true, exits: []bool{false, true, false}, followUp: "gone",
			world: func(e *killEnv, _ killRow, s []kabSeeded) {
				e.setAfterWaiting(2*api.KillPollInterval, procfix.Gone(), s[2].pid)
			}, polls: 2},
		{name: "the youngest's agent unreadable, $9's agent pane moved: its agent outlives the wait",
			youngest: defBound, agent: agentUnreadable, moved: true, exits: []bool{false, true, false}, followUp: "gone",
			errName: "ErrTmuxKillFailed", survivor: true, desc: survivor9},
		{name: "no pane carries the youngest's token, $9's agent pane moved: its agent exits at its pane kill",
			youngest: defBound, agent: agentNotRecorded, moved: true, exits: []bool{false, true, true}, followUp: "gone",
			world: youngestHangsUp},
		{name: "no pane carries the youngest's token, $9's agent pane moved: its agent outlives the wait",
			youngest: defBound, agent: agentNotRecorded, moved: true, exits: []bool{false, true, false}, followUp: "gone",
			world: youngestHangsUp, errName: "ErrTmuxKillFailed", survivor: true, desc: survivor9},
		{name: "the youngest still starting: nothing sent", youngest: defBound - time.Second,
			exits: []bool{true, true, true}, errName: "ErrTmuxUnresponsive",
			desc: func(e *killEnv, r killRow, s []kabSeeded) apitest.DescCase {
				return kabStarting(e, r, s[2], s[1], s[0])
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r, seeded := e.seedAbandoned(t, store.StateEnded, sessions(tc.youngest, tc.agent, tc.moved)...)
			kabExitAtOwnPaneKill(e, seeded, tc.exits)
			if tc.world != nil {
				tc.world(e, r, seeded)
			}
			var sleeps []time.Duration
			sleep := e.sleep
			e.sleep = func(d time.Duration) { sleeps = append(sleeps, d); sleep(d) }
			before, listed := e.columns(t, r.ID), e.rec.Sessions(r.Socket)

			res, err := e.killOptIn(r.ID)

			var desc apitest.DescCase
			if tc.desc != nil {
				desc = tc.desc(e, r, seeded)
			}
			kabAssertErr(t, err, tc.errName, desc, kabForbid(r, seeded))
			e.assertRowUnchanged(t, r.ID, before)
			polls := tc.polls
			if tc.survivor {
				polls = int(e.cfg.EffectiveKillExitWait() / api.KillPollInterval)
			}
			kcAssertPolls(t, sleeps, polls)
			var got []tmuxfix.SocketCall
			for _, c := range e.rec.SocketCalls() {
				got = append(got, tmuxfix.SocketCall{Call: c.Call, Target: c.Target})
			}
			want := []tmuxfix.SocketCall{{Call: tmux.CallLookup}}
			if tc.errName == "ErrTmuxUnresponsive" {
				if !reflect.DeepEqual(got, want) {
					t.Errorf("tmux calls = %+v; want %+v", got, want)
				}
				if after := e.rec.Sessions(r.Socket); !reflect.DeepEqual(after, listed) {
					t.Errorf("sessions changed:\n got %+v\nwant %+v", after, listed)
				}
				kolAssertCalled(t, r.ID, map[string]any{"include_finished": true, "kill_sent": false, "lookup_outcome": "leftover"})
				return
			}
			if tc.errName == "" && !res.KillSent {
				t.Error("kill_sent = false; want true")
			}
			want = append(want, tmuxfix.SocketCall{Call: tmux.CallListPanes})
			for _, i := range []int{2, 1, 0} { // $9, $12, $30; no pane kill without a pane carrying the token
				if i > 0 || tc.agent != agentNotRecorded {
					want = append(want, tmuxfix.SocketCall{Call: tmux.CallKillPane, Target: seeded[i].pane})
				}
				want = append(want, tmuxfix.SocketCall{Call: tmux.CallKillSession, Target: seeded[i].ID})
			}
			check, followUp, agentPID := "gone", "not_run", any(float64(seeded[0].pid))
			if tc.followUp != "" {
				want = append(want, tmuxfix.SocketCall{Call: tmux.CallLookup})
				check, followUp = "unreadable", tc.followUp
			}
			if tc.agent == agentNotRecorded {
				check, agentPID = "not_recorded", nil
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("tmux calls = %+v; want %+v", got, want)
			}
			for i, s := range seeded {
				if stays := tc.stays && i == 2; seqHas(e, r.Socket, s.ID) != stays {
					t.Errorf("session %s listed after the call: %t; want %t", s.ID, !stays, stays)
				}
			}
			rec := kcTrail(t, r.ID)
			var survivors []int
			if tc.survivor {
				survivors = []int{seeded[2].pid}
			}
			if rec["agent_pid"] != agentPID || rec["process_check"] != check ||
				rec["followup_outcome"] != followUp || rec["kill_sent"] != true ||
				!slices.Equal(kcInts(rec["survivor_pids"]), survivors) {
				t.Errorf("ad.kill.called = %v; want agent_pid %v (the youngest's), process_check %s, followup_outcome %s, "+
					"kill_sent true, survivor_pids %v", rec, agentPID, check, followUp, survivors)
			}
		})
	}
}

// TestKillIncludeFinishedLeftoverRecordedLaunch: a finished row that records
// its latest launch's server or pane, or no launch token, does not read an
// earlier launch's session past the bound as this id's own abandoned launch:
// "never reported in", nothing sent.
func TestKillIncludeFinishedLeftoverRecordedLaunch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		spec killRowSpec
	}{
		{"a pane recorded, no server", killRowSpec{NoServerIdentity: true}},
		{"a server recorded, no pane", killRowSpec{NoPane: true, Agent: agentNotRecorded}},
		{"no launch token", killRowSpec{NoServerIdentity: true, NoPane: true, Agent: agentNotRecorded,
			Opts: []apitest.SpawnOption{apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket})}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			spec := tc.spec
			spec.State, spec.NoSession = store.StateEnded, true
			r := e.seedRow(t, spec)
			s := e.seedLeftover(t, r, tmuxfix.OtherToken, rlkSettled(e))
			e.syncServers()
			kohAssertRefused(t, e, r.ID, apitest.DescKillOptInNeverReportedInLeftover(r.ID,
				[]apitest.DescSession{{Name: s.Name, ID: s.ID}}), "leftover")
		})
	}
}
