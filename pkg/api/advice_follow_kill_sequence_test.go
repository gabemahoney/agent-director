package api_test

// advice_follow_kill_sequence_test.go: b.fji literal-follow tests for the
// live-row sequence in kill's Description (advice inventory C11) and delete's
// recovery pointer (F1), run through one Client on the kill fixture's
// virtual clock: rows made by real spawns and resumes where a launch matters,
// each sequence step taken as written.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// advKillPace is the sequence's "about 5 s" between find-missing runs and after step 4's kill.
const advKillPace = 5 * time.Second

// advKillSteps are the six steps of the live-row sequence, word for word.
var advKillSteps = []string{
	"1. kill and check the result; on an error follow its class, never delete the row.",
	"2. If the row is pending, wait until its launch start (status) plus the pending grace period (60 s unless configured); inside it, wait and check again, never escalate.",
	"3. Run find-missing, then check status; repeat about 5 s apart until the row is ended or missing, at most three runs.",
	"4. Still live: kill once more, wait about 5 s, run find-missing once more and check.",
	"5. Still live: escalate to a human.",
	"6. Then resume the row if it has a session id and the caller wants the conversation back; otherwise spawn with reuse_finished (--reuse-finished on the CLI); callers whose ids agent-director mints spawn fresh.",
}

// advKillScene is a row to end and relaunch: its id and what happens in the
// world right after the sequence's first kill (nil: nothing).
type advKillScene struct {
	id             string
	afterFirstKill func()
}

// advKillAgentExitsAtKill makes the agent pane of id's current launch exit
// when its pane is killed.
func advKillAgentExitsAtKill(t *testing.T, e *killEnv, id string) {
	t.Helper()
	row, err := e.st.GetSpawn(id)
	if err != nil || row.Identity.PanePID <= 0 {
		t.Fatalf("GetSpawn(%s) = pane pid %d, %v; want a recorded pane", id, row.Identity.PanePID, err)
	}
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), row.Identity.PanePID)
}

// advKillServer binds a server on e's default socket holding a bystander session.
func advKillServer(t *testing.T, e *killEnv) {
	t.Helper()
	srv := killRow{Socket: e.defaultSocket}
	e.ensureServer(&srv)
	e.seedBystander(t, srv.Socket)
	e.syncServers()
}

// advKillTrust is a new CLAUDE_CONFIG_DIR for a spawn's pre-trust.
func advKillTrust(t *testing.T) map[string]string {
	return seedTrustConfig(t, t.TempDir(), trustLacksEntry).extraEnv()
}

// advKillLive is a waiting row with a session id, its agent running in its own session.
func advKillLive(t *testing.T, e *killEnv, _ *api.Client) advKillScene {
	t.Helper()
	r := e.seedResumableRow(t, killRowSpec{State: store.StateWaiting})
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
	return advKillScene{id: r.ID}
}

// advKillPendingSpawn is a fresh plain spawn's pending row, its agent at a startup prompt.
func advKillPendingSpawn(t *testing.T, e *killEnv, c *api.Client) advKillScene {
	t.Helper()
	advKillServer(t, e)
	id := "advkill-" + uuid.NewString()[:8]
	e.agentOnCreate(id, agentAlive)
	if _, err := c.Spawn(api.SpawnParams{ClaudeInstanceID: id, CWD: t.TempDir(), ExtraEnv: advKillTrust(t)}); err != nil {
		t.Fatalf("Spawn(%s): %v", id, err)
	}
	advKillAgentExitsAtKill(t, e, id)
	return advKillScene{id: id}
}

// advKillPendingResume is a resumed row's pending launch, its agent at a startup prompt.
func advKillPendingResume(t *testing.T, e *killEnv, c *api.Client) advKillScene {
	t.Helper()
	r := e.seedResumable(t, defWindow, agentGone)
	e.agentOnCreate(r.ID, agentAlive)
	if _, err := c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID}); err != nil {
		t.Fatalf("Resume(%s): %v", r.ID, err)
	}
	advKillAgentExitsAtKill(t, e, r.ID)
	return advKillScene{id: r.ID}
}

