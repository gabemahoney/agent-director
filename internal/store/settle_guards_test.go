package store_test

// settle_guards_test.go (b.3ie): every store read and conditional write a
// guard rests on acts on the raw state, whatever the settle columns make
// status, get and list report: the guard reads (GetSpawn, GetSpawnState,
// SpawnState, SpawnStatus, ReadForReuse, find-missing's live rows, expire's
// candidates) return or select by the raw state, and the guarded writes
// (find-missing's mark, resume's move, reuse's reset, expire's delete) apply
// by it. Each row's raw state changed just now from the state it reports, by
// a hook's write, the case a guard and status can disagree on.

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSettleGuardReadsAreRaw: with a row raw in one state and reported in
// another (a resurrected row still reporting ended, and the converse, which
// only hand-placed columns give), each guard read gives the raw state and
// each list a guard selects from is selected by it.
func TestSettleGuardReadsAreRaw(t *testing.T) {
	f := newV5Store(t)
	now := time.Now().UnixMilli()
	live := f.seed(store.StateWaiting, "", apitest.WithEndedAt(expOld), apitest.WithSettle(store.StateEnded, now))
	ended := f.seed(store.StateEnded, "", apitest.WithEndedAt(expOld), apitest.WithSettle(store.StateWaiting, now))
	for id, raw := range map[string]string{live: store.StateWaiting, ended: store.StateEnded} {
		reported := reportedAt(t, f, id, settleT, now)
		if reported == raw {
			t.Fatalf("%s reports its raw state %s; the guard check would be vacuous", id, raw)
		}
		sp, err := f.s.GetSpawn(id)
		state, stateErr := f.s.GetSpawnState(id)
		pre, found, preErr := f.s.SpawnState(id)
		status, _, statusErr := f.s.SpawnStatus(id)
		reuse, reuseFound, reuseErr := f.s.ReadForReuse(id)
		if err != nil || stateErr != nil || preErr != nil || statusErr != nil || reuseErr != nil || !found || !reuseFound {
			t.Fatalf("guard reads of %s: %v, %v, %v, %v, %v", id, err, stateErr, preErr, statusErr, reuseErr)
		}
		if got := []string{sp.State, sp.ReportedState, state, pre, status, reuse.State}; slices.ContainsFunc(got, func(s string) bool { return s != raw }) {
			t.Errorf("%s (raw %s, reported %s): GetSpawn %q (reported %q), GetSpawnState %q, SpawnState %q, SpawnStatus %q, ReadForReuse %q; want the raw state from each",
				id, raw, reported, sp.State, sp.ReportedState, state, pre, status, reuse.State)
		}
	}
	lives, err := f.s.ListLiveSpawnIdentities()
	if err != nil {
		t.Fatalf("ListLiveSpawnIdentities: %v", err)
	}
	var liveIDs []string
	for _, l := range lives {
		liveIDs = append(liveIDs, l.InstanceID)
	}
	if !slices.Equal(liveIDs, []string{live}) {
		t.Errorf("find-missing's live rows = %v; want the raw live row only [%s]", liveIDs, live)
	}
	if got := expCandidateIDs(t, f, expCutoff); !slices.Equal(got, []string{ended}) {
		t.Errorf("expire's candidates = %v; want the raw ended row only [%s]", got, ended)
	}
}

// TestSettleGuardedWritesAreRaw: each guarded write applies, or refuses, by
// the raw state of a row that reports another.
func TestSettleGuardedWritesAreRaw(t *testing.T) {
	cases := []struct {
		name          string
		raw, reported string
		write         func(t *testing.T, f *v5Store, id string, examined store.ReuseRow) store.CondResult
		want          store.CondResult
	}{
		{"find-missing's mark of a raw live row reported missing", store.StateWaiting, store.StateMissing,
			func(t *testing.T, f *v5Store, id string, e store.ReuseRow) store.CondResult {
				_, res, err := f.s.MarkMissingIfSameLife(id, e.Snapshot)
				wantNoErr(t, err)
				return res
			}, store.CondApplied},
		{"resume's move of a raw live row reported missing", store.StateWaiting, store.StateMissing,
			func(t *testing.T, f *v5Store, id string, e store.ReuseRow) store.CondResult {
				res, _, err := f.s.MoveToPending(id, e.Snapshot, rvMoveStart, rvMoveToken, rvMoveSocket, "", store.LaunchOwner{})
				wantNoErr(t, err)
				return res
			}, store.CondChanged},
		{"reuse's reset of a raw live row reported missing", store.StateWorking, store.StateMissing,
			func(t *testing.T, f *v5Store, id string, e store.ReuseRow) store.CondResult {
				res, _, _, err := f.s.ResetForReuse(id, e.Snapshot, e.Agent, rvReuseFresh(""))
				wantNoErr(t, err)
				return res
			}, store.CondChanged},
		{"expire's delete of a raw live row reported ended", store.StateWaiting, store.StateEnded,
			func(t *testing.T, f *v5Store, id string, e store.ReuseRow) store.CondResult {
				res, err := f.s.DeleteFinishedIfSameLife(id, e.Snapshot)
				wantNoErr(t, err)
				return res
			}, store.CondChanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newV5Store(t)
			now := time.Now().UnixMilli()
			id := f.seed(tc.raw, "", append(rowVersionSeed(), apitest.WithSettle(tc.reported, now))...)
			if got := reportedAt(t, f, id, settleT, now); got != tc.reported {
				t.Fatalf("row reports %q; want %q", got, tc.reported)
			}
			before := f.rawColumns(id)
			if got := tc.write(t, f, id, f.readForReuse(t, id)); got != tc.want {
				t.Errorf("write on raw %s = %v; want %v", tc.raw, got, tc.want)
			}
			if after := f.rawColumns(id); tc.want != store.CondApplied && !reflect.DeepEqual(after, before) {
				t.Errorf("a refused write changed the row:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

// wantNoErr fails t on err.
func wantNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
