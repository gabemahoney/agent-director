package api_test

// lookup_calltable_resume_test.go is resume's row of the call-site table
// (lookup_calltable_test.go; SR-8.2, SR-3.16, SR-20.6): the column's row,
// seeded resumable on the kill fixture (ended an hour before the rule's
// instant; its own session, when the column has one, an hour old), meets one
// resume. A refusal makes the lookup only and writes nothing; a launch makes
// the lookup and the create. The rule's boundaries, the ceiling and the trail
// are the other resume pre-launch tests'.

import (
	"maps"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// callTableResumeAgents are the agent processes a Gone cell runs with, each
// leaving the lookup to decide (LFR M6); a running one gives the
// starting-session rule with no session instead (callTableResumeOwnOld).
var callTableResumeAgents = []struct {
	name  string
	agent agentState
}{
	{"process gone", agentGone},
	{"no process recorded", agentNotRecorded},
	{"process unreadable", agentUnreadable},
}

// runCallTableResume runs cell once, or, for a Gone column (its agent gone),
// once per callTableResumeAgents entry and once with the process running.
func runCallTableResume(t *testing.T, _ callTableVerb, col callTableColumn, cell callTableCell) {
	if col.spec.Agent != agentGone {
		runCallTableResumeCell(t, col, cell, col.spec.Agent)
		return
	}
	for _, a := range callTableResumeAgents {
		t.Run(a.name, func(t *testing.T) { runCallTableResumeCell(t, col, cell, a.agent) })
	}
	t.Run("process running", func(t *testing.T) {
		runCallTableResumeCell(t, col, callTableResumeOwnOld(true), agentAlive)
	})
}

// runCallTableResumeCell seeds col's row resumable with agent a (gone in an
// action column, so the create runs), builds col's world, resumes it and
// checks cell: one name, description, calls, then "wrote nothing" for a
// refusal, else the row's state after the launch.
func runCallTableResumeCell(t *testing.T, col callTableColumn, cell callTableCell, a agentState) {
	e := newKillEnv(t)
	if col.actionFailure != 0 {
		a = agentGone
	}
	r := e.seedResumable(t, time.Hour, a)
	if !col.spec.NoSession && col.actionFailure == 0 {
		e.seedSession(t, &r.killRow, e.createdBefore(time.Hour))
	}
	if col.world != nil {
		col.world(t, e, &r.killRow)
	}
	if col.actionFailure != 0 {
		e.rec.Script(r.Socket, tmuxfix.Script{Failure: col.actionFailure}, tmux.CallCreate)
	}
	before := e.snapshotResume(t, r)

	_, err := e.resume(r.ID)

	if cell.errName == "" && err != nil {
		t.Fatalf("err = %v; want success", err)
	}
	if cell.errName != "" {
		assertOneName(t, err, cell.errName)
		apitest.AssertDescription(t, err.Error(), cell.desc(e, r.killRow), r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
	}
	e.assertKillCalls(t, cell.calls...)
	if !cell.sent {
		e.assertResumeWroteNothing(t, before)
		return
	}
	want := store.StatePending // launched, or a timeout: the row stays pending
	if cell.errName != "" && cell.errName != "ErrTmuxUnresponsive" {
		want = store.StateEnded // restored after the failed create
	}
	if got := e.columns(t, r.ID).State; got != want {
		t.Errorf("row state = %v; want %s", got, want)
	}
}

// callTableResumeOwnOld is the own-id conflict past the window and the bound
// (SR-4.2 step 3): with the Ours session, or with none while the agent runs.
func callTableResumeOwnOld(noSession bool) callTableCell {
	return callTableCell{errName: "ErrTmuxSessionConflict", calls: []tmux.Call{tmux.CallLookup},
		desc: func(e *killEnv, r killRow) apitest.DescCase {
			return apitest.DescOwnOldSession(apitest.StartingSession{InstanceID: r.ID, Name: r.Name,
				Window: e.cfg.EffectiveStoppingWindow(), Bound: e.cfg.EffectiveStartingSession(),
				NoSession: noSession, WindowChecked: true, SessionID: true})
		}}
}

// callTableResumeHolder is the id of the session holding r's recorded name.
func callTableResumeHolder(e *killEnv, r killRow) string {
	for _, s := range e.rec.Sessions(r.Socket) {
		if s.Name == r.Name {
			return s.ID
		}
	}
	return ""
}

// callTableResume is resume's row (SR-8.2): Ours past both limits is the
// own-id conflict; Leftover and a name holder on Gone are conflicts; a free
// name launches; Can't tell refuses by its kind; a failed create restores
// the row, a timed-out one leaves it pending.
func callTableResume() callTableVerb {
	lookup, launched := []tmux.Call{tmux.CallLookup}, []tmux.Call{tmux.CallLookup, tmux.CallCreate}
	launch := callTableCell{sent: true, calls: launched}
	held := func(desc func(e *killEnv, p apitest.HeldName) apitest.DescCase) callTableCell {
		return callTableCell{errName: "ErrTmuxSessionConflict", calls: lookup, desc: func(e *killEnv, r killRow) apitest.DescCase {
			return desc(e, apitest.HeldName{Name: r.Name, SessionID: callTableResumeHolder(e, r), BeforeLaunch: true})
		}}
	}
	otherStore := held(func(e *killEnv, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldOtherStore(p, e.storeID) })
	cells := callTableLookupRefusals()
	maps.Copy(cells, map[callTableOutcome]callTableCell{
		ctOurs: callTableResumeOwnOld(false),
		ctLeftover: {errName: "ErrTmuxSessionConflict", calls: lookup, desc: func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescPreLaunchLeftover(r.ID, []apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}})
		}},
		ctGoneNoLabel:              launch,
		ctGoneOtherStoreRowToken:   otherStore,
		ctGoneOtherStoreOtherToken: otherStore,
		ctGoneForeignLabel:         launch,
		ctGoneNameUnlabelled: held(func(_ *killEnv, p apitest.HeldName) apitest.DescCase {
			return apitest.DescHeldNoValidID(p)
		}),
		ctGoneServerRestarted: launch,
		ctGoneNoServer:        launch,
		ctGoneNoSocket:        launch,
		ctActionRecognised: {errName: "ErrTmuxSessionCreate", sent: true, calls: launched,
			desc: func(*killEnv, killRow) apitest.DescCase {
				return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{}).AfterResumeRestore(
					apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded})
			}},
		ctActionTimeout: {errName: "ErrTmuxUnresponsive", sent: true, calls: launched,
			desc: func(e *killEnv, r killRow) apitest.DescCase {
				return apitest.DescLaunchTimeout(apitest.LaunchTimeout{InstanceID: r.ID, Timeout: e.cfg.EffectiveCreateTimeout()})
			}},
	})
	return callTableVerb{name: "resume", run: runCallTableResume, firstAction: tmux.CallCreate,
		actions: []tmux.Call{tmux.CallCreate}, cells: cells}
}
