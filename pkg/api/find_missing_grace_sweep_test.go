package api_test

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
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

// TestFindMissingGraceSweep: a pending row inside the grace period (measured from its launch start) is not
// judged; past it, with no launch start, or in a non-pending state it is judged (tmux can't tell) (SR-11.2, SR-22.8).
func TestFindMissingGraceSweep(t *testing.T) {
	floor := time.Duration(config.PendingGraceFloorSeconds) * time.Second
	cases := []struct {
		name     string
		state    string
		launchMs int64
		agent    fmRowOpt
		proc     procfix.Process // the recorded process's answer
		grace    time.Duration   // 0 = fmGrace (the config default)
		want     string          // "inside", "marked" or "unverified"
	}{
		{name: "pending just inside pane", state: store.StatePending, launchMs: startedAgo(fmGrace - time.Second), agent: gracePane, want: "inside"},
		{name: "pending just inside sessionstart", state: store.StatePending, launchMs: startedAgo(fmGrace - time.Second), agent: graceSession, want: "inside"},
		{name: "pending just inside no identity", state: store.StatePending, launchMs: startedAgo(fmGrace - time.Second), agent: graceNone, want: "inside"},
		{name: "pending past pane gone", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), agent: gracePane, want: "marked"},
		{name: "pending past sessionstart gone", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), agent: graceSession, want: "marked"},
		{name: "pending past sessionstart unreadable", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), agent: graceSession, proc: procfix.Unreadable(), want: "unverified"},
		{name: "pending past no identity", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), agent: graceNone, want: "unverified"},
		{name: "pending at exactly grace pane", state: store.StatePending, launchMs: startedAgo(fmGrace), agent: gracePane, want: "marked"},
		{name: "pending at exactly grace sessionstart", state: store.StatePending, launchMs: startedAgo(fmGrace), agent: graceSession, want: "marked"},
		{name: "pending future start pane", state: store.StatePending, launchMs: startedAgo(-5 * time.Second), agent: gracePane, want: "inside"},
		{name: "pending future start sessionstart", state: store.StatePending, launchMs: startedAgo(-5 * time.Second), agent: graceSession, want: "inside"},
		{name: "pending absent or unreadable start pane", state: store.StatePending, launchMs: 0, agent: gracePane, want: "marked"},
		{name: "pending absent or unreadable start sessionstart", state: store.StatePending, launchMs: 0, agent: graceSession, want: "marked"},
		{name: "pending far-past extreme start", state: store.StatePending, launchMs: math.MinInt64, agent: gracePane, want: "marked"},
		{name: "pending max int64 start", state: store.StatePending, launchMs: math.MaxInt64, agent: gracePane, want: "inside"},
		{name: "shorter grace inside", state: store.StatePending, launchMs: startedAgo(floor - time.Second), agent: gracePane, grace: floor, want: "inside"},
		{name: "shorter grace past", state: store.StatePending, launchMs: startedAgo(floor + time.Second), agent: gracePane, grace: floor, want: "marked"},
		{name: "waiting recent start sessionstart", state: store.StateWaiting, launchMs: startedAgo(time.Second), agent: graceSession, want: "marked"},
		{name: "working recent start pane", state: store.StateWorking, launchMs: startedAgo(time.Second), agent: gracePane, want: "marked"},
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

// TestFindMissingGraceSweepMixed: one sweep skips only the inside-grace row; marked and unverified rows are
// judged, counts exclude the inside row, and its transcript still heals (SR-11.7).
func TestFindMissingGraceSweepMixed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "boot-session-uuid"
	transcript, err := spawn.JsonlPath("/tmp/proj", session)
	if err != nil {
		t.Fatalf("compute path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	pc := procfix.New()
	pc.Set(12, procfix.Unreadable()) // walled; boot, dead and gone answer gone
	boot := liveRow("boot", withLaunch(store.StatePending, startedAgo(fmGrace-time.Second)), withPane(10, fmStart))
	st := &fakeFindMissingStore{
		rows: []store.LiveSpawnIdentity{
			boot,
			liveRow("dead", withSessionStart(11, fmStart)),
			liveRow("walled", withLaunch(store.StateWaiting, 0), withSessionStart(12, fmStart)),
			liveRow("gone", withLaunch(store.StateWaiting, 0), withPane(13, fmStart)),
		},
		provisional: []store.ProvisionalTranscript{{ClaudeInstanceID: "boot", ClaudeSessionID: session, CWD: "/tmp/proj"}},
	}

	res := mustSweep(t, st, pc, fmSweep{tmux: fmCantTell()})
	assertLists(t, res, []string{"dead", "gone"}, []string{"walled"})
	assertUntouched(t, boot, st, pc, res)
	if len(st.healed) != 1 || st.healed[0].id != "boot" {
		t.Errorf("healed = %+v; want boot healed (healing runs inside grace)", st.healed)
	}
}
