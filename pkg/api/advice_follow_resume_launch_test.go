package api_test

// advice_follow_resume_launch_test.go holds b.fji's literal-follow tests for
// resume's lookup and launch errors (advice inventory B7-B10): the pre-launch
// "retry later" refusals, the launch timeout, and every restore sentence after
// a failed launch, alone and after "duplicate session". Helpers are in
// advice_follow_resume_test.go.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The advice B7 and B8 pin, exactly as the description words it.
const (
	advResumeRetryLater = "nothing was done; retry later"
	advResumeLaunchRule = "the session may have been created; the row stays pending; do not retry until get shows the row ended or missing"
)

// advResumeConditionEnds ends what kept r from launching: its agent process exits
// and every session under its name, its own included, goes.
func advResumeConditionEnds(t *testing.T, e *killEnv, r resumeRow) {
	t.Helper()
	if r.AgentPID > 0 {
		e.pc.Set(r.AgentPID, procfix.Gone())
	}
	for _, s := range e.rec.Sessions(r.Socket) {
		if s.Name == storedFormOf(r.Name) || s.ID == r.Session.ID {
			if err := e.rec.KillSessionID(r.Socket, s.ID); err != nil {
				t.Fatalf("KillSessionID(%s): %v", s.ID, err)
			}
		}
	}
}

// TestAdviceFollow_B7_PreLaunchRetryLater: wait past the window and the bound, re-issue; it launches, else the documented refusal.
// B7: "nothing was done; retry later"
func TestAdviceFollow_B7_PreLaunchRetryLater(t *testing.T) {
	stopping := func(session bool) func(*testing.T, *killEnv) resumeRow {
		return func(t *testing.T, e *killEnv) resumeRow {
			r := e.seedResumable(t, 0, agentAlive)
			if session {
				e.seedSession(t, &r.killRow, e.createdBefore(rlkSettled(e)))
			}
			return r
		}
	}
	starting := func(t *testing.T, e *killEnv) resumeRow {
		r := e.seedResumable(t, rlkSettled(e), agentGone)
		e.seedSession(t, &r.killRow, e.createdBefore(0))
		return r
	}
	lookupFails := func(s tmuxfix.Script) func(*testing.T, *killEnv) resumeRow {
		return func(t *testing.T, e *killEnv) resumeRow {
			r := e.seedResumable(t, rlkSettled(e), agentGone)
			s.Times = 1
			e.rec.Script(r.Socket, s, tmux.CallLookup)
			return r
		}
	}
	ambiguous := func(t *testing.T, e *killEnv) resumeRow {
		r := e.seedResumable(t, rlkSettled(e), agentGone)
		e.seedHolder(t, r.killRow, holderAmbiguous)
		return r
	}
	ownOld := func(noSession bool) func(*killEnv, resumeRow) apitest.DescCase {
		return func(e *killEnv, r resumeRow) apitest.DescCase {
			return apitest.DescOwnOldSession(rlkStarting(e, r, noSession))
		}
	}
	cases := []struct {
		name string
		seed func(*testing.T, *killEnv) resumeRow       // the row with the condition in place
		ends bool                                       // the condition ends during the wait: the retry launches
		held func(*killEnv, resumeRow) apitest.DescCase // else the retry's own-id conflict; nil: the same refusal
	}{
		{"still stopping, session up: the agent stops", stopping(true), true, nil},
		{"still stopping, session up: it keeps running", stopping(true), false, ownOld(false)},
		{"still stopping, no session: the agent process stops", stopping(false), true, nil},
		{"still stopping, no session: the agent process keeps running", stopping(false), false, ownOld(true)},
		{"still starting: the session goes", starting, true, nil},
		{"still starting: the session keeps running", starting, false, ownOld(false)},
		{"lookup timed out", lookupFails(tmuxfix.Script{Failure: tmux.FailTimeout}), true, nil},
		{"lookup reply not recognised", lookupFails(tmuxfix.Script{Failure: tmux.FailUnrecognized,
			FirstLine: "adv: unexpected reply", ExitStatus: 1, HadStdout: true}), true, nil},
		{"two sessions match the name: they go", ambiguous, true, nil},
		{"two sessions match the name: they stay", ambiguous, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := tc.seed(t, e)
			before := e.snapshotResume(t, r)

			_, refused := e.resume(r.ID)

			adviceAssertAdvice(t, refused, api.ErrTmuxUnresponsive, advResumeRetryLater)
			e.assertResumeWroteNothing(t, before)
			e.clock.Advance(rlkSettled(e))
			if tc.ends {
				advResumeConditionEnds(t, e, r)
				advResumeLaunches(t, e, r.ID)
				return
			}
			before = e.snapshotResume(t, r)
			_, err := e.resume(r.ID)
			if tc.held == nil {
				if err == nil || err.Error() != refused.Error() {
					t.Errorf("retry while the condition holds = %v; want the same refusal %q", err, refused)
				}
			} else {
				assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
				if err != nil {
					apitest.AssertDescription(t, err.Error(), tc.held(e, r), r.Token, e.storeID)
				}
			}
			e.assertResumeWroteNothing(t, before)
		})
	}
}

