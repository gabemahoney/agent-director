package store_test

// SR-5.2 versioning cases for expire's candidate read and conditional delete
// (SR-12.1, SR-12.3), appended to row_version_test.go's no-op table; an
// applied delete leaves no row, so its test checks only that.

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rvExpireCutoff is later than any ended_at a seeded row stores.
var rvExpireCutoff = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// rvExpireDelete returns expire's delete of target ("" = the seeded row) with
// *examined (nil: the row read now), expecting want.
func rvExpireDelete(target string, examined *store.RowSnapshot, want store.CondResult) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		snap := rvExamine(t, f, id).Snapshot
		if examined != nil {
			snap = *examined
		}
		to := target
		if to == "" {
			to = id
		}
		if got, err := f.s.DeleteFinishedIfSameLife(to, snap); err != nil || got != want {
			t.Fatalf("DeleteFinishedIfSameLife = %v, %v; want %v, nil", got, err, want)
		}
	}
}

// rvExpireStale is expire's delete of an ended row the row's own agent wrote
// to after expire examined it; the delete must apply nothing.
func rvExpireStale(name string, write func(*testing.T, *v5Store, string)) rowVersionCase {
	var examined store.RowSnapshot
	return rowVersionCase{name: "DeleteFinishedIfSameLife/" + name, state: "ended",
		setup: func(t *testing.T, f *v5Store, id string) { examined = rvExamine(t, f, id).Snapshot; write(t, f, id) },
		write: rvExpireDelete("", &examined, store.CondChanged)}
}

// rvExpireNoOps are the delete refused and the candidate read: each writes nothing.
func rvExpireNoOps() []rowVersionCase {
	return []rowVersionCase{
		rvExpireStale("stale snapshot, own agent's hook first", rvSoftRefresh),
		rvExpireStale("stale snapshot, relaunch's SessionStart first", sessionStart("sess-rv-relaunch", "", false)),
		{name: "DeleteFinishedIfSameLife/live row", state: "waiting", write: rvExpireDelete("", nil, store.CondChanged)},
		{name: "DeleteFinishedIfSameLife/absent row", state: "ended", write: rvExpireDelete("rv-absent", nil, store.CondAbsent)},
		{name: "ListExpireCandidates/the read", state: "ended",
			write: func(t *testing.T, f *v5Store, id string) {
				if got := expCandidateIDs(t, f, rvExpireCutoff); !slices.Equal(got, []string{id}) {
					t.Fatalf("ListExpireCandidates = %v; want [%s]", got, id)
				}
			}},
	}
}

// TestRowVersionExpireDeleteRemovesRow checks an applied delete of a finished
// row leaves no row and keeps the store_id.
func TestRowVersionExpireDeleteRemovesRow(t *testing.T) {
	for _, st := range []string{"ended", "missing"} {
		t.Run(st, func(t *testing.T) {
			f := newV5Store(t)
			id := seedCase(t, f, rowVersionCase{state: st, opts: []apitest.SpawnOption{apitest.WithEndedAt("2026-01-01 00:00:00")}})
			rvExpireDelete("", nil, store.CondApplied)(t, f, id)
			if _, err := apitest.ReadSpawnColumns(f.path, id); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("ReadSpawnColumns after the delete: %v; want ErrSpawnNotFound", err)
			}
			assertStoreIDKept(t, f)
		})
	}
}
