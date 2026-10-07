package api_test

// lookup_calltable_resume_test.go is resume's row of the call-site table
// (lookup_calltable_test.go; SR-8.2, SR-3.16, SR-20.6): the column's row,
// seeded resumable on the kill fixture (ended an hour before the rule's
// instant; its own session, when the column has one, an hour old), meets one
// resume. A refusal makes the lookup only and writes nothing; a launch makes
// the lookup and the create. The rule's boundaries, the ceiling and the trail
// are the other resume pre-launch tests'. A second row, "resume after
// duplicate session", re-reads each column as the one re-lookup after the
// create answered "duplicate session" (SR-8.5, SR-3.10), arranged by
// arrangeHeld (resume_held_fixture_test.go); trail and ceiling are resume_held*_test.go's.

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
// action column, so the create runs) and col.spec.Opts, builds col's world, resumes it and
// checks cell: one name, description, calls, then "wrote nothing" for a
// refusal, else the row's state after the launch.
func runCallTableResumeCell(t *testing.T, col callTableColumn, cell callTableCell, a agentState) {
	e := newKillEnv(t)
	if col.actionFailure != 0 {
		a = agentGone
	}
	r := e.seedResumable(t, time.Hour, a, col.spec.Opts...)
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
		ctGoneEmptyServer:     launch,
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
	maps.Copy(cells, callTableUnusableRefused())
	return callTableVerb{name: "resume", run: runCallTableResume, firstAction: tmux.CallCreate,
		actions: []tmux.Call{tmux.CallCreate}, cells: cells,
		serial: "a refused cell checks every record written to the shared trail since its mark (assertWroteNothing)"}
}

// callTableHeldResume is a "resume after duplicate session" cell: spec for
// arrangeHeld; late runs the column's world as the create returns, after the
// holders are placed (the server changes before the re-lookup); named gives
// the holder's $N to the description; desc is the description case for p.
type callTableHeldResume struct {
	spec        heldSpec
	late, named bool
	desc        func(e *killEnv, sc *heldScene, p apitest.HeldName) apitest.DescCase
}

// runCallTableResumeHeld resumes a row that ended an hour before the
// re-lookup, its agent gone, in cell's arrangement: one name, the description
// with the restore sentence, the lookup, create and re-lookup with nothing on
// the holder, and the row restored exactly.
func runCallTableResumeHeld(t *testing.T, _ callTableVerb, col callTableColumn, cell callTableCell) {
	e := newKillEnv(t)
	h := cell.heldResume
	r := e.seedHeldResumable(t, time.Hour, agentGone)
	sc := e.arrangeHeld(t, r, h.spec)
	if h.late {
		e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
			if c.Socket == r.Socket {
				col.world(t, e, &r.killRow)
			}
		})
	}

	_, err := e.resume(r.ID)

	assertOneName(t, err, cell.errName)
	p := apitest.HeldName{Name: r.Name, Restore: apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded}}
	if h.named {
		p.SessionID = sc.Holder().ID
	}
	token, _ := sc.Moved.LaunchToken.(string)
	apitest.AssertDescription(t, err.Error(), h.desc(e, sc, p), r.Token, token, r.StoreID, apitest.OtherStoreID(r.StoreID))
	e.assertKillCalls(t, cell.calls...)
	e.assertHolderUntouched(t, sc)
	e.assertHeldRestored(t, sc)
}

