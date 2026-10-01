package api_test

// kill_optin_hookgate_test.go: TLA+ ci_G3b_ren end to end (AC-HOOK-03;
// SR-6.5 "A finished row is finished by its own agent", SR-6.7, SR-22.9): a
// renamed leftover of an earlier life of X sends hooks while X's agent works;
// the gate ignores them, so kill's finished-row opt-in refuses X as live and
// never kills the working agent. The ad.hook.ignored record is
// internal/hook's (hook_gate_test.go), not re-proven here.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kogAssertAlive fails unless pid runs in the process fake.
func kogAssertAlive(t *testing.T, e *killEnv, pid int, what string) {
	t.Helper()
	if _, alive, _ := e.pc.StartTime(pid); !alive {
		t.Errorf("%s (pid %d) is not running; want it untouched", what, pid)
	}
}

// TestKillIncludeFinishedRenamedLeftoverHooks (ci_G3b_ren): the leftover's
// SessionEnd, Stop and PermissionRequest change nothing, so the opt-in refuses
// the working row; after the agent's own SessionEnd it kills only its session.
func TestKillIncludeFinishedRenamedLeftoverHooks(t *testing.T) {
	e := newKillEnv(t)
	s1, s0 := "sess-"+uuid.NewString()[:8], "sess-"+uuid.NewString()[:8]
	r := e.seedRow(t, killRowSpec{State: store.StateWorking, SessionID: s1})
	leftoverPID := e.newPID()
	e.pc.Set(leftoverPID, procfix.Alive(apitest.LinuxProcStarttime))
	leftover := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "renamed-" + uuid.NewString()[:8], Label: r.old(),
		Panes: []tmuxfix.SeedPane{{PID: leftoverPID, AdPane: tmuxfix.OtherToken}}})
	before := e.columns(t, r.ID)

	for _, event := range []string{"SessionEnd", "Stop", "PermissionRequest"} {
		got := apitest.ApplyForeignHook(t, e.dbPath, r.ID, event, s0)
		if want := (store.HookApplied{Reason: store.HookReasonPIDMismatch}); got != want {
			t.Errorf("leftover's %s = %+v; want %+v", event, got, want)
		}
		e.assertRowUnchanged(t, r.ID, before)
		if perms, err := e.st.PermissionRequestsForSpawn(r.ID); err != nil || len(perms) != 0 {
			t.Errorf("permission requests after the leftover's %s = %+v (%v); want none", event, perms, err)
		}
	}

	sessions := e.rec.Sessions(r.Socket)
	res, err := e.killOptIn(r.ID)
	kolAssertRefused(t, e, r, store.StateWorking, res, err, before, sessions)
	kogAssertAlive(t, e, r.AgentPID, "the working agent")

	if got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionEnd", s1); !got.Applied {
		t.Fatalf("the agent's own SessionEnd = %+v; want applied", got)
	}
	row := rlfRow(t, e, r.ID)
	if row.State != store.StateEnded || row.EndedAt == nil || r.Session.Created >= row.EndedAt.Unix() {
		t.Fatalf("after the agent's SessionEnd: state %s, ended_at %v, session created %d; want ended after it",
			row.State, row.EndedAt, r.Session.Created)
	}
	kohPastBoth(t, e, row, r.Session.Created)
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
	mark := len(e.rec.SocketCalls())
	if res, err := e.killOptIn(r.ID); err != nil || !res.KillSent {
		t.Fatalf("kill after the agent's SessionEnd = %+v, %v; want kill_sent true, nil", res, err)
	}
	kohAssertKilled(t, e, mark, r.Spawn.Identity.PaneID, r.Session.ID)
	kogAssertAlive(t, e, leftoverPID, "the leftover")
	held := false
	for _, s := range e.rec.Sessions(r.Socket) {
		held = held || s.ID == leftover.ID
	}
	if !held {
		t.Errorf("leftover session %s is gone; want it still running", leftover.ID)
	}
	if recs := killCalled(t, r.ID); len(recs) != 2 || recs[1]["include_finished"] != true ||
		recs[1]["kill_sent"] != true || recs[1]["lookup_outcome"] != "ours" {
		t.Errorf("ad.kill.called records = %v; want a second with include_finished, kill_sent true, ours", recs)
	}
}
