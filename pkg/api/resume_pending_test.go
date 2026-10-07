package api_test

// resume_pending_test.go covers resume around a pending row (SR-8.4, SR-8.5,
// SR-8.6, SR-4.2, SR-22.9; AC-RES-09, AC-RES-11, AC-RES-13): the
// launch-in-progress refusal, a loser's re-read finding the row deleted, and
// a second resume after a resumed agent's life.

import (
	"errors"
	"os"
	"reflect"
	"slices"
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

// pendRefusal is one refused resume: the row before and after, the error, and the calls and move events added.
type pendRefusal struct {
	before, after apitest.SpawnColumns
	err           error
	calls, moved  int
}

// pendRefuse resumes id as a caller whose parent is callerParent and records what the call changed.
func pendRefuse(t *testing.T, e *resumeEnv, id, callerParent string) pendRefusal {
	t.Helper()
	prev := os.Getenv("AGENT_DIRECTOR_INSTANCE_ID")
	// Restored on return, and at cleanup by t.Setenv (which keeps t serial).
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", callerParent)
	defer t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", prev)
	r := pendRefusal{before: e.columns(t, id)}
	calls, moved := pendCalls(e.rec), pendMoved(t, id)
	_, r.err = e.resume(id)
	r.after = e.columns(t, id)
	r.calls, r.moved = pendCalls(e.rec)-calls, pendMoved(t, id)-moved
	return r
}

// TestResumeRefusesLaunchInProgress: a resume of any pending row gets the
// launch-in-progress refusal (with the reuse-opt-in step for a row with no
// session id, b.uey), makes no tmux call and writes nothing.
func TestResumeRefusesLaunchInProgress(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID with t.Setenv; it checks the shared trail by literal row
	// ids other find-missing tests reuse.
	// seededNoStart seeds a pending row whose launch_started_at, NULL or outside years 0 to 9999, reads as absent (SR-5.5).
	seededNoStart := func(raw any) func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
		return func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			opt := apitest.WithNoLaunchStartedAt()
			if ms, ok := raw.(int64); ok {
				opt = apitest.WithLaunchStartedAt(ms)
			}
			id, err := apitest.SeedSpawn(e.dbPath, "", store.StatePending, t.TempDir(), "off", "sess-"+uuid.NewString()[:8], false, opt)
			if err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			if got := e.columns(t, id).LaunchStartedAt; got != raw {
				t.Fatalf("seeded launch_started_at = %#v; want %#v", got, raw)
			}
			if err := apitest.SeedParentChild(e.dbPath, pendParent(t, e), id); err != nil {
				t.Fatalf("SeedParentChild: %v", err)
			}
			return id, nil, 0
		}
	}
	cases := []struct {
		name string
		// setup makes a pending row: its id, the refusal if it ran one (nil: refuse after) and its launch start (0: none).
		setup func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64)
	}{
		{"moved by another resume, before its create", func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			r := e.seedResumable(t, store.StateEnded)
			var got pendRefusal
			e.store.afterMove(func() { got = pendRefuse(t, e, r.ID, p) })
			start := e.moveStart().UnixMilli()
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("first Resume: %v", err)
			}
			return r.ID, &got, start
		}},
		{"moved by another resume, session up and not reported in", func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			r := e.seedResumable(t, store.StateMissing)
			start := e.moveStart().UnixMilli()
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("first Resume: %v", err)
			}
			if len(e.rec.Sessions(e.socket)) != 1 {
				t.Fatalf("sessions = %+v; want the first resume's", e.rec.Sessions(e.socket))
			}
			return r.ID, nil, start
		}},
		{"seeded pending with no launch start", seededNoStart(nil)},
		{"seeded pending at year 10000", seededNoStart(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())},
		{"seeded pending before year 0", seededNoStart(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() - 1)},
		{"plain spawn whose launch failed", func(t *testing.T, e *resumeEnv, p string) (string, *pendRefusal, int64) {
			e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
			id := uuid.NewString()
			if _, err := e.c.Spawn(api.SpawnParams{ClaudeInstanceID: id, CWD: t.TempDir()}); !errors.Is(err, api.ErrTmuxSessionCreate) {
				t.Fatalf("Spawn = %v; want ErrTmuxSessionCreate", err)
			}
			start, _ := e.columns(t, id).LaunchStartedAt.(int64)
			if start == 0 {
				t.Fatalf("plain spawn's row records no launch start")
			}
			return id, nil, start
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			callerParent := pendParent(t, e)
			id, ran, wantStart := tc.setup(t, e, callerParent)
			if ran == nil {
				r := pendRefuse(t, e, id, callerParent)
				ran = &r
			}
			got := *ran
			if got.before.State != store.StatePending {
				t.Fatalf("row before the refusal is %v; want pending", got.before.State)
			}
			sid, _ := got.before.ClaudeSessionID.(string)
			p := apitest.LaunchInProgress{InstanceID: id, NoSessionID: sid == ""}
			if wantStart != 0 {
				p.LaunchStart = time.UnixMilli(wantStart).UTC()
				if got.before.LaunchStartedAt != wantStart {
					t.Errorf("launch_started_at = %#v; want %d", got.before.LaunchStartedAt, wantStart)
				}
			}
			if !errors.Is(got.err, api.ErrSpawnNotResumable) {
				t.Fatalf("Resume = %v; want ErrSpawnNotResumable", got.err)
			}
			tok, _ := got.before.LaunchToken.(string)
			apitest.AssertDescription(t, got.err.Error(), apitest.DescResumeLaunchInProgress(p), tok, e.storeID)
			if got.calls != 0 || got.moved != 0 {
				t.Errorf("the refusal made %d tmux calls and %d move events; want none", got.calls, got.moved)
			}
			if !reflect.DeepEqual(got.after, got.before) {
				t.Errorf("row after the refusal = %+v; want unchanged %+v", got.after, got.before)
			}
		})
	}
}

