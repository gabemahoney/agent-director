package api_test

// lookup_calltable_expire_test.go is expire's row of the call-site table
// (lookup_calltable_test.go; SR-12.2, SR-12.3, SR-20.6): the column's row,
// seeded finished on the fixture clock with its agent process gone or not
// recorded so its socket's lookup decides, meets one expire run that selects
// every finished row. Gone deletes the row; every other outcome keeps it,
// unchanged, with its reason. Ordering, the budget, the conditional delete's
// races and the trail fields are the expire_*_test.go files'.

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// callTableExpireAgents are the agent processes a cell runs with: gone
// (tmux.ProcGone) and none recorded (tmux.ProcNone).
var callTableExpireAgents = []struct {
	name  string
	agent agentState
}{
	{"process gone", agentGone},
	{"no process recorded", agentNotRecorded},
}

// runCallTableExpire runs cell for each agent: col's row ended a day before
// the clock, col's world, one expire run with a zero window, then the result
// lists and kept record, the disagree actions, the row (absent when deleted,
// else unchanged), the sessions, the log and the one lookup on the row's socket.
func runCallTableExpire(t *testing.T, _ callTableVerb, col callTableColumn, cell callTableCell) {
	for _, a := range callTableExpireAgents {
		t.Run(a.name, func(t *testing.T) {
			e := newKillEnv(t)
			spec := col.spec
			spec.State, spec.Agent = store.StateEnded, a.agent
			spec.Opts = append(slices.Clone(spec.Opts), apitest.WithEndedAt(e.clock.Now().Add(-24*time.Hour)))
			r := e.seedRow(t, spec)
			if col.world != nil {
				col.world(t, e, &r)
			}
			rowBefore, sessionsBefore := e.columns(t, r.ID), e.rec.Sessions(r.Socket)
			mark := trailMark(t)

			res, lg, err := e.expire(olderThan(0))

			if err != nil {
				t.Fatalf("Expire: %v; want success", err)
			}
			assertExpired(t, res, mark, map[string]string{r.ID: cell.kept})
			action := cell.kept
			if action == "" {
				action = "deleted"
				if _, err := apitest.ReadSpawnColumns(e.dbPath, r.ID); !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("row read after expire: %v; want ErrSpawnNotFound", err)
				}
			} else if after := e.columns(t, r.ID); !reflect.DeepEqual(after, rowBefore) {
				t.Errorf("kept row changed:\n got %+v\nwant %+v", after, rowBefore)
			}
			for _, d := range expireDisagreesSince(t, mark, r.ID) {
				if d["action"] != action {
					t.Errorf("ad.provenance.disagree %v action = %v; want %s", d["reason"], d["action"], action)
				}
			}
			if after := e.rec.Sessions(r.Socket); !reflect.DeepEqual(after, sessionsBefore) {
				t.Errorf("sessions on %s changed:\n got %+v\nwant %+v", r.Socket, after, sessionsBefore)
			}
			if len(lg.lines) != 0 {
				t.Errorf("expire log = %q; want none", lg.lines)
			}
			e.assertLookupsOn(t, r.Socket)
		})
	}
}

// callTableExpire is expire's row (SR-12.2): every applicable column makes
// the one lookup only; Gone deletes the row, and Leftover, Ours, a different
// server, a provenance conflict, an unreadable answer and tmux unavailable
// keep it with their reasons.
func callTableExpire() callTableVerb {
	kept := func(reason string) callTableCell { return callTableCell{kept: reason} }
	deleted := callTableCell{}
	noAction := "expire makes no action call: its run makes only lookups"
	return callTableVerb{
		name: "expire",
		run:  runCallTableExpire,
		cells: map[callTableOutcome]callTableCell{
			ctOurs:                     kept("ours"),
			ctLeftover:                 kept("leftover_running"),
			ctGoneNoLabel:              deleted,
			ctGoneOtherStoreRowToken:   deleted,
			ctGoneOtherStoreOtherToken: deleted,
			ctGoneForeignLabel:         deleted,
			ctGoneNameUnlabelled:       deleted,
			ctGoneServerRestarted:      deleted,
			ctGoneNoServer:             deleted,
			ctGoneNoSocket:             deleted,
			ctDifferentRebound:         kept("tmux_server_changed"),
			ctDifferentRestarted:       kept("tmux_server_changed"),
			ctDifferentNoServer:        kept("tmux_server_changed"),
			ctConflictScope:            kept("provenance_conflict"),
			ctConflictDuplicate:        kept("provenance_conflict"),
			ctUnreadableTimeout:        kept("cant_tell"),
			ctUnreadableUnrecognised:   kept("cant_tell"),
			ctUnreadableMalformed:      kept("cant_tell"),
			ctUnavailableBinary:        kept("tmux_unavailable"),
			ctUnavailableSocket:        kept("tmux_unavailable"),
			ctActionRecognised:         {na: noAction},
			ctActionTimeout:            {na: noAction},
		},
	}
}
