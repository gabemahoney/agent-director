package store_test

// The launch owner (b.kdf, b.146 rule 11; schema v6): each write that begins a
// launch records the owner it is given, the reads return it, the launch's
// identity write and its release end the hold, and reuse's restore writes the
// prior life's back. The release's refusals and version rules are
// row_version_find_missing_test.go's.

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// wantOwnerColumns is ownerColumns as o is stored: zero as NULL.
func wantOwnerColumns(o store.LaunchOwner) []any {
	if o == (store.LaunchOwner{}) {
		return []any{nil, nil, nil}
	}
	return []any{int64(o.PID), o.Starttime, o.PIDNamespace}
}

// liveOwner is id's launch owner as ListLiveSpawnIdentities returns it.
func liveOwner(t *testing.T, f *v5Store, id string) store.LaunchOwner {
	t.Helper()
	live, err := f.s.ListLiveSpawnIdentities()
	if err != nil {
		t.Fatalf("ListLiveSpawnIdentities: %v", err)
	}
	for _, it := range live {
		if it.ClaudeInstanceID == id {
			return it.LaunchOwner
		}
	}
	t.Fatalf("ListLiveSpawnIdentities has no %s", id)
	return store.LaunchOwner{}
}

// TestLaunchOwnerRecordedByEachLaunch: spawn's insert, resume's move and
// reuse's reset each store the owner they are given (the zero owner as NULL),
// and GetSpawn and ListLiveSpawnIdentities return it.
func TestLaunchOwnerRecordedByEachLaunch(t *testing.T) {
	launches := []struct {
		name  string
		begin func(t *testing.T, f *v5Store, owner store.LaunchOwner) string // the id it left pending
	}{
		{"spawn's insert", func(t *testing.T, f *v5Store, owner store.LaunchOwner) string {
			sp := store.Spawn{ClaudeInstanceID: "lo-insert", CWD: "/tmp", TmuxSessionName: "ts-lo-insert", RelayMode: "off",
				LaunchStartedAtMillis: launchStart, Identity: store.LaunchIdentity{Token: goodToken, Socket: "/tmp/ad-lo/sock"},
				LaunchOwner: owner}
			if err := f.s.InsertPending(sp); err != nil {
				t.Fatalf("InsertPending: %v", err)
			}
			return sp.ClaudeInstanceID
		}},
		{"resume's move", func(t *testing.T, f *v5Store, owner store.LaunchOwner) string {
			r := seedMoveRow(t, f, moveSpec{})
			if res, _, err := f.s.MoveToPending(r.id, r.examined.Snapshot, moveLaunchMillis, moveToken, moveSocket, "", owner); err != nil || res != store.CondApplied {
				t.Fatalf("MoveToPending = %v, %v; want CondApplied", res, err)
			}
			return r.id
		}},
		{"reuse's reset", func(t *testing.T, f *v5Store, owner store.LaunchOwner) string {
			fresh := reuseFresh("", false)
			fresh.LaunchOwner = owner
			return rrDoReset(t, f, seedReuseRow(t, f, reuseSpec{}).id, fresh).id
		}},
	}
	for _, l := range launches {
		for name, owner := range map[string]store.LaunchOwner{"owner": rvOwner, "no owner": {}} {
			t.Run(l.name+"/"+name, func(t *testing.T) {
				f := newV5Store(t)
				id := l.begin(t, f, owner)
				if got := ownerColumns(f.rawColumns(id)); !reflect.DeepEqual(got, wantOwnerColumns(owner)) {
					t.Errorf("launch owner columns = %#v; want %#v", got, wantOwnerColumns(owner))
				}
				if got := rvExamine(t, f, id).LaunchOwner; got != owner {
					t.Errorf("GetSpawn LaunchOwner = %+v; want %+v", got, owner)
				}
				if got := liveOwner(t, f, id); got != owner {
					t.Errorf("ListLiveSpawnIdentities LaunchOwner = %+v; want %+v", got, owner)
				}
			})
		}
	}
}

