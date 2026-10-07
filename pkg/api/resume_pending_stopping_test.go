package api_test

// resume_pending_stopping_test.go covers the lookup half of AC-RES-13 (SR-8.4,
// SR-8.5 "Consequences that must hold", SR-4.2 step 1, SR-22.9): a resumed
// agent reports in and ends while its labelled session still runs, so an
// immediate second resume is decided by the lookup and refused as still
// stopping, writing nothing; once the session and the agent process are gone
// the second resume launches. On the kill fixture's resume extension
// (resume_lookup_fixture_test.go) against a real store; the second resume's
// move after the session went is resume_pending_relife_test.go.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// stoppingOwnSession returns the session named r.Name on r's socket, failing
// the test unless exactly one is listed.
func stoppingOwnSession(t *testing.T, e *killEnv, r resumeRow) tmuxfix.SeedSession {
	t.Helper()
	var own []tmuxfix.SeedSession
	for _, s := range e.rec.Sessions(r.Socket) {
		if s.Name == r.Name {
			own = append(own, s)
		}
	}
	if len(own) != 1 {
		t.Fatalf("sessions named %q on %s = %+v; want the first resume's one", r.Name, r.Socket, own)
	}
	return own[0]
}

// TestResumeInsideStoppingWindowAfterResumedAgentEnds: resume X succeeds, its
// agent (the pane the launch recorded, SR-22.9) reports in and then ends while
// its labelled session still runs. A second resume 89 s after ended_at (the
// default window less a second) is refused with ErrTmuxUnresponsive "appears
// to still be stopping" and writes nothing: first with the session up (Ours),
// then with the session gone but the agent process still running (Gone).
// With the session gone and the process gone, a second resume at the same
// instant launches: the window never blocks a launch nothing still runs for.
func TestResumeInsideStoppingWindowAfterResumedAgentEnds(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	e := newKillEnv(t)
	r := e.seedResumable(t, time.Hour, agentGone)
	window := e.cfg.EffectiveStoppingWindow()

	// (1) resume X succeeds.
	if _, err := e.resume(r.ID); err != nil {
		t.Fatalf("first Resume: %v", err)
	}
	own := stoppingOwnSession(t, e, r)

	// (2) Its agent reports in, then (3) ends while the session still runs.
	for _, ev := range []string{"SessionStart", "SessionEnd"} {
		if got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, r.Spawn.ClaudeSessionID,
			apitest.HookTranscript(r.JSONLPath, true)); !got.Applied {
			t.Fatalf("%s from the agent = %+v; want applied", ev, got)
		}
	}
	ended := e.columns(t, r.ID)
	raw, _ := ended.EndedAt.(string)
	endedAt, err := time.Parse(heldStoreLayout, raw)
	pid, _ := ended.PID.(int64)
	start, _ := ended.ProcStarttime.(string)
	if ended.State != store.StateEnded || err != nil || pid <= 0 || start == "" {
		t.Fatalf("after SessionEnd: {state %v, ended_at %#v (%v), pid %#v, start %#v}; want ended with ended_at and the agent recorded",
			ended.State, ended.EndedAt, err, ended.PID, ended.ProcStarttime)
	}
	// The pre-trust entry the first resume wrote goes, so the snapshot's
	// trust check pins that a refusal writes none.
	r.Trust.reset(t)
	tok, _ := ended.LaunchToken.(string)

	// refused moves the clock so the rule reads 89 s after ended_at, resumes
	// and checks the still-stopping refusal and that it wrote nothing.
	refused := func(what string, noSession bool) {
		t.Helper()
		e.clock.Advance(endedAt.Add(window - time.Second).Sub(e.ruleInstant()))
		before := e.snapshotResume(t, r)
		_, err := e.resume(r.ID)
		if !errors.Is(err, api.ErrTmuxUnresponsive) {
			t.Fatalf("second Resume, %s = %v; want ErrTmuxUnresponsive (still stopping)", what, err)
		}
		apitest.AssertDescription(t, err.Error(), apitest.DescStillStopping(apitest.StartingSession{InstanceID: r.ID,
			Name: r.Name, Window: window, Bound: e.cfg.EffectiveStartingSession(), NoSession: noSession}), tok, e.storeID)
		e.assertResumeWroteNothing(t, before)
	}

	// (4) An immediate second resume, the session up.
	refused("session up", false)

	// (5) The session goes; the agent process still runs.
	if err := e.rec.KillSessionID(r.Socket, own.ID); err != nil {
		t.Fatalf("KillSessionID: %v", err)
	}
	e.pc.Set(int(pid), procfix.Alive(start))
	refused("session gone, agent process running", true)

	// (6) The agent process is gone too: the second resume launches.
	e.pc.Set(int(pid), procfix.Gone())
	e.clock.Advance(endedAt.Add(window - time.Second).Sub(e.ruleInstant()))
	calls := len(e.rec.SocketCalls())
	if _, err := e.resume(r.ID); err != nil {
		t.Fatalf("second Resume, session and agent gone: %v", err)
	}
	var kinds []tmux.Call
	for _, c := range e.rec.SocketCalls()[calls:] {
		kinds = append(kinds, c.Call)
	}
	if len(kinds) < 2 || kinds[0] != tmux.CallLookup || kinds[1] != tmux.CallCreate {
		t.Errorf("second resume's tmux calls = %v; want the lookup, then the create", kinds)
	}
	if c := e.columns(t, r.ID); c.State != store.StatePending || c.LaunchToken == ended.LaunchToken {
		t.Errorf("row after the second resume: {state %v, token %#v}; want pending with a new token (was %#v)", c.State, c.LaunchToken, ended.LaunchToken)
	}
	if n := len(pendTrail(t, "ad.resume.moved_to_pending", r.ID)); n != 2 {
		t.Errorf("ad.resume.moved_to_pending lines = %d; want 2", n)
	}
	if recs := resumeDisagrees(t, r.ID); len(recs) != 0 {
		t.Errorf("ad.provenance.disagree records = %v; want none (own session and server as recorded)", recs)
	}
}
