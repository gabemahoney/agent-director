package api_test

// find_missing_grace_launch_test.go covers launches that never report in and
// resumed rows at find-missing's grace period (SR-11.2, SR-11.3, SR-8.3,
// SR-20.6; AC-FM-15, AC-FM-16, AC-RES-14) through resumeEnv
// (resume_fixture_test.go) and the helpers of find_missing_grace_test.go.

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// fgcResumed is a row resumed by fgcResumeLaunched: its launch start (ms) and the pane pid resume recorded.
type fgcResumed struct {
	resumableRow
	start   int64
	panePID int
}

// fgcResumeLaunched resumes an ended row (seeded with opts) through e.c, its
// new agent pane alive in e.pc, and returns it once pending with that pane's identity recorded.
func fgcResumeLaunched(t *testing.T, e *resumeEnv, opts ...apitest.SpawnOption) fgcResumed {
	t.Helper()
	e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
		for _, s := range e.rec.Sessions(c.Socket) {
			if s.Name == c.Target {
				e.pc.Set(s.Panes[0].PID, procfix.Alive(apitest.LinuxProcStarttime))
			}
		}
	})
	r := e.seedResumable(t, store.StateEnded, opts...)
	if _, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	cols := e.columns(t, r.ID)
	start, ok := cols.LaunchStartedAt.(int64)
	pid, _ := cols.PanePID.(int64)
	if cols.State != store.StatePending || !ok || pid <= 0 || cols.PaneStarttime != apitest.LinuxProcStarttime {
		t.Fatalf("after resume: {state %v, launch %#v, pane %#v@%#v}; want pending with a launch start and the alive pane",
			cols.State, cols.LaunchStartedAt, cols.PanePID, cols.PaneStarttime)
	}
	return fgcResumed{resumableRow: r, start: start, panePID: int(pid)}
}

// fgcAgentDies ends r's agent: its pane process is gone in e.pc and every session on e's socket goes with it.
func fgcAgentDies(t *testing.T, e *resumeEnv, r fgcResumed) {
	t.Helper()
	e.pc.Set(r.panePID, procfix.Gone())
	for _, s := range e.rec.Sessions(e.socket) {
		if err := e.rec.KillSessionID(e.socket, s.ID); err != nil {
			t.Fatalf("KillSessionID: %v", err)
		}
	}
}

// fgcAssertResumeRefused fails unless Resume on id returns ErrSpawnNotResumable with no tmux call.
func fgcAssertResumeRefused(t *testing.T, e *resumeEnv, id string) {
	t.Helper()
	before := pendCalls(e.rec)
	if _, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: id}); !errors.Is(err, api.ErrSpawnNotResumable) {
		t.Errorf("Resume = %v; want ErrSpawnNotResumable", err)
	}
	if n := pendCalls(e.rec) - before; n != 0 {
		t.Errorf("tmux calls by the refused resume = %d; want 0", n)
	}
}

// fgcAssertResumeLaunches fails unless Resume on id makes one create and moves
// the row to pending with the launch start its move records (moveStart: after
// its pre-launch lookup), other than prior.
func fgcAssertResumeLaunches(t *testing.T, e *resumeEnv, id string, prior int64) {
	t.Helper()
	creates, want := len(e.rec.SocketCallsOf(tmux.CallCreate)), e.moveStart().UnixMilli()
	if _, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: id}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)) - creates; n != 1 {
		t.Errorf("creates by the resume = %d; want 1", n)
	}
	if cols := e.columns(t, id); cols.State != store.StatePending || cols.LaunchStartedAt != want || want == prior {
		t.Errorf("row {state %v, launch %#v}; want pending at the move's %d, not the earlier %d",
			cols.State, cols.LaunchStartedAt, want, prior)
	}
}

// fgcFailCreate makes e's next create fail with failure, creating nothing.
func fgcFailCreate(e *resumeEnv, failure tmux.Failure) {
	e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: failure, ExitStatus: 1, Times: 1}, tmux.CallCreate)
}

