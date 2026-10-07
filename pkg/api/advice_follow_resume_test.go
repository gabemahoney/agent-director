package api_test

// advice_follow_resume_test.go holds b.fji's literal-follow tests for resume's
// refusals (advice inventory B1-B6) and the resume helpers B7-B10 share
// (advice_follow_resume_launch_test.go; the shared ones are in
// advice_follow_helpers_test.go): trigger the error, pin its advice,
// follow it as an automated caller would, check the promised outcome. Advice
// that does not work as written is gated by knownBrokenAdvice.

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The advice B1-B6 pin, exactly as the description or the Go doc words it.
const (
	advResumeLaunchInProgress          = "if the agent reports in, the row becomes live, and if the launch was abandoned or failed, find-missing marks the row missing once the pending grace period has passed since its launch start; nothing was written"
	advResumeLaunchInProgressNoSession = "if the agent reports in, the row becomes live, and if the launch was abandoned or failed, find-missing marks the row missing once the pending grace period has passed since its launch start; the row has no session id, so there is no conversation to resume: once find-missing marks it missing, if get still shows no session id, spawn the id again with the reuse opt-in reuse_finished (--reuse-finished on the CLI); nothing was written"
	advResumeLaunchInProgressDoc       = "find-missing marks it missing once the pending grace period has passed since its launch start; then a row with a session id (typically a resume's launch) can be resumed, and a row with none (typically a spawn's or reuse's launch), whose resume would return ErrNoSessionId, is spawned again with the same id, opting in to reuse (SpawnParams.ReuseFinished), if get still shows no session id"
	advResumeLaunchNoSessionErrDoc     = "the step after that: if get still shows no session id, spawn the id again, opting in to reuse (SpawnParams.ReuseFinished, --reuse-finished), since there is no conversation to resume"
	advResumeLostRaceChanged           = "the row changed after resume examined it and nothing was written; nothing was launched"
	advResumeLostRaceRemoved           = "was removed after resume examined it; nothing was written and nothing was launched"
	advResumeLiveDoc                   = "a live Spawn must be paused, or killed and then marked by find-missing, before it can be resumed: follow the live-row sequence in kill's description"
	advResumeReuseRecourse             = "recourse is to spawn again with the same id, opting in to reuse (SpawnParams.ReuseFinished, --reuse-finished)"
)

// advResumeState is id's state as get shows it; "" when get finds no row.
func advResumeState(t *testing.T, c *api.Client, id string) string {
	t.Helper()
	row, err := c.Get(id)
	if errors.Is(err, api.ErrSpawnNotFound) {
		return ""
	}
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return row.State
}

// advResumeAssertState fails unless get shows id's row in state want ("": no row).
func advResumeAssertState(t *testing.T, c *api.Client, id, want string) {
	t.Helper()
	if got := advResumeState(t, c, id); got != want {
		t.Fatalf("get %s: state %q; want %q", id, got, want)
	}
}

// advResumeLaunches resumes id and fails unless it launched: no error, one create, the row pending.
func advResumeLaunches(t *testing.T, e *killEnv, id string) {
	t.Helper()
	creates := len(e.rec.SocketCallsOf(tmux.CallCreate))
	if _, err := e.resume(id); err != nil {
		t.Fatalf("resume of %s = %v; want it to launch", id, err)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)) - creates; n != 1 {
		t.Errorf("creates by the resume = %d; want 1", n)
	}
	if st := e.columns(t, id).State; st != store.StatePending {
		t.Errorf("state after the resume = %v; want pending", st)
	}
}

// advResumeNextStep acts on get's answer after a failed resume: a finished row is
// resumed again; a pending one waits for find-missing (resume refuses it meanwhile); no row: spawn the id.
func advResumeNextStep(t *testing.T, e *killEnv, c *api.Client, id string) {
	t.Helper()
	switch advResumeState(t, c, id) {
	case "":
		if _, err := e.resume(id); !errors.Is(err, api.ErrSpawnNotFound) {
			t.Errorf("resume of the removed row = %v; want ErrSpawnNotFound", err)
		}
		if res, err := c.Spawn(fgcSpawnParams(t, id, false)); err != nil || res.ClaudeInstanceID != id {
			t.Fatalf("spawn of %s = %+v, %v; want it launched", id, res, err)
		}
		advResumeAssertState(t, c, id, store.StatePending)
		return
	case store.StatePending:
		if st := adviceAwaitFinished(t, c, e.clock, id, func() {
			if _, err := e.resume(id); !errors.Is(err, api.ErrSpawnNotResumable) {
				t.Errorf("resume of the pending row = %v; want ErrSpawnNotResumable", err)
			}
		}); st != store.StateMissing {
			t.Fatalf("get %s past the pending grace period: state %q; want missing", id, st)
		}
	}
	advResumeLaunches(t, e, id)
}

