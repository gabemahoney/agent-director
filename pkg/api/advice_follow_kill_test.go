package api_test

// advice_follow_kill_test.go: b.fji literal-follow tests for kill's own
// errors (advice inventory C1 to C6 and C8). Each test triggers the error on
// the kill fixture, checks the advice phrase, follows it as an automated
// caller would on the virtual clock and checks the promised outcome. The
// finished-row opt-in (C9, C10) is advice_follow_kill_optin_test.go's; the
// live-row sequence and delete's pointer (C11, F1)
// advice_follow_kill_sequence_test.go's.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// advKillFailedTail is the advice every ErrTmuxKillFailed description ends with.
const advKillFailedTail = "retry kill later; never delete this row"

// advKillLater is how much later a retry is made on the virtual clock.
const advKillLater = 5 * time.Second

// advKillFollow follows a refusal on r: a later kill while the condition
// holds gets want with advice again (its text equal to first's unless first
// is nil); after clear, a later kill succeeds with kill_sent sent. The row is
// never deleted and its state never changes.
func advKillFollow(t *testing.T, e *killEnv, r killRow, want error, advice string, first error, clear func(), sent bool) {
	t.Helper()
	state := e.columns(t, r.ID).State
	e.clock.Advance(advKillLater)
	_, err := e.kill(r.ID)
	adviceAssertAdvice(t, err, want, advice)
	if first != nil && err.Error() != first.Error() {
		t.Errorf("refusal while the condition holds = %q; want it unchanged: %q", err, first)
	}
	clear()
	e.clock.Advance(advKillLater)
	res, err := e.kill(r.ID)
	if err != nil {
		t.Fatalf("kill once the condition cleared: %v; want success", err)
	}
	if res.KillSent != sent {
		t.Errorf("kill_sent = %v; want %v", res.KillSent, sent)
	}
	if got := e.columns(t, r.ID).State; got != state {
		t.Errorf("state = %v; want %v kept", got, state)
	}
}

// TestAdviceFollow_C1_AgentOutlivesExitWaitRetryKillLater: the retry is
// refused while the agent runs and succeeds once it has exited.
func TestAdviceFollow_C1_AgentOutlivesExitWaitRetryKillLater(t *testing.T) {
	// C1 ErrTmuxKillFailed, agent still running after the exit wait: "retry kill later; never delete this row".
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	e.seedBystander(t, r.Socket)
	_, err := e.kill(r.ID)
	adviceAssertAdvice(t, err, api.ErrTmuxKillFailed, advKillFailedTail)
	// The first kill ended the session, so the retry while the agent runs is
	// the no-pane variant: refused again, no kill sent.
	advKillFollow(t, e, r, api.ErrTmuxKillFailed, advKillFailedTail, nil,
		func() { e.pc.Set(r.AgentPID, procfix.Gone()) }, false)
}

// TestAdviceFollow_C2_SurvivorRetrySucceedsAsDocumented: decision-0930b Q4's
// residual: a retried kill succeeds while the named non-agent survivor runs.
func TestAdviceFollow_C2_SurvivorRetrySucceedsAsDocumented(t *testing.T) {
	// C2 ErrTmuxKillFailed naming another pane process: "retry kill later"; manifest: "a retried kill checks only the agent process. A retry's success means only that the agent is gone; the named process needs a human".
	adviceAssertManifest(t, "kill", "", "a retried kill checks only the agent process. "+
		"A retry's success means only that the agent is gone; the named process needs a human")
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{Teammates: 1})
	e.seedBystander(t, r.Socket)
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
	survivor := r.TeammatePIDs[0]
	_, err := e.kill(r.ID)
	adviceAssertAdvice(t, err, api.ErrTmuxKillFailed, advKillFailedTail)
	if named := fmt.Sprintf("(pid %d)", survivor); !strings.Contains(err.Error(), named) {
		t.Errorf("description %q does not name the survivor %s", err, named)
	}

	e.clock.Advance(advKillLater)
	res, err := e.kill(r.ID)
	if err != nil || res.KillSent {
		t.Fatalf("retried kill = %+v, %v; want success with kill_sent false", res, err)
	}
	if _, alive, _ := e.pc.StartTime(survivor); !alive {
		t.Errorf("survivor %d is gone; the residual is a success while it still runs", survivor)
	}
	if got := e.columns(t, r.ID).State; got != store.StateWaiting {
		t.Errorf("state = %v; want waiting kept", got)
	}
}

// TestAdviceFollow_C3_UncheckableSessionStillThereRetryKillLater: the same
// refusal while tmux's kills do not take effect; success once they do.
func TestAdviceFollow_C3_UncheckableSessionStillThereRetryKillLater(t *testing.T) {
	// C3 ErrTmuxKillFailed, process uncheckable and its labelled session still there: "retry kill later; never delete this row".
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{Agent: agentUnreadable})
	e.seedBystander(t, r.Socket)
	// The first two calls' pane and session kills time out without effect.
	e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 2}, tmux.CallKillPane, tmux.CallKillSession)
	_, err := e.kill(r.ID)
	adviceAssertAdvice(t, err, api.ErrTmuxKillFailed, advKillFailedTail)
	advKillFollow(t, e, r, api.ErrTmuxKillFailed, advKillFailedTail, err, func() {}, true)
	if seqHas(e, r.Socket, r.Session.ID) {
		t.Errorf("session %s still runs after the successful retry", r.Session.ID)
	}
}