// advKillCreateTimedOut is a fresh plain spawn whose create timed out creating
// nothing, so its row stays pending with no session; with late, the launch's
// session appears right after the first kill, its agent at a startup prompt.
func advKillCreateTimedOut(late bool) func(*testing.T, *killEnv, *api.Client) advKillScene {
	return func(t *testing.T, e *killEnv, c *api.Client) advKillScene {
		t.Helper()
		advKillServer(t, e)
		id := "advkill-" + uuid.NewString()[:8]
		e.agentOnCreate(id, agentAlive)
		e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallCreate)
		if _, err := c.Spawn(api.SpawnParams{ClaudeInstanceID: id, CWD: t.TempDir(),
			ExtraEnv: advKillTrust(t)}); !errors.Is(err, api.ErrTmuxUnresponsive) {
			t.Fatalf("Spawn(%s) = %v; want the create's ErrTmuxUnresponsive", id, err)
		}
		sc := advKillScene{id: id}
		if late {
			sc.afterFirstKill = func() {
				row, err := e.st.GetSpawn(id)
				if err != nil {
					t.Fatalf("GetSpawn(%s): %v", id, err)
				}
				reply, err := e.rec.NewSession(row.Identity.Socket, row.TmuxSessionName, row.CWD, nil, nil,
					row.Identity.Token, id, e.storeID)
				if err != nil {
					t.Fatalf("the launch's late create: %v", err)
				}
				e.setAfterCall(tmux.CallKillPane, procfix.Gone(), reply.PanePID)
			}
		}
		return sc
	}
}

// advKillSeq is one run of the live-row sequence on id through c: what it
// waited at step 2, the find-missing runs, whether step 4 ran and step 6's branch.
type advKillSeq struct {
	e      *killEnv
	c      *api.Client
	id     string
	grace  time.Duration
	waited time.Duration
	sweeps int
	step4  bool
	branch string
}

// state is the row's state as status shows it.
func (s *advKillSeq) state(t *testing.T) api.StatusResult {
	t.Helper()
	st, err := s.c.Status(s.id)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return st
}

// finished reports that status shows the row ended or missing.
func (s *advKillSeq) finished(t *testing.T) bool {
	t.Helper()
	st := s.state(t).State
	return st == store.StateEnded || st == store.StateMissing
}

// findMissing runs find-missing once.
func (s *advKillSeq) findMissing(t *testing.T) {
	t.Helper()
	if _, err := s.c.FindMissing(context.Background()); err != nil {
		t.Fatalf("find-missing: %v", err)
	}
	s.sweeps++
}

// kill runs kill and fails on an error (the scenes' kills all succeed).
func (s *advKillSeq) kill(t *testing.T, step string) api.KillResult {
	t.Helper()
	res, err := s.c.Kill(api.KillParams{ClaudeInstanceID: s.id})
	if err != nil {
		t.Fatalf("%s: kill: %v; want success", step, err)
	}
	return res
}

// run takes the six steps as written; afterFirstKill (nil: none) runs after
// step 1. It returns step 1's result.
func (s *advKillSeq) run(t *testing.T, wantConversation bool, afterFirstKill func()) api.KillResult {
	t.Helper()
	// Step 1.
	first := s.kill(t, "step 1")
	if afterFirstKill != nil {
		afterFirstKill()
	}
	// Step 2: wait until launch start plus grace, checking again inside it.
	for st := s.state(t); st.State == store.StatePending; st = s.state(t) {
		if st.LaunchStartedAt == nil {
			t.Fatalf("step 2: status shows a pending row with no launch start")
		}
		left := st.LaunchStartedAt.Add(s.grace).Sub(s.e.clock.Now())
		if left <= 0 {
			break
		}
		d := min(left, advKillPace)
		s.e.clock.Advance(d)
		s.waited += d
	}
	// Step 3: at most three find-missing runs about 5 s apart.
	done := false
	for run := 1; run <= 3 && !done; run++ {
		if run > 1 {
			s.e.clock.Advance(advKillPace)
		}
		s.findMissing(t)
		done = s.finished(t)
	}
	if !done {
		// Step 4, then step 5.
		s.step4 = true
		s.kill(t, "step 4")
		s.e.clock.Advance(advKillPace)
		s.findMissing(t)
		if !s.finished(t) {
			t.Fatalf("step 5: row %s still %s after step 4; the sequence escalates to a human", s.id, s.state(t).State)
		}
	}
	// Step 6.
	row, err := s.c.Get(s.id)
	if err != nil {
		t.Fatalf("step 6: get: %v", err)
	}
	creates := len(s.e.rec.SocketCallsOf(tmux.CallCreate))
	if row.ClaudeSessionID != "" && wantConversation {
		s.branch = "resume"
		_, err = s.c.Resume(api.ResumeParams{ClaudeInstanceID: s.id})
	} else {
		s.branch = "reuse"
		_, err = s.c.Spawn(api.SpawnParams{ClaudeInstanceID: s.id, ReuseFinished: true, CWD: row.CWD, ExtraEnv: advKillTrust(t)})
	}
	if err != nil {
		t.Fatalf("step 6: %s of the %s row: %v", s.branch, row.State, err)
	}
	if n := len(s.e.rec.SocketCallsOf(tmux.CallCreate)) - creates; n != 1 {
		t.Errorf("step 6: %d creates; want 1", n)
	}
	if st := s.state(t).State; st != store.StatePending {
		t.Errorf("step 6: state %v; want pending (a new launch)", st)
	}
	return first
}