// TestResumeLoserRowDeletedBeforeReRead: a leftover at the lookup, the row
// deleted after the read: the one re-read gives ErrSpawnNotFound, not the
// Leftover conflict, after only the lookup, and nothing is written.
func TestResumeLoserRowDeletedBeforeReRead(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	e := newKillEnv(t)
	r := e.seedResumable(t, time.Hour, agentGone)
	e.seedHolder(t, r.killRow, holderOld)
	w := &hookedResumeStore{st: e.st}
	w.failMove(nil) // a move would return the injected store error instead
	w.afterGet(func() {
		if err := e.st.DeleteSpawn(r.ID); err != nil {
			t.Fatalf("DeleteSpawn: %v", err)
		}
	})
	before := e.snapshotResume(t, r)

	_, err := e.resumeWith(w, r.ID)

	if !errors.Is(err, api.ErrSpawnNotFound) || errors.Is(err, api.ErrTmuxSessionConflict) {
		t.Fatalf("Resume = %v; want ErrSpawnNotFound, not the Leftover conflict", err)
	}
	if got := e.rec.SocketCalls()[before.calls:]; len(got) != 1 || got[0].Call != tmux.CallLookup {
		t.Errorf("tmux calls = %+v; want the one lookup", got)
	}
	if n := len(e.rec.Calls()) - before.nameCalls; n != 0 {
		t.Errorf("%d name-based tmux calls; want none", n)
	}
	if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, before.sessions[r.Socket]) {
		t.Errorf("sessions on %s = %+v; want unchanged %+v", r.Socket, got, before.sessions[r.Socket])
	}
	r.Trust.check(t, r.CWD, false, "after the refused resume")
	for _, l := range readAPITrailLines(t)[before.mark:] {
		if ev, _ := l["event"].(string); strings.HasPrefix(ev, "ad.resume.") {
			t.Errorf("trail record %s for %v; want no ad.resume.* line", ev, l["claude_instance_id"])
		}
	}
	if _, err := apitest.ReadSpawnColumns(e.dbPath, r.ID); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns = %v; want the row still absent", err)
	}
}

