package api_test

// lookup_calltable_reuse_test.go is reuse's rows of the call-site table
// (lookup_calltable_test.go; SR-10.2, SR-10.4, SR-10.8, SR-20.6), run by
// TestCallTableReuse: spawn with the reuse opt-in on the column's row, seeded
// reusable on the kill fixture (spawn_reuse_fixture_test.go) and requesting
// its recorded name. Its cells are resume's (callTableResume,
// callTableResumeHeld) with reuse's restore and row-reset wording; the Ours
// column also runs with a young own session. "Reuse after duplicate session"
// reads each column at the re-lookup (arrangeHeld). Behaviour beyond the
// cells is the spawn_reuse*_test.go files'.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestCallTableReuse runs reuse's two rows in every column, as TestCallTable
// runs every other verb's.
func TestCallTableReuse(t *testing.T) {
	// Serial: its reuse cells check every record written to the shared trail since their mark, and one
	// column sets TMUX_TMPDIR; its reuse-after-duplicate-session cells run in parallel.
	runCallTableVerbs(t, []callTableVerb{callTableReuse(), callTableReuseHeld()})
}

// callTableReuseRestored is the restore's result a reuse's failed create
// reports: the row restored to ended after this spawn reset it.
var callTableReuseRestored = apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded,
	Launch: apitest.LaunchReuse}

// callTableReuse is reuse's row at the old-row lookup and the new-name
// pre-check (SR-10.2, SR-10.8): resume's cells, a failed create's restore and
// a timed-out create's "the row was reset" worded as reuse's.
func callTableReuse() callTableVerb {
	v := callTableResume()
	v.name, v.run = "Reuse", runCallTableReuse
	failed, timeout := v.cells[ctActionRecognised], v.cells[ctActionTimeout]
	failed.desc = func(*killEnv, killRow) apitest.DescCase {
		return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{}).AfterResumeRestore(callTableReuseRestored)
	}
	timeout.desc = func(e *killEnv, r killRow) apitest.DescCase {
		return apitest.DescLaunchTimeout(apitest.LaunchTimeout{InstanceID: r.ID, Timeout: e.cfg.EffectiveCreateTimeout(),
			RowReset: true})
	}
	v.cells[ctActionRecognised], v.cells[ctActionTimeout] = failed, timeout
	return v
}

// runCallTableReuse runs cell once, with variants: the Ours column with its
// session an hour old (cell) and younger than the bound (still starting); a
// Gone column once per callTableResumeAgents entry and once with the process
// running (the own-id conflict with no session).
func runCallTableReuse(t *testing.T, _ callTableVerb, col callTableColumn, cell callTableCell) {
	switch {
	case col.outcome == ctOurs:
		t.Run("session an hour old", func(t *testing.T) { runCallTableReuseCell(t, col, cell, col.spec.Agent, time.Hour) })
		t.Run("session young", func(t *testing.T) {
			runCallTableReuseCell(t, col, callTableReuseYoung(), col.spec.Agent, 0)
		})
	case col.spec.Agent != agentGone:
		runCallTableReuseCell(t, col, cell, col.spec.Agent, time.Hour)
	default:
		for _, a := range callTableResumeAgents {
			t.Run(a.name, func(t *testing.T) { runCallTableReuseCell(t, col, cell, a.agent, time.Hour) })
		}
		t.Run("process running", func(t *testing.T) {
			runCallTableReuseCell(t, col, callTableResumeOwnOld(true), agentAlive, time.Hour)
		})
	}
}

// callTableReuseYoung is the own session younger than the starting-session
// bound (SR-4.2 step 2): still starting, after the lookup alone.
func callTableReuseYoung() callTableCell {
	return callTableCell{errName: "ErrTmuxUnresponsive", calls: []tmux.Call{tmux.CallLookup},
		desc: func(e *killEnv, r killRow) apitest.DescCase {
			return apitest.DescStillStarting(apitest.StartingSession{InstanceID: r.ID, Name: r.Name,
				Bound: e.cfg.EffectiveStartingSession()})
		}}
}

