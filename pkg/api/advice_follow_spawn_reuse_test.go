package api_test

// advice_follow_spawn_reuse_test.go follows, literally, the next step that
// reuse's (spawn with the reuse opt-in) error descriptions prescribe or imply
// (b.fji, inventory A9-A13) on the kill fixture's reuse helpers
// (spawn_reuse_fixture_test.go); the shared follow helpers are in
// advice_follow_spawn_test.go and advice_follow_helpers_test.go.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// advSpawnGetThenAct follows a failed reuse p by reading its row with get and
// acting on the state: no row, a plain spawn of the id (p without the
// opt-in); pending, the wait adviceAwaitFinished makes, then p; finished, p.
// It returns the state get showed ("" for no row) and the retry's result.
func advSpawnGetThenAct(t *testing.T, e *killEnv, p api.SpawnParams) (string, api.SpawnResult, error) {
	t.Helper()
	c, _ := e.client(t)
	row, err := c.Get(p.ClaudeInstanceID)
	switch {
	case errors.Is(err, api.ErrSpawnNotFound):
		p.ReuseFinished = false
		res, err := c.Spawn(p)
		return "", res, err
	case err != nil:
		t.Fatalf("Get(%s): %v", p.ClaudeInstanceID, err)
	case row.State == store.StatePending:
		adviceAwaitFinished(t, c, e.clock, p.ClaudeInstanceID, nil)
	}
	res, err := c.Spawn(p)
	return row.State, res, err
}

// TestAdviceFollow_A9_ReuseLookupRetryLater: A9 "...nothing was done; retry later" at reuse's lookup. A retry while the condition holds
// gets the same refusal; past the window or bound with the session or agent still there, the own-id conflict (spawn's Go doc); cleared, it launches.
func TestAdviceFollow_A9_ReuseLookupRetryLater(t *testing.T) {
	cases := []struct {
		name    string
		words   string        // the case's own words before "nothing was done; retry later"
		limit   time.Duration // the window or bound the condition lasts at most (0: none)
		arrange func(t *testing.T, e *killEnv) (r reuseRow, clear func())
	}{
		{"still stopping, own session present", "appears to still be stopping", defWindow,
			func(t *testing.T, e *killEnv) (reuseRow, func()) {
				r := e.seedReusable(t, agentAlive, reuseRowSpec{Age: defWindow / 2, Bare: true})
				e.seedSession(t, &r.killRow, e.createdBefore(rlkSettled(e)))
				return r, func() {
					e.pc.Set(r.AgentPID, procfix.Gone())
					adviceEndSession(t, e.rec, r.Socket, r.Session.ID)
				}
			}},
		{"still stopping, no session", "appears to still be stopping", defWindow,
			func(t *testing.T, e *killEnv) (reuseRow, func()) {
				r := e.seedReusable(t, agentAlive, reuseRowSpec{Age: defWindow / 2, Bare: true})
				return r, func() { e.pc.Set(r.AgentPID, procfix.Gone()) }
			}},
		{"still starting", "appears to still be starting", defBound,
			func(t *testing.T, e *killEnv) (reuseRow, func()) {
				r := adviceReuseSettled(t, e)
				e.seedSession(t, &r.killRow, e.createdBefore(defBound/2))
				return r, func() { adviceEndSession(t, e.rec, r.Socket, r.Session.ID) }
			}},
		{"unreadable", "no answer within", 0,
			func(t *testing.T, e *killEnv) (reuseRow, func()) {
				r := adviceReuseSettled(t, e)
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 2}, tmux.CallLookup)
				return r, func() {}
			}},
		// Time does not clear this one: the same refusal comes back until a session holding the name goes.
		{"more than one session matches the name", "so the session holding it cannot be told", 0,
			func(t *testing.T, e *killEnv) (reuseRow, func()) {
				r := adviceReuseSettled(t, e)
				placed := e.placeHolder(t, r.killRow, holderAmbiguous, e.holderSessions(t, r.killRow, holderAmbiguous))
				return r, func() {
					for _, s := range placed {
						adviceEndSession(t, e.rec, r.Socket, s.ID)
					}
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, clear := tc.arrange(t, e)
			p := reuseParams(t, r, reuseRequest{})

			_, _, err := e.reuse(t, p)

			assertOneName(t, err, "ErrTmuxUnresponsive")
			adviceAssertPhrase(t, err, tc.words)
			adviceAssertPhrase(t, err, "nothing was done; retry later")
			e.clock.Advance(time.Second)
			before := e.snapshotReuse(t, r)
			if _, _, again := e.reuse(t, p); again == nil || again.Error() != err.Error() {
				t.Fatalf("retry while the condition holds = %v; want the same refusal %q", again, err)
			}
			e.assertWroteNothing(t, before)
			if tc.limit > 0 {
				e.clock.Advance(tc.limit)
				_, _, err := e.reuse(t, p)
				assertOneName(t, err, "ErrTmuxSessionConflict")
				adviceAssertPhrase(t, err, "this row's own id")
			}
			clear()
			calls := len(e.rec.SocketCalls())

			_, _, err = e.reuse(t, p)

			e.assertReused(t, r, reuseLife, calls, err)
		})
	}
}

