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
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.ob8 F1: the bounded terminal writer must drain a waiting row's retained
// display before comparing contributions. A retained idle_screen keeps the
// member working, so draining it changes the families. b.zpv: the
// needs_intelligence an earlier release stored keeps no member working, but
// that release's stored aggregates counted it as working; draining it
// refreshes them, in the same transaction, to waiting. Plain waiting still
// skips the scan. Cursor is the only agent with this writer and never enters
// idle_screen, but the family rule does not look at the agent.
func TestFamilyRetainedDisplayBoundedTerminalExit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		display string // stored display state; "" for none
		reject  bool
	}{
		{"retained display commits", store.StateIdleScreen, false},
		{"retained display rolls back failed refresh", store.StateIdleScreen, true},
		{"stored needs_intelligence commits", store.StateNeedsIntelligence, false},
		{"stored needs_intelligence rolls back failed refresh", store.StateNeedsIntelligence, true},
		{"plain waiting skips refresh", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			parent := e.f.seed(store.StateWaiting, "")
			if err := apitest.SeedParentChild(e.f.path, parent, e.b.InstanceID); err != nil {
				t.Fatal(err)
			}
			switch tc.display {
			case store.StateIdleScreen:
				e.showStoredDisplay(t, storedIdleScreen)
			case store.StateNeedsIntelligence:
				e.insert(t, storefix.TestRequestTokenA)
				e.showStoredDisplay(t, storedNeedsIntelligence)
				pr := e.read(t, storefix.TestRequestTokenA)
				if err := apitest.SetPermissionFacts(e.f.path, pr.RequestID, apitest.PermissionProof(e.until)); err != nil {
					t.Fatal(err)
				}
			}
			gate := store.HookGate{Event: "Stop", ParentPID: e.b.GatewayPID, ParentStart: e.b.GatewayStart, SessionID: e.b.SessionID, Cursor: &e.b}
			if a, err := e.f.s.ApplyHookTransition(e.b.InstanceID, gate, store.StateWaiting, false, "Stop", "", false); err != nil || !a.Applied {
				t.Fatalf("Stop=%+v %v", a, err)
			}
			if tc.display == store.StateNeedsIntelligence {
				seedStaleNeedsIntelligenceFamilies(t, e.f.path, parent, e.b.InstanceID)
			}
			row := rvExamine(t, e.f, e.b.InstanceID)
			if row.State != store.StateWaiting {
				t.Fatalf("raw state=%s", row.State)
			}
			if tc.display != "" && row.CommittedDisplay().State != tc.display {
				t.Fatalf("Stop lost display: %+v", row)
			}
			before := map[string][3]any{}
			for _, id := range []string{parent, row.InstanceID} {
				a, b, at := familyHistory(t, e.f, id)
				before[id] = [3]any{a, b, at}
				switch tc.display {
				case store.StateIdleScreen:
					if a != store.StateWorking {
						t.Errorf("Stop removed retained family %s: %v", id, before[id])
					}
				case store.StateNeedsIntelligence:
					if a != store.StateWorking || b != store.StateWorking || at != staleFamilyStampMs {
						t.Fatalf("seeded stale family %s: %v; want working/working/%d", id, before[id], staleFamilyStampMs)
					}
				}
			}
			blocked := errors.New("fixture: terminal family refresh failed")
			var refreshes int
			store.ObserveDecisionBoundaryForTest(t, e.f.s, e.f.path, func(_ context.Context, q string) error {
				if strings.HasPrefix(q, "WITH RECURSIVE working(id)") {
					refreshes++
					if tc.reject {
						return blocked
					}
				}
				return nil
			}, nil)
			mark := store.TrailMark(t)
			var prior string
			var result store.CondResult
			var err error
			_, _, attempted := apitest.RepeatUntilCommitted(func() bool {
				prior, result, err = e.f.s.MarkCursorMissingIfSameLaunch(row, e.b)
				return !errors.Is(err, store.ErrStoreBusy)
			})
			if !attempted {
				t.Fatalf("terminal write never attempted: %v", err)
			}
			after := rvExamine(t, e.f, row.InstanceID)
			if tc.display != "" && tc.reject {
				if !errors.Is(err, blocked) || result == store.CondApplied || !reflect.DeepEqual(after, row) {
					t.Fatalf("failed refresh did not roll back: %s/%v %v before=%+v after=%+v", prior, result, err, row, after)
				}
				for id, want := range before {
					a, b, at := familyHistory(t, e.f, id)
					if [3]any{a, b, at} != want {
						t.Errorf("rollback changed family %s", id)
					}
				}
				assertDisplayRecords(t, mark, row.InstanceID)
				return
			}
			if err != nil || result != store.CondApplied || prior != store.StateWaiting || after.State != store.StateMissing || after.DisplayObservation != "" {
				t.Fatalf("terminal write=%s/%v %v row=%+v", prior, result, err, after)
			}
			_, _, stamp := familyHistory(t, e.f, row.InstanceID)
			for _, id := range []string{parent, row.InstanceID} {
				a, b, at := familyHistory(t, e.f, id)
				switch tc.display {
				case store.StateIdleScreen, store.StateNeedsIntelligence:
					if a != store.StateWaiting || b != store.StateWorking || at != stamp || stamp == 0 || stamp == before[id][2] {
						t.Errorf("terminal family %s=%s/%s/%d; want a new waiting/working change from %v", id, a, b, at, before[id])
					}
				default:
					if a != store.StateWaiting {
						t.Errorf("terminal family %s=%s/%s/%d", id, a, b, at)
					}
				}
			}
			if tc.display != "" {
				if refreshes == 0 {
					t.Error("retained display exit bypassed refresh")
				}
				_, at, err := apitest.ReadSettle(e.f.path, row.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				assertDisplayRecords(t, mark, row.InstanceID, displayRecord{tc.display, store.StateMissing, time.UnixMilli(at)})
			} else {
				if refreshes != 0 {
					t.Errorf("plain waiting refreshed families %d times", refreshes)
				}
				assertDisplayRecords(t, mark, row.InstanceID)
			}
		})
	}
}

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
