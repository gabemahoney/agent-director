package api_test

// spawn_reuse_concurrency_test.go covers competing writes to one finished id
// (SR-10.5, SR-8.6, SR-20.6; AC-REUSE-10, 11, 18, AC-RES-08): two reuses, and
// reuse against resume in each order, interleaved through the stores' hooks:
// the first write to apply launches, the other creates and writes nothing.

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rcoAs runs fn with AGENT_DIRECTOR_INSTANCE_ID (the parent id a launch
// records) set to parent, then restores it (reuseParams set it with t.Setenv).
func rcoAs(parent string, fn func()) {
	prev := os.Getenv("AGENT_DIRECTOR_INSTANCE_ID")
	os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", parent)     //nolint:errcheck
	defer os.Setenv("AGENT_DIRECTOR_INSTANCE_ID", prev) //nolint:errcheck
	fn()
}

// rcoParent seeds a finished row a caller can name as its parent and returns its id.
func (e *killEnv) rcoParent(t *testing.T) string {
	t.Helper()
	return e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
}

// rcoSeed seeds a reusable row whose agent is gone and which ended long
// before (a lookup reads Gone with the name free) and returns it with its
// snapshot as seeded.
func (e *killEnv) rcoSeed(t *testing.T) (reuseRow, writesSnapshot) {
	t.Helper()
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
	return r, e.snapshotReuse(t, r)
}

// rcoAssertOneLaunch fails unless exactly one create ran, under name, and
// r's row describes it: its token, name, life and parent ("" = NULL), with
// that create's session the only one on r's socket labelled with r's id.
func (e *killEnv) rcoAssertOneLaunch(t *testing.T, r reuseRow, name string, life int64, parent string) {
	t.Helper()
	creates := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(creates) != 1 {
		t.Fatalf("creates = %+v; want exactly one", creates)
	}
	c := creates[0]
	if c.Target != name {
		t.Errorf("create's name = %q; want %q", c.Target, name)
	}
	cols := e.columns(t, r.ID)
	if cols.LaunchToken != c.Token || cols.TmuxSessionName != c.Target || cols.LifeNumber != life ||
		cols.ParentID != pendRowNullOr(parent) {
		t.Errorf("row {token %v, name %v, life %v, parent %v}; want the launch's {%s, %s, %d, %q}",
			cols.LaunchToken, cols.TmuxSessionName, cols.LifeNumber, cols.ParentID, c.Token, c.Target, life, parent)
	}
	var tokens []string
	for _, s := range e.rec.Sessions(r.Socket) {
		if s.Label.Kind == tmux.LabelValid && s.Label.InstanceID == r.ID {
			tokens = append(tokens, s.Label.Token)
		}
	}
	if !slices.Equal(tokens, []string{c.Token}) {
		t.Errorf("sessions labelled with %s carry tokens %q; want only the launch's %q", r.ID, tokens, c.Token)
	}
}

// rcoAssertHistory fails unless id's history over every life is want (rchgHistory's keys).
func (e *killEnv) rcoAssertHistory(t *testing.T, id string, want []string) {
	t.Helper()
	if got, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id); err != nil || !slices.Equal(rchgHistory(got), want) {
		t.Errorf("history of %s = %q (%v); want %q, nothing lost or duplicated", id, rchgHistory(got), err, want)
	}
}

// rcoAssertReuseWon fails unless r's row is one reuse's launch under name
// with parent: life reuseLife+1, the old session archived once and one
// ad.spawn.reused.
func (e *killEnv) rcoAssertReuseWon(t *testing.T, r reuseRow, name, parent string, seeded writesSnapshot) {
	t.Helper()
	e.rcoAssertOneLaunch(t, r, name, reuseLife+1, parent)
	e.rcoAssertHistory(t, r.ID, rchgArchived(seeded))
	if n := len(pendTrail(t, "ad.spawn.reused", r.ID)); n != 1 {
		t.Errorf("ad.spawn.reused lines = %d; want the winner's one", n)
	}
}