// TestAdviceFollow_A10_ReuseLaunchTimeoutRetryAfterFinished: A10 "the row was reset; the row stays pending; do not retry until get shows the row ended or missing".
func TestAdviceFollow_A10_ReuseLaunchTimeoutRetryAfterFinished(t *testing.T) {
	e := newKillEnv(t)
	r := adviceReuseSettled(t, e)
	e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallCreate)
	p := reuseParams(t, r, reuseRequest{})

	_, _, err := e.reuse(t, p)

	assertOneName(t, err, "ErrTmuxUnresponsive")
	adviceAssertPhrase(t, err, "the row was reset; the row stays pending; do not retry until get shows the row ended or missing")
	reset := e.columns(t, r.ID).LifeNumber
	c, _ := e.client(t)
	adviceAwaitFinished(t, c, e.clock, r.ID, nil)
	calls := len(e.rec.SocketCalls())

	_, _, err = e.reuse(t, p)

	e.assertReused(t, r, reset, calls, err)
}

// advSpawnChangeAfterReset makes rs's next reset be followed by another
// versioned write to id's row (its parent id), so the restore does not apply.
func advSpawnChangeAfterReset(t *testing.T, e *killEnv, id string, rs *hookedReuseStore) {
	other := adviceOtherRow(t, e)
	rs.afterReset(func() {
		if err := e.st.SetParentID(id, other); err != nil {
			t.Errorf("SetParentID(%s): %v", id, err)
		}
	})
}

// advSpawnRestoreCase is one restore result of a failed reuse: its row
// sentence, how the restore is made to give it, the state get then shows (""
// = no row) and the life a retry that launches starts (nil: a fresh row's).
type advSpawnRestoreCase struct {
	name, sentence, shown string
	arrange               func(t *testing.T, e *killEnv, id string, rs *hookedReuseStore)
	life                  any
}

// advSpawnRestoreCases are the restore's four results.
func advSpawnRestoreCases() []advSpawnRestoreCase {
	return []advSpawnRestoreCase{
		{"restored", "the row was restored to its prior state, ended", store.StateEnded, nil, reuseLife + 1},
		{"changed", "the row changed after this spawn reset it and was left as it is", store.StatePending,
			advSpawnChangeAfterReset, reuseLife + 2},
		{"removed", "the row was removed after this spawn reset it, so nothing was restored", "",
			func(t *testing.T, e *killEnv, id string, _ *hookedReuseStore) {
				adviceOnceAfter(e.rec, tmux.CallCreate, func() {
					if err := e.st.DeleteSpawn(id); err != nil {
						t.Errorf("DeleteSpawn(%s): %v", id, err)
					}
				})
			}, nil},
		{"stays pending", "the row could not be restored and stays pending", store.StatePending,
			func(t *testing.T, e *killEnv, id string, _ *hookedReuseStore) {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
			}, reuseLife + 2},
	}
}

