package store_test

// Store tests for session history by life across reuse (SR-5.9, SR-10.3,
// SR-10.4, AC-REUSE-23): a reset starts an empty life and archives into the
// ending one, a re-archived entry moves lives, and a restore returns the row
// to its life, whose number the next reuse hands out again. Read through the
// store's life-taking ListSessionHistory and apitest's every-life read.

import (
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rlOld is an old recorded_at, so seeded entries order before archived ones.
func rlOld(day int) time.Time { return time.Date(2020, 1, day, 0, 0, 0, 0, time.UTC) }

// rlEntry seeds one history entry of sessionID in life, recorded on day.
func rlEntry(sessionID, path string, life int64, day int) apitest.SpawnOption {
	return apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: sessionID, JSONLPath: path, Life: life, RecordedAt: rlOld(day)})
}

// rlLaunch plays a launch of id's reset row: the identity write records a pane,
// then the agent's SessionStart per session (each after the first rotates).
func (f *v5Store) rlLaunch(t *testing.T, id string, resetVersion int64, sessions ...string) {
	t.Helper()
	pane := store.LaunchIdentity{PaneID: "%7", PanePID: apitest.TestPanePID, PaneStarttime: apitest.LinuxProcStarttime}
	if res, err := f.s.RecordLaunchIdentity(id, resetVersion, reuseFresh("", false).Identity.Token, pane); err != nil || res != store.CondApplied {
		t.Fatalf("RecordLaunchIdentity = %v, %v; want CondApplied", res, err)
	}
	for _, s := range sessions {
		f.rotate(id, s)
	}
}

// rlEnd ends id's live row through its agent's SessionEnd.
func (f *v5Store) rlEnd(t *testing.T, id, sessionID string) {
	t.Helper()
	if got := storefix.ApplyAgentHook(t, f.s, id, "SessionEnd", sessionID); !got.Applied {
		t.Fatalf("SessionEnd(%s) = %+v; want applied", id, got)
	}
}

// rlLife returns id's stored life_number.
func (f *v5Store) rlLife(t *testing.T, id string) int64 {
	t.Helper()
	sp, err := f.s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return sp.LifeNumber
}

// rlLives maps each session id in id's history, from every life, to its life.
func (f *v5Store) rlLives(id string) map[string]int64 {
	lives := map[string]int64{}
	for _, e := range f.historyAllLives(id) {
		lives[e.ClaudeSessionID] = e.LifeNumber
	}
	return lives
}

// rlWantReads fails unless ListSessionHistory gives exactly want[life] (newest
// first) for each life listed.
func rlWantReads(t *testing.T, f *v5Store, id string, want map[int64][]string) {
	t.Helper()
	for life, ids := range want {
		if ids == nil {
			ids = []string{}
		}
		if got := f.historyIDs(t, id, life); !reflect.DeepEqual(got, ids) {
			t.Errorf("life %d history = %q; want %q", life, got, ids)
		}
	}
}

// TestReuseLifeNewLifeStartsEmpty checks a reset's new life reads empty while
// the archived current session and older entries stay in the ending life.
func TestReuseLifeNewLifeStartsEmpty(t *testing.T) {
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			f := newV5Store(t)
			id := f.seed(state, "S0", apitest.WithJsonlPath("/x/S0.jsonl"), rlEntry("H0", "/x/H0.jsonl", 0, 1))
			rrDoReset(t, f, id, reuseFresh("", false))

			if got := f.rlLife(t, id); got != 1 {
				t.Fatalf("life after the reset = %d; want 1", got)
			}
			rlWantReads(t, f, id, map[int64][]string{1: nil, 0: {"S0", "H0"}})
			if got, want := f.rlLives(id), map[string]int64{"S0": 0, "H0": 0}; !reflect.DeepEqual(got, want) {
				t.Errorf("every-life history lives = %v; want %v", got, want)
			}
		})
	}
}

// TestReuseLifeRotationsThenReuse checks rotation entries and the archived
// session stay in life 0, and a rotation in the new life is life 1's only entry.
func TestReuseLifeRotationsThenReuse(t *testing.T) {
	f := newV5Store(t)
	id := f.seed(store.StateWaiting, "A", apitest.WithJsonlPath("/x/A.jsonl"), apitest.WithLaunchIdentity(reuseIdentity()))
	f.rotate(id, "B")
	f.rotate(id, "C")
	f.rlEnd(t, id, "C")
	r := rrDoReset(t, f, id, reuseFresh("", false))
	if r.archived != "C" {
		t.Fatalf("archived session = %q; want C", r.archived)
	}

	f.rlLaunch(t, id, r.version, "D", "E")
	rlWantReads(t, f, id, map[int64][]string{0: {"C", "B", "A"}, 1: {"D"}})
	if got, want := f.rlLives(id), map[string]int64{"A": 0, "B": 0, "C": 0, "D": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("every-life history lives = %v; want %v", got, want)
	}
}

