package store_test

// Session history by life across reuse (SR-5.9, SR-10.3, SR-10.4,
// AC-REUSE-23), read through the store's life-taking ListSessionHistory and
// apitest's every-life read.

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestReuseLifeHistory plays one row through reuses: a reset starts an empty
// life and archives the outgoing session into the ending one; a restore
// returns the row to its life, whose number the next reset hands out again;
// a rotation stays in its life; an earlier life's entry whose session is
// current again moves to the current life on the next reset.
func TestReuseLifeHistory(t *testing.T) {
	f := newV5Store(t)
	entry := func(sid string, life int64, day int) apitest.SpawnOption {
		return apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: sid, JSONLPath: "/x/" + sid + ".jsonl",
			Life: life, RecordedAt: time.Date(2020, 1, day, 0, 0, 0, 0, time.UTC)})
	}
	id := f.seed(store.StateEnded, "S2", apitest.WithLifeNumber(2), apitest.WithJsonlPath("/x/S2.jsonl"),
		entry("E1", 1, 1), entry("R2", 2, 2))
	check := func(step string, life int64, reads map[int64][]string) {
		t.Helper()
		if sp, err := f.s.GetSpawn(id); err != nil || sp.LifeNumber != life {
			t.Fatalf("%s: life = %d, %v; want %d", step, sp.LifeNumber, err, life)
		}
		for l, want := range reads {
			if got := f.historyIDs(t, id, l); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: life %d reads %q; want %q", step, l, got, want)
			}
		}
	}

	r := rrDoReset(t, f, id, reuseFresh("", false))
	check("reset", 3, map[int64][]string{3: {}, 2: {"S2", "R2"}, 1: {"E1"}})
	if res, err := f.rrRestore(r, r.version); err != nil || res != store.CondApplied {
		t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
	}
	check("restore", 2, map[int64][]string{2: {"S2", "R2"}, 3: {}})
	r = rrDoReset(t, f, id, reuseFresh("", false))
	check("reset after the restore", 3, map[int64][]string{3: {}, 2: {"S2", "R2"}})

	// The new launch's identity write, then its SessionStarts: D (nothing to
	// archive), then R2, which rotates D out within life 3.
	pane := store.LaunchIdentity{PaneID: "%7", PanePID: apitest.TestPanePID, PaneStarttime: apitest.LinuxProcStarttime}
	if res, err := f.s.RecordLaunchIdentity(id, r.version, reuseFresh("", false).Identity.Token, pane); err != nil || res != store.CondApplied {
		t.Fatalf("RecordLaunchIdentity = %v, %v; want CondApplied", res, err)
	}
	f.rotate(id, "D")
	f.rotate(id, "R2")
	check("rotations", 3, map[int64][]string{3: {"D"}, 2: {"S2", "R2"}})
	if got := storefix.ApplyAgentHook(t, f.s, id, "SessionEnd", "R2"); !got.Applied {
		t.Fatalf("SessionEnd = %+v; want applied", got)
	}
	if r = rrDoReset(t, f, id, reuseFresh("", false)); r.archived != "R2" {
		t.Errorf("archived session = %q; want R2", r.archived)
	}
	check("second reuse", 4, map[int64][]string{4: {}, 2: {"S2"}, 1: {"E1"}}) // life 3: D and R2, ordered by time
	lives := map[string]int64{}
	for _, e := range f.historyAllLives(id) {
		lives[e.ClaudeSessionID] = e.LifeNumber
	}
	if want := map[string]int64{"E1": 1, "S2": 2, "D": 3, "R2": 3}; !reflect.DeepEqual(lives, want) {
		t.Errorf("every-life history lives = %v; want %v", lives, want)
	}
}
