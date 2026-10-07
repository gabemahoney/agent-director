package api_test

// kill_optin_sequence_test.go: with the finished-row opt-in, an ended or
// missing row whose own session reported in and is past the stopping window
// and the starting-session bound gets Epic 10's kill sequence by ids (SR-6.1,
// SR-6.5, SR-20.6; AC-KILL-11): the wait or one follow-up, never both, failed
// kills left to the check, a lost reply's pane used without a write, the row
// unchanged and a later resume launching.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kosFinished are the finished states the opt-in acts on.
var kosFinished = []string{store.StateEnded, store.StateMissing}

// kosReportedIn is a row in state that ended the window ago, records a pid
// and a session id, and whose own session (agent a) predates ended_at by the bound.
func kosReportedIn(state string, a agentState) startingRow {
	return startingRow{state: state, endedAgo: defWindow, agent: a, age: defWindow + defBound}
}

// kosKill runs kill with the opt-in on r and checks o (seqOutcome): the result
// or error, kill_sent in the result and the trail, the calls, each kill
// targeting r's agent pane and session by id, the sleeps (none after a
// follow-up) and the row unchanged in every column.
func kosKill(t *testing.T, e *killEnv, r killRow, o seqOutcome) {
	t.Helper()
	before := e.columns(t, r.ID)
	slept := 0
	prev := e.sleep
	e.sleep = func(d time.Duration) { slept++; prev(d) }
	res, err := e.killOptIn(r.ID)
	if o.errName == "" {
		if err != nil {
			t.Fatalf("kill: %v; want success", err)
		}
		if res.KillSent != o.sent {
			t.Errorf("kill_sent = %v; want %v", res.KillSent, o.sent)
		}
	} else {
		assertOneName(t, err, o.errName)
		apitest.AssertDescription(t, err.Error(), o.desc(e, r), r.Token, r.StoreID)
	}
	kolAssertCalled(t, r.ID, map[string]any{"include_finished": true, "lookup_outcome": "ours", "kill_sent": o.sent})
	e.assertKillCalls(t, o.calls...)
	if got := seqTarget(e, tmux.CallKillPane); got != "" && got != labelledPane(t, r.Session, r.Token) {
		t.Errorf("pane kill targets %q; want the agent's pane", got)
	}
	if got := seqTarget(e, tmux.CallKillSession); got != r.Session.ID {
		t.Errorf("session kill targets %q; want the labelled session %s", got, r.Session.ID)
	}
	if followUp := len(e.rec.SocketCallsOf(tmux.CallLookup)) > 1; followUp && slept != 0 {
		t.Errorf("%d sleeps with a follow-up lookup; want the wait or the follow-up, never both", slept)
	}
	e.assertRowUnchanged(t, r.ID, before)
}

