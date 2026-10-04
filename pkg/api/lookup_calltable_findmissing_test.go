package api_test

// lookup_calltable_findmissing_test.go is find-missing's row of the call-site
// table (lookup_calltable_test.go; SR-3.2, SR-3.16, SR-11.3, SR-20.6): the column's
// row, seeded on the kill fixture, has its agent process made uncheckable (a
// start time that cannot be read, or none recorded) so its socket's lookup
// decides, and one sweep writes its note or mark. Each cell runs for both
// evidence kinds, on a waiting row and on a pending row past its grace
// period. Adoption, the budget and the trail fields are the
// find_missing_*_test.go files'.

import (
	"maps"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// callTableFMResult is one evidence kind's expected row result: the liveness
// note of an unverified row, or (note "") the tick reason of its mark.
type callTableFMResult struct {
	note, mark string
}

// callTableFM is find-missing's cell: the result when the agent's start time
// cannot be read (unknown) and when no agent process is recorded (none).
type callTableFM struct {
	unknown, none callTableFMResult
}

// callTableFMEvidence is one way the row's agent process cannot be checked.
type callTableFMEvidence struct {
	name  string
	agent agentState
	want  func(callTableFM) callTableFMResult
}

// callTableFMEvidences are the two: unknown (tmux.ProcUnknown) and none recorded (tmux.ProcNone).
var callTableFMEvidences = []callTableFMEvidence{
	{"start time unreadable", agentUnreadable, func(c callTableFM) callTableFMResult { return c.unknown }},
	{"no process recorded", agentNotRecorded, func(c callTableFM) callTableFMResult { return c.none }},
}

// callTableFMStates are the row states a cell runs on: waiting, and pending
// with its launch start twice the default grace period before the clock.
var callTableFMStates = []struct {
	state string
	opts  func() []apitest.SpawnOption
}{
	{store.StateWaiting, func() []apitest.SpawnOption { return nil }},
	{store.StatePending, func() []apitest.SpawnOption {
		return []apitest.SpawnOption{apitest.WithLaunchStartedAt(killClockStart.Add(-2 * fmGrace).UnixMilli())}
	}},
}

// runCallTableFindMissing runs cell for every evidence kind and row state:
// col's row with that agent, col's world, one sweep, then the row's result,
// the result lists, the tick's reason (the note, or the mark's reason), the
// name-held record and the socket calls.
func runCallTableFindMissing(t *testing.T, _ callTableVerb, col callTableColumn, cell callTableCell) {
	for _, ev := range callTableFMEvidences {
		for _, st := range callTableFMStates {
			t.Run(ev.name+"/"+st.state, func(t *testing.T) {
				e := newKillEnv(t)
				spec := col.spec
				spec.Agent, spec.State = ev.agent, st.state
				spec.Opts = append(slices.Clone(spec.Opts), st.opts()...)
				r := e.seedRow(t, spec)
				if col.world != nil {
					col.world(t, e, &r)
				}
				before := trailLen(t)

				res := mustSweep(t, e.st, e.pc, fmSweep{tmux: e.rec, now: e.clock.Now})

				want := ev.want(cell.fm)
				got, err := e.st.GetSpawn(r.ID)
				if err != nil {
					t.Fatalf("GetSpawn(%s): %v", r.ID, err)
				}
				if want.note != "" {
					if got.State != st.state || got.LivenessNote != want.note {
						t.Errorf("row {state %s, note %q}; want {%s, %q}", got.State, got.LivenessNote, st.state, want.note)
					}
					assertLists(t, res, nil, []string{r.ID})
					assertMarkReason(t, before, r.ID, want.note)
				} else {
					if got.State != store.StateMissing || got.LivenessNote != "" {
						t.Errorf("row {state %s, note %q}; want {missing, \"\"}", got.State, got.LivenessNote)
					}
					assertLists(t, res, []string{r.ID}, nil)
					assertMarkReason(t, before, r.ID, want.mark)
				}
				assertCallTableNameHeld(t, r.ID, want.mark == "tmux_name_held")
				e.assertKillCalls(t, cell.calls...)
			})
		}
	}
}

// assertCallTableNameHeld fails unless id has exactly one find-missing
// ad.launch.name_held record, marked_missing, when held, and none otherwise.
func assertCallTableNameHeld(t *testing.T, id string, held bool) {
	t.Helper()
	recs := pendTrail(t, "ad.launch.name_held", id)
	switch {
	case !held && len(recs) != 0:
		t.Errorf("ad.launch.name_held = %v; want none", recs)
	case held && (len(recs) != 1 || recs[0]["source"] != "ad_find_missing" || recs[0]["row_result"] != "marked_missing"):
		t.Errorf("ad.launch.name_held = %v; want one, source ad_find_missing, row_result marked_missing", recs)
	}
}

// callTableFindMissing is find-missing's row (SR-3.16, SR-11.3): every
// applicable column makes the one lookup only; Ours is unverified by its
// evidence, Leftover and Gone are marked missing (tmux_name_held when a
// session holds the recorded name, else tmux_absent), a different server
// and a provenance conflict are noted, and an unreadable answer or tmux
// unavailable leave the "not called" note. An unusable recorded name is
// noted with no tmux call (SR-3.2).
func callTableFindMissing() callTableVerb {
	lookup := []tmux.Call{tmux.CallLookup}
	noted := func(unknown, none string) callTableCell {
		return callTableCell{calls: lookup, fm: callTableFM{unknown: callTableFMResult{note: unknown},
			none: callTableFMResult{note: none}}}
	}
	marked := func(reason string) callTableCell {
		m := callTableFMResult{mark: reason}
		return callTableCell{calls: lookup, fm: callTableFM{unknown: m, none: m}}
	}
	nameHeld, absent := marked("tmux_name_held"), marked("tmux_absent")
	notCalled := noted("probe_eacces", "process_not_seen_tmux_unchecked")
	noAction := "find-missing sends no action: its sweep makes only lookups and pane listings"
	v := callTableVerb{
		name: "find-missing",
		run:  runCallTableFindMissing,
		cells: map[callTableOutcome]callTableCell{
			ctOurs:                     noted("probe_eacces", "process_not_seen_session_present"),
			ctLeftover:                 nameHeld,
			ctGoneNoLabel:              absent,
			ctGoneOtherStoreRowToken:   nameHeld,
			ctGoneOtherStoreOtherToken: nameHeld,
			ctGoneForeignLabel:         absent,
			ctGoneNameUnlabelled:       nameHeld,
			ctGoneServerRestarted:      absent,
			ctGoneEmptyServer:          absent,
			ctGoneNoServer:             absent,
			ctGoneNoSocket:             absent,
			ctDifferentRebound:         noted("tmux_server_changed", "tmux_server_changed"),
			ctDifferentRestarted:       noted("tmux_server_changed", "tmux_server_changed"),
			ctDifferentNoServer:        noted("tmux_server_changed", "tmux_server_changed"),
			ctConflictScope:            noted("provenance_conflict", "provenance_conflict"),
			ctConflictDuplicate:        noted("provenance_conflict", "provenance_conflict"),
			ctUnreadableTimeout:        notCalled,
			ctUnreadableUnrecognised:   notCalled,
			ctUnreadableMalformed:      notCalled,
			ctUnavailableBinary:        notCalled,
			ctUnavailableSocket:        notCalled,
			ctActionRecognised:         {na: noAction},
			ctActionTimeout:            {na: noAction},
		},
	}
	maps.Copy(v.cells, callTableFindMissingUnusable())
	return v
}

// callTableFindMissingUnusable is find-missing's unusable-name cells (SR-11.3):
// for either evidence, the row left unverified with the fixture's note and
// its entry tick, with no tmux call.
func callTableFindMissingUnusable() map[callTableOutcome]callTableCell {
	cells := map[callTableOutcome]callTableCell{}
	for _, u := range callTableUnusables() {
		noted := callTableFMResult{note: u.fixture.note}
		cells[u.outcome] = callTableCell{fm: callTableFM{unknown: noted, none: noted}}
	}
	return cells
}