// callTableResumeHeld is resume's row after "duplicate session" (SR-8.5):
// every applicable column makes the lookup, the create and one re-lookup and
// returns its class's error with the restore sentence; the action columns are
// not applicable. Unlike plain spawn, the row records its server, so the
// server columns are live.
func callTableResumeHeld() callTableVerb {
	calls := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}
	cell := func(errName string, h callTableHeldResume) callTableCell {
		return callTableCell{errName: errName, calls: calls, heldResume: h}
	}
	// after is base after "duplicate session", with no holder named unless named.
	after := func(errName string, spec heldSpec, late, named bool, base func(e *killEnv, sc *heldScene) apitest.DescCase) callTableCell {
		return cell(errName, callTableHeldResume{spec: spec, late: late, named: named,
			desc: func(e *killEnv, sc *heldScene, p apitest.HeldName) apitest.DescCase {
				return base(e, sc).AfterHeldName(p)
			}})
	}
	// held is a holder of spec's kind, judged and named by its $N.
	held := func(spec heldSpec, desc func(e *killEnv, p apitest.HeldName) apitest.DescCase) callTableCell {
		return cell("ErrTmuxSessionConflict", callTableHeldResume{spec: spec, named: true,
			desc: func(e *killEnv, _ *heldScene, p apitest.HeldName) apitest.DescCase { return desc(e, p) }})
	}
	otherStore := func(e *killEnv, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldOtherStore(p, e.storeID) }
	noValidID := func(_ *killEnv, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldNoValidID(p) }
	// vanished: no session holds the name at the re-lookup (late: the server is gone by then).
	vanished := func(late bool) callTableCell {
		return after("ErrTmuxSessionCreate", heldSpec{Holder: holderVanished}, late, false,
			func(_ *killEnv, sc *heldScene) apitest.DescCase {
				return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: sc.r.Name, Duplicate: true})
			})
	}
	differentServer := func(spec heldSpec, late bool) callTableCell {
		return after("ErrTmuxNotAvailable", spec, late, false, func(_ *killEnv, sc *heldScene) apitest.DescCase {
			return apitest.DescDifferentServer(sc.r.ID)
		})
	}
	// relookup answers the re-lookup with s, an unlabelled holder on the name.
	relookup := func(errName string, s tmuxfix.Script, base func(e *killEnv, sc *heldScene) apitest.DescCase) callTableCell {
		return after(errName, heldSpec{Holder: holderNone, Relookup: s}, false, false, base)
	}
	noAction := "resume sends nothing after the re-lookup: it never acts on the holder"
	v := callTableVerb{
		name: "resume after duplicate session",
		run:  runCallTableResumeHeld,
		cells: map[callTableOutcome]callTableCell{
			// The row's own session for its examined token, past the window and the bound.
			ctOurs: after("ErrTmuxSessionConflict", heldSpec{Holder: holderCurrent, Created: time.Hour}, false, false,
				func(e *killEnv, sc *heldScene) apitest.DescCase {
					return apitest.DescOwnOldSession(apitest.StartingSession{InstanceID: sc.r.ID, Name: sc.r.Name,
						Window: e.cfg.EffectiveStoppingWindow(), Bound: e.cfg.EffectiveStartingSession(),
						WindowChecked: true, SessionID: true})
				}),
			// resume's Leftover wording, not plain spawn's DescHeldLeftover.
			ctLeftover: after("ErrTmuxSessionConflict", heldSpec{Holder: holderOld}, false, true,
				func(_ *killEnv, sc *heldScene) apitest.DescCase {
					return apitest.DescPreLaunchLeftover(sc.r.ID, []apitest.DescSession{{Name: sc.r.Name, ID: sc.Holder().ID}})
				}),
			ctGoneNoLabel:              vanished(false),
			ctGoneOtherStoreRowToken:   held(heldSpec{Holder: holderOtherStoreOwn}, otherStore),
			ctGoneOtherStoreOtherToken: held(heldSpec{Holder: holderOtherStore}, otherStore),
			ctGoneForeignLabel: held(heldSpec{Holder: holderForeign}, func(_ *killEnv, p apitest.HeldName) apitest.DescCase {
				return apitest.DescHeldDifferentID(p)
			}),
			ctGoneNameUnlabelled: held(heldSpec{Holder: holderNone}, noValidID),
			// Judged on the answering server: restarted at both lookups, its recorded server gone.
			ctGoneServerRestarted: held(heldSpec{Holder: holderNone, Server: heldServerRestarted}, noValidID),
			// The recorded server is left with no session as the create returns.
			ctGoneEmptyServer:    vanished(true),
			ctGoneNoServer:       vanished(true),
			ctGoneNoSocket:       vanished(true),
			ctDifferentRebound:   differentServer(heldSpec{Holder: holderNone, Server: heldServerRebound}, false),
			ctDifferentRestarted: differentServer(heldSpec{Holder: holderVanished}, true),
			ctDifferentNoServer:  differentServer(heldSpec{Holder: holderVanished}, true),
			ctConflictScope: after("ErrTmuxSessionConflict", heldSpec{Holder: holderConflicting}, false, true,
				func(_ *killEnv, sc *heldScene) apitest.DescCase {
					return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: sc.r.ID, Scope: true, NothingWasDone: true})
				}),
			// Two sessions carry the row's examined label: the holder and the row's own session renamed.
			ctConflictDuplicate: after("ErrTmuxSessionConflict", heldSpec{Holder: holderCurrent, OursRenamed: "calltable-dup"}, false, true,
				func(_ *killEnv, sc *heldScene) apitest.DescCase {
					var carrying []apitest.DescSession
					for _, s := range append(append([]tmuxfix.SeedSession(nil), sc.Holders...), sc.Ours) {
						carrying = append(carrying, apitest.DescSession{Name: s.Name, ID: s.ID})
					}
					return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: sc.r.ID, Sessions: carrying,
						NothingWasDone: true})
				}),
			ctUnreadableTimeout: relookup("ErrTmuxUnresponsive", tmuxfix.Script{Failure: tmux.FailTimeout},
				func(e *killEnv, _ *heldScene) apitest.DescCase {
					return apitest.DescCallTimeout(tmux.CallLookup, e.cfg.EffectiveQueryTimeout())
				}),
			ctUnreadableUnrecognised: relookup("ErrTmuxUnresponsive", tmuxfix.Script{Failure: tmux.FailUnrecognized,
				FirstLine: callTableFirstLine(), ExitStatus: 1}, func(*killEnv, *heldScene) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallLookup, callTableFirstLine())
			}),
			ctUnreadableMalformed: relookup("ErrTmuxUnresponsive", tmuxfix.Script{Failure: tmux.FailUnrecognized, HadStdout: true},
				func(*killEnv, *heldScene) apitest.DescCase { return apitest.DescUnrecognisedReply(tmux.CallLookup, "") }),
			ctUnavailableBinary: relookup("ErrTmuxNotAvailable", tmuxfix.Script{Failure: tmux.FailUnavailable},
				func(*killEnv, *heldScene) apitest.DescCase { return apitest.DescTmuxNotRun() }),
			ctUnavailableSocket: relookup("ErrTmuxNotAvailable", tmuxfix.Script{Failure: tmux.FailSocketDenied},
				func(_ *killEnv, sc *heldScene) apitest.DescCase { return apitest.DescSocketPermission(sc.r.Socket) }),
			ctActionRecognised: {na: noAction},
			ctActionTimeout:    {na: noAction},
		},
	}
	maps.Copy(v.cells, callTableUnusableNA("resume refuses an unusable recorded name before its lookup, so no create can meet a holder"))
	return v
}