// advResumeAgentDies ends id's launched agent before it reports in: its pane process and session go.
func advResumeAgentDies(t *testing.T, e *killEnv, id string) {
	t.Helper()
	row, err := e.st.GetSpawn(id)
	if err != nil || row.Identity.PanePID <= 0 {
		t.Fatalf("GetSpawn(%s) = pane pid %d, %v; want the launch's pane recorded", id, row.Identity.PanePID, err)
	}
	e.pc.Set(row.Identity.PanePID, procfix.Gone())
	for _, s := range e.rec.Sessions(row.Identity.Socket) {
		if s.Label.Token == row.Identity.Token {
			if err := e.rec.KillSessionID(row.Identity.Socket, s.ID); err != nil {
				t.Fatalf("KillSessionID: %v", err)
			}
		}
	}
}

// advResumePending is a pending row whose agent never reports in, by origin;
// noSession: a spawn's or reuse's row, which has no session id.
type advResumePending struct {
	name      string
	pend      func(t *testing.T, e *killEnv, c *api.Client) string
	noSession bool
}

// advResumePendings is a pending row of each origin: spawn's, reuse's and resume's.
func advResumePendings() []advResumePending {
	return []advResumePending{
		{"spawn: create timed out", func(t *testing.T, e *killEnv, c *api.Client) string {
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallCreate)
			id := "adv-resume-" + uuid.NewString()[:8]
			if _, err := c.Spawn(fgcSpawnParams(t, id, false)); !errors.Is(err, api.ErrTmuxUnresponsive) {
				t.Fatalf("spawn = %v; want ErrTmuxUnresponsive", err)
			}
			return id
		}, true},
		{"reuse: create timed out", func(t *testing.T, e *killEnv, _ *api.Client) string {
			return e.reuseTimesOut(t, e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)}), reuseRequest{}, false).ID
		}, true},
		{"resume: launched, agent died before reporting in", func(t *testing.T, e *killEnv, _ *api.Client) string {
			r := e.seedResumable(t, rlkSettled(e), agentGone)
			e.agentOnCreate(r.ID, agentAlive)
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("resume: %v", err)
			}
			advResumeAgentDies(t, e, r.ID)
			return r.ID
		}, false},
	}
}

// TestAdviceFollow_B1_LaunchInProgressFindMissing: refused alike inside the grace period; then find-missing, get,
// and resume a row with a session id, or (none, b.uey) spawn the id with the reuse opt-in.
// B1: "... since its launch start; nothing was written" / "... since its launch start; the row has no session id, so there is no conversation to resume: once find-missing marks it missing, if get still shows no session id, spawn the id again with the reuse opt-in reuse_finished (--reuse-finished on the CLI); nothing was written"
func TestAdviceFollow_B1_LaunchInProgressFindMissing(t *testing.T) {
	t.Parallel()
	adviceAssertGoDoc(t, "resume.go", "Resume", advResumeLaunchInProgressDoc)
	adviceAssertGoDoc(t, "errors.go", "ErrSpawnNotResumable", advResumeLaunchNoSessionErrDoc)
	for _, o := range advResumePendings() {
		t.Run(o.name, func(t *testing.T) {
			e := newKillEnv(t)
			c, _ := e.client(t)
			id := o.pend(t, e, c)
			before := e.columns(t, id)
			advice := advResumeLaunchInProgress
			if o.noSession {
				advice = advResumeLaunchInProgressNoSession
			}

			_, refused := e.resume(id)

			adviceAssertAdvice(t, refused, api.ErrSpawnNotResumable, advice)
			e.assertRowUnchanged(t, id, before)
			if st := adviceAwaitFinished(t, c, e.clock, id, func() {
				if _, err := e.resume(id); err == nil || err.Error() != refused.Error() {
					t.Errorf("resume inside the grace period = %v; want the same refusal %q", err, refused)
				}
			}); st != store.StateMissing {
				t.Fatalf("get %s past the pending grace period: state %q; want missing", id, st)
			}
			row, err := c.Get(id)
			if err != nil || (row.ClaudeSessionID == "") != o.noSession {
				t.Fatalf("get %s once missing = session id %q, %v; want it empty: %v", id, row.ClaudeSessionID, err, o.noSession)
			}
			if row.ClaudeSessionID != "" {
				advResumeLaunches(t, e, id)
				return
			}
			creates := len(e.rec.SocketCallsOf(tmux.CallCreate))
			res, err := c.Spawn(fgcSpawnParams(t, id, true))
			advSpawnLaunched(t, e.dbPath, id, advSpawnNextLife(before), res, err)
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)) - creates; n != 1 {
				t.Errorf("creates by the spawn with the reuse opt-in = %d; want 1", n)
			}
		})
	}
}

