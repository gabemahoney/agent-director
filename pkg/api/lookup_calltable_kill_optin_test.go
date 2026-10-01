package api_test

// lookup_calltable_kill_optin_test.go is kill's row of the call-site table
// (lookup_calltable_test.go; SR-20.5, SR-20.6) with the operator-only
// finished-row opt-in (SR-6.5; Epic 18): each column's row is seeded ended
// just after its own session was created (so that session reported in, SR-6.7),
// and the clock is moved past the stopping window and the starting-session
// bound, so Ours runs the kill sequence. Leftover is "never reported in";
// every other cell is kill's own. It also holds koEnded and koPastBoth, the
// finished-row spec the opt-in's security and one-name rows use too.

import (
	"maps"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// koEnded is spec as an ended row whose ended_at is endedAfter past
// killClockStart, when the fixture seeds its session: that session predates
// ended_at (reported in, with a pid) only when endedAfter is positive.
func koEnded(spec killRowSpec, endedAfter time.Duration) killRowSpec {
	spec.State = store.StateEnded
	spec.Opts = append([]apitest.SpawnOption{apitest.WithEndedAt(killClockStart.Add(endedAfter))}, spec.Opts...)
	return spec
}

// koPastBoth moves e's clock by the stopping window plus the starting-session
// bound: past both for a koEnded row ended up to a second after its session.
func koPastBoth(e *killEnv) {
	e.clock.Advance(e.cfg.EffectiveStoppingWindow() + e.cfg.EffectiveStartingSession())
}

// callTableKillOptIn is kill's row with the opt-in on an ended row (SR-6.5):
// Ours past both and reported in runs the kill sequence by id; Leftover is
// "never reported in"; Gone, Can't tell, tmux unavailable and the action
// failures are as for a live row.
func callTableKillOptIn() callTableVerb {
	v := callTableKill()
	v.name = "kill with the opt-in, ended"
	v.cells = maps.Clone(v.cells)
	v.cells[ctLeftover] = callTableCell{errName: "ErrTmuxSessionConflict", calls: []tmux.Call{tmux.CallLookup},
		desc: func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescKillOptInNeverReportedInLeftover(r.ID, []apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}})
		}}
	v.invoke = func(_ *testing.T, e *killEnv, r killRow) (bool, error) {
		res, err := e.killOptIn(r.ID)
		return res.KillSent, err
	}
	v.run = runCallTableKillOptIn
	return v
}

// runCallTableKillOptIn runs cell as runCallTableRowCell does, with col's row
// ended a second after its session and the clock past both before cell.prepare.
func runCallTableKillOptIn(t *testing.T, v callTableVerb, col callTableColumn, cell callTableCell) {
	col.spec = koEnded(col.spec, time.Second)
	prepare := cell.prepare
	cell.prepare = func(e *killEnv, r killRow) {
		koPastBoth(e)
		if prepare != nil {
			prepare(e, r)
		}
	}
	runCallTableRowCell(t, v, col, cell)
}
