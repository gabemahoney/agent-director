package api_test

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// countingProber is a fakeProber that also counts Probe calls.
type countingProber struct {
	set   map[string]struct{}
	calls int
}

func (p *countingProber) Probe(_ context.Context) (map[string]struct{}, error) {
	p.calls++
	return p.set, nil
}

// graceRow builds a live row: full identity (checker path) or partial (environ fallback).
func graceRow(id, state string, launchMs int64, full bool) store.LiveSpawnIdentity {
	r := store.LiveSpawnIdentity{ClaudeInstanceID: id, State: state, LaunchStartedAtMillis: launchMs}
	if full {
		r.PID, r.ProcStarttime = 4242, "9988"
	}
	return r
}

// startedAgo is the launch start, in ms, that lies age before the sweep clock.
func startedAgo(age time.Duration) int64 { return fmNow.Add(-age).UnixMilli() }

// assertUntouched fails unless the sweep left id unjudged: no checker query, no judgement write, in no result list.
func assertUntouched(t *testing.T, id string, st *fakeFindMissingStore, chk *fakeChecker, res api.FindMissingResult) {
	t.Helper()
	if chk.queried(id) {
		t.Errorf("checker queried for %s; want no query inside grace", id)
	}
	for _, c := range st.callSeq {
		if c.id == id {
			t.Errorf("store %s call for %s; want no judgement write inside grace", c.op, id)
		}
	}
	for _, got := range append(append([]string{}, res.IDs...), res.UnverifiedIDs...) {
		if got == id {
			t.Errorf("%s in result lists (ids=%v unverified_ids=%v); want neither", id, res.IDs, res.UnverifiedIDs)
		}
	}
}

