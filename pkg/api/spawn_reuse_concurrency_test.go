package api_test

// spawn_reuse_concurrency_test.go covers competing writes to one finished id
// against a reuse (SR-10.5, SR-20.6; AC-REUSE-10, AC-REUSE-15): two reuses,
// interleaved through the reuse-store seam's hooks and truly concurrent, and
// expire deleting the row after the reuse read it. Reuse against resume is in
// spawn_reuse_resume_race_test.go. On the reuse fixture
// (spawn_reuse_fixture_test.go).

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rcoRounds is how many rounds the truly concurrent case runs.
const rcoRounds = 8

// rcoAs runs fn with AGENT_DIRECTOR_INSTANCE_ID, the parent id a launch
// records, set to parent, then puts the previous value back (the test set
// it first through reuseParams' t.Setenv, whose cleanup restores it).
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
// before (a lookup reads Gone with the name free); it returns the row, its
// session id and its history as seeded.
func (e *killEnv) rcoSeed(t *testing.T, spec reuseRowSpec) (reuseRow, string, []apitest.HistoryEntry) {
	t.Helper()
	spec.Age = rceSettled(e)
	r := e.seedReusable(t, agentGone, spec)
	history, err := apitest.ReadSessionHistoryAllLives(e.dbPath, r.ID)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", r.ID, err)
	}
	session, _ := e.columns(t, r.ID).ClaudeSessionID.(string)
	return r, session, history
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

// rcoAssertHistory fails unless id's history over every life is before with
// nothing lost or duplicated, plus exactly one entry of session in life when
// session is not "" (a reset's archive), else nothing more.
func (e *killEnv) rcoAssertHistory(t *testing.T, id string, before []apitest.HistoryEntry, session string, life int64) {
	t.Helper()
	got, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", id, err)
	}
	type key struct {
		session string
		life    int64
	}
	left := map[key]int{}
	for _, h := range got {
		left[key{h.ClaudeSessionID, h.LifeNumber}]++
	}
	var want []key
	if session != "" {
		want = append(want, key{session, life})
	}
	for _, h := range before {
		want = append(want, key{h.ClaudeSessionID, h.LifeNumber})
	}
	for _, k := range want {
		if left[k] == 0 {
			t.Errorf("history of %s lacks %+v; want it once", id, k)
		}
		left[k]--
	}
	for k, n := range left {
		if n > 0 {
			t.Errorf("history of %s has %d unexpected %+v entries; want each entry once (got %+v)", id, n, k, got)
		}
	}
}

// rcoAssertReuseWon fails unless r's row is one reuse's launch under name
// with parent: life reuseLife+1, the old session archived once and one
// ad.spawn.reused.
func (e *killEnv) rcoAssertReuseWon(t *testing.T, r reuseRow, name, parent, session string, history []apitest.HistoryEntry) {
	t.Helper()
	e.rcoAssertOneLaunch(t, r, name, reuseLife+1, parent)
	e.rcoAssertHistory(t, r.ID, history, session, reuseLife)
	if n := len(pendTrail(t, "ad.spawn.reused", r.ID)); n != 1 {
		t.Errorf("ad.spawn.reused lines = %d; want the winner's one", n)
	}
}

