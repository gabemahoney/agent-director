package api_test

// find_missing_grace_test.go covers find-missing's pending grace period at the
// Client (SR-11.2, SR-22.8, SR-5.5, SR-4.1; AC-FM-12, AC-FM-14, AC-FM-16,
// AC-RES-14, AC-CFG-02): Client.FindMissing on a real store with the Client's
// clock and configured grace, its tmux client a tmuxfix.Recorder. Rows come
// from killEnv (kill_fixture_test.go) and resumeEnv (resume_fixture_test.go).
// Past the grace period a pending row records no pid, so the real environ
// probe finds no process carrying its id and marks it missing.

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
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

// fgcSeedPending seeds a pending row through apitest.SeedSpawn with sessionID
// ("" for none) and opts, and returns its id.
func fgcSeedPending(t *testing.T, dbPath, sessionID string, opts ...apitest.SpawnOption) string {
	t.Helper()
	id, err := apitest.SeedSpawn(dbPath, "fgc-"+uuid.NewString()[:8], store.StatePending, "", "off", sessionID, false, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	return id
}

// fgcSweep runs c.FindMissing and returns its result and the tmux calls rec
// recorded during it.
func fgcSweep(t *testing.T, c *api.Client, rec *tmuxfix.Recorder) (api.FindMissingResult, int) {
	t.Helper()
	before := pendCalls(rec)
	res, err := c.FindMissing(context.Background())
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	return res, pendCalls(rec) - before
}

// fgcAssertInside fails unless the sweep that returned res with calls tmux
// calls left id's row alone: pending, no liveness note, no tick, in no list.
func fgcAssertInside(t *testing.T, dbPath, id string, res api.FindMissingResult, calls int) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	if cols.State != store.StatePending || cols.LivenessNote != nil || cols.LivenessUnverifiedSince != nil {
		t.Errorf("row {state %v, liveness_note %#v, unverified_since %#v}; want pending, NULL, NULL",
			cols.State, cols.LivenessNote, cols.LivenessUnverifiedSince)
	}
	if slices.Contains(res.IDs, id) || slices.Contains(res.UnverifiedIDs, id) {
		t.Errorf("result %+v lists %s; want it in neither ids nor unverified_ids", res, id)
	}
	if n := len(pendTrail(t, "ad.find_missing.tick", id)); n != 0 {
		t.Errorf("ad.find_missing.tick lines for the row = %d; want 0", n)
	}
	if calls != 0 {
		t.Errorf("tmux calls by find-missing = %d; want 0", calls)
	}
}

// fgcAssertMarked fails unless id's row is missing, listed in res.IDs and has
// one proc_absent tick.
func fgcAssertMarked(t *testing.T, dbPath, id string, res api.FindMissingResult) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	if cols.State != store.StateMissing || !slices.Contains(res.IDs, id) {
		t.Errorf("row state %v, ids %v; want missing and listed", cols.State, res.IDs)
	}
	ticks := apiTicksWithReason(pendTrail(t, "ad.find_missing.tick", id), "proc_absent")
	if len(ticks) != 1 {
		t.Errorf("proc_absent ticks for the row = %d; want 1", len(ticks))
	}
}

// TestFindMissingGraceLaunchKinds: a pending row of each launch kind is untouched
// 1 s before the default grace ends, measured from its launch start, and marked 1 s after.
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
			id := fgcSeedPending(t, e.dbPath, tc.sessionID, tc.opts(launch)...)

			fgcSetClock(e.clock, launch.Add(fmGrace-time.Second))
			res, calls := fgcSweep(t, c, e.rec)
			fgcAssertInside(t, e.dbPath, id, res, calls)

			fgcSetClock(e.clock, launch.Add(fmGrace+time.Second))
			res, _ = fgcSweep(t, c, e.rec)
			fgcAssertMarked(t, e.dbPath, id, res)
		})
	}
}