// TestResumeAgainAfterResumedAgentEnds (AC-RES-13; SR-8.4, SR-8.5, SR-4.2
// step 1, SR-22.9): resume X's agent (its recorded pane) reports in with a new
// session id and ends while its session runs. A second resume 89 s after
// ended_at is refused as still stopping and writes nothing, with the session
// up (Ours) and then gone but the agent running (Gone); with both gone it
// launches the reported session: no disagree record, the move applied again.
func TestResumeAgainAfterResumedAgentEnds(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	e := newKillEnv(t)
	r := e.seedResumable(t, time.Hour, agentGone)
	window := e.cfg.EffectiveStoppingWindow()

	// (1) resume X succeeds.
	if _, err := e.resume(r.ID); err != nil {
		t.Fatalf("first Resume: %v", err)
	}
	own := rplSessionNamed(t, e.rec, r.Socket, r.Name)

	// (2) Its agent reports in with a new session id, then (3) ends while the session still runs.
	newSess := "relife-" + uuid.NewString()[:8]
	newJSONL := apitest.SeedJsonlUnder(t, r.Trust.dir, r.CWD, newSess)
	for _, ev := range []string{"SessionStart", "SessionEnd"} {
		if got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, newSess, apitest.HookTranscript(newJSONL, true)); !got.Applied {
			t.Fatalf("%s from the agent = %+v; want applied", ev, got)
		}
	}
	ended := e.columns(t, r.ID)
	raw, _ := ended.EndedAt.(string)
	endedAt, err := time.Parse(heldStoreLayout, raw)
	pid, _ := ended.PID.(int64)
	start, _ := ended.ProcStarttime.(string)
	if ended.State != store.StateEnded || err != nil || pid <= 0 || start == "" || ended.ClaudeSessionID != newSess {
		t.Fatalf("after SessionEnd: {state %v, ended_at %#v (%v), pid %#v, start %#v, session %#v}; want ended with ended_at, the agent and session %s recorded",
			ended.State, ended.EndedAt, err, ended.PID, ended.ProcStarttime, ended.ClaudeSessionID, newSess)
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
	calls, wantStart := len(e.rec.SocketCalls()), e.ruleInstant().UnixMilli()
	w := &hookedResumeStore{st: e.st}
	var moved apitest.SpawnColumns
	w.afterMove(func() { moved = e.columns(t, r.ID) })
	if _, err := e.resumeWith(w, r.ID); err != nil {
		t.Fatalf("second Resume, session and agent gone: %v", err)
	}
	if got := callKinds(e.rec)[calls:]; !slices.Equal(got, []tmux.Call{tmux.CallLookup, tmux.CallCreate}) {
		t.Errorf("second resume's tmux calls = %v; want the lookup, then the create", got)
	}
	creates := e.rec.SocketCallsOf(tmux.CallCreate)
	cmd := creates[len(creates)-1].Command
	if i := slices.Index(cmd, "--resume"); i < 0 || i+1 >= len(cmd) || cmd[i+1] != newSess {
		t.Errorf("argv = %q; want --resume %s (the session the resumed agent reported)", cmd, newSess)
	}
	newTok, _ := moved.LaunchToken.(string)
	bv, _ := ended.RowVersion.(int64)
	if moved.State != store.StatePending || moved.LaunchStartedAt != wantStart || moved.EndedAt != nil || newTok == tok ||
		creates[len(creates)-1].Token != newTok || moved.RowVersion != bv+1 {
		t.Errorf("row at the second move {%v, launch %#v, ended_at %#v, token %q, version %#v}; want pending, %d, NULL, a new token the create labels with, %d",
			moved.State, moved.LaunchStartedAt, moved.EndedAt, newTok, moved.RowVersion, wantStart, bv+1)
	}
	lines := pendTrail(t, "ad.resume.moved_to_pending", r.ID)
	if len(lines) != 2 {
		t.Fatalf("ad.resume.moved_to_pending lines = %d; want 2", len(lines))
	}
	assertAPITrailStr(t, lines[1], "prior_state", store.StateEnded)
	assertAPITrailStr(t, lines[1], "claude_session_id", newSess)
	if recs := resumeDisagrees(t, r.ID); len(recs) != 0 {
		t.Errorf("ad.provenance.disagree records = %v; want none (own session and server as recorded)", recs)
	}
}