// TestSpawnReuseTwoReusesOneLaunch: of two reuses of one finished id, the
// first to reset launches; the other, examined before that reset (or after
// it), gets ErrInstanceIdCollision, creates nothing and writes nothing.
func TestSpawnReuseTwoReusesOneLaunch(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with os.Setenv around a call; it checks every record
	// written to the shared trail since its mark.
	cases := []struct {
		name     string
		at       func(*hookedReuseStore, func()) // where the winner runs inside the loser; nil: before it
		newName  bool                            // the loser requests a name other than the winner's
		lostRace bool                            // the loser's lost-race refusal; else the live-row collision
		calls    int                             // the loser's tmux calls after the winner returned (a lookup)
	}{
		{"winner between the loser's read and lookup", (*hookedReuseStore).afterRead, false, true, 1},
		{"winner between the loser's read and lookup, other name", (*hookedReuseStore).afterRead, true, true, 1},
		{"winner between the loser's lookup and reset", (*hookedReuseStore).beforeReset, false, true, 0},
		{"winner between the loser's lookup and reset, other name", (*hookedReuseStore).beforeReset, true, true, 0},
		{"winner before the loser's read", nil, false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, session, history := e.rcoSeed(t, reuseRowSpec{})
			winnerParent, loserParent := e.rcoParent(t), e.rcoParent(t)
			lq := reuseRequest{Parent: loserParent}
			if tc.newName {
				lq.Name = r.ID + "-new"
			}
			pl, pw := reuseParams(t, r, lq), reuseParams(t, r, reuseRequest{Parent: winnerParent})
			var winErr error
			var after writesSnapshot
			winner := func() {
				rcoAs(winnerParent, func() { _, _, winErr = e.reuse(t, pw) })
				after = e.snapshotReuse(t, r)
			}
			w := &hookedReuseStore{st: e.st}
			if tc.at == nil {
				winner()
			} else {
				tc.at(w, winner)
			}

			var err error
			rcoAs(loserParent, func() { _, _, err = e.reuseWith(t, w, pl) })

			if winErr != nil {
				t.Fatalf("winning reuse: %v", winErr)
			}
			if !errors.Is(err, spawn.ErrInstanceIdCollision) {
				t.Fatalf("losing reuse = %v; want ErrInstanceIdCollision", err)
			}
			if tc.lostRace {
				apitest.AssertDescription(t, err.Error(), apitest.DescReuseLostRace(r.ID))
			}
			if n := len(e.rec.SocketCalls()) - after.calls; n != tc.calls {
				t.Errorf("losing reuse made %d tmux calls after the winner; want %d", n, tc.calls)
			}
			e.assertWroteNothing(t, after, exceptTrust)
			e.rcoAssertReuseWon(t, r, r.Name, winnerParent, session, history)
		})
	}
}

// TestSpawnReuseTwoConcurrentReuses: two reuses of one finished id released
// together, every round: one launches, the other gets ErrInstanceIdCollision;
// one create, one session and one archive entry.
func TestSpawnReuseTwoConcurrentReuses(t *testing.T) {
	t.Parallel()
	for round := range rcoRounds {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
			e := newKillEnv(t)
			r, session, history := e.rcoSeed(t, reuseRowSpec{})
			var clients [2]*api.Client
			var params [2]api.SpawnParams
			for i := range clients {
				clients[i], _ = e.client(t)
				params[i] = reuseParams(t, r, reuseRequest{})
			}
			var errs [2]error
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range clients {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, errs[i] = clients[i].Spawn(params[i])
				}()
			}

			close(start)
			wg.Wait()

			won := 0
			for _, err := range errs {
				switch {
				case err == nil:
					won++
				case !errors.Is(err, spawn.ErrInstanceIdCollision):
					t.Errorf("losing reuse = %v; want ErrInstanceIdCollision", err)
				}
			}
			if won != 1 {
				t.Fatalf("reuses = %v; want exactly one success", errs)
			}
			e.rcoAssertReuseWon(t, r, r.Name, "", session, history)
		})
	}
}

// TestSpawnReuseRowExpiredAfterRead: expire deleting the row after the reuse
// read it, before its lookup or its reset, makes the reset find it removed:
// ErrInstanceIdCollision, no create, no ad.spawn.reused (AC-REUSE-15).
func TestSpawnReuseRowExpiredAfterRead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		at   func(*hookedReuseStore, func())
	}{
		{"before the lookup", (*hookedReuseStore).afterRead},
		{"before the reset", (*hookedReuseStore).beforeReset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r, _, _ := e.rcoSeed(t, reuseRowSpec{Bare: true})
			w := &hookedReuseStore{st: e.st}
			var expired api.ExpireResult
			var expErr error
			tc.at(w, func() { expired, _, expErr = e.expire(olderThan(0)) })
			p, mark := reuseParams(t, r, reuseRequest{}), trailMark(t)

			_, _, err := e.reuseWith(t, w, p)

			if expErr != nil || !slices.Contains(expired.IDs, r.ID) {
				t.Fatalf("expire = %+v, %v; want %s deleted", expired, expErr, r.ID)
			}
			if !errors.Is(err, spawn.ErrInstanceIdCollision) {
				t.Fatalf("reuse = %v; want ErrInstanceIdCollision", err)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescReuseLostRace(r.ID))
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
				t.Errorf("creates = %d; want none", n)
			}
			if _, err := apitest.ReadSpawnColumns(e.dbPath, r.ID); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("ReadSpawnColumns = %v; want the row still deleted", err)
			}
			if got := ptRecords(t, mark, "ad.spawn.reused", r.ID); len(got) != 0 {
				t.Errorf("ad.spawn.reused = %v; want none", got)
			}
		})
	}
}