// fgcLaunchStart returns id's launch start (ms), failing unless the row is pending with one and e holds no session.
func fgcLaunchStart(t *testing.T, e *resumeEnv, id string) int64 {
	t.Helper()
	cols := e.columns(t, id)
	start, ok := cols.LaunchStartedAt.(int64)
	if cols.State != store.StatePending || !ok || len(e.rec.Sessions(e.socket)) != 0 {
		t.Fatalf("precondition: {state %v, launch %#v}, sessions %+v; want pending with a launch start and no session",
			cols.State, cols.LaunchStartedAt, e.rec.Sessions(e.socket))
	}
	return start
}

// TestFindMissingGraceNeverReportedIn (AC-FM-15): a pending row with no session and
// no pane process is left alone inside the grace period and marked tmux_absent past it; then resume by its history.
func TestFindMissingGraceNeverReportedIn(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	spawnFails := func(failure tmux.Failure) func(*testing.T, *resumeEnv) (string, bool) {
		return func(t *testing.T, e *resumeEnv) (string, bool) {
			fgcFailCreate(e, failure)
			id := uuid.NewString()
			if _, err := e.c.Spawn(api.SpawnParams{ClaudeInstanceID: id, CWD: t.TempDir()}); err == nil {
				t.Fatalf("Spawn succeeded; want the create's failure")
			}
			return id, false
		}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, e *resumeEnv) (id string, resumable bool)
	}{
		{"fresh spawn: create timed out creating nothing", spawnFails(tmux.FailTimeout)},
		{"fresh spawn: launching process stopped before its create", func(t *testing.T, e *resumeEnv) (string, bool) {
			now := e.clock.Now()
			id, err := apitest.SeedSpawn(e.dbPath, "", store.StatePending, "", "off", "", false, apitest.WithNoPane(),
				apitest.WithTmuxSocket(e.socket), apitest.WithStartedAt(now), apitest.WithLaunchStartedAt(now.UnixMilli()))
			if err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			return id, false
		}},
		{"resume: restore failed in the store", func(t *testing.T, e *resumeEnv) (string, bool) {
			r := e.seedResumable(t, store.StateEnded)
			// The injected failure is scoped to this subtest: it would also fail the sweep's mark.
			t.Run("launch", func(t *testing.T) {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.ID)
				fgcFailCreate(e, tmux.FailUnrecognized)
				if _, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID}); !errors.Is(err, api.ErrTmuxSessionCreate) {
					t.Fatalf("Resume = %v; want ErrTmuxSessionCreate", err)
				}
			})
			return r.ID, true
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			id, resumable := tc.setup(t, e)
			start := fgcLaunchStart(t, e, id)

			fgcSetClock(e.clock, time.UnixMilli(start).Add(fmGrace-time.Second))
			fgcAssertInside(t, e.dbPath, id, fgcSweep(t, e.c, e.rec, e.pc))

			fgcSetClock(e.clock, time.UnixMilli(start).Add(fmGrace+time.Second))
			fgcAssertMarked(t, e.dbPath, id, fgcSweep(t, e.c, e.rec, e.pc), fgcWant{"tmux_absent", 1, 0})

			if resumable {
				fgcAssertResumeLaunches(t, e, id, start)
			} else if _, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: id}); !errors.Is(err, api.ErrNoSessionId) {
				t.Errorf("Resume = %v; want ErrNoSessionId", err)
			}
		})
	}
}