// TestAdviceFollow_A11_ReuseRestoreSentenceGetThenAct: A11 the restore's row sentence ("the row was restored to its prior state, <state>" /
// "... left as it is" / "... so nothing was restored" / "... stays pending"); get, then act on the state, launches.
func TestAdviceFollow_A11_ReuseRestoreSentenceGetThenAct(t *testing.T) {
	for _, tc := range advSpawnRestoreCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := adviceReuseSettled(t, e)
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
			rs := &hookedReuseStore{st: e.st}
			p := reuseParams(t, r, reuseRequest{})
			if !t.Run("trigger", func(t *testing.T) { // an injected write failure is removed when it ends
				if tc.arrange != nil {
					tc.arrange(t, e, r.ID, rs)
				}
				_, _, err := e.reuseWith(t, rs, p)
				assertOneName(t, err, "ErrTmuxSessionCreate")
				adviceAssertPhrase(t, err, tc.sentence)
			}) {
				t.FailNow()
			}

			shown, res, err := advSpawnGetThenAct(t, e, p)

			if shown != tc.shown {
				t.Errorf("get showed %q; want %q", shown, tc.shown)
			}
			advSpawnLaunched(t, e.dbPath, r.ID, tc.life, res, err)
		})
	}
}

// TestAdviceFollow_A12_ReuseHeldUnreadableRetryLater: A12 "<restore sentence>; retry later" after reuse's "duplicate session" whose
// re-lookup timed out; retried long after, with the name free and tmux answering.
func TestAdviceFollow_A12_ReuseHeldUnreadableRetryLater(t *testing.T) {
	const broken = "the row is still pending (the restore did not apply), so the opted-in retry collides with it (ErrInstanceIdCollision) " +
		"until find-missing marks it missing; \"retry later\" does not name that step"
	for _, tc := range advSpawnRestoreCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			s := e.ruhArrange(t, store.StateEnded, rlkSettled(e), heldSpec{Holder: holderNone,
				Relookup: tmuxfix.Script{Failure: tmux.FailTimeout}})
			rs := &hookedReuseStore{st: e.st}
			if !t.Run("trigger", func(t *testing.T) { // an injected write failure is removed when it ends
				if tc.arrange != nil {
					tc.arrange(t, e, s.r.ID, rs)
				}
				_, _, err := e.reuseWith(t, rs, s.p)
				assertOneName(t, err, "ErrTmuxUnresponsive")
				adviceAssertPhrase(t, err, tc.sentence+"; retry later")
			}) {
				t.FailNow()
			}
			e.clock.Advance(hnPast)
			e.removeHolders(t, s.sc)
			if tc.shown == store.StatePending {
				knownBrokenAdvice(t, "A12", broken)
			}

			res, _, err := e.reuse(t, s.p)

			advSpawnLaunched(t, e.dbPath, s.r.ID, tc.life, res, err)
		})
	}
}

// TestAdviceFollow_A13_ReuseLostRaceGetThenAct: A13 "the row changed or was removed after this spawn examined it and nothing was changed";
// get, then act on the state, launches.
func TestAdviceFollow_A13_ReuseLostRaceGetThenAct(t *testing.T) {
	cases := []struct {
		name, shown string
		change      func(t *testing.T, e *killEnv, id string) // between the reuse's read and its reset
		life        any
	}{
		{"removed", "", func(t *testing.T, e *killEnv, id string) {
			if err := e.st.DeleteSpawn(id); err != nil {
				t.Errorf("DeleteSpawn(%s): %v", id, err)
			}
		}, nil},
		{"changed, still finished", store.StateEnded, func(t *testing.T, e *killEnv, id string) {
			if err := e.st.SetParentID(id, adviceOtherRow(t, e)); err != nil {
				t.Errorf("SetParentID(%s): %v", id, err)
			}
		}, reuseLife + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := adviceReuseSettled(t, e)
			rs := &hookedReuseStore{st: e.st}
			rs.beforeReset(func() { tc.change(t, e, r.ID) })
			p := reuseParams(t, r, reuseRequest{})

			_, _, err := e.reuseWith(t, rs, p)

			assertOneName(t, err, "ErrInstanceIdCollision")
			adviceAssertPhrase(t, err, "the row changed or was removed after this spawn examined it and nothing was changed")
			shown, res, err := advSpawnGetThenAct(t, e, p)
			if shown != tc.shown {
				t.Errorf("get showed %q; want %q", shown, tc.shown)
			}
			advSpawnLaunched(t, e.dbPath, r.ID, tc.life, res, err)
		})
	}
}