// TestAdviceFollow_B2_KillOrPauseThenResume: pause a live row, or kill it and run find-missing, then resume it.
// B2 (Go doc of Resume): "a live Spawn must be paused, or killed and then marked by find-missing, before it can be resumed: follow the live-row sequence in kill's description"
func TestAdviceFollow_B2_KillOrPauseThenResume(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs); it checks every
	// record written to the shared trail since its mark.
	adviceAssertGoDoc(t, "resume.go", "Resume", advResumeLiveDoc)
	cases := []struct {
		name string
		stop func(t *testing.T, e *killEnv, r resumeRow)
	}{
		{"kill", func(t *testing.T, e *killEnv, r resumeRow) {
			c, _ := e.client(t)
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			if _, err := e.kill(r.ID); err != nil {
				t.Fatalf("kill: %v", err)
			}
			// Killed but not yet marked: the row is still live, so resume is refused and writes nothing.
			before := e.snapshotResume(t, r)
			_, err := e.resume(r.ID)
			assertOneSentinel(t, err, api.ErrSpawnNotResumable)
			e.assertResumeWroteNothing(t, before)
			if _, err := c.FindMissing(context.Background()); err != nil {
				t.Fatalf("find-missing: %v", err)
			}
			advResumeAssertState(t, c, r.ID, store.StateMissing)
		}},
		{"pause", func(t *testing.T, e *killEnv, r resumeRow) {
			fastPausePolls(t)
			e.endAfterEnter(t, r.killRow)
			exited := false
			e.rec.AfterCall(tmux.CallSendEnter, func(tmuxfix.SocketCall, error) {
				if !exited {
					exited = true
					advResumeConditionEnds(t, e, r)
				}
			})
			if _, err := e.pause(pauseParams(r.killRow)); err != nil {
				t.Fatalf("pause: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedResumableRow(t, killRowSpec{State: store.StateWaiting})
			before := e.columns(t, r.ID)
			_, err := e.resume(r.ID)
			assertOneSentinel(t, err, api.ErrSpawnNotResumable)
			e.assertRowUnchanged(t, r.ID, before)

			tc.stop(t, e, r)

			advResumeLaunches(t, e, r.ID)
		})
	}
}

// TestAdviceFollow_B3_LostRaceGetThenAct: the losing resume wrote and launched nothing; get, then act on the state.
// B3: "the row changed after resume examined it and nothing was written; nothing was launched" / "was removed after resume examined it; nothing was written and nothing was launched"
func TestAdviceFollow_B3_LostRaceGetThenAct(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		race   func(t *testing.T, e *killEnv, id, other string) // lands between the resume's read and its move
		want   error
		advice string
		after  string // get's state after the refusal ("": no row)
	}{
		{"another write changed the row", func(t *testing.T, e *killEnv, id, other string) {
			if err := e.st.SetParentID(id, other); err != nil {
				t.Errorf("SetParentID: %v", err)
			}
		}, api.ErrSpawnNotResumable, advResumeLostRaceChanged, store.StateEnded},
		{"a competing resume moved the row", func(t *testing.T, e *killEnv, id, _ string) {
			if _, err := e.resume(id); err != nil {
				t.Errorf("competing resume: %v", err)
			}
		}, api.ErrSpawnNotResumable, advResumeLostRaceChanged, store.StatePending},
		{"the row was removed", func(t *testing.T, e *killEnv, id, _ string) {
			if err := e.st.DeleteSpawn(id); err != nil {
				t.Errorf("DeleteSpawn: %v", err)
			}
		}, api.ErrSpawnNotFound, advResumeLostRaceRemoved, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			c, _ := e.client(t)
			other := adviceOtherRow(t, e)
			r := e.seedResumable(t, rlkSettled(e), agentGone)
			s := &hookedResumeStore{st: e.st}
			var raced apitest.SpawnColumns
			var racedPresent bool
			var creates int
			s.afterGet(func() {
				tc.race(t, e, r.ID, other)
				raced, racedPresent = advResumeRow(t, e, r.ID)
				creates = len(e.rec.SocketCallsOf(tmux.CallCreate))
			})

			_, err := e.resumeWith(s, r.ID)

			adviceAssertAdvice(t, err, tc.want, tc.advice)
			if after, present := advResumeRow(t, e, r.ID); present != racedPresent || !reflect.DeepEqual(after, raced) {
				t.Errorf("row after the refused resume (present %v) = %+v; want it as the race left it (present %v) %+v",
					present, after, racedPresent, raced)
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)) - creates; n != 0 {
				t.Errorf("creates by the refused resume = %d; want 0", n)
			}
			advResumeAssertState(t, c, r.ID, tc.after)
			if tc.after == store.StatePending {
				return // another launch is in progress: the caller waits
			}
			advResumeNextStep(t, e, c, r.ID)
		})
	}
}