// TestFindMissingGraceResumedAgentDiesBeforeReportingIn (AC-RES-14): once the resumed
// agent's pane process is gone, resume is refused until the first sweep past the grace period marks the row, then it launches.
func TestFindMissingGraceResumedAgentDiesBeforeReportingIn(t *testing.T) {
	t.Parallel()
	e := newResumeEnv(t)
	r := fgcResumeLaunched(t, e)
	launch := time.UnixMilli(r.start)
	fgcAgentDies(t, e, r)
	fgcAssertResumeRefused(t, e, r.ID)

	fgcSetClock(e.clock, launch.Add(fmGrace-time.Second))
	fgcAssertInside(t, e.dbPath, r.ID, fgcSweep(t, e.c, e.rec, e.pc))
	fgcAssertResumeRefused(t, e, r.ID)

	fgcSetClock(e.clock, launch.Add(fmGrace+time.Second))
	fgcAssertMarked(t, e.dbPath, r.ID, fgcSweep(t, e.c, e.rec, e.pc), fgcProcAbsent)
	fgcAssertResumeLaunches(t, e, r.ID, r.start)
}

// TestFindMissingGraceResumedPastGraceNotMarked (AC-FM-16): past the grace period a resumed row
// (started_at hours old) stays pending with its agent alive, and unverified (probe_eacces) behind a /proc wall with its session Ours.
func TestFindMissingGraceResumedPastGraceNotMarked(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		agent procfix.Process
		note  any // the row's liveness_note after the sweep; nil: the row is left alone
	}{
		{"agent alive", procfix.Alive(apitest.LinuxProcStarttime), nil},
		{"behind a /proc wall, session Ours", procfix.Unreadable(), "probe_eacces"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			startedAt := e.clock.Now().Add(-9 * time.Hour)
			r := fgcResumeLaunched(t, e, apitest.WithStartedAt(startedAt))
			if cols := e.columns(t, r.ID); cols.StartedAt != startedAt.UTC().Format("2006-01-02 15:04:05") {
				t.Fatalf("precondition: started_at %#v; want the seeded %v kept by resume", cols.StartedAt, startedAt)
			}
			e.pc.Set(r.panePID, tc.agent)

			fgcSetClock(e.clock, time.UnixMilli(r.start).Add(fmGrace+time.Second))
			run := fgcSweep(t, e.c, e.rec, e.pc)
			if tc.note == nil {
				fgcAssertPending(t, e.dbPath, r.ID, run, 1)
				return
			}
			cols := e.columns(t, r.ID)
			if cols.State != store.StatePending || cols.LivenessNote != tc.note || cols.LivenessUnverifiedSince == nil {
				t.Errorf("row {state %v, liveness_note %#v, unverified_since %#v}; want pending, %q, set",
					cols.State, cols.LivenessNote, cols.LivenessUnverifiedSince, tc.note)
			}
			if !slices.Contains(run.res.UnverifiedIDs, r.ID) || slices.Contains(run.res.IDs, r.ID) {
				t.Errorf("result %+v; want %s in unverified_ids only", run.res, r.ID)
			}
			if run.lookups != 1 {
				t.Errorf("lookups %d; want 1", run.lookups)
			}
		})
	}
}

// TestFindMissingGraceResumedAgentReportsInThenEnds (AC-RES-14): once the resumed
// agent reports in the row is live and refused; after its life ends resume launches.
func TestFindMissingGraceResumedAgentReportsInThenEnds(t *testing.T) {
	t.Parallel()
	e := newResumeEnv(t)
	r := fgcResumeLaunched(t, e)

	pendSessionStart(t, e, r.ID, r.JSONLPath)
	if cols := e.columns(t, r.ID); cols.State != store.StateWaiting {
		t.Fatalf("after SessionStart: state %v; want waiting", cols.State)
	}
	fgcAssertResumeRefused(t, e, r.ID)

	relifeSessionEnd(t, e, r.ID)
	fgcAgentDies(t, e, r)
	if cols := e.columns(t, r.ID); cols.State != store.StateEnded {
		t.Fatalf("after SessionEnd: state %v; want ended", cols.State)
	}
	fgcSetClock(e.clock, time.UnixMilli(r.start).Add(fmGrace+time.Second))
	fgcAssertResumeLaunches(t, e, r.ID, r.start)
}
