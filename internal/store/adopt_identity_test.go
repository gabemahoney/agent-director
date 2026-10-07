package store_test

// The adoption write after a lost create reply (SR-3.6, SR-5.2, SR-5.3;
// Appendix F.3/F.4): AdoptIdentityIfUnchanged, which kill, send-keys and pause
// make. Its stale-snapshot and absent-row outcomes are also in
// row_version_test.go; the snapshot's every field, in find_missing_writes_test.go.

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// adoptSeed seeds the launch identity seeded and a non-default value in every
// column the write must keep, so "unchanged" is never vacuous.
func adoptSeed(seeded store.LaunchIdentity) []apitest.SpawnOption {
	return []apitest.SpawnOption{
		apitest.WithLaunchIdentity(seeded), apitest.WithPID(4242), apitest.WithProcStarttime(apitest.LinuxProcStarttime),
		apitest.WithLaunchStartedAt(launchStart), apitest.WithEndedAt("2026-01-02 03:04:05"),
		apitest.WithLivenessUnverifiedSince("2026-01-01 00:00:00"), apitest.WithLivenessNote("probe_eacces"),
		apitest.WithJsonlPath("/tmp/ad-adopt-test/a.jsonl"), apitest.WithLifeNumber(3), apitest.WithNoPreTrust(),
	}
}

// adoptNoTrail fails unless no store trail line was written since mark (SR-14).
func adoptNoTrail(t *testing.T, mark int) {
	t.Helper()
	if got := store.TrailMark(t); got != mark {
		t.Errorf("trail lines %d -> %d; want no line written", mark, got)
	}
}

// TestAdoptIdentityIfUnchanged checks an applied adoption writes the six
// identity columns (zero as NULL: no pane found records the server only), a
// pane onto a recorded server included, and +1, nothing else and no trail
// line; the same examined snapshot again, or one a SessionStart or a non-hook
// write overtook, gives CondChanged and writes nothing.
func TestAdoptIdentityIfUnchanged(t *testing.T) {
	lost := store.LaunchIdentity{Token: goodToken, Socket: "/tmp/ad-adopt-test/sock"}
	serverNoPane := lost
	serverNoPane.ServerPID, serverNoPane.ServerStart, serverNoPane.ServerStarttime = 111, 1767225600, apitest.LinuxProcStarttime
	paneOnServer := serverNoPane
	paneOnServer.PaneID, paneOnServer.PanePID, paneOnServer.PaneStarttime = "%9", 444, apitest.LinuxProcStarttime
	serverOnly := createdIdentity()
	serverOnly.PaneID, serverOnly.PanePID, serverOnly.PaneStarttime = "", 0, ""
	for _, c := range []struct {
		name          string
		seeded, adopt store.LaunchIdentity
	}{
		{"no identity, server and pane adopted", lost, createdIdentity()},
		{"server and no pane, pane adopted", serverNoPane, paneOnServer},
		{"no pane found, server only adopted, pane columns NULL", lost, serverOnly},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			id := f.seed(store.StateWaiting, "sess-adopt", adoptSeed(c.seeded)...)
			before, examined, mark := f.rawColumns(id), rvExamine(t, f, id).Snapshot, store.TrailMark(t)
			if got, err := f.s.AdoptIdentityIfUnchanged(id, examined, c.adopt); err != nil || got != store.CondApplied {
				t.Fatalf("AdoptIdentityIfUnchanged = %v, %v; want CondApplied", got, err)
			}
			want := withIdentity(before, c.adopt)
			want.RowVersion = before.RowVersion.(int64) + 1
			if after := f.rawColumns(id); !reflect.DeepEqual(after, want) {
				t.Errorf("row after the adoption:\n got %+v\nwant %+v", after, want)
			}
			adoptNoTrail(t, mark)
			if got, err := f.s.AdoptIdentityIfUnchanged(id, examined, createdIdentity()); err != nil || got != store.CondChanged {
				t.Fatalf("second adoption from the same snapshot = %v, %v; want CondChanged", got, err)
			}
			if after := f.rawColumns(id); !reflect.DeepEqual(after, want) {
				t.Errorf("second adoption changed the row:\n got %+v\nwant %+v", after, want)
			}
		})
	}

	paneNoServer := lost
	paneNoServer.PaneID, paneNoServer.PanePID, paneNoServer.PaneStarttime = "%7", 222, apitest.DarwinProcStarttime
	for _, c := range []struct {
		name, state string
		seeded      store.LaunchIdentity
		between     func(t *testing.T, f *v5Store, id string)
	}{
		{"SessionStart first", store.StatePending, paneNoServer, func(t *testing.T, f *v5Store, id string) {
			if got := storefix.ApplyAgentHook(t, f.s, id, "SessionStart", "sess-adopt"); !got.Applied {
				t.Fatalf("SessionStart = %+v; want applied", got)
			}
		}},
		{"non-hook write first", store.StateWaiting, lost, rvMark.write("", nil, store.CondApplied, store.StateWaiting)},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			id := f.seed(c.state, "", adoptSeed(c.seeded)...)
			examined := rvExamine(t, f, id).Snapshot
			c.between(t, f, id)
			before, mark := f.rawColumns(id), store.TrailMark(t)
			if got, err := f.s.AdoptIdentityIfUnchanged(id, examined, createdIdentity()); err != nil || got != store.CondChanged {
				t.Fatalf("AdoptIdentityIfUnchanged = %v, %v; want CondChanged", got, err)
			}
			if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
				t.Errorf("row changed:\nbefore %+v\nafter  %+v", before, after)
			}
			adoptNoTrail(t, mark)
		})
	}
}
