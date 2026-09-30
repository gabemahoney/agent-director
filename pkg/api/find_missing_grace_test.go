package api_test

// find_missing_grace_test.go covers find-missing's pending grace period at the
// Client (SR-11.2, SR-22.8, SR-5.5, SR-4.1, SR-11.1; AC-FM-12, AC-FM-14,
// AC-FM-16, AC-FM-19, AC-RES-14, AC-CFG-02): Client.FindMissing on a real
// store with the Client's clock and configured grace, its tmux client a
// tmuxfix.Recorder and its start-time reader the procfix fake. Rows come from
// killEnv (kill_fixture_test.go) and resumeEnv (resume_fixture_test.go). Each
// row records a pane identity (pid and start time) and every liveness answer
// is the fake's: past the grace period a row whose pane process the fake
// answers gone is marked missing (proc_absent) with no tmux call, and one
// whose pane process is alive stays pending.

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// fgcMinGraceSeconds is the key's safe minimum at the default create timeout
// and pipe-close wait (30 s); fmGrace is the default grace period.
var fgcMinGraceSeconds = config.PendingGraceMinimumSeconds(config.DefaultCreateTimeoutMs, config.DefaultPipeCloseWaitMs)

// fgcSetClock moves clock to at.
func fgcSetClock(clock *tmuxfix.Clock, at time.Time) { clock.Advance(at.Sub(clock.Now())) }

// fgcDeadAgentPane gives a fresh pane pid with a recorded start time, which
// e.pc answers gone (the booting agent died), and the option recording it.
func fgcDeadAgentPane(e *killEnv) (int, apitest.SpawnOption) {
	pid := e.newPID()
	e.pc.Set(pid, procfix.Gone())
	return pid, apitest.WithLaunchIdentity(store.LaunchIdentity{
		Token:  strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: pid, PaneStarttime: apitest.LinuxProcStarttime})
}

// fgcSeedPending seeds a pending row through apitest.SeedSpawn with sessionID
// ("" for none), a dead agent pane (fgcDeadAgentPane) and opts, and returns its id and pane pid.
func fgcSeedPending(t *testing.T, e *killEnv, sessionID string, opts ...apitest.SpawnOption) (string, int) {
	t.Helper()
	pid, pane := fgcDeadAgentPane(e)
	id, err := apitest.SeedSpawn(e.dbPath, "fgc-"+uuid.NewString()[:8], store.StatePending, "", "off", sessionID, false,
		append([]apitest.SpawnOption{pane}, opts...)...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	return id, pid
}

// fgcRun is one sweep's result, its tmux calls and its reader calls for the row's pane pid.
type fgcRun struct {
	res         api.FindMissingResult
	tmux, reads int
}

// fgcSweep runs c.FindMissing and returns what it did, counting pc's reads of pid.
func fgcSweep(t *testing.T, c *api.Client, rec *tmuxfix.Recorder, pc *procfix.Checker, pid int) fgcRun {
	t.Helper()
	tmuxBefore, readsBefore := pendCalls(rec), len(pc.StartTimeCalls())
	res, err := c.FindMissing(context.Background())
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	run := fgcRun{res: res, tmux: pendCalls(rec) - tmuxBefore}
	for _, p := range pc.StartTimeCalls()[readsBefore:] {
		if p == pid {
			run.reads++
		}
	}
	return run
}

// fgcAssertPending fails unless the sweep left id's row alone (pending, no
// liveness note, no tick, in no list, no tmux call) after wantReads reads of its pane pid.
func fgcAssertPending(t *testing.T, dbPath, id string, run fgcRun, wantReads int) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	if cols.State != store.StatePending || cols.LivenessNote != nil || cols.LivenessUnverifiedSince != nil {
		t.Errorf("row {state %v, liveness_note %#v, unverified_since %#v}; want pending, NULL, NULL",
			cols.State, cols.LivenessNote, cols.LivenessUnverifiedSince)
	}
	if slices.Contains(run.res.IDs, id) || slices.Contains(run.res.UnverifiedIDs, id) {
		t.Errorf("result %+v lists %s; want it in neither ids nor unverified_ids", run.res, id)
	}
	if n := len(pendTrail(t, "ad.find_missing.tick", id)); n != 0 {
		t.Errorf("ad.find_missing.tick lines for the row = %d; want 0", n)
	}
	if run.tmux != 0 || run.reads != wantReads {
		t.Errorf("tmux calls %d, reads of the pane pid %d; want 0 and %d", run.tmux, run.reads, wantReads)
	}
}

// fgcAssertInside fails unless the sweep held id's row inside its grace period: untouched and its process never read.
func fgcAssertInside(t *testing.T, dbPath, id string, run fgcRun) {
	t.Helper()
	fgcAssertPending(t, dbPath, id, run, 0)
}

