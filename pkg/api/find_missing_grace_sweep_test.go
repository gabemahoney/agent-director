package api_test

import (
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// gracePID is the recorded agent pid of the grace-table row.
const gracePID = 4242

// The grace-table row's recorded agent process: SessionStart, pane, or none recorded.
var (
	graceSession          = withSessionStart(gracePID, fmStart)
	gracePane             = withPane(gracePID, fmStart)
	graceNone    fmRowOpt = func(*store.LiveSpawnIdentity) {}
)

// startedAgo is the launch start, in ms, that lies age before the sweep clock.
func startedAgo(age time.Duration) int64 { return fmNow.Add(-age).UnixMilli() }

// assertUntouched fails unless the sweep left r unjudged: no reader call for its pids, no write, in no result list.
func assertUntouched(t *testing.T, r store.LiveSpawnIdentity, st *fakeFindMissingStore, pc *procfix.Checker, res api.FindMissingResult) {
	t.Helper()
	for _, pid := range pc.StartTimeCalls() {
		if pid == r.PID || pid == r.Identity.PanePID {
			t.Errorf("StartTime(%d) called for %s; want no reader call inside grace", pid, r.ClaudeInstanceID)
		}
	}
	if ops := st.ops(r.ClaudeInstanceID); len(ops) != 0 {
		t.Errorf("writes on %s = %v; want none inside grace", r.ClaudeInstanceID, ops)
	}
	if slices.Contains(res.IDs, r.ClaudeInstanceID) || slices.Contains(res.UnverifiedIDs, r.ClaudeInstanceID) {
		t.Errorf("%s in result lists (ids=%v unverified_ids=%v); want neither", r.ClaudeInstanceID, res.IDs, res.UnverifiedIDs)
	}
}

// TestFindMissingGraceSweep: a pending row inside the grace period passed to the sweep (measured from its launch
// start) is not judged, even with an unusable name; past it, with no launch start, or in a non-pending state it is
// judged (tmux can't tell) (SR-11.2, SR-22.8). The rule's boundaries are store.InsidePendingGrace's (internal/store).
func TestFindMissingGraceSweep(t *testing.T) {
	t.Parallel()
	floor := time.Duration(config.PendingGraceFloorSeconds) * time.Second
	cases := []struct {
		name     string
		state    string
		launchMs int64
		agent    fmRowOpt        // the recorded agent process (fmuNamed: none, and an unusable name)
		proc     procfix.Process // the recorded process's answer
		grace    time.Duration   // 0 = fmGrace (the config default)
		want     string          // "inside", "marked" or "unverified"
	}{
		{name: "pending just inside pane", state: store.StatePending, launchMs: startedAgo(fmGrace - time.Second), agent: gracePane, want: "inside"},
		{name: "pending just inside no identity", state: store.StatePending, launchMs: startedAgo(fmGrace - time.Second), agent: graceNone, want: "inside"},
		{name: "pending just inside unusable name", state: store.StatePending, launchMs: startedAgo(fmGrace - time.Second), agent: fmuNamed(fmuReps()[0].raw), want: "inside"},
		{name: "pending past sessionstart gone", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), agent: graceSession, want: "marked"},
		{name: "pending past sessionstart unreadable", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), agent: graceSession, proc: procfix.Unreadable(), want: "unverified"},
		{name: "pending past no identity", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), agent: graceNone, want: "unverified"},
		{name: "pending absent or unreadable start", state: store.StatePending, launchMs: 0, agent: gracePane, want: "marked"},
		{name: "shorter grace past", state: store.StatePending, launchMs: startedAgo(floor + time.Second), agent: gracePane, grace: floor, want: "marked"},
		{name: "waiting recent start", state: store.StateWaiting, launchMs: startedAgo(time.Second), agent: graceSession, want: "marked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := liveRow("g-1", withLaunch(tc.state, tc.launchMs), tc.agent)
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
			pc := procfix.New()
			pc.Set(gracePID, tc.proc) // the zero Process answers gone

			res, err := runFindMissing(st, pc, fmSweep{grace: tc.grace, tmux: fmCantTell()})
			if err != nil {
				t.Fatalf("FindMissing: %v", err)
			}
			switch tc.want {
			case "inside":
				assertUntouched(t, r, st, pc, res)
				return
			case "marked":
				assertLists(t, res, []string{"g-1"}, nil)
			case "unverified":
				assertLists(t, res, nil, []string{"g-1"})
			}
			wantCalls := []int{gracePID}
			if r.PID == 0 && r.Identity.PanePID == 0 {
				wantCalls = nil
			}
			if got := pc.StartTimeCalls(); !slices.Equal(got, wantCalls) {
				t.Errorf("StartTime calls = %v; want %v", got, wantCalls)
			}
		})
	}
}