// TestLaunchOwnerHoldEnds: the launch's identity write and its release each
// NULL the owner with one version advance, the release writing nothing else;
// reuse's applied restore writes the prior life's owner back, and resume's
// writes no owner column, leaving the move's on the finished row.
func TestLaunchOwnerHoldEnds(t *testing.T) {
	inserted := func(t *testing.T, f *v5Store) string {
		t.Helper()
		if err := f.s.InsertPending(store.Spawn{ClaudeInstanceID: "lo-row", CWD: "/tmp", TmuxSessionName: "ts-lo-row",
			RelayMode: "off", LaunchStartedAtMillis: launchStart, Identity: store.LaunchIdentity{Token: goodToken, Socket: "/tmp/ad-lo/sock"},
			LaunchOwner: rvOwner}); err != nil {
			t.Fatalf("InsertPending: %v", err)
		}
		return "lo-row"
	}
	cases := []struct {
		name string
		end  func(t *testing.T, f *v5Store, id string)
		want func(before apitest.SpawnColumns) apitest.SpawnColumns // the row after, but for the owner and version
	}{
		{"identity write", func(t *testing.T, f *v5Store, id string) {
			if res, err := f.s.RecordLaunchIdentity(id, 0, goodToken, createdIdentity()); err != nil || res != store.CondApplied {
				t.Fatalf("RecordLaunchIdentity = %v, %v; want CondApplied", res, err)
			}
		}, func(b apitest.SpawnColumns) apitest.SpawnColumns { return withIdentity(b, createdIdentity()) }},
		{"release", func(t *testing.T, f *v5Store, id string) {
			if res, err := f.s.ReleaseLaunchOwner(id, goodToken); err != nil || res != store.CondApplied {
				t.Fatalf("ReleaseLaunchOwner = %v, %v; want CondApplied", res, err)
			}
		}, func(b apitest.SpawnColumns) apitest.SpawnColumns { return b }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			id := inserted(t, f)
			before := f.rawColumns(id)
			tc.end(t, f, id)
			want := tc.want(before)
			want.RowVersion = before.RowVersion.(int64) + 1
			want.LaunchOwnerPID, want.LaunchOwnerStarttime, want.LaunchOwnerPIDNS = nil, nil, nil
			if after := f.rawColumns(id); !reflect.DeepEqual(after, want) {
				t.Errorf("row after the %s:\n got  %+v\n want %+v", tc.name, after, want)
			}
			if got := rvExamine(t, f, id).LaunchOwner; got != (store.LaunchOwner{}) {
				t.Errorf("GetSpawn LaunchOwner = %+v; want none", got)
			}
		})
	}

	t.Run("reuse restore", func(t *testing.T) {
		f := newV5Store(t)
		prior := store.LaunchOwner{PID: 4400, Starttime: apitest.DarwinProcStarttime, PIDNamespace: "pid:[4026532001]"}
		fresh := reuseFresh("", false)
		fresh.LaunchOwner = rvOwner
		r := rrDoReset(t, f, seedReuseRow(t, f, reuseSpec{opts: []apitest.SpawnOption{apitest.WithLaunchOwner(prior)}}).id, fresh)
		if got := ownerColumns(r.reset); !reflect.DeepEqual(got, wantOwnerColumns(rvOwner)) {
			t.Fatalf("owner after the reset = %#v; want %#v", got, wantOwnerColumns(rvOwner))
		}
		if res, err := f.rrRestore(r, r.version); err != nil || res != store.CondApplied {
			t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
		}
		if got := ownerColumns(f.rawColumns(r.id)); !reflect.DeepEqual(got, wantOwnerColumns(prior)) {
			t.Errorf("owner after the restore = %#v; want the prior life's %#v", got, wantOwnerColumns(prior))
		}
	})

	t.Run("resume restore", func(t *testing.T) {
		f := newV5Store(t)
		prior := store.LaunchOwner{PID: 4400, Starttime: apitest.DarwinProcStarttime, PIDNamespace: "pid:[4026532001]"}
		r := seedMoveRow(t, f, moveSpec{opts: []apitest.SpawnOption{apitest.WithLaunchOwner(prior)}})
		res, v, err := f.s.MoveToPending(r.id, r.examined.Snapshot, moveLaunchMillis, moveToken, moveSocket, "", rvOwner)
		if err != nil || res != store.CondApplied {
			t.Fatalf("MoveToPending = %v, %v; want CondApplied", res, err)
		}
		e := r.examined
		if res, err := f.s.RestoreAfterFailedResume(r.id, v, store.ResumePrior{State: e.State, EndedAtText: e.EndedAtText,
			PID: e.PID, ProcStarttime: e.ProcStarttime, Identity: e.Identity}); err != nil || res != store.CondApplied {
			t.Fatalf("RestoreAfterFailedResume = %v, %v; want CondApplied", res, err)
		}
		after := f.rawColumns(r.id)
		if after.State != e.State || !reflect.DeepEqual(ownerColumns(after), wantOwnerColumns(rvOwner)) {
			t.Errorf("after the restore: state %v, owner %#v; want %s, the move's %#v", after.State, ownerColumns(after),
				e.State, wantOwnerColumns(rvOwner))
		}
	})
}