// TestFindMissingGraceConfigured: pending_grace_seconds at its safe minimum holds
// a row 1 s before that period ends and marks it 1 s after; a default Client still holds it then.
func TestFindMissingGraceConfigured(t *testing.T) {
	grace := time.Duration(fgcMinGraceSeconds) * time.Second
	if grace+time.Second >= fmGrace {
		t.Fatalf("precondition: minimum grace %v + 1 s is not below the default %v", grace, fmGrace)
	}
	e := newKillEnv(t)
	configured, _ := e.client(t, apitest.TmuxInt(config.TmuxPendingGraceSeconds, fgcMinGraceSeconds))
	dflt, _ := e.client(t)
	launch := e.clock.Now()
	id := fgcSeedPending(t, e.dbPath, "", apitest.WithStartedAt(launch), apitest.WithLaunchStartedAt(launch.UnixMilli()))

	fgcSetClock(e.clock, launch.Add(grace-time.Second))
	res, calls := fgcSweep(t, configured, e.rec)
	fgcAssertInside(t, e.dbPath, id, res, calls)

	fgcSetClock(e.clock, launch.Add(grace+time.Second))
	res, calls = fgcSweep(t, dflt, e.rec)
	fgcAssertInside(t, e.dbPath, id, res, calls)
	res, _ = fgcSweep(t, configured, e.rec)
	fgcAssertMarked(t, e.dbPath, id, res)
}

// TestFindMissingGraceLaunchStartValues: with the clock at started_at, an absent,
// unreadable or out-of-range launch start is judged at once; a future one is held.
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
			id := fgcSeedPending(t, e.dbPath, "", apitest.WithStartedAt(now), tc.opt(now))

			res, calls := fgcSweep(t, c, e.rec)
			if tc.inside {
				fgcAssertInside(t, e.dbPath, id, res, calls)
			} else {
				fgcAssertMarked(t, e.dbPath, id, res)
			}
		})
	}
}

// fgcResumeLaunched resumes an ended row through e.c and returns it with the
// launch start (ms) its move recorded.
func fgcResumeLaunched(t *testing.T, e *resumeEnv) (resumableRow, int64) {
	t.Helper()
	r := e.seedResumable(t, store.StateEnded)
	if _, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	cols := e.columns(t, r.ID)
	start, ok := cols.LaunchStartedAt.(int64)
	if cols.State != store.StatePending || !ok {
		t.Fatalf("after resume: {state %v, launch %#v}; want pending with a launch start", cols.State, cols.LaunchStartedAt)
	}
	return r, start
}

// fgcKillSessions removes every session on e's socket, as an agent that died takes its session.
func fgcKillSessions(t *testing.T, e *resumeEnv) {
	t.Helper()
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

// TestFindMissingGraceResumedAgentDiesBeforeReportingIn (AC-RES-14): resume is
// refused until the first sweep past the grace period marks the row missing, then it launches.
func TestFindMissingGraceResumedAgentDiesBeforeReportingIn(t *testing.T) {
	e := newResumeEnv(t)
	r, start := fgcResumeLaunched(t, e)
	launch := time.UnixMilli(start)
	fgcKillSessions(t, e)
	fgcAssertResumeRefused(t, e, r.ID)

	fgcSetClock(e.clock, launch.Add(fmGrace-time.Second))
	res, calls := fgcSweep(t, e.c, e.rec)
	fgcAssertInside(t, e.dbPath, r.ID, res, calls)
	fgcAssertResumeRefused(t, e, r.ID)

	fgcSetClock(e.clock, launch.Add(fmGrace+time.Second))
	res, _ = fgcSweep(t, e.c, e.rec)
	fgcAssertMarked(t, e.dbPath, r.ID, res)
	fgcAssertResumeLaunches(t, e, r.ID, start)
}

// TestFindMissingGraceResumedAgentReportsInThenEnds (AC-RES-14): once the resumed
// agent reports in the row is live and refused; after its life ends resume launches.
func TestFindMissingGraceResumedAgentReportsInThenEnds(t *testing.T) {
	e := newResumeEnv(t)
	r, start := fgcResumeLaunched(t, e)

	pendSessionStart(t, e, r.ID, r.JSONLPath)
	if cols := e.columns(t, r.ID); cols.State != store.StateWaiting {
		t.Fatalf("after SessionStart: state %v; want waiting", cols.State)
	}
	fgcAssertResumeRefused(t, e, r.ID)

	relifeSessionEnd(t, e, r.ID)
	fgcKillSessions(t, e)
	if cols := e.columns(t, r.ID); cols.State != store.StateEnded {
		t.Fatalf("after SessionEnd: state %v; want ended", cols.State)
	}
	fgcSetClock(e.clock, time.UnixMilli(start).Add(fmGrace+time.Second))
	fgcAssertResumeLaunches(t, e, r.ID, start)
}