// fgcAssertMarked fails unless id's row is missing, listed in ids, has one
// proc_absent tick, and was judged by one read of its pane pid with no tmux call.
func fgcAssertMarked(t *testing.T, dbPath, id string, run fgcRun) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	if cols.State != store.StateMissing || !slices.Contains(run.res.IDs, id) {
		t.Errorf("row state %v, ids %v; want missing and listed", cols.State, run.res.IDs)
	}
	ticks := apiTicksWithReason(pendTrail(t, "ad.find_missing.tick", id), "proc_absent")
	if len(ticks) != 1 {
		t.Errorf("proc_absent ticks for the row = %d; want 1", len(ticks))
	}
	if run.tmux != 0 || run.reads != 1 {
		t.Errorf("tmux calls %d, reads of the pane pid %d; want 0 and 1", run.tmux, run.reads)
	}
}

// TestFindMissingGraceLaunchKinds: a pending row of each launch kind whose agent
// died is untouched 1 s before the default grace ends, measured from its launch start, and marked 1 s after.
func TestFindMissingGraceLaunchKinds(t *testing.T) {
	cases := []struct {
		name, sessionID string
		opts            func(launch time.Time) []apitest.SpawnOption
	}{
		{"fresh spawn", "", func(launch time.Time) []apitest.SpawnOption {
			return []apitest.SpawnOption{apitest.WithStartedAt(launch), apitest.WithLaunchStartedAt(launch.UnixMilli())}
		}},
		{"reuse: a later life, started_at reset", "", func(launch time.Time) []apitest.SpawnOption {
			return []apitest.SpawnOption{apitest.WithStartedAt(launch), apitest.WithLaunchStartedAt(launch.UnixMilli()),
				apitest.WithLifeNumber(2), apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "fgc-earlier-life", Life: 1})}
		}},
		{"resume: started_at hours old, session kept", "sess-fgc-resumed", func(launch time.Time) []apitest.SpawnOption {
			return []apitest.SpawnOption{apitest.WithStartedAt(launch.Add(-9 * time.Hour)),
				apitest.WithLaunchStartedAt(launch.UnixMilli()), apitest.WithLifeNumber(1)}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			c, _ := e.client(t)
			launch := e.clock.Now()
			id, pid := fgcSeedPending(t, e, tc.sessionID, tc.opts(launch)...)

			fgcSetClock(e.clock, launch.Add(fmGrace-time.Second))
			fgcAssertInside(t, e.dbPath, id, fgcSweep(t, c, e.rec, e.pc, pid))

			fgcSetClock(e.clock, launch.Add(fmGrace+time.Second))
			fgcAssertMarked(t, e.dbPath, id, fgcSweep(t, c, e.rec, e.pc, pid))
		})
	}
}

// TestFindMissingGraceConfigured: pending_grace_seconds at its safe minimum holds
// a dead agent's row 1 s before that period ends and marks it 1 s after; a default Client still holds it then.
func TestFindMissingGraceConfigured(t *testing.T) {
	grace := time.Duration(fgcMinGraceSeconds) * time.Second
	if grace+time.Second >= fmGrace {
		t.Fatalf("precondition: minimum grace %v + 1 s is not below the default %v", grace, fmGrace)
	}
	e := newKillEnv(t)
	configured, _ := e.client(t, apitest.TmuxInt(config.TmuxPendingGraceSeconds, fgcMinGraceSeconds))
	dflt, _ := e.client(t)
	launch := e.clock.Now()
	id, pid := fgcSeedPending(t, e, "", apitest.WithStartedAt(launch), apitest.WithLaunchStartedAt(launch.UnixMilli()))

	fgcSetClock(e.clock, launch.Add(grace-time.Second))
	fgcAssertInside(t, e.dbPath, id, fgcSweep(t, configured, e.rec, e.pc, pid))

	fgcSetClock(e.clock, launch.Add(grace+time.Second))
	fgcAssertInside(t, e.dbPath, id, fgcSweep(t, dflt, e.rec, e.pc, pid))
	fgcAssertMarked(t, e.dbPath, id, fgcSweep(t, configured, e.rec, e.pc, pid))
}