// TestAdviceFollow_C11_LiveRowSequence: the six steps taken literally end
// every live or pending row resumable or reusable, within the stated bounds.
func TestAdviceFollow_C11_LiveRowSequence(t *testing.T) {
	// C11 kill Description's live-row sequence, its six steps quoted in advKillSteps: "1. kill and check the result; ... otherwise spawn with
	// reuse_finished (--reuse-finished on the CLI); ...".
	adviceAssertManifest(t, "kill", "", advKillSteps...)
	cases := []struct {
		name         string
		scene        func(*testing.T, *killEnv, *api.Client) advKillScene
		conversation bool   // the caller wants the conversation back
		branch       string // step 6's
		sent         bool   // step 1's kill_sent
		step4        bool
	}{
		{"live row, conversation wanted", advKillLive, true, "resume", true, false},
		{"live row, a new life wanted", advKillLive, false, "reuse", true, false},
		{"pending fresh spawn, its session running", advKillPendingSpawn, true, "reuse", true, false},
		{"pending resume, its session running", advKillPendingResume, true, "resume", true, false},
		{"pending fresh spawn, its create made no session", advKillCreateTimedOut(false), true, "reuse", false, false},
		{"pending fresh spawn, its session created after the kill", advKillCreateTimedOut(true), true, "reuse", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			c, _ := e.client(t)
			sc := tc.scene(t, e, c)
			pending := e.columns(t, sc.id).State == store.StatePending
			s := &advKillSeq{e: e, c: c, id: sc.id, grace: e.cfg.EffectivePendingGrace()}
			first := s.run(t, tc.conversation, sc.afterFirstKill)

			if first.KillSent != tc.sent {
				t.Errorf("step 1 kill_sent = %v; want %v", first.KillSent, tc.sent)
			}
			if s.branch != tc.branch || s.step4 != tc.step4 {
				t.Errorf("step 6 %s, step 4 ran %v; want %s, %v", s.branch, s.step4, tc.branch, tc.step4)
			}
			if pending != (s.waited > 0) || s.waited > s.grace {
				t.Errorf("step 2 waited %v (row pending %v); want a wait within the %v grace exactly when pending",
					s.waited, pending, s.grace)
			}
			limit := 3 // step 3's runs
			if tc.step4 {
				limit++
			}
			if s.sweeps > limit {
				t.Errorf("find-missing runs = %d; want at most %d", s.sweeps, limit)
			}
		})
	}
}

// TestAdviceFollow_F1_DeleteDescriptionKillThenFindMissing: kill then
// find-missing marks a stuck live row missing, and the respawn with the
// reuse opt-in starts it again.
func TestAdviceFollow_F1_DeleteDescriptionKillThenFindMissing(t *testing.T) {
	// F1 delete Description: "respawn with spawn reuse_finished (--reuse-finished on the CLI); for a stuck live row, kill then find-missing."
	adviceAssertManifest(t, "delete", "", "respawn with spawn reuse_finished (--reuse-finished on the CLI); for a stuck live row, kill then find-missing.")
	grace := config.Default().Tmux.EffectivePendingGrace()
	cases := []struct {
		name   string
		scene  func(*testing.T, *killEnv, *api.Client) advKillScene
		stuck  time.Duration // how long the row has been stuck when the caller acts
		broken string        // why the follow fails ("": it works)
	}{
		{"live row", advKillLive, 0, ""},
		{"pending row stuck past the pending grace period", advKillPendingSpawn, grace + time.Second, ""},
		{"pending row inside the pending grace period", advKillPendingSpawn, 0,
			"kill then find-missing leaves a pending row inside the pending grace period pending (find-missing " +
				"does not judge it), so the respawn with the reuse opt-in collides; the text omits the live-row " +
				"sequence's step 2 wait"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			c, _ := e.client(t)
			sc := tc.scene(t, e, c)
			e.clock.Advance(tc.stuck)

			if _, err := c.Kill(api.KillParams{ClaudeInstanceID: sc.id}); err != nil {
				t.Fatalf("kill: %v; want success", err)
			}
			if _, err := c.FindMissing(context.Background()); err != nil {
				t.Fatalf("find-missing: %v", err)
			}
			if tc.broken != "" {
				knownBrokenAdvice(t, "F1", tc.broken)
			}
			if st, _ := c.Status(sc.id); st.State != store.StateMissing {
				t.Errorf("state after kill then find-missing = %v; want missing", st.State)
			}
			row, err := c.Get(sc.id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if _, err := c.Spawn(api.SpawnParams{ClaudeInstanceID: sc.id, ReuseFinished: true, CWD: row.CWD,
				ExtraEnv: advKillTrust(t)}); err != nil {
				t.Fatalf("respawn with the reuse opt-in: %v; want a new launch", err)
			}
			if st, _ := c.Status(sc.id); st.State != store.StatePending {
				t.Errorf("state after the respawn = %v; want pending", st.State)
			}
		})
	}
}
