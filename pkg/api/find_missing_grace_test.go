package api_test

// find_missing_grace_test.go covers find-missing's pending grace period at the
// Client (SR-11.2, SR-11.3, SR-22.8, SR-5.5, SR-4.1, SR-11.1; AC-FM-12,
// AC-FM-14, AC-FM-19, AC-KILL-17, AC-CFG-02): Client.FindMissing on a real
// store with the Client's clock, configured grace and sweep budget, its tmux
// client a tmuxfix.Recorder and its start-time reader the procfix fake. Rows
// come from killEnv (kill_fixture_test.go); the resumed-row cases are in
// find_missing_grace_launch_test.go. Inside the grace period a row is never
// judged; past it a dead recorded pane process is marked proc_absent with no
// tmux call, and a row whose process cannot be checked is decided by one
// lookup, which the Recorder (no server on the row's socket) answers Gone:
// tmux_absent.

import (
	"context"
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

// fgcWant is how a sweep past the grace period marks a row: its tick reason,
// and the sweep's lookups and start-time reads (its only tmux calls are the lookups).
type fgcWant struct {
	reason         string
	lookups, reads int
}

// fgcProcAbsent is the mark of a row judged by its dead pane process: one read, no tmux call.
var fgcProcAbsent = fgcWant{"proc_absent", 0, 1}

// fgcEvidence is a pending row's recorded agent process (seed returns the
// option recording it) and how a sweep past the grace period marks the row.
type fgcEvidence struct {
	name string
	seed func(e *killEnv) apitest.SpawnOption
	want fgcWant
}

// fgcEvidences: a dead pane is judged by its process; a pane behind a /proc wall
// whose session is gone (an aborted launch, AC-KILL-17) and a row with no pane
// (SR-11.3) are judged by the lookup.
var fgcEvidences = []fgcEvidence{
	{"dead pane", func(e *killEnv) apitest.SpawnOption { return fgcAgentPane(e, procfix.Gone()) }, fgcProcAbsent},
	{"pane behind a /proc wall, session gone", func(e *killEnv) apitest.SpawnOption {
		return fgcAgentPane(e, procfix.Unreadable())
	}, fgcWant{"tmux_absent", 1, 1}},
	{"no process identity", func(*killEnv) apitest.SpawnOption { return apitest.WithNoPane() }, fgcWant{"tmux_absent", 1, 0}},
}

// fgcAgentPane records a fresh pane pid with a start time, p in e.pc, and returns the option recording it.
func fgcAgentPane(e *killEnv, p procfix.Process) apitest.SpawnOption {
	pid := e.newPID()
	e.pc.Set(pid, p)
	return apitest.WithLaunchIdentity(store.LaunchIdentity{
		Token:  strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: pid, PaneStarttime: apitest.LinuxProcStarttime})
}

// fgcSeedPending seeds a pending row through apitest.SeedSpawn with sessionID
// ("" for none), the evidence option ev and opts, and returns its id.
func fgcSeedPending(t *testing.T, e *killEnv, sessionID string, ev apitest.SpawnOption, opts ...apitest.SpawnOption) string {
	t.Helper()
	id, err := apitest.SeedSpawn(e.dbPath, "fgc-"+uuid.NewString()[:8], store.StatePending, "", "off", sessionID, false,
		append([]apitest.SpawnOption{ev}, opts...)...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	return id
}

// fgcRun is one sweep's result, its tmux calls, lookups and start-time reads.
type fgcRun struct {
	res                  api.FindMissingResult
	tmux, lookups, reads int
}

// fgcSweep runs c.FindMissing and returns what it did.
func fgcSweep(t *testing.T, c *api.Client, rec *tmuxfix.Recorder, pc *procfix.Checker) fgcRun {
	t.Helper()
	tmuxBefore, lookupsBefore, readsBefore := pendCalls(rec), len(rec.SocketCallsOf(tmux.CallLookup)), len(pc.StartTimeCalls())
	res, err := c.FindMissing(context.Background())
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	return fgcRun{res: res, tmux: pendCalls(rec) - tmuxBefore,
		lookups: len(rec.SocketCallsOf(tmux.CallLookup)) - lookupsBefore, reads: len(pc.StartTimeCalls()) - readsBefore}
}

// fgcAssertPending fails unless the sweep left id's row alone (pending, no
// liveness note, no tick, in no list, no tmux call) after wantReads start-time reads.
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
		t.Errorf("tmux calls %d, start-time reads %d; want 0 and %d", run.tmux, run.reads, wantReads)
	}
}