// TestFindMissingGraceLaunchStartValues: with the clock at started_at, a dead
// agent's row with an absent, unreadable or out-of-range launch start is marked at once; a future one is held.
func TestFindMissingGraceLaunchStartValues(t *testing.T) {
	year10000 := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	year0 := time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	cases := []struct {
		name   string
		opt    func(now time.Time) apitest.SpawnOption
		inside bool
	}{
		{"absent (NULL)", func(time.Time) apitest.SpawnOption { return apitest.WithNoLaunchStartedAt() }, false},
		{"stored 0", func(time.Time) apitest.SpawnOption { return apitest.WithLaunchStartedAt(0) }, false},
		{"non-integer text", func(time.Time) apitest.SpawnOption { return apitest.WithRawLaunchStartedAt("not-a-time") }, false},
		{"real number", func(time.Time) apitest.SpawnOption { return apitest.WithRawLaunchStartedAt(1.5) }, false},
		{"year 10000", func(time.Time) apitest.SpawnOption { return apitest.WithLaunchStartedAt(year10000) }, false},
		{"before year 0", func(time.Time) apitest.SpawnOption { return apitest.WithLaunchStartedAt(year0 - 1) }, false},
		{"largest int64", func(time.Time) apitest.SpawnOption { return apitest.WithLaunchStartedAt(math.MaxInt64) }, false},
		{"one hour in the future", func(now time.Time) apitest.SpawnOption {
			return apitest.WithLaunchStartedAt(now.Add(time.Hour).UnixMilli())
		}, true},
		{"last millisecond of year 9999", func(time.Time) apitest.SpawnOption { return apitest.WithLaunchStartedAt(year10000 - 1) }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			c, _ := e.client(t)
			now := e.clock.Now()
			id, pid := fgcSeedPending(t, e, "", apitest.WithStartedAt(now), tc.opt(now))

			run := fgcSweep(t, c, e.rec, e.pc, pid)
			if tc.inside {
				fgcAssertInside(t, e.dbPath, id, run)
			} else {
				fgcAssertMarked(t, e.dbPath, id, run)
			}
		})
	}
}

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
// the row to pending with the clock's launch start, other than prior.
func fgcAssertResumeLaunches(t *testing.T, e *resumeEnv, id string, prior int64) {
	t.Helper()
	creates, want := len(e.rec.SocketCallsOf(tmux.CallCreate)), e.clock.Now().UnixMilli()
	if _, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: id}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)) - creates; n != 1 {
		t.Errorf("creates by the resume = %d; want 1", n)
	}
	if cols := e.columns(t, id); cols.State != store.StatePending || cols.LaunchStartedAt != want || want == prior {
		t.Errorf("row {state %v, launch %#v}; want pending at the clock's %d, not the earlier %d",
			cols.State, cols.LaunchStartedAt, want, prior)
	}
}

// TestFindMissingGraceResumedAgentDiesBeforeReportingIn (AC-RES-14): once the resumed
// agent's pane process is gone, resume is refused until the first sweep past the grace period marks the row, then it launches.
func TestFindMissingGraceResumedAgentDiesBeforeReportingIn(t *testing.T) {
	e := newResumeEnv(t)
	r := fgcResumeLaunched(t, e)
	launch := time.UnixMilli(r.start)
	fgcAgentDies(t, e, r)
	fgcAssertResumeRefused(t, e, r.ID)

	fgcSetClock(e.clock, launch.Add(fmGrace-time.Second))
	fgcAssertInside(t, e.dbPath, r.ID, fgcSweep(t, e.c, e.rec, e.pc, r.panePID))
	fgcAssertResumeRefused(t, e, r.ID)

	fgcSetClock(e.clock, launch.Add(fmGrace+time.Second))
	fgcAssertMarked(t, e.dbPath, r.ID, fgcSweep(t, e.c, e.rec, e.pc, r.panePID))
	fgcAssertResumeLaunches(t, e, r.ID, r.start)
}

// TestFindMissingGraceResumedAgentAliveStaysPending (AC-FM-16): past the grace period a
// resumed row whose pane process is alive stays pending, though its started_at is hours old.
func TestFindMissingGraceResumedAgentAliveStaysPending(t *testing.T) {
	e := newResumeEnv(t)
	startedAt := e.clock.Now().Add(-9 * time.Hour)
	r := fgcResumeLaunched(t, e, apitest.WithStartedAt(startedAt))
	if cols := e.columns(t, r.ID); cols.StartedAt != startedAt.UTC().Format("2006-01-02 15:04:05") {
		t.Fatalf("precondition: started_at %#v; want the seeded %v kept by resume", cols.StartedAt, startedAt)
	}

	fgcSetClock(e.clock, time.UnixMilli(r.start).Add(fmGrace+time.Second))
	fgcAssertPending(t, e.dbPath, r.ID, fgcSweep(t, e.c, e.rec, e.pc, r.panePID), 1)
}

// TestFindMissingGraceResumedAgentReportsInThenEnds (AC-RES-14): once the resumed
// agent reports in the row is live and refused; after its life ends resume launches.
func TestFindMissingGraceResumedAgentReportsInThenEnds(t *testing.T) {
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