// TestSpawnReuseTwoReusesOneLaunch: of two reuses of one finished id, the
// first to reset launches; the other, examined before that reset, gets the
// lost race's ErrInstanceIdCollision, creates nothing and writes nothing. A
// reuse after the winner meets the live-row collision (TestSpawnExistingRowCollides).
func TestSpawnReuseTwoReusesOneLaunch(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with os.Setenv around a call; it checks every record
	// written to the shared trail since its mark.
	cases := []struct {
		name  string
		at    func(*hookedReuseStore, func()) // where the winner runs inside the loser
		calls int                             // the loser's tmux calls after the winner returned (a lookup)
	}{
		{"winner between the loser's read and lookup", (*hookedReuseStore).afterRead, 1},
		{"winner between the loser's lookup and reset", (*hookedReuseStore).beforeReset, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, seeded := e.rcoSeed(t)
			winnerParent, loserParent := e.rcoParent(t), e.rcoParent(t)
			pl, pw := reuseParams(t, r, reuseRequest{Parent: loserParent}), reuseParams(t, r, reuseRequest{Parent: winnerParent})
			var winErr error
			var after writesSnapshot
			w := &hookedReuseStore{st: e.st}
			tc.at(w, func() {
				rcoAs(winnerParent, func() { _, _, winErr = e.reuse(t, pw) })
				after = e.snapshotReuse(t, r)
			})

			var err error
			rcoAs(loserParent, func() { _, _, err = e.reuseWith(t, w, pl) })

			if winErr != nil {
				t.Fatalf("winning reuse: %v", winErr)
			}
			if !errors.Is(err, spawn.ErrInstanceIdCollision) {
				t.Fatalf("losing reuse = %v; want ErrInstanceIdCollision", err)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescReuseLostRace(r.ID))
			if n := len(e.rec.SocketCalls()) - after.calls; n != tc.calls {
				t.Errorf("losing reuse made %d tmux calls after the winner; want %d", n, tc.calls)
			}
			e.assertWroteNothing(t, after, exceptTrust)
			e.rcoAssertReuseWon(t, r, r.Name, winnerParent, seeded)
		})
	}
}

// rrsScene is one reuse-against-resume race: the row, the reuse's request and each caller's
// parent id.
type rrsScene struct {
	e                         *killEnv
	r                         reuseRow
	p                         api.SpawnParams
	reuseParent, resumeParent string
}

// reuse runs the reuse through Client.Spawn, or the seam with w when given.
func (s *rrsScene) reuse(t *testing.T, w *hookedReuseStore) (err error) {
	t.Helper()
	rcoAs(s.reuseParent, func() {
		if w == nil {
			_, _, err = s.e.reuse(t, s.p)
		} else {
			_, _, err = s.e.reuseWith(t, w, s.p)
		}
	})
	return err
}

// resume runs resume on the row through w (a plain wrapper when nil).
func (s *rrsScene) resume(w *hookedResumeStore) (err error) {
	if w == nil {
		w = &hookedResumeStore{st: s.e.st}
	}
	rcoAs(s.resumeParent, func() { _, err = s.e.resumeWith(w, s.r.ID) })
	return err
}

// snap takes the row's writesSnapshot for the losing verb, its trust file
// left to the winner's pre-trust (checked with exceptTrust).
func (s *rrsScene) snap(t *testing.T, verb string) writesSnapshot {
	t.Helper()
	return s.e.snapshotWrites(t, verb, s.r.ID, s.r.Trust, s.r.Socket)
}

// loser fails unless, since before, the losing call made calls tmux calls
// and wrote nothing.
func (s *rrsScene) loser(t *testing.T, before writesSnapshot, calls int) {
	t.Helper()
	if n := len(s.e.rec.SocketCalls()) - before.calls; n != calls {
		t.Errorf("losing %s made %d tmux calls; want %d", before.verb, n, calls)
	}
	s.e.assertWroteNothing(t, before, exceptTrust)
}