// fgcAssertInside fails unless the sweep held id's row inside its grace period: untouched, nothing read, no tmux call.
func fgcAssertInside(t *testing.T, dbPath, id string, run fgcRun) {
	t.Helper()
	fgcAssertPending(t, dbPath, id, run, 0)
}

// fgcAssertMarked fails unless id's row is missing with ended_at set and no
// launch start, listed in ids, with one tick of want's reason, after want's lookups and reads.
func fgcAssertMarked(t *testing.T, dbPath, id string, run fgcRun, want fgcWant) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	if cols.State != store.StateMissing || cols.EndedAt == nil || cols.LaunchStartedAt != nil || !slices.Contains(run.res.IDs, id) {
		t.Errorf("row {state %v, ended_at %#v, launch %#v}, ids %v; want missing, set, NULL and listed",
			cols.State, cols.EndedAt, cols.LaunchStartedAt, run.res.IDs)
	}
	ticks := pendTrail(t, "ad.find_missing.tick", id)
	if len(ticks) != 1 || ticks[0]["reconciliation_reason"] != want.reason {
		t.Errorf("ad.find_missing.tick lines = %v; want one with reason %s", ticks, want.reason)
	} else if gone := (tmux.Result{Verdict: tmux.Gone}).Token(); want.lookups > 0 && ticks[0]["lookup_outcome"] != gone {
		t.Errorf("tick lookup_outcome = %#v; want %q", ticks[0]["lookup_outcome"], gone)
	}
	if run.tmux != want.lookups || run.lookups != want.lookups || run.reads != want.reads {
		t.Errorf("tmux calls %d (lookups %d), start-time reads %d; want %d lookup(s) only and %d",
			run.tmux, run.lookups, run.reads, want.lookups, want.reads)
	}
}

// TestFindMissingGraceLaunchKinds: a pending row of each launch kind and recorded
// agent process is untouched 1 s before the default grace ends, measured from its launch start, and marked 1 s after.
func TestFindMissingGraceLaunchKinds(t *testing.T) {
	kinds := []struct {
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
	for _, kind := range kinds {
		for _, ev := range fgcEvidences {
			t.Run(kind.name+"/"+ev.name, func(t *testing.T) {
				e := newKillEnv(t)
				c, _ := e.client(t)
				launch := e.clock.Now()
				id := fgcSeedPending(t, e, kind.sessionID, ev.seed(e), kind.opts(launch)...)

				fgcSetClock(e.clock, launch.Add(fmGrace-time.Second))
				fgcAssertInside(t, e.dbPath, id, fgcSweep(t, c, e.rec, e.pc))

				fgcSetClock(e.clock, launch.Add(fmGrace+time.Second))
				fgcAssertMarked(t, e.dbPath, id, fgcSweep(t, c, e.rec, e.pc), ev.want)
			})
		}
	}
}

// TestFindMissingGraceConfigured: pending_grace_seconds at its safe minimum holds a
// row 1 s before that period ends and marks it 1 s after, by its evidence; a default Client still holds it then.
func TestFindMissingGraceConfigured(t *testing.T) {
	grace := time.Duration(fgcMinGraceSeconds) * time.Second
	if grace+time.Second >= fmGrace {
		t.Fatalf("precondition: minimum grace %v + 1 s is not below the default %v", grace, fmGrace)
	}
	for _, ev := range fgcEvidences {
		t.Run(ev.name, func(t *testing.T) {
			e := newKillEnv(t)
			configured, _ := e.client(t, apitest.TmuxInt(config.TmuxPendingGraceSeconds, fgcMinGraceSeconds))
			dflt, _ := e.client(t)
			launch := e.clock.Now()
			id := fgcSeedPending(t, e, "", ev.seed(e), apitest.WithStartedAt(launch), apitest.WithLaunchStartedAt(launch.UnixMilli()))

			fgcSetClock(e.clock, launch.Add(grace-time.Second))
			fgcAssertInside(t, e.dbPath, id, fgcSweep(t, configured, e.rec, e.pc))

			fgcSetClock(e.clock, launch.Add(grace+time.Second))
			fgcAssertInside(t, e.dbPath, id, fgcSweep(t, dflt, e.rec, e.pc))
			fgcAssertMarked(t, e.dbPath, id, fgcSweep(t, configured, e.rec, e.pc), ev.want)
		})
	}
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
			id := fgcSeedPending(t, e, "", fgcAgentPane(e, procfix.Gone()), apitest.WithStartedAt(now), tc.opt(now))

			run := fgcSweep(t, c, e.rec, e.pc)
			if tc.inside {
				fgcAssertInside(t, e.dbPath, id, run)
			} else {
				fgcAssertMarked(t, e.dbPath, id, run, fgcProcAbsent)
			}
		})
	}
}
