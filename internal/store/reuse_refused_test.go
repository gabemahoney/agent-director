package store_test

// Store tests for resets that write nothing (SR-10.3, SR-5.3, SR-5.6, SR-5.8):
// not applied (changed or absent), failed store writes, and the write lock
// taken before the read under a concurrent reset; over reuse_test.go's
// seedReuseRow and reuseFresh.

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// reuseState is what a reset may touch: the row, its history and requests,
// and its child.
type reuseState struct {
	row      apitest.SpawnColumns
	history  []apitest.HistoryEntry
	requests []store.PermissionRow
	child    apitest.SpawnColumns
}

// reuseStateOf reads r's reuseState now; requests go through the store's own
// connection, so a transaction left open on it would show.
func (f *v5Store) reuseStateOf(t *testing.T, r reuseRow) reuseState {
	t.Helper()
	return reuseState{f.rawColumns(r.id), f.historyAllLives(r.id), f.reuseRequests(t, r.id), f.rawColumns(r.child)}
}

// assertReuseWroteNothing fails unless r's reuseState reads exactly as before.
func assertReuseWroteNothing(t *testing.T, f *v5Store, r reuseRow, before reuseState) {
	t.Helper()
	if after := f.reuseStateOf(t, r); !reflect.DeepEqual(after, before) {
		t.Errorf("a reset that did not apply wrote:\n before %+v\n after  %+v", before, after)
	}
}

// TestReuseResetChangedWritesNothing checks each changed case (a snapshot
// field, a non-finished row, a write after examination, the losing reset)
// returns CondChanged with no archived id or version and writes nothing.
func TestReuseResetChangedWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		spec   reuseSpec
		mutate func(*store.RowSnapshot)                   // the examined snapshot's difference
		write  func(t *testing.T, f *v5Store, r reuseRow) // a write after examination
	}{
		{name: "row_version ahead", mutate: func(s *store.RowSnapshot) { s.RowVersion++ }},
		{name: "started_at same instant, other text", mutate: func(s *store.RowSnapshot) { s.StartedAt = "2026-03-04T05:06:07.25+02:00" }},
		{name: "claude_session_id differs", mutate: func(s *store.RowSnapshot) { s.ClaudeSessionID = "sess-other" }},
		{name: "claude_session_id empty, row has one", mutate: func(s *store.RowSnapshot) { s.ClaudeSessionID = "" }},
		{name: "pid differs", mutate: func(s *store.RowSnapshot) { s.PID++ }},
		{name: "proc_starttime differs", mutate: func(s *store.RowSnapshot) { s.ProcStarttime = apitest.DarwinProcStarttime }},
		{name: "tmux_session_name differs", mutate: func(s *store.RowSnapshot) { s.TmuxSessionName = "reuse-ts-2" }},
		{name: "live row", spec: reuseSpec{state: store.StateWaiting}},
		{name: "pending row", spec: reuseSpec{state: store.StatePending}},
		{name: "own agent's hook after examination",
			write: func(t *testing.T, f *v5Store, r reuseRow) {
				if got := apitest.ApplyAgentHook(t, f.path, r.id, "Notification", reuseSessionID); !got.Applied {
					t.Fatalf("own agent's Notification = %+v; want applied", got)
				}
			}},
		{name: "HealJsonlPath after examination", spec: reuseSpec{noJsonl: true},
			write: func(t *testing.T, f *v5Store, r reuseRow) {
				ok, err := f.s.HealJsonlPath(r.id, reuseSessionID, "/tmp/ad-reuse-test/healed.jsonl")
				wantBool(t, "HealJsonlPath", ok, err, true)
			}},
		{name: "SetParentID after examination",
			write: func(t *testing.T, f *v5Store, r reuseRow) {
				if err := f.s.SetParentID(r.id, f.seed(store.StateWaiting, "")); err != nil {
					t.Fatalf("SetParentID: %v", err)
				}
			}},
		{name: "resume's move after examination",
			write: func(t *testing.T, f *v5Store, r reuseRow) {
				res, _, err := f.s.MoveToPending(r.id, r.read.Snapshot, 1790000000789, "fedcba9876543210", "/tmp/ad-reuse-test/move-sock", "")
				if err != nil || res != store.CondApplied {
					t.Fatalf("MoveToPending = %v, %v; want CondApplied", res, err)
				}
			}},
		{name: "second reset from the same snapshot loses",
			write: func(t *testing.T, f *v5Store, r reuseRow) {
				if res, _, _, err := f.reuseReset(r, reuseFresh("", false)); err != nil || res != store.CondApplied {
					t.Fatalf("first ResetForReuse = %v, %v; want CondApplied", res, err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedReuseRow(t, f, tc.spec)
			if tc.mutate != nil {
				tc.mutate(&r.read.Snapshot)
			}
			if tc.write != nil {
				tc.write(t, f, r)
				if f.rawColumns(r.id).RowVersion == r.before.RowVersion {
					t.Fatal("the intervening write did not advance row_version")
				}
			}
			before := f.reuseStateOf(t, r)
			res, archived, v, err := f.reuseReset(r, reuseFresh(r.parent, false))
			if err != nil || res != store.CondChanged || archived != "" || v != 0 {
				t.Fatalf("ResetForReuse = %v, %q, %d, %v; want CondChanged, \"\", 0, nil", res, archived, v, err)
			}
			assertReuseWroteNothing(t, f, r, before)
		})
	}
}

// TestReuseResetAbsent checks a row deleted after examination (as expire
// deletes) gives CondAbsent and the reset creates no row or history.
func TestReuseResetAbsent(t *testing.T) {
	f := newV5Store(t)
	r := seedReuseRow(t, f, reuseSpec{})
	if err := f.s.DeleteSpawn(r.id); err != nil {
		t.Fatalf("DeleteSpawn: %v", err)
	}
	history := f.historyAllLives(r.id)
	res, archived, v, err := f.reuseReset(r, reuseFresh("", false))
	if err != nil || res != store.CondAbsent || archived != "" || v != 0 {
		t.Fatalf("ResetForReuse = %v, %q, %d, %v; want CondAbsent, \"\", 0, nil", res, archived, v, err)
	}
	if _, err := apitest.ReadSpawnColumns(f.path, r.id); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns after the reset: %v; want ErrSpawnNotFound", err)
	}
	if got := f.historyAllLives(r.id); !reflect.DeepEqual(got, history) {
		t.Errorf("history %+v -> %+v; want unchanged", history, got)
	}
}

