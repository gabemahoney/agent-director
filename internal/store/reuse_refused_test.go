package store_test

// Resets that write nothing (SR-10.3, SR-5.3, SR-5.6, SR-5.8): not applied,
// failed store writes, and the write lock taken before the read under a
// concurrent reset; over reuse_test.go's seedReuseRow and reuseFresh. The
// stale-snapshot cases are row_version_reuse_test.go's.

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// reuseState is what a reset may touch: the row, its history and requests
// (read on the store's own connection, so a transaction left open shows),
// and its child.
type reuseState struct {
	row      apitest.SpawnColumns
	history  []apitest.HistoryEntry
	requests []store.PermissionRow
	child    apitest.SpawnColumns
}

// reuseStateOf reads r's reuseState now.
func (f *v5Store) reuseStateOf(t *testing.T, r reuseRow) reuseState {
	t.Helper()
	return reuseState{f.rawColumns(r.id), f.historyAllLives(r.id), f.reuseRequests(t, r.id), f.rawColumns(r.child)}
}

// TestReuseResetNotApplied checks a reset after its own agent's hook moved the
// snapshot, of a live row, or of a row deleted after examination, gives
// CondChanged or CondAbsent with no archived id or version, and archives,
// deletes and writes nothing.
func TestReuseResetNotApplied(t *testing.T) {
	cases := []struct {
		name  string
		spec  reuseSpec
		write func(t *testing.T, f *v5Store, r reuseRow) // after examination
		want  store.CondResult
	}{
		{"own agent's hook after examination", reuseSpec{}, func(t *testing.T, f *v5Store, r reuseRow) {
			if got := apitest.ApplyAgentHook(t, f.path, r.id, "Notification", reuseSessionID); !got.Applied {
				t.Fatalf("own agent's Notification = %+v; want applied", got)
			}
		}, store.CondChanged},
		{"live row", reuseSpec{state: store.StateWaiting}, nil, store.CondChanged},
		{"row deleted after examination", reuseSpec{}, func(t *testing.T, f *v5Store, r reuseRow) {
			if err := f.s.DeleteSpawn(r.id); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
		}, store.CondAbsent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedReuseRow(t, f, tc.spec)
			if tc.write != nil {
				tc.write(t, f, r)
			}
			var before reuseState
			if tc.want != store.CondAbsent {
				before = f.reuseStateOf(t, r)
			}
			history := f.historyAllLives(r.id)
			res, archived, v, err := f.reuseReset(r, reuseFresh(r.parent, false))
			if err != nil || res != tc.want || archived != "" || v != 0 {
				t.Fatalf("ResetForReuse = %v, %q, %d, %v; want %v, \"\", 0, nil", res, archived, v, err, tc.want)
			}
			if tc.want == store.CondAbsent {
				if _, err := apitest.ReadSpawnColumns(f.path, r.id); !errors.Is(err, store.ErrSpawnNotFound) || !reflect.DeepEqual(f.historyAllLives(r.id), history) {
					t.Errorf("the reset of a deleted row made a row (%v) or history", err)
				}
			} else if after := f.reuseStateOf(t, r); !reflect.DeepEqual(after, before) {
				t.Errorf("a reset that did not apply wrote:\n before %+v\n after  %+v", before, after)
			}
		})
	}
}

// TestReuseResetFailClosed checks each injected store failure and a parent id
// naming no row return an error (the archive marker only for the archive), no
// outcome, and leave row, history, requests and child as before.
func TestReuseResetFailClosed(t *testing.T) {
	cases := []struct {
		name        string
		kind        storefix.WriteFailureKind // 0: no injection
		parent      string
		wantArchive bool // errors.Is(err, store.ErrReuseArchive)
	}{
		{"archive", storefix.WriteFailReuseArchive, "", true},
		{"reset", storefix.WriteFailReuseReset, "", false},
		{"permission-request deletion", storefix.WriteFailReusePermissionDelete, "", false},
		{"parent id names no row", 0, "no-such-parent", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := seedReuseRow(t, f, reuseSpec{})
			if tc.kind != 0 {
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
			if after := f.reuseStateOf(t, r); !reflect.DeepEqual(after, before) {
				t.Errorf("a failed reset wrote:\n before %+v\n after  %+v", before, after)
			}
		})
	}
}

// TestReuseResetConcurrent checks two stores on one file resetting one row
// from one snapshot: exactly one applies, the other sees CondChanged, no store
// error, one archived entry. Several rounds, a fresh row each.
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
		if errs[0] != nil || errs[1] != nil || (res != [2]store.CondResult{store.CondApplied, store.CondChanged} &&
			res != [2]store.CondResult{store.CondChanged, store.CondApplied}) {
			t.Fatalf("round %d: outcomes %v, errors %v; want one CondApplied and one CondChanged", round, res, errs)
		}
		if cur, _ := splitHistory(f.historyAllLives(r.id), reuseSessionID); len(cur) != 1 {
			t.Errorf("round %d: archived entries %+v; want exactly one", round, cur)
		}
		if after := f.rawColumns(r.id); after.LifeNumber != reuseLife+1 || after.RowVersion != r.read.Snapshot.RowVersion+1 {
			t.Errorf("round %d: life %#v, row_version %#v; want one reset applied", round, after.LifeNumber, after.RowVersion)
		}
	}
}
