package api_test

// advice_follow_busy_send_lifecycle_test.go: literal-follow tests (b.fji) for
// the lifecycle verbs' advice when a covered row's earlier Launch cannot be
// shown unable to deliver (SR-6.1, SR-6.3; Task t2.xis.oo.sf), on
// busy_send_lifecycle_test.go's covered-row fixture (lcEnv).

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// adviceFollowIssuerRetryOnce follows verb's advice ("<retry> once that
// process has exited", or "... can be checked gone") after it refused a
// finished covered row whose Launch's issuing hook job still runs, or cannot
// be checked (Main Agent decision 16; SR-6.1, SR-6.3; M1 G4
// NotStartedAfterExit, NoStalePublication): retried while that holds, also
// after a kill (the finished row's no-op success), the verb is refused the
// same way with nothing written or launched; retried once the job reads
// gone, on a new Client reading the store alone, it goes on and leaves what
// its success promises (assertRetired).
func adviceFollowIssuerRetryOnce(t *testing.T, verb lcVerb, retry string) {
	t.Helper()
	for _, issuer := range []struct {
		name, state, advice string
		p                   procfix.Process
	}{
		{"hook job running", "still runs",
			retry + " once that process has exited (an agent-director hook or delivery job ends on its own within its job budget)",
			procfix.Alive(lcHelper.StartTime)},
		{"hook job uncheckable", "cannot be checked", retry + " once that process can be checked gone", procfix.Unreadable()},
	} {
		t.Run(issuer.name, func(t *testing.T) {
			t.Parallel()
			l := newLifecycleEnv(t, killRowSpec{State: store.StateWaiting, NoSession: true}, issuer.p)
			l.endBySessionEnd(t, l.r.killRow)
			issued, held, before := lcLookup(t, l.c, "lc-0"), l.held(t), l.snapshot(t)
			err := verb.run(t, l, l.c)
			l.assertRefused(t, verb, err, issuer.state, before)
			adviceAssertPhrase(t, err, issuer.advice)

			l.assertRefused(t, verb, verb.run(t, l, l.c), issuer.state, before)
			if res, err := l.c.Kill(api.KillParams{InstanceID: l.r.ID}); err != nil || res.KillSent {
				t.Fatalf("kill of the finished row = %+v, %v; want the no-op success", res, err)
			}
			l.assertRefused(t, verb, verb.run(t, l, l.c), issuer.state, before)

			l.pc.Set(lcHelper.PID, procfix.Gone())
			c, _ := l.client(t)
			if err := verb.run(t, l, c); err != nil {
				t.Fatalf("%s once the hook job reads gone: %v", verb.name, err)
			}
			l.assertRetired(t, verb, c, issued, held, before.facts.Binding)
		})
	}
}

// TestAdviceFollow_B19_ResumeIssuerRetryOnceExited: B19 "retry resume once
// that process has exited" (and "... can be checked gone";
// adviceFollowIssuerRetryOnce).
func TestAdviceFollow_B19_ResumeIssuerRetryOnceExited(t *testing.T) {
	t.Parallel()
	adviceFollowIssuerRetryOnce(t, lcResume, "nothing was written and nothing was launched; retry resume")
}

// TestAdviceFollow_A33_ReuseIssuerSpawnAgainOnceExited: A33 "spawn again once
// that process has exited" (and "... can be checked gone";
// adviceFollowIssuerRetryOnce).
func TestAdviceFollow_A33_ReuseIssuerSpawnAgainOnceExited(t *testing.T) {
	t.Parallel()
	adviceFollowIssuerRetryOnce(t, lcReuse, "spawn again")
}

// TestAdviceFollow_F14_DeleteNotDeletableKillSweepRetry: F14 "end the agent
// with kill, then sweep, and run delete again, or, on a finished row, run
// delete again once its processes have exited" (agent-director-admin delete's
// help) after delete refused a covered live row whose agent and issuing hook
// job run: kill ends the agent's pane, the job in it with it, sweep settles
// the row, and delete again removes it (assertRetired: lc-2 Final not_started
// record_deleted, the issued Message's Final Result found after it). A job
// that survives the kill leaves the row finished: delete again keeps the same
// refusal, with nothing changed, until the job exits.
func TestAdviceFollow_F14_DeleteNotDeletableKillSweepRetry(t *testing.T) {
	t.Parallel()
	const advice = "end the agent with kill, then sweep, and run delete again, or, on a finished row, run delete again once its processes have exited"
	if v, ok := adminapi.Lookup("delete"); !ok || !strings.Contains(v.Description, "refused with ErrSpawnNotDeletable and nothing changes: "+advice) ||
		!strings.Contains(v.Output, `"ErrSpawnNotDeletable" when a covered row's owned processes are not all checked gone (kill, then sweep, then retry; on a finished row, retry once they have exited)`) {
		t.Errorf("delete help = %+v; want it to carry %q", v, advice)
	}
	for _, survives := range []bool{false, true} {
		t.Run(map[bool]string{false: "hook job ends with the pane", true: "hook job survives the kill"}[survives], func(t *testing.T) {
			t.Parallel()
			l := newLifecycleEnv(t, killRowSpec{State: store.StateWaiting}, procfix.Alive(lcHelper.StartTime))
			ended := []int{l.r.AgentPID}
			if !survives {
				ended = append(ended, lcHelper.PID)
			}
			l.setAfterCall(tmux.CallKillPane, procfix.Gone(), ended...)
			l.assertRefused(t, lcDelete, lcDelete.run(t, l, l.c), "", l.snapshot(t))

			if res, err := l.c.Kill(api.KillParams{InstanceID: l.r.ID}); err != nil || !res.KillSent {
				t.Fatalf("kill = %+v, %v; want the agent's pane killed", res, err)
			}
			l.sweep(t, l.c)
			if survives {
				l.assertRefused(t, lcDelete, lcDelete.run(t, l, l.c), "", l.snapshot(t))
				l.pc.Set(lcHelper.PID, procfix.Gone())
			}
			issued := lcLookup(t, l.c, "lc-0")
			if err := lcDelete.run(t, l, l.c); err != nil {
				t.Fatalf("delete again: %v", err)
			}
			l.assertRetired(t, lcDelete, l.c, issued, nil, "")
		})
	}
}

// TestAdviceFollow_B20_ResumeExitScopeChangedRetry: B20 "nothing was written
// and nothing was launched; retry resume" after the earlier Launch's owned
// processes changed between resume's exit judgement and its move (a
// concurrent kill or sweep recorded the agent's exit): the row is unchanged
// and nothing was created, and the retried resume launches.
func TestAdviceFollow_B20_ResumeExitScopeChangedRetry(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedResumable(t, time.Hour, agentGone)
	seedBusyCovered(t, e, r.ID)
	w := &hookedResumeStore{st: e.st}
	w.afterScope(func() {
		if ok, err := e.st.EstablishBusySendExit(*exitEvidenceFor(t, e.st, r.ID)); err != nil || !ok {
			t.Errorf("concurrent exit = %t, %v; want applied", ok, err)
		}
	})
	cols := e.columns(t, r.ID)

	_, err := e.resumeWith(w, r.ID)

	adviceAssertAdvice(t, err, api.ErrSpawnNotResumable,
		"the processes its earlier Launch owns changed after resume checked them; nothing was written and nothing was launched; retry resume")
	e.assertRowUnchanged(t, r.ID, cols)
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
		t.Errorf("%d creates; want none", n)
	}
	if _, err := e.resumeWith(w, r.ID); err != nil {
		t.Fatalf("retried resume: %v", err)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
		t.Errorf("%d creates after the retry; want one", n)
	}
}
