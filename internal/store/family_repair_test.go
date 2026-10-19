package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// repairFamily seeds a stale Claude permission row without a holding request, below two waiting ancestors.
func repairFamily(t *testing.T) (*v5Store, []string) {
	t.Helper()
	f := newV5Store(t)
	parent := f.seed(store.StateWaiting, "")
	child := f.seed(store.StateWaiting, "")
	leaf := f.seed(store.StateCheckPermission, "", rvRelayOn, apitest.WithIdleSince(rvIdleSince))
	for _, link := range [][2]string{{parent, child}, {child, leaf}} {
		if err := apitest.SeedParentChild(f.path, link[0], link[1]); err != nil {
			t.Fatal(err)
		}
	}
	return f, []string{parent, child, leaf}
}

func TestRepairCheckPermissionRefreshesFamily(t *testing.T) {
	for _, ms := range []int64{0, settleT} {
		t.Run(fmt.Sprintf("settle=%d", ms), func(t *testing.T) {
			t.Parallel()
			f, ids := repairFamily(t)
			f.s.SetSettleMs(ms)
			leaf := ids[len(ids)-1]
			before := rvExamine(t, f, leaf)
			from := time.Now().UnixMilli()
			state, res, err := f.s.RepairCheckPermissionIfSameLife(leaf, before.Snapshot)
			to := time.Now().UnixMilli()
			if err != nil || res != store.CondApplied || state != store.StateWaiting {
				t.Fatalf("repair: state=%s result=%v err=%v", state, res, err)
			}
			_, _, stamp := familyHistory(t, f, leaf)
			for _, id := range ids {
				raw, base, changed := familyHistory(t, f, id)
				if raw != store.StateWaiting || base != store.StateWorking || changed != stamp {
					t.Errorf("family %s: raw=%s base=%s stamp=%d; want waiting/working/%d", id, raw, base, changed, stamp)
				}
				assertStamped(t, changed, from, to)
				familyAt(t, f, id, 0, stamp, store.StateWaiting)
				if ms > 0 {
					familyAt(t, f, id, ms, stamp+ms-1, store.StateWorking)
				}
				// Repair settles its own state immediately, independently of the family.
				assertReportedAt(t, f, id, settleT, stamp, store.StateWaiting)
			}
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			f.s = reopened
			for _, id := range ids {
				familyAt(t, f, id, settleT, stamp+settleT, store.StateWaiting)
				raw, base, changed := familyHistory(t, f, id)
				if raw != store.StateWaiting || base != store.StateWorking || changed != stamp {
					t.Errorf("reopen/read rewrote family history: %s/%s/%d", raw, base, changed)
				}
			}
		})
	}
}

func TestRepairCheckPermissionFamilyAtomicity(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%v", stale), func(t *testing.T) {
			t.Parallel()
			f, ids := repairFamily(t)
			leaf := ids[len(ids)-1]
			snap := rvExamine(t, f, leaf).Snapshot
			if stale {
				snap.RowVersion++
			}
			before := make([]apitest.SpawnColumns, len(ids))
			for i, id := range ids {
				before[i] = f.rawColumns(id)
			}
			blocked := errors.New("fixture: family refresh unavailable")
			store.ObserveDecisionBoundaryForTest(t, f.s, f.path, func(_ context.Context, q string) error {
				if strings.HasPrefix(q, "WITH RECURSIVE working(id)") {
					return blocked
				}
				return nil
			}, nil)
			state, res, err := f.s.RepairCheckPermissionIfSameLife(leaf, snap)
			if stale {
				if err != nil || res != store.CondChanged || state != "" {
					t.Fatalf("stale repair depended on family refresh: %q %v %v", state, res, err)
				}
			} else if !errors.Is(err, blocked) || res == store.CondApplied || state != "" {
				t.Fatalf("repair did not roll back failed family refresh: %q %v %v", state, res, err)
			}
			for i, id := range ids {
				if after := f.rawColumns(id); !reflect.DeepEqual(before[i], after) {
					t.Errorf("refused/failed repair changed row %s:\n before %+v\n after %+v", id, before[i], after)
				}
				raw, base, stamp := familyHistory(t, f, id)
				if raw != store.StateWorking || base != store.StateWorking || stamp != 0 {
					t.Errorf("refused/failed repair changed family %s: %s/%s/%d", id, raw, base, stamp)
				}
			}
		})
	}
}