// TestSpawnReuseAgainstResume: in each order, with either name, at most one
// create runs and the loser writes nothing (parent id included): a losing
// reuse gets ErrInstanceIdCollision, a losing resume ErrSpawnNotResumable.
func TestSpawnReuseAgainstResume(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with os.Setenv around a call; it checks every record
	// written to the shared trail since its mark.
	cases := []struct {
		name      string
		reuseWins bool
		lostRace  bool // the loser's lost-race refusal; else the live-row collision
		run       func(t *testing.T, s *rrsScene) (reuseErr, resumeErr error)
	}{
		{name: "resume launches between the reuse's read and lookup", lostRace: true,
			run: func(t *testing.T, s *rrsScene) (reuseErr, resumeErr error) {
				var before writesSnapshot
				w := &hookedReuseStore{st: s.e.st}
				w.afterRead(func() { resumeErr, before = s.resume(nil), s.snap(t, "spawn") })
				reuseErr = s.reuse(t, w)
				s.loser(t, before, 1)
				return reuseErr, resumeErr
			}},
		{name: "resume launches between the reuse's lookup and reset", lostRace: true,
			run: func(t *testing.T, s *rrsScene) (reuseErr, resumeErr error) {
				var before writesSnapshot
				w := &hookedReuseStore{st: s.e.st}
				w.beforeReset(func() { resumeErr, before = s.resume(nil), s.snap(t, "spawn") })
				reuseErr = s.reuse(t, w)
				s.loser(t, before, 0)
				return reuseErr, resumeErr
			}},
		{name: "resume's move lands before the reuse's read",
			run: func(t *testing.T, s *rrsScene) (reuseErr, resumeErr error) {
				w := &hookedResumeStore{st: s.e.st}
				w.afterMove(func() {
					before := s.snap(t, "spawn")
					reuseErr = s.reuse(t, nil)
					s.loser(t, before, 0)
				})
				return reuseErr, s.resume(w)
			}},
		{name: "reuse launches between the resume's read and lookup", reuseWins: true, lostRace: true,
			run: func(t *testing.T, s *rrsScene) (reuseErr, resumeErr error) {
				var before writesSnapshot
				w := &hookedResumeStore{st: s.e.st}
				w.afterGet(func() { reuseErr, before = s.reuse(t, nil), s.snap(t, "resume") })
				resumeErr = s.resume(w)
				s.loser(t, before, 1)
				return reuseErr, resumeErr
			}},
		{name: "reuse launches between the resume's lookup and move", reuseWins: true, lostRace: true,
			run: func(t *testing.T, s *rrsScene) (reuseErr, resumeErr error) {
				var before writesSnapshot
				ran := false // stays registered: the reuse's own lookup must not rerun it
				s.e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
					if !ran {
						ran = true
						reuseErr, before = s.reuse(t, nil), s.snap(t, "resume")
					}
				})
				resumeErr = s.resume(nil)
				s.loser(t, before, 0)
				return reuseErr, resumeErr
			}},
	}
	for _, tc := range cases {
		for _, newName := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/recorded name", true: "/other name"}[newName], func(t *testing.T) {
				e := newKillEnv(t)
				r, seeded := e.rcoSeed(t)
				s := &rrsScene{e: e, r: r, reuseParent: e.rcoParent(t), resumeParent: e.rcoParent(t)}
				name := r.Name
				if newName {
					name = r.ID + "-new"
				}
				s.p = reuseParams(t, r, reuseRequest{Name: name, Parent: s.reuseParent})

				reuseErr, resumeErr := tc.run(t, s)

				if tc.reuseWins {
					if reuseErr != nil {
						t.Fatalf("winning reuse: %v", reuseErr)
					}
					if !errors.Is(resumeErr, api.ErrSpawnNotResumable) {
						t.Fatalf("losing resume = %v; want ErrSpawnNotResumable", resumeErr)
					}
					apitest.AssertDescription(t, resumeErr.Error(), apitest.DescResumeLostRace())
					e.rcoAssertReuseWon(t, r, name, s.reuseParent, seeded)
					if n := pendMoved(t, r.ID); n != 0 {
						t.Errorf("ad.resume.moved_to_pending lines = %d; want none", n)
					}
					return
				}
				if resumeErr != nil {
					t.Fatalf("winning resume: %v", resumeErr)
				}
				if !errors.Is(reuseErr, spawn.ErrInstanceIdCollision) {
					t.Fatalf("losing reuse = %v; want ErrInstanceIdCollision", reuseErr)
				}
				if tc.lostRace {
					apitest.AssertDescription(t, reuseErr.Error(), apitest.DescReuseLostRace(r.ID))
				}
				e.rcoAssertOneLaunch(t, r, r.Name, reuseLife, s.resumeParent)
				e.rcoAssertHistory(t, r.ID, rchgHistory(seeded.history))
				if n := len(pendTrail(t, "ad.spawn.reused", r.ID)); n != 0 {
					t.Errorf("ad.spawn.reused lines = %d; want none", n)
				}
			})
		}
	}
}
