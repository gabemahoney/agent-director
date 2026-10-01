package api_test

// spawn_reuse_resume_race_test.go covers reuse against resume on one finished
// id (SR-10.5, SR-8.6, SR-20.6; AC-REUSE-11, AC-REUSE-18, AC-RES-08), in each
// order and with the reuse requesting the recorded name or another: the call
// whose write applies first launches, the other creates and writes nothing,
// and the row describes the one launch. The shared assertions are in
// spawn_reuse_concurrency_test.go.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rrsScene is one race: the row, the reuse's request and each caller's
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
		{name: "the resumed agent reported in before the reuse",
			run: func(t *testing.T, s *rrsScene) (reuseErr, resumeErr error) {
				if resumeErr = s.resume(nil); resumeErr == nil {
					sid := strings.TrimSuffix(filepath.Base(s.r.JSONLPath), ".jsonl")
					if got := apitest.ApplyAgentHook(t, s.e.dbPath, s.r.ID, "SessionStart", sid,
						apitest.HookTranscript(s.r.JSONLPath, true)); !got.Applied {
						t.Fatalf("SessionStart from the resumed agent = %+v; want applied", got)
					}
				}
				before := s.snap(t, "spawn")
				reuseErr = s.reuse(t, nil)
				s.loser(t, before, 0)
				return reuseErr, resumeErr
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
				r, session, history := e.rcoSeed(t, reuseRowSpec{})
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
					e.rcoAssertReuseWon(t, r, name, s.reuseParent, session, history)
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
				e.rcoAssertHistory(t, r.ID, history, "", 0)
				if n := len(pendTrail(t, "ad.spawn.reused", r.ID)); n != 0 {
					t.Errorf("ad.spawn.reused lines = %d; want none", n)
				}
			})
		}
	}
}