// TestReuseLifeTwoReuses checks that after a second reset only the third life
// is read, while the first two lives keep their entries.
func TestReuseLifeTwoReuses(t *testing.T) {
	f := newV5Store(t)
	id := f.seed(store.StateEnded, "S0", apitest.WithJsonlPath("/x/S0.jsonl"))
	r1 := rrDoReset(t, f, id, reuseFresh("", false))
	f.rlLaunch(t, id, r1.version, "S1")
	f.rlEnd(t, id, "S1")
	r2 := rrDoReset(t, f, id, reuseFresh("", false))
	if got := f.rlLife(t, id); got != 2 {
		t.Fatalf("life after the second reset = %d; want 2", got)
	}
	rlWantReads(t, f, id, map[int64][]string{2: nil, 1: {"S1"}, 0: {"S0"}})

	f.rlLaunch(t, id, r2.version, "S2a", "S2b")
	rlWantReads(t, f, id, map[int64][]string{2: {"S2a"}, 1: {"S1"}, 0: {"S0"}})
	if got, want := f.rlLives(id), map[string]int64{"S0": 0, "S1": 1, "S2a": 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("every-life history lives = %v; want %v", got, want)
	}
}

// TestReuseLifeReArchiveMovesLives checks a life-0 entry whose session is current
// again in life 1 moves to life 1 on the next reset, taking its path (NULL included).
func TestReuseLifeReArchiveMovesLives(t *testing.T) {
	for _, path := range []string{"/x/X-life1.jsonl", ""} {
		name := map[bool]string{true: "no path", false: "different path"}[path == ""]
		t.Run(name, func(t *testing.T) {
			f := newV5Store(t)
			opts := []apitest.SpawnOption{apitest.WithLifeNumber(1),
				rlEntry("X", "/x/X-life0.jsonl", 0, 2), rlEntry("Y", "/x/Y.jsonl", 0, 1)}
			if path != "" {
				opts = append(opts, apitest.WithJsonlPath(path))
			}
			id := f.seed(store.StateEnded, "X", opts...)
			rrDoReset(t, f, id, reuseFresh("", false))

			var xs []apitest.HistoryEntry
			for _, e := range f.historyAllLives(id) {
				if e.ClaudeSessionID == "X" {
					xs = append(xs, e)
				}
			}
			wantPath := sql.NullString{String: path, Valid: path != ""}
			if len(xs) != 1 || xs[0].LifeNumber != 1 || xs[0].JSONLPath != wantPath {
				t.Fatalf("X entries = %+v; want one in life 1 with path %+v", xs, wantPath)
			}
			rlWantReads(t, f, id, map[int64][]string{0: {"Y"}, 1: {"X"}, 2: nil})
		})
	}
}

// TestReuseLifeFailedReuseKeepsLife2 checks a reset and restore of a life-2 row
// leaves it in life 2, whose read is its rotation and archived entries only.
func TestReuseLifeFailedReuseKeepsLife2(t *testing.T) {
	f := newV5Store(t)
	id := f.seed(store.StateEnded, "S2", apitest.WithLifeNumber(2), apitest.WithJsonlPath("/x/S2.jsonl"),
		rlEntry("E1", "/x/E1.jsonl", 1, 1), rlEntry("R2", "/x/R2.jsonl", 2, 2))
	r := rrDoReset(t, f, id, reuseFresh("", false))
	if got := f.rlLife(t, id); got != 3 {
		t.Fatalf("life after the reset = %d; want 3", got)
	}
	if res, err := f.rrRestore(r, r.version); err != nil || res != store.CondApplied {
		t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
	}

	if got := f.rlLife(t, id); got != 2 {
		t.Fatalf("life after the restore = %d; want 2", got)
	}
	rlWantReads(t, f, id, map[int64][]string{2: {"S2", "R2"}, 1: {"E1"}, 3: nil})
}

// TestReuseLifeRestoredNumberHandedOutAgain checks that after a reset and restore
// return a row to life N, the next reset gives life N+1 with an empty read.
func TestReuseLifeRestoredNumberHandedOutAgain(t *testing.T) {
	for _, n := range []int64{0, 2} {
		t.Run(fmt.Sprintf("life %d", n), func(t *testing.T) {
			f := newV5Store(t)
			id := f.seed(store.StateEnded, "S", apitest.WithLifeNumber(n), apitest.WithJsonlPath("/x/S.jsonl"))
			r := rrDoReset(t, f, id, reuseFresh("", false))
			if res, err := f.rrRestore(r, r.version); err != nil || res != store.CondApplied {
				t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
			}

			rrDoReset(t, f, id, reuseFresh("", false))
			if got := f.rlLife(t, id); got != n+1 {
				t.Fatalf("life after the next reset = %d; want %d", got, n+1)
			}
			rlWantReads(t, f, id, map[int64][]string{n + 1: nil, n: {"S"}})
			if got, want := f.rlLives(id), map[string]int64{"S": n}; !reflect.DeepEqual(got, want) {
				t.Errorf("every-life history lives = %v; want %v", got, want)
			}
		})
	}
}