// runCallTableReuseCell seeds col's row reusable with agent a (gone in an
// action column, so the create runs) and col.spec.Opts, requesting its
// recorded name unless that is unusable, its own session (when the column has
// one) created age before the rule's instant, builds col's world, reuses it
// and checks cell: one name, description, calls, then "wrote nothing" for a
// refusal, else the row's state after the launch.
func runCallTableReuseCell(t *testing.T, col callTableColumn, cell callTableCell, a agentState, age time.Duration) {
	e := newKillEnv(t)
	if col.actionFailure != 0 {
		a = agentGone
	}
	r := e.seedReusable(t, a, reuseRowSpec{Age: time.Hour, Opts: col.spec.Opts})
	if !col.spec.NoSession && col.actionFailure == 0 {
		e.seedSession(t, &r.killRow, e.createdBefore(age))
	}
	if col.world != nil {
		col.world(t, e, &r.killRow)
	}
	if col.actionFailure != 0 {
		e.rec.Script(r.Socket, tmuxfix.Script{Failure: col.actionFailure}, tmux.CallCreate)
	}
	var q reuseRequest
	if tmux.Unusable(r.Name) != tmux.UnusableNone {
		q.Name = "calltable-reuse" // the request is validated first; the guard judges the recorded name
	}
	before := e.snapshotReuse(t, r)

	_, _, err := e.reuse(t, reuseParams(t, r, q))

	if cell.errName == "" && err != nil {
		t.Fatalf("err = %v; want success", err)
	}
	if cell.errName != "" {
		assertOneName(t, err, cell.errName)
		apitest.AssertDescription(t, err.Error(), cell.desc(e, r.killRow), r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
	}
	e.assertKillCalls(t, cell.calls...)
	if !cell.sent {
		e.assertWroteNothing(t, before)
		return
	}
	want := store.StatePending // launched, or a timeout: the reset row stays pending
	if cell.errName != "" && cell.errName != "ErrTmuxUnresponsive" {
		want = store.StateEnded // restored after the failed create
	}
	if got := e.columns(t, r.ID).State; got != want {
		t.Errorf("row state = %v; want %s", got, want)
	}
}

// callTableReuseHeld is reuse's row after "duplicate session" (SR-10.4,
// SR-10.8): resume's cells (callTableResumeHeld), each description given
// reuse's restore sentence by runCallTableReuseHeld.
func callTableReuseHeld() callTableVerb {
	v := callTableResumeHeld()
	v.name, v.run = "Reuse after duplicate session", runCallTableReuseHeld
	return v
}

// runCallTableReuseHeld reuses a row that ended an hour before the
// re-lookup, its agent gone, in cell's arrangement: one name, the
// description with reuse's restore sentence, the lookup, create and
// re-lookup with nothing on the holder, and the row restored to its prior
// life (row_version two past, no launch start).
func runCallTableReuseHeld(t *testing.T, _ callTableVerb, col callTableColumn, cell callTableCell) {
	e := newKillEnv(t)
	h := cell.heldResume
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour, Held: true})
	sc := e.arrangeHeld(t, r.resumeRow, h.spec)
	if h.late {
		e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
			if c.Socket == r.Socket {
				col.world(t, e, &r.killRow)
			}
		})
	}

	_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))

	if !sc.Placed {
		t.Fatalf("the create on %s never answered %q (err %v)", r.Socket, "duplicate session", err)
	}
	assertOneName(t, err, cell.errName)
	p := apitest.HeldName{Name: r.Name, Restore: callTableReuseRestored}
	if h.named {
		p.SessionID = sc.Holder().ID
	}
	token, _ := sc.Moved.LaunchToken.(string)
	apitest.AssertDescription(t, err.Error(), h.desc(e, sc, p), r.Token, token, r.StoreID, apitest.OtherStoreID(r.StoreID))
	e.assertKillCalls(t, cell.calls...)
	e.assertHolderUntouched(t, sc)
	restored := sc.before.cols
	restored.RowVersion, restored.LaunchStartedAt = restored.RowVersion.(int64)+2, nil
	e.assertRowUnchanged(t, r.ID, restored)
}
