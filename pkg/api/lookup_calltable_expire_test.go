package api_test

// lookup_calltable_expire_test.go is expire's row of the call-site table
// (lookup_calltable_test.go; SR-3.2, SR-12.2, SR-12.3, SR-20.6): the column's
// row, seeded finished on the fixture clock with its agent process gone or not
// recorded so its socket's lookup decides, meets one expire run that selects
// every finished row; with the agent gone, a later row on the same socket
// (no server identity, no session) shows what the lookup left it. Gone
// deletes the row; every other outcome keeps it, unchanged, with its reason;
// an unusable recorded name keeps it with that name's reason and no tmux call.
// Ordering, the budget, the conditional delete's races and the trail fields
// are the expire_*_test.go files'.

import (
	"maps"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// callTableExpireAgents are the agent processes a cell runs with: gone
// (tmux.ProcGone), beside the later row, and none recorded (tmux.ProcNone).
var callTableExpireAgents = []struct {
	name  string
	agent agentState
	later bool
}{
	{"process gone, with a later row", agentGone, true},
	{"no process recorded", agentNotRecorded, false},
}

// runCallTableExpire runs cell for each agent: col's row ("a-…") ended a day
// before the clock, the later row ("b-…") when the agent is gone, col's world
// and one expire run with a zero window, checked by rsnExpectWith (the result
// lists and kept records, the rows, the sessions, the log, one lookup on the
// shared socket or none), then the row's disagree actions.
func runCallTableExpire(t *testing.T, _ callTableVerb, col callTableColumn, cell callTableCell) {
	for _, a := range callTableExpireAgents {
		t.Run(a.name, func(t *testing.T) {
			e := newKillEnv(t)
			spec := col.spec
			spec.ID, spec.State, spec.Agent = rsnID("a"), store.StateEnded, a.agent
			spec.Opts = append([]apitest.SpawnOption{apitest.WithEndedAt(e.clock.Now().Add(-24 * time.Hour))}, spec.Opts...)
			r := e.seedRow(t, spec)
			want, sockets := map[string]string{r.ID: cell.kept}, []string(nil)
			if len(cell.calls) > 0 {
				sockets = []string{r.Socket}
			}
			if a.later {
				later := e.seedRow(t, e.rsnFinish(killRowSpec{NoSession: true, NoServerIdentity: true}, rsnID("b")))
				want[later.ID], sockets = cell.keptLater, []string{later.Socket}
			}
			if col.world != nil {
				col.world(t, e, &r)
			}
			mark := trailMark(t)

			e.rsnExpect(t, 0, want, sockets...)

			action := cell.kept
			if action == "" {
				action = "deleted"
			}
			disagrees := expireDisagreesSince(t, mark, r.ID)
			if len(cell.calls) == 0 && len(disagrees) != 0 {
				t.Errorf("ad.provenance.disagree %v; want none for a row with no lookup", disagrees)
			}
			for _, d := range disagrees {
				if d["action"] != action {
					t.Errorf("ad.provenance.disagree %v action = %v; want %s", d["reason"], d["action"], action)
				}
			}
		})
	}
}

// callTableExpire is expire's row (SR-12.2): every lookup column makes the one
// lookup only; Gone deletes the row, and Leftover, Ours, a different server,
// a provenance conflict, an unreadable answer and tmux unavailable keep it
// with their reasons; the later row is deleted by the same lookup, or kept
// tmux_skipped once the socket stops. Each unusable recorded name keeps the
// row with its reason and no tmux call (SR-3.2).
func callTableExpire() callTableVerb {
	lookup := []tmux.Call{tmux.CallLookup}
	kept := func(reason, later string) callTableCell {
		return callTableCell{calls: lookup, kept: reason, keptLater: later}
	}
	deleted := kept("", "")
	stops := func(reason string) callTableCell { return kept(reason, "tmux_skipped") }
	noAction := "expire makes no action call: its run makes only lookups"
	v := callTableVerb{
		name:   "expire",
		run:    runCallTableExpire,
		serial: "a cell checks every ad.expire.kept record written to the shared trail since its mark (assertExpired)",
		cells: map[callTableOutcome]callTableCell{
			ctOurs:                     kept("ours", ""),
			ctLeftover:                 kept("leftover_running", ""),
			ctGoneNoLabel:              deleted,
			ctGoneOtherStoreRowToken:   deleted,
			ctGoneOtherStoreOtherToken: deleted,
			ctGoneForeignLabel:         deleted,
			ctGoneNameUnlabelled:       deleted,
			ctGoneServerRestarted:      deleted,
			ctGoneEmptyServer:          deleted,
			ctGoneNoServer:             deleted,
			ctGoneNoSocket:             deleted,
			ctDifferentRebound:         kept("tmux_server_changed", ""),
			ctDifferentRestarted:       kept("tmux_server_changed", ""),
			ctDifferentNoServer:        kept("tmux_server_changed", ""),
			ctConflictScope:            kept("provenance_conflict", "provenance_conflict"),
			ctConflictDuplicate:        kept("provenance_conflict", ""),
			ctUnreadableTimeout:        stops("cant_tell"),
			ctUnreadableUnrecognised:   stops("cant_tell"),
			ctUnreadableMalformed:      stops("cant_tell"),
			ctUnavailableBinary:        stops("tmux_unavailable"),
			ctUnavailableSocket:        stops("tmux_unavailable"),
			ctActionRecognised:         {na: noAction},
			ctActionTimeout:            {na: noAction},
		},
	}
	maps.Copy(v.cells, callTableExpireUnusable())
	return v
}

// callTableExpireUnusable is expire's unusable-name cells: the fixture's kept
// reason with no tmux call; the later row is decided by its own lookup.
func callTableExpireUnusable() map[callTableOutcome]callTableCell {
	cells := map[callTableOutcome]callTableCell{}
	for _, u := range callTableUnusables() {
		cells[u.outcome] = callTableCell{kept: u.fixture.kept}
	}
	return cells
}