// TestKillIncludeFinishedSequence: a reported-in session past both on an ended
// or missing row is killed by ids, then the wait or one follow-up decides.
func TestKillIncludeFinishedSequence(t *testing.T) {
	t.Parallel()
	both := apitest.KillSent{Pane: true, Session: true}
	ok := seqOutcome{calls: seqOurs, sent: true}
	okFollowUp := seqOutcome{calls: withFollowUp(seqOurs), sent: true}
	agentGoneAfter := func(call tmux.Call) func(*testing.T, *killEnv, *killRow) {
		return func(_ *testing.T, e *killEnv, r *killRow) { e.setAfterCall(call, procfix.Gone(), r.AgentPID) }
	}
	failOnce := func(f tmux.Failure, call tmux.Call, then func(*testing.T, *killEnv, *killRow)) func(*testing.T, *killEnv, *killRow) {
		return func(t *testing.T, e *killEnv, r *killRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: f, Times: 1}, call)
			if then != nil {
				then(t, e, r)
			}
		}
	}
	followUpFails := func(f tmux.Failure) func(*testing.T, *killEnv, *killRow) {
		return func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.AfterCall(tmux.CallKillSession, func(tmuxfix.SocketCall, error) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: f}, tmux.CallLookup)
			})
		}
	}
	cases := []struct {
		name   string
		agent  agentState
		setup  func(*testing.T, *killEnv, *killRow)
		out    seqOutcome
		resume bool // a later resume launches
	}{
		{name: "agent gone after the pane kill", setup: agentGoneAfter(tmux.CallKillPane), out: ok, resume: true},
		{name: "agent runs past the exit wait", setup: func(*testing.T, *killEnv, *killRow) {},
			out: seqWaitExpired(seqOurs, both, true, false)},
		{name: "pane kill refused, the session kill ends the agent", out: ok,
			setup: failOnce(tmux.FailSocketDenied, tmux.CallKillPane, agentGoneAfter(tmux.CallKillSession))},
		{name: "session kill times out after the pane kill", out: ok,
			setup: failOnce(tmux.FailTimeout, tmux.CallKillSession, agentGoneAfter(tmux.CallKillPane))},
		{name: "both kills fail, the agent runs on", out: seqWaitExpired(seqOurs, both, true, false),
			setup: failOnce(tmux.FailTimeout, tmux.CallKillPane, failOnce(tmux.FailSocketDenied, tmux.CallKillSession, nil))},
		{name: "agent unreadable, follow-up label gone", agent: agentUnreadable,
			setup: func(*testing.T, *killEnv, *killRow) {}, out: okFollowUp},
		{name: "agent unreadable, follow-up label still there", agent: agentUnreadable,
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallKillPane, tmux.CallKillSession)
			}, out: seqOutcome{calls: withFollowUp(seqOurs), sent: true, errName: "ErrTmuxKillFailed",
				desc: func(_ *killEnv, r killRow) apitest.DescCase { return apitest.DescKillUncheckable(r.ID, r.Name, both) }}},
		{name: "agent unreadable, follow-up unreadable", agent: agentUnreadable, setup: followUpFails(tmux.FailTimeout),
			out: seqOutcome{calls: withFollowUp(seqOurs), sent: true, errName: "ErrTmuxUnresponsive",
				desc: func(e *killEnv, r killRow) apitest.DescCase {
					return apitest.DescKillFollowUpUnresponsive(apitest.KillFollowUp{Name: r.Name, Timeout: e.cfg.EffectiveQueryTimeout()})
				}}},
		{name: "agent unreadable, follow-up tmux not run", agent: agentUnreadable, setup: followUpFails(tmux.FailUnavailable),
			out: seqOutcome{calls: withFollowUp(seqOurs), sent: true, errName: "ErrTmuxNotAvailable",
				desc: func(*killEnv, killRow) apitest.DescCase { return apitest.DescTmuxNotRun().AfterKillSent() }}},
	}
	for _, state := range kosFinished {
		for _, tc := range cases {
			t.Run(state+", "+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := e.seedStarting(t, kosReportedIn(state, tc.agent))
				tc.setup(t, e, &r.killRow)
				kosKill(t, e, r.killRow, tc.out)
				if tc.resume {
					kosAssertResumeLaunches(t, e, r.ID)
				}
			})
		}
	}
}

// kosAssertResumeLaunches fails unless a resume of id through e's Recorder
// creates one session and moves the row to pending.
func kosAssertResumeLaunches(t *testing.T, e *killEnv, id string) {
	t.Helper()
	if _, err := e.resume(id); err != nil {
		t.Fatalf("resume after the kill: %v", err)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
		t.Errorf("session creations after resume = %d; want 1", n)
	}
	if got := e.columns(t, id).State; got != store.StatePending {
		t.Errorf("state after resume = %v; want pending", got)
	}
}

// TestKillIncludeFinishedSequenceUsesLostReplyPane: a reported-in row with no
// pane or server identity recorded has the pane carrying its token killed by
// id, with no adoption write (row_version and the identity unchanged).
func TestKillIncludeFinishedSequenceUsesLostReplyPane(t *testing.T) {
	t.Parallel()
	for _, state := range kosFinished {
		t.Run(state, func(t *testing.T) {
			e := newKillEnv(t)
			pid := e.newPID()
			spec := e.resumableSpec(defWindow, agentAlive, apitest.WithPID(pid), apitest.WithProcStarttime(apitest.LinuxProcStarttime))
			spec.State, spec.NoPane, spec.NoServerIdentity = state, true, true
			r := e.seedResumableRow(t, spec).killRow
			r.Session = e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: r.Name, Label: r.current(),
				Created: e.ruleInstant().Add(-(defWindow + defBound)).Unix(), Panes: []tmuxfix.SeedPane{{PID: pid, AdPane: r.Token}}})
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), pid)

			kosKill(t, e, r, seqOutcome{calls: seqOurs, sent: true})
			if got := seqTarget(e, tmux.CallKillPane); got != r.Session.Panes[0].ID {
				t.Errorf("pane kill targets %q; want the token's pane %s", got, r.Session.Panes[0].ID)
			}
			if e.store.adoptTries != 0 {
				t.Errorf("adoption writes attempted = %d; want none on a finished row", e.store.adoptTries)
			}
		})
	}
}