// TestReuseResetAfterForeignHook checks a foreign process's hook is refused
// (pid_mismatch), changes nothing, and leaves the reset applicable.
func TestReuseResetAfterForeignHook(t *testing.T) {
	f := newV5Store(t)
	r := seedReuseRow(t, f, reuseSpec{})
	got := apitest.ApplyForeignHook(t, f.path, r.id, "Notification", reuseSessionID)
	if got.Applied || got.Reason != store.HookReasonPIDMismatch {
		t.Fatalf("foreign Notification = %+v; want not applied, %s", got, store.HookReasonPIDMismatch)
	}
	if res, _, _, err := f.reuseReset(r, reuseFresh("", false)); err != nil || res != store.CondApplied {
		t.Fatalf("ResetForReuse = %v, %v; want CondApplied", res, err)
	}
}

// TestReuseResetFailClosed checks each injected store failure and a parent id
// naming no row return an error (archive marker only for the archive), no
// outcome, and leave row, history, requests and child as before.
func TestReuseResetFailClosed(t *testing.T) {
	cases := []struct {
		name        string
		inject      bool
		kind        storefix.WriteFailureKind
		parent      string
		wantArchive bool // errors.Is(err, store.ErrReuseArchive)
	}{
		{"archive", true, storefix.WriteFailReuseArchive, "", true},
		{"reset", true, storefix.WriteFailReuseReset, "", false},
		{"permission-request deletion", true, storefix.WriteFailReusePermissionDelete, "", false},
		{"parent id names no row", false, 0, "no-such-parent", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedReuseRow(t, f, reuseSpec{})
			if r.before.ClaudeSessionID == nil || len(r.requests) == 0 {
				t.Fatal("seed has no session id or request; the failing write would not run")
			}
			if tc.inject {
				storefix.InjectWriteFailure(t, f.path, tc.kind, r.id)
			}
			before := f.reuseStateOf(t, r)
			res, archived, v, err := f.reuseReset(r, reuseFresh(tc.parent, false))
			if err == nil || res != 0 || archived != "" || v != 0 {
				t.Fatalf("ResetForReuse = %v, %q, %d, %v; want an error with no outcome", res, archived, v, err)
			}
			if got := errors.Is(err, store.ErrReuseArchive); got != tc.wantArchive {
				t.Errorf("errors.Is(%v, ErrReuseArchive) = %v; want %v", err, got, tc.wantArchive)
			}
			assertReuseWroteNothing(t, f, r, before)
		})
	}
}

// TestReuseResetConcurrent checks two stores on one file resetting one row
// from one snapshot: exactly one applies, the other sees CondChanged, no
// store error, one archived entry. Several rounds, a fresh row each.
func TestReuseResetConcurrent(t *testing.T) {
	f := newV5Store(t)
	s2, err := store.Open(f.path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	for round := 0; round < 5; round++ {
		r := seedReuseRow(t, f, reuseSpec{})
		var (
			wg    sync.WaitGroup
			start = make(chan struct{})
			res   [2]store.CondResult
			errs  [2]error
		)
		for i, s := range []*store.Store{f.s, s2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				res[i], _, _, errs[i] = s.ResetForReuse(r.id, r.read.Snapshot, reuseFresh("", false))
			}()
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("round %d: errors %v, %v; want none", round, errs[0], errs[1])
		}
		if (res != [2]store.CondResult{store.CondApplied, store.CondChanged}) &&
			(res != [2]store.CondResult{store.CondChanged, store.CondApplied}) {
			t.Fatalf("round %d: outcomes %v; want one CondApplied and one CondChanged", round, res)
		}
		if cur, _ := splitHistory(f.historyAllLives(r.id), reuseSessionID); len(cur) != 1 {
			t.Errorf("round %d: archived entries %+v; want exactly one", round, cur)
		}
		if after := f.rawColumns(r.id); after.LifeNumber != reuseLife+1 || after.RowVersion != r.read.Snapshot.RowVersion+1 {
			t.Errorf("round %d: life %#v, row_version %#v; want one reset applied", round, after.LifeNumber, after.RowVersion)
		}
	}
}