// advResumeRow reads id's row raw; present is false when the row is gone.
func advResumeRow(t *testing.T, e *killEnv, id string) (cols apitest.SpawnColumns, present bool) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if errors.Is(err, store.ErrSpawnNotFound) {
		return apitest.SpawnColumns{}, false
	}
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return cols, true
}

// advResumeRecourse resumes r, refused with want and writing nothing, then follows the Go doc's
// recourse: spawn r's id again with the reuse opt-in, which starts a new life.
func advResumeRecourse(t *testing.T, e *killEnv, r reuseRow, want error) {
	t.Helper()
	before := e.snapshotResume(t, r.resumeRow)
	_, err := e.resume(r.ID)
	assertOneSentinel(t, err, want)
	e.assertResumeWroteNothing(t, before)

	res, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
	if err != nil || res.ClaudeInstanceID != r.ID {
		t.Fatalf("spawn of %s with the reuse opt-in = %+v, %v (log %q); want it launched", r.ID, res, err, logs)
	}
	if cols := e.columns(t, r.ID); cols.State != store.StatePending || cols.LifeNumber != any(reuseLife+1) {
		t.Errorf("row after the reuse {state %v, life %#v}; want pending in life %d", cols.State, cols.LifeNumber, reuseLife+1)
	}
}

// TestAdviceFollow_B4_NoSessionIdSpawnWithReuse: resume refuses a row with no session id; spawn the id with reuse.
// B4 (Go doc of ErrNoSessionId): "the caller's recourse is to spawn again with the same id, opting in to reuse (SpawnParams.ReuseFinished, --reuse-finished)"
func TestAdviceFollow_B4_NoSessionIdSpawnWithReuse(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	adviceAssertGoDoc(t, "errors.go", "ErrNoSessionId", "the caller's "+advResumeReuseRecourse)
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e), NoSessionID: true})
	advResumeRecourse(t, e, r, api.ErrNoSessionId)
}

// TestAdviceFollow_B5_JsonlMissingSpawnWithReuse: resume refuses a row whose transcript is gone; spawn the id with reuse.
// B5 (Go doc of ErrJsonlMissing): "the recourse is to spawn again with the same id, opting in to reuse (SpawnParams.ReuseFinished, --reuse-finished)"
func TestAdviceFollow_B5_JsonlMissingSpawnWithReuse(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	adviceAssertGoDoc(t, "errors.go", "ErrJsonlMissing", "the "+advResumeReuseRecourse)
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
	if err := os.Remove(r.JSONLPath); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}
	advResumeRecourse(t, e, r, api.ErrJsonlMissing)
}

// TestAdviceFollow_B6_JsonlNeverWrittenSpawnWithReuse: resume refuses a row that never wrote a transcript; spawn with reuse.
// B6 (Go doc of ErrJsonlNeverWritten): "the recourse is to spawn again with the same id, opting in to reuse (SpawnParams.ReuseFinished, --reuse-finished)"
func TestAdviceFollow_B6_JsonlNeverWrittenSpawnWithReuse(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	adviceAssertGoDoc(t, "errors.go", "ErrJsonlNeverWritten", "the "+advResumeReuseRecourse)
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e), Bare: true,
		Opts: []apitest.SpawnOption{apitest.WithJsonlPath("")}})
	if err := os.Remove(r.JSONLPath); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}
	advResumeRecourse(t, e, r, api.ErrJsonlNeverWritten)
}