// TestAdviceFollow_B8_LaunchTimeoutWaitForGet: no retry while get shows pending; find-missing past grace, then resume.
// B8: "the session may have been created; the row stays pending; do not retry until get shows the row ended or missing"
func TestAdviceFollow_B8_LaunchTimeoutWaitForGet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script tmuxfix.Script
	}{
		{"create timed out", tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}},
		{"non-zero exit, reply not recognised", tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, HadStdout: true, Times: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			c, _ := e.client(t)
			r := e.seedResumable(t, rlkSettled(e), agentGone)
			e.rec.Script(r.Socket, tc.script, tmux.CallCreate)

			_, err := e.resume(r.ID)

			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, advResumeLaunchRule)
			advResumeAssertState(t, c, r.ID, store.StatePending)
			noRetry := func() {} // inside the grace period get shows pending: the caller does not retry
			if st := adviceAwaitFinished(t, c, e.clock, r.ID, noRetry); st != store.StateMissing {
				t.Fatalf("get %s past the pending grace period: state %q; want missing", r.ID, st)
			}
			advResumeLaunches(t, e, r.ID)
		})
	}
}

// advResumeRestore is one result of the restore after a failed resume launch:
// arrange makes it happen (through the resume's store, or once on the create),
// sentence is the error's row sentence, after the state get then shows ("":
// no row), and heldBroken why "retry later" after it fails (B10).
type advResumeRestore struct {
	name       string
	arrange    func(t *testing.T, e *killEnv, s *hookedResumeStore, id, other string)
	sentence   string
	after      string
	heldBroken string
}

// advResumeOnCreate runs write once, when the next create returns.
func advResumeOnCreate(write func(e *killEnv, id, other string) error) func(*testing.T, *killEnv, *hookedResumeStore, string, string) {
	return func(t *testing.T, e *killEnv, _ *hookedResumeStore, id, other string) {
		adviceOnceAfter(e.rec, tmux.CallCreate, func() {
			if err := write(e, id, other); err != nil {
				t.Errorf("write as the create returned: %v", err)
			}
		})
	}
}

// advResumeRestores is every restore result for a row whose prior state is prior.
func advResumeRestores(prior string) []advResumeRestore {
	const stillPending = "the row stays pending, so the retried resume is refused as a launch in progress (ErrSpawnNotResumable) until find-missing marks it missing, which \"retry later\" does not say"
	return []advResumeRestore{
		{"restored", nil, "the row was restored to its prior state, " + prior, prior, ""},
		{"changed", advResumeOnCreate(func(e *killEnv, id, other string) error { return e.st.SetParentID(id, other) }),
			"the row changed after resume moved it to pending and was left as it is", store.StatePending, stillPending},
		{"removed", advResumeOnCreate(func(e *killEnv, id, _ string) error { return e.st.DeleteSpawn(id) }),
			"the row was removed after resume moved it to pending, so nothing was restored", "",
			"the row is gone, so every retried resume returns ErrSpawnNotFound"},
		{"stays pending", func(_ *testing.T, _ *killEnv, s *hookedResumeStore, _, _ string) { s.failRestore(nil) },
			"the row could not be restored and stays pending", store.StatePending, stillPending},
	}
}

// TestAdviceFollow_B9_RestoreSentenceNextStep: per restore sentence after a failed launch, get, then act on the state.
// B9: "the row was restored to its prior state, <state>" / "the row changed after resume moved it to pending and was left as it is" / "the row was removed after resume moved it to pending, so nothing was restored" / "the row could not be restored and stays pending"
func TestAdviceFollow_B9_RestoreSentenceNextStep(t *testing.T) {
	triggers := []struct {
		name    string
		failure tmux.Failure
		want    error
		prior   string
	}{
		{"tmux not available", tmux.FailUnavailable, api.ErrTmuxNotAvailable, store.StateEnded},
		{"session creation failed", tmux.FailUnrecognized, api.ErrTmuxSessionCreate, store.StateMissing},
	}
	for _, tr := range triggers {
		for _, rs := range advResumeRestores(tr.prior) {
			t.Run(tr.name+"/"+rs.name, func(t *testing.T) {
				e := newKillEnv(t)
				c, _ := e.client(t)
				other := adviceOtherRow(t, e)
				spec := e.resumableSpec(rlkSettled(e), agentGone)
				spec.State = tr.prior
				r := e.seedResumableRow(t, spec)
				s := &hookedResumeStore{st: e.st}
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tr.failure, ExitStatus: 1, Times: 1}, tmux.CallCreate)
				if rs.arrange != nil {
					rs.arrange(t, e, s, r.ID, other)
				}

				_, err := e.resumeWith(s, r.ID)

				adviceAssertAdvice(t, err, tr.want, rs.sentence)
				advResumeAssertState(t, c, r.ID, rs.after)
				advResumeNextStep(t, e, c, r.ID)
			})
		}
	}
}

// TestAdviceFollow_B10_HeldRetryLater: after "duplicate session" (re-lookup timed out) the holder goes; wait, re-issue.
// B10: "<restore sentence>; retry later"
func TestAdviceFollow_B10_HeldRetryLater(t *testing.T) {
	for _, rs := range advResumeRestores(store.StateEnded) {
		t.Run(rs.name, func(t *testing.T) {
			e := newKillEnv(t)
			c, _ := e.client(t)
			other := adviceOtherRow(t, e)
			r := e.seedHeldResumable(t, rlkSettled(e), agentGone)
			s := &hookedResumeStore{st: e.st}
			sc := e.arrangeHeld(t, r, heldSpec{Holder: holderNone, Relookup: tmuxfix.Script{Failure: tmux.FailTimeout}})
			if rs.arrange != nil {
				rs.arrange(t, e, s, r.ID, other)
			}

			_, err := e.resumeWith(s, r.ID)

			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, rs.sentence+"; retry later")
			advResumeAssertState(t, c, r.ID, rs.after)
			e.removeHolders(t, sc)
			e.clock.Advance(rlkSettled(e))
			if rs.heldBroken != "" {
				knownBrokenAdvice(t, "B10", rs.heldBroken)
			}
			advResumeLaunches(t, e, r.ID)
		})
	}
}
