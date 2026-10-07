package api_test

// find_missing_grace_test.go covers find-missing's pending grace period at the Client (SR-11.2, SR-11.3, SR-4.1;
// AC-FM-12, AC-FM-14, AC-CFG-02): Client.FindMissing on a real store with the Client's clock and configured grace,
// its tmux client a tmuxfix.Recorder (no server: Gone) and its start-time reader the procfix fake; rows come from
// killEnv (kill_fixture_test.go). Inside the grace period a row is never judged; past it a dead recorded pane
// process is marked proc_absent with no tmux call, and a row whose process cannot be checked is decided by one
// lookup. The grace rule's boundaries and launch-start values are store.InsidePendingGrace's and the live-row
// read's (internal/store); spawn's, reuse's and resume's rows that never report in are advice_follow_resume_test.go's
// (B1) through this same Client path.

import (
	"context"
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

// fgcMinGraceSeconds is the key's safe minimum at the default create timeout and pipe-close wait (30 s).
var fgcMinGraceSeconds = config.PendingGraceMinimumSeconds(config.DefaultCreateTimeoutMs, config.DefaultPipeCloseWaitMs)

// fgcSetClock moves clock to at.
func fgcSetClock(clock *tmuxfix.Clock, at time.Time) { clock.Advance(at.Sub(clock.Now())) }

// fgcWant is how a sweep past the grace period marks a row: its tick reason, and the sweep's lookups and
// start-time reads (its only tmux calls are the lookups).
type fgcWant struct {
	reason         string
	lookups, reads int
}

// fgcEvidences: a dead pane is judged by its process; a pane behind a /proc wall whose session is gone (an aborted
// launch, AC-KILL-17) and a row with no pane (SR-11.3) are judged by the lookup.
var fgcEvidences = []struct {
	name string
	pane bool            // a pane process is recorded
	proc procfix.Process // its answer
	want fgcWant
}{
	{"dead pane", true, procfix.Gone(), fgcWant{"proc_absent", 0, 1}},
	{"pane behind a /proc wall, session gone", true, procfix.Unreadable(), fgcWant{"tmux_absent", 1, 1}},
	{"no process identity", false, procfix.Process{}, fgcWant{"tmux_absent", 1, 0}},
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

// fgcAssertInside fails unless the sweep held id's row inside its grace period: pending, no liveness note, no tick,
// in no list, nothing read and no tmux call.
func fgcAssertInside(t *testing.T, dbPath, id string, run fgcRun) {
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
	if run.tmux != 0 || run.reads != 0 {
		t.Errorf("tmux calls %d, start-time reads %d; want none", run.tmux, run.reads)
	}
}

// fgcAssertMarked fails unless id's row is missing with ended_at set and no launch start, listed in ids, with one
// tick of want's reason from pending, after want's lookups and reads.
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
	if len(ticks) != 1 || ticks[0]["reconciliation_reason"] != want.reason || ticks[0]["prior_state"] != store.StatePending {
		t.Errorf("ad.find_missing.tick lines = %v; want one with reason %s from pending", ticks, want.reason)
	} else if gone := (tmux.Result{Verdict: tmux.Gone}).Token(); want.lookups > 0 && ticks[0]["lookup_outcome"] != gone {
		t.Errorf("tick lookup_outcome = %#v; want %q", ticks[0]["lookup_outcome"], gone)
	}
	if run.tmux != want.lookups || run.lookups != want.lookups || run.reads != want.reads {
		t.Errorf("tmux calls %d (lookups %d), start-time reads %d; want %d lookup(s) only and %d",
			run.tmux, run.lookups, run.reads, want.lookups, want.reads)
	}
}

// TestFindMissingGraceConfigured: pending_grace_seconds at its safe minimum holds a row 1 s before that period
// ends, measured from its launch start though its started_at is hours old (AC-FM-16), and marks it 1 s after, by
// its evidence; a default Client still holds it then.
func TestFindMissingGraceConfigured(t *testing.T) {
	t.Parallel()
	grace := time.Duration(fgcMinGraceSeconds) * time.Second
	if grace+time.Second >= fmGrace {
		t.Fatalf("precondition: minimum grace %v + 1 s is not below the default %v", grace, fmGrace)
	}
	for _, ev := range fgcEvidences {
		t.Run(ev.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			configured, _ := e.client(t, apitest.TmuxInt(config.TmuxPendingGraceSeconds, fgcMinGraceSeconds))
			dflt, _ := e.client(t)
			launch := e.clock.Now()
			agent := apitest.WithNoPane()
			if ev.pane {
				pid := e.newPID()
				e.pc.Set(pid, ev.proc)
				agent = apitest.WithLaunchIdentity(store.LaunchIdentity{Token: strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
					Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: pid, PaneStarttime: apitest.LinuxProcStarttime})
			}
			id, err := apitest.SeedSpawn(e.dbPath, "fgc-"+uuid.NewString()[:8], store.StatePending, "", "off", "", false,
				agent, apitest.WithStartedAt(launch.Add(-9*time.Hour)), apitest.WithLaunchStartedAt(launch.UnixMilli()))
			if err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}

			fgcSetClock(e.clock, launch.Add(grace-time.Second))
			fgcAssertInside(t, e.dbPath, id, fgcSweep(t, configured, e.rec, e.pc))

			fgcSetClock(e.clock, launch.Add(grace+time.Second))
			fgcAssertInside(t, e.dbPath, id, fgcSweep(t, dflt, e.rec, e.pc))
			fgcAssertMarked(t, e.dbPath, id, fgcSweep(t, configured, e.rec, e.pc), ev.want)
		})
	}
}

// fgcSpawnParams is a spawn of id (reuse: with the opt-in) pre-trusting in its own new CLAUDE_CONFIG_DIR.
func fgcSpawnParams(t *testing.T, id string, reuse bool) api.SpawnParams {
	return api.SpawnParams{ClaudeInstanceID: id, ReuseFinished: reuse, CWD: t.TempDir(),
		ExtraEnv: map[string]string{"CLAUDE_CONFIG_DIR": t.TempDir()}}
}
