package api_test

// find_missing_held_name_launch_test.go: find-missing and the row's own launch (SR-11.3, SR-14; AC-FM-18,
// AC-SPN-09): the current launch's session never gets its row marked, and a plain spawn's pending row whose end
// write failed after "duplicate session" is marked only past grace. It uses hnEnv (find_missing_held_name_test.go).

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestFindMissingHeldNameOursNeverMarked (AC-FM-18): a pending row whose current-launch session is present, under
// its recorded name or renamed, is never marked, however late the sweep.
func TestFindMissingHeldNameOursNeverMarked(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	cases := []struct {
		name string
		seed func(t *testing.T, e *hnEnv, rename func(string) string) hnRow
	}{
		{"pid-only pane alive", func(t *testing.T, e *hnEnv, rename func(string) string) hnRow {
			r := e.seedAs(t, "o-1", store.StatePending, "", apitest.WithLaunchStartedAt(e.clock.Now().UnixMilli()))
			e.pc.Set(apitest.TestPanePID, procfix.Alive(fmStart))
			e.rec.SeedRowSession(t, e.dbPath, r.id, tmuxfix.WithRowSessionName(rename(r.name)))
			return r
		}},
		{"lost reply adopted", func(t *testing.T, e *hnEnv, rename func(string) string) hnRow {
			r := e.pending(t, "o-1", "")
			e.pc.Set(hnAgentPID, procfix.Alive(fmStart))
			e.rec.SeedSessions(apitest.TestSocket, tmuxfix.SeedSession{Name: rename(r.name),
				Label: tmuxfix.Valid(r.token, r.id, e.storeID), Panes: []tmuxfix.SeedPane{{PID: hnAgentPID, AdPane: r.token}}})
			return r
		}},
	}
	renames := map[string]func(string) string{
		"recorded name": func(n string) string { return n },
		"renamed":       func(n string) string { return "renamed-" + n },
	}
	for _, tc := range cases {
		for how, rename := range renames {
			t.Run(tc.name+"/"+how, func(t *testing.T) {
				e := newHNEnv(t)
				r := tc.seed(t, e, rename)
				sessions := e.rec.Sessions(apitest.TestSocket)
				for _, late := range []time.Duration{hnPast, 30 * 24 * time.Hour} {
					res, mark := e.sweep(t, late)
					if slices.Contains(res.IDs, r.id) || e.cols(t, r.id).State != store.StatePending {
						t.Errorf("after +%v: ids %v, state %v; want not marked, pending", late, res.IDs, e.cols(t, r.id).State)
					}
					for _, tick := range ticksSince(t, mark, r.id) {
						if tick["new_state"] != nil {
							t.Errorf("tick %v; want no mark", tick)
						}
					}
					if recs := ptRecords(t, mark, "ad.launch.name_held", r.id); len(recs) != 0 {
						t.Errorf("ad.launch.name_held records = %v; want none", recs)
					}
				}
				if got := e.rec.Sessions(apitest.TestSocket); !reflect.DeepEqual(got, sessions) {
					t.Errorf("sessions changed:\nbefore %+v\nafter  %+v", sessions, got)
				}
			})
		}
	}
}

// TestFindMissingHeldNameAfterSpawnEndWriteFailed (Epic 13, AC-SPN-09): a plain spawn meeting "duplicate session"
// whose end write fails leaves the row pending; find-missing marks it only past grace, with one sweep record.
func TestFindMissingHeldNameAfterSpawnEndWriteFailed(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv; it checks the
	// shared trail by literal row ids other find-missing tests reuse.
	e := newHeldEnv(t)
	id := heldID()
	e.rec.SeedSessions(e.socket, heldSession(heldRowName, "$4", tmux.Label{}, false))
	sessions := e.rec.Sessions(e.socket)
	mark := trailLen(t)
	t.Run("spawn, end write failing", func(t *testing.T) { // the trigger also fails the mark; it goes at this cleanup
		storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
		assertOneSentinel(t, e.spawnHeld(t, id, heldRowName, nil).err, api.ErrTmuxSessionConflict)
	})
	was := e.readRow(t, id)
	launch, _ := was.LaunchStartedAt.(int64)
	if was.State != store.StatePending || launch == 0 {
		t.Fatalf("row after the spawn: state %v, launch_started_at %v; want pending with a launch start", was.State, was.LaunchStartedAt)
	}
	calls := len(e.rec.SocketCalls())
	toInside := time.UnixMilli(launch).Add(fmGrace - time.Second).Sub(e.clock.Now())
	if toInside < 0 {
		t.Fatalf("clock %v is already past launch start + %v", e.clock.Now(), fmGrace-time.Second)
	}

	e.clock.Advance(toInside)
	res, err := e.c.FindMissing(context.Background())
	if err != nil || res.Count != 0 || res.Unverified != 0 {
		t.Errorf("FindMissing at 59 s = %+v, %v; want nothing marked or noted", res, err)
	}
	e.assertRowIs(t, id, "at 59 s", was)
	if n := len(e.rec.SocketCalls()); n != calls {
		t.Errorf("tmux calls at 59 s = %+v; want none", e.rec.SocketCalls()[calls:])
	}

	e.clock.Advance(2 * time.Second)
	sweepMark := trailLen(t)
	res, err = e.c.FindMissing(context.Background())
	if err != nil || !slices.Equal(res.IDs, []string{id}) {
		t.Fatalf("FindMissing at 61 s = %+v, %v; want %s marked", res, err, id)
	}
	if c := e.readRow(t, id); c.State != store.StateMissing {
		t.Errorf("state = %v; want missing", c.State)
	}
	r := hnRow{id: id, name: heldRowName}
	assertMarkTick(t, sweepMark, r, "tmux_name_held", "gone")
	var spawnRecs, sweepRecs []map[string]any
	for _, rec := range ptRecords(t, mark, "ad.launch.name_held", id) {
		if rec["source"] == "ad_spawn" {
			spawnRecs = append(spawnRecs, rec)
		} else {
			sweepRecs = append(sweepRecs, rec)
		}
	}
	if len(spawnRecs) != 1 {
		t.Errorf("ad_spawn records = %d; want the spawn's own one", len(spawnRecs))
	}
	assertSweepRecord(t, sweepRecs, r, e.socket, e.storeID,
		hnWant{holder: &sessions[0], carries: false, lookup: "gone", rowResult: "marked_missing"})
	if got := e.rec.Sessions(e.socket); !reflect.DeepEqual(got, sessions) {
		t.Errorf("holder changed:\nbefore %+v\nafter  %+v", sessions, got)
	}
}