// TestFindMissingGraceSweep: a pending row inside the grace period (measured from its launch start) is not
// judged; past it, with no launch start, or in a non-pending state it is judged as before (SR-11.2, SR-22.8).
func TestFindMissingGraceSweep(t *testing.T) {
	floor := time.Duration(config.PendingGraceFloorSeconds) * time.Second
	cases := []struct {
		name     string
		state    string
		launchMs int64
		full     bool
		verdict  probe.LivenessVerdict // checker verdict for a full-identity row
		grace    time.Duration         // 0 = fmGrace (the config default)
		want     string                // "inside", "marked" or "unverified"
	}{
		{name: "pending just inside partial", state: store.StatePending, launchMs: startedAgo(fmGrace - time.Second), want: "inside"},
		{name: "pending just inside full", state: store.StatePending, launchMs: startedAgo(fmGrace - time.Second), full: true, verdict: probe.VerdictProvablyDead, want: "inside"},
		{name: "pending past partial", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), want: "marked"},
		{name: "pending past full dead", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), full: true, verdict: probe.VerdictProvablyDead, want: "marked"},
		{name: "pending past full unknown", state: store.StatePending, launchMs: startedAgo(fmGrace + time.Second), full: true, verdict: probe.VerdictUnknown, want: "unverified"},
		{name: "pending at exactly grace partial", state: store.StatePending, launchMs: startedAgo(fmGrace), want: "marked"},
		{name: "pending at exactly grace full", state: store.StatePending, launchMs: startedAgo(fmGrace), full: true, verdict: probe.VerdictProvablyDead, want: "marked"},
		{name: "pending future start partial", state: store.StatePending, launchMs: startedAgo(-5 * time.Second), want: "inside"},
		{name: "pending future start full", state: store.StatePending, launchMs: startedAgo(-5 * time.Second), full: true, verdict: probe.VerdictProvablyDead, want: "inside"},
		{name: "pending absent or unreadable start partial", state: store.StatePending, launchMs: 0, want: "marked"},
		{name: "pending absent or unreadable start full", state: store.StatePending, launchMs: 0, full: true, verdict: probe.VerdictProvablyDead, want: "marked"},
		{name: "pending far-past extreme start", state: store.StatePending, launchMs: math.MinInt64, want: "marked"},
		{name: "pending max int64 start", state: store.StatePending, launchMs: math.MaxInt64, want: "inside"},
		{name: "shorter grace inside", state: store.StatePending, launchMs: startedAgo(floor - time.Second), grace: floor, want: "inside"},
		{name: "shorter grace past", state: store.StatePending, launchMs: startedAgo(floor + time.Second), grace: floor, want: "marked"},
		{name: "waiting recent start full", state: store.StateWaiting, launchMs: startedAgo(time.Second), full: true, verdict: probe.VerdictProvablyDead, want: "marked"},
		{name: "working recent start partial", state: store.StateWorking, launchMs: startedAgo(time.Second), want: "marked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "g-1"
			grace := tc.grace
			if grace == 0 {
				grace = fmGrace
			}
			st := &fakeFindMissingStore{liveRows: []store.LiveSpawnIdentity{graceRow(id, tc.state, tc.launchMs, tc.full)}}
			chk := newFakeChecker()
			chk.verdicts[id] = tc.verdict
			prober := &countingProber{set: map[string]struct{}{}} // id absent: a judged partial row is marked

			res, err := api.FindMissing(context.Background(), st, prober, chk, grace, fmClock, &recordingLogger{})
			if err != nil {
				t.Fatalf("FindMissing: %v", err)
			}
			switch tc.want {
			case "inside":
				assertUntouched(t, id, st, chk, res)
				if prober.calls != 0 {
					t.Errorf("Probe called %d times; want 0 when every partial row is inside grace", prober.calls)
				}
			case "marked":
				if !equalStrings(res.IDs, []string{id}) || !equalStrings(st.marked, []string{id}) {
					t.Errorf("ids=%v marked=%v; want [%s] (judged and marked)", res.IDs, st.marked, id)
				}
			case "unverified":
				if !equalStrings(res.UnverifiedIDs, []string{id}) || len(st.setUnverified) != 1 {
					t.Errorf("unverified_ids=%v set=%v; want [%s] (judged unverified)", res.UnverifiedIDs, st.setUnverified, id)
				}
			}
			if tc.full != chk.queried(id) && tc.want != "inside" {
				t.Errorf("checker queried=%v; want %v (full identity only)", chk.queried(id), tc.full)
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

	chk := newFakeChecker()
	chk.verdicts["dead"] = probe.VerdictProvablyDead
	chk.verdicts["walled"] = probe.VerdictUnknown
	st := &fakeFindMissingStore{
		liveRows: []store.LiveSpawnIdentity{
			graceRow("boot", store.StatePending, startedAgo(fmGrace-time.Second), false),
			graceRow("dead", store.StateWorking, 0, true),
			graceRow("walled", store.StateWaiting, 0, true),
			graceRow("gone", store.StateWaiting, 0, false),
		},
		provisional: []store.ProvisionalTranscript{{ClaudeInstanceID: "boot", ClaudeSessionID: session, CWD: "/tmp/proj"}},
	}
	prober := &countingProber{set: map[string]struct{}{}} // boot and gone both absent from the probe

	res, err := api.FindMissing(context.Background(), st, prober, chk, fmGrace, fmClock, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 2 || !equalStrings(res.IDs, []string{"dead", "gone"}) {
		t.Errorf("count=%d ids=%v; want 2 [dead gone]", res.Count, res.IDs)
	}
	if res.Unverified != 1 || !equalStrings(res.UnverifiedIDs, []string{"walled"}) {
		t.Errorf("unverified=%d unverified_ids=%v; want 1 [walled]", res.Unverified, res.UnverifiedIDs)
	}
	assertUntouched(t, "boot", st, chk, res)
	if len(st.healed) != 1 || st.healed[0].id != "boot" {
		t.Errorf("healed = %+v; want boot healed (healing runs inside grace)", st.healed)
	}
}