// TestAdviceFollow_C4_NoPaneRetryKillLater: the same refusal, no kill sent,
// while the agent runs; success once it has exited.
func TestAdviceFollow_C4_NoPaneRetryKillLater(t *testing.T) {
	// C4 ErrTmuxKillFailed, no session or pane of this launch while the agent runs: "a human can find and look at the process, see "Operator actions"...; retry kill later; never delete this row".
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{NoSession: true})
	e.ensureServer(&r)
	e.seedBystander(t, r.Socket)
	e.syncServers()
	_, err := e.kill(r.ID)
	adviceAssertAdvice(t, err, api.ErrTmuxKillFailed,
		`a human can find and look at the process, see "Operator actions" in the agent-director README; `+advKillFailedTail)
	advKillFollow(t, e, r, api.ErrTmuxKillFailed, advKillFailedTail, err,
		func() { e.pc.Set(r.AgentPID, procfix.Gone()) }, false)
	if n := len(e.rec.SocketCallsOf(tmux.CallKillPane)) + len(e.rec.SocketCallsOf(tmux.CallKillSession)); n != 0 {
		t.Errorf("%d kills sent; want none (no pane of the launch)", n)
	}
}

// TestAdviceFollow_C5_UnresponsiveBeforeKillRetryLater: the same refusal
// while tmux cannot be read; the later kill proceeds once it can.
func TestAdviceFollow_C5_UnresponsiveBeforeKillRetryLater(t *testing.T) {
	// C5 ErrTmuxUnresponsive before any kill was sent: "nothing was done; retry later".
	const advice = "nothing was done; retry later"
	cases := []struct {
		name   string
		script tmuxfix.Script
		call   tmux.Call
	}{
		{"lookup timed out", tmuxfix.Script{Failure: tmux.FailTimeout, Times: 2}, tmux.CallLookup},
		{"lookup reply not recognised", tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: "garbled reply",
			ExitStatus: 1, Times: 2}, tmux.CallLookup},
		{"pane listing timed out", tmuxfix.Script{Failure: tmux.FailTimeout, Times: 2}, tmux.CallListPanes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.seedBystander(t, r.Socket)
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			e.rec.Script(r.Socket, tc.script, tc.call)
			_, err := e.kill(r.ID)
			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, advice)
			if n := len(e.rec.SocketCallsOf(tmux.CallKillPane)); n != 0 {
				t.Fatalf("%d pane kills before the refusal; want none (nothing was done)", n)
			}
			advKillFollow(t, e, r, api.ErrTmuxUnresponsive, advice, err, func() {}, true)
		})
	}
}

// TestAdviceFollow_C6_FollowUpUnreadableRetryLater: after a sent kill whose
// follow-up could not answer, the retry is safe and ends the agent.
func TestAdviceFollow_C6_FollowUpUnreadableRetryLater(t *testing.T) {
	// C6 ErrTmuxUnresponsive, the follow-up lookup after a sent kill unreadable: "the kill was sent and may or may not have taken effect; retry later".
	cases := []struct {
		name string
		took bool // the first call's kills took effect
	}{{"the kills took effect", true}, {"the kills did not take effect", false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{Agent: agentUnreadable})
			e.seedBystander(t, r.Socket)
			if !tc.took {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallKillPane, tmux.CallKillSession)
			}
			scripted := false
			e.rec.AfterCall(tmux.CallKillSession, func(tmuxfix.SocketCall, error) {
				if !scripted {
					scripted = true
					e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallLookup)
				}
			})
			_, err := e.kill(r.ID)
			adviceAssertAdvice(t, err, api.ErrTmuxUnresponsive, "the kill was sent and may or may not have taken effect; retry later")

			e.clock.Advance(advKillLater)
			res, err := e.kill(r.ID)
			if err != nil {
				t.Fatalf("retried kill: %v; want success", err)
			}
			if res.KillSent == tc.took {
				t.Errorf("kill_sent = %v; want %v (a kill only when the first did not take effect)", res.KillSent, !tc.took)
			}
			if seqHas(e, r.Socket, r.Session.ID) {
				t.Errorf("session %s still runs after the retry", r.Session.ID)
			}
			if got := e.columns(t, r.ID).State; got != store.StateWaiting {
				t.Errorf("state = %v; want waiting kept", got)
			}
		})
	}
}

// TestAdviceFollow_C8_RowFinishesWhileKillWaits: the row ends during the
// wait while the agent outlives it; the retried kill is a finished-row no-op.
func TestAdviceFollow_C8_RowFinishesWhileKillWaits(t *testing.T) {
	// C8 kill manifest: "If the row finishes while kill waits and the agent outlives the wait, kill returns ErrTmuxKillFailed, and a retried kill is a finished-row no-op."
	adviceAssertManifest(t, "kill", "", "If the row finishes while kill waits and the agent outlives the wait, "+
		"kill returns ErrTmuxKillFailed, and a retried kill is a finished-row no-op.")
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{SessionID: "sess-" + uuid.NewString()[:8]})
	e.seedBystander(t, r.Socket)
	sleep, ended := e.sleep, false
	e.sleep = func(d time.Duration) {
		sleep(d)
		if !ended {
			ended = true
			if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", ""); !a.Applied {
				t.Fatalf("SessionEnd from the agent during the wait = %+v; want applied", a)
			}
		}
	}
	_, err := e.kill(r.ID)
	adviceAssertAdvice(t, err, api.ErrTmuxKillFailed, advKillFailedTail)
	if got := e.columns(t, r.ID).State; got != store.StateEnded {
		t.Fatalf("state after the wait = %v; want ended", got)
	}

	calls := len(e.rec.SocketCalls())
	e.clock.Advance(advKillLater)
	res, err := e.kill(r.ID)
	if err != nil || res.KillSent {
		t.Fatalf("retried kill = %+v, %v; want success with kill_sent false", res, err)
	}
	if got := e.rec.SocketCalls()[calls:]; len(got) != 0 {
		t.Errorf("tmux calls by the retry = %+v; want none (a finished-row no-op)", got)
	}
	killAssertAgentRuns(t, e, r)
}
