package store_test

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func familyAt(t *testing.T, f *v5Store, id string, ms, now int64, want string) {
	t.Helper()
	r := store.SettleRead{SettleMs: ms, NowMs: now}
	_, _, family, _, err := f.s.SpawnStatusWithFamily(id, r)
	if err != nil {
		t.Fatal(err)
	}
	if family != want {
		t.Errorf("status family = %q; want %q", family, want)
	}
	row, err := f.s.GetSpawnSettled(id, r)
	if err != nil {
		t.Fatal(err)
	}
	if row.FamilyState != want {
		t.Errorf("family %s at %d/%d = %q; want %q", id, now, ms, row.FamilyState, want)
	}
	rows, err := f.s.ListSpawns(store.ListFilters{Settle: r})
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range rows {
		if listed.InstanceID == id && listed.FamilyState != want {
			t.Errorf("listed family = %q; want %q", listed.FamilyState, want)
		}
	}
}

func familyHistory(t *testing.T, f *v5Store, id string) (string, string, int64) {
	t.Helper()
	raw, base, stamp, err := apitest.ReadFamilyHistory(f.path, id)
	if err != nil {
		t.Fatal(err)
	}
	return raw, base, stamp
}

func TestFamilySettling(t *testing.T) {
	for _, tc := range []struct {
		name, initial, next string
		terminal            bool
	}{
		{"work begins", store.StateWaiting, store.StateWorking, false},
		{"work finishes", store.StateWorking, store.StateWaiting, false},
		{"last live member ends", store.StateWorking, store.StateEnded, true},
		{"last live member missing", store.StateWorking, store.StateMissing, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newV5Store(t)
			parent := f.seed(store.StateWaiting, "")
			child := f.seed(store.StateWaiting, "")
			leaf := f.seed(tc.initial, "", rowVersionSeed()...)
			for _, link := range [][2]string{{parent, child}, {child, leaf}} {
				if err := apitest.SeedParentChild(f.path, link[0], link[1]); err != nil {
					t.Fatal(err)
				}
			}
			f.s.SetSettleMs(settleT)
			hookTo(tc.next)(t, f, leaf)
			_, _, stamp := familyHistory(t, f, parent)
			before, after := tc.initial, store.StateWorking
			if tc.next == store.StateWaiting || tc.terminal {
				after = store.StateWaiting
			}
			for _, id := range []string{parent, child, leaf} {
				for _, offset := range []int64{-1, 0, settleT - 1} {
					familyAt(t, f, id, settleT, stamp+offset, before)
				}
				familyAt(t, f, id, settleT, stamp+settleT, after)
				familyAt(t, f, id, 0, stamp, after)
				familyAt(t, f, id, -1, stamp, after)
			}
			// The ancestor's own state never changed; terminal own-state reads stay immediate.
			assertReportedAt(t, f, parent, settleT, stamp, store.StateWaiting)
			if tc.terminal {
				assertReportedAt(t, f, leaf, settleT, stamp, tc.next)
			}
			// No polls are needed for durable history, and reopening does not reset it.
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			f.s = reopened
			familyAt(t, f, parent, settleT, stamp+settleT, after)
			familyAt(t, f, parent, 2*settleT, stamp+settleT, before)
		})
	}
}

func TestFamilyShortReversalsAndUnchangedAggregate(t *testing.T) {
	for _, initial := range []string{store.StateWaiting, store.StateWorking} {
		t.Run(initial, func(t *testing.T) {
			t.Parallel()
			f := newV5Store(t)
			id := f.seed(initial, "", rowVersionSeed()...)
			f.s.SetSettleMs(settleT)
			next := store.StateWorking
			if initial == store.StateWorking {
				next = store.StateWaiting
			}
			hookTo(next)(t, f, id)
			_, _, first := familyHistory(t, f, id)
			hookTo(initial)(t, f, id)
			_, _, last := familyHistory(t, f, id)
			if last-first >= settleT {
				t.Fatal("fixture reversal exceeded interval")
			}
			familyAt(t, f, id, settleT, last, initial)
			familyAt(t, f, id, settleT, last+settleT, initial)
		})
	}
	t.Run("permission and own state changes do not restart family clock", func(t *testing.T) {
		f := newV5Store(t)
		id := f.seed(store.StateWaiting, "", rowVersionSeed()...)
		f.s.SetSettleMs(settleT)
		hookTo(store.StateWorking)(t, f, id)
		_, _, stamp := familyHistory(t, f, id)
		hookTo(store.StateAskUser)(t, f, id)
		_, base, after := familyHistory(t, f, id)
		if after != stamp || base != store.StateWaiting {
			t.Fatalf("unchanged aggregate restarted history: %q %d -> %d", base, stamp, after)
		}
		familyAt(t, f, id, settleT, stamp+settleT, store.StateWorking)
		// Deliberately give own state a newer, independent clock.
		if err := apitest.SetSettle(f.path, id, store.StateWaiting, stamp+settleT); err != nil {
			t.Fatal(err)
		}
		assertReportedAt(t, f, id, settleT, stamp+settleT, store.StateWaiting)
		familyAt(t, f, id, settleT, stamp+settleT, store.StateWorking)
	})
}

func TestFamilyLifecycleMembership(t *testing.T) {
	for _, mode := range []string{"resume", "reuse"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newV5Store(t)
			oldParent := f.seed(store.StateWaiting, "")
			newParent := f.seed(store.StateWaiting, "")
			id := f.seed(store.StateEnded, "", rowVersionSeed()...)
			leaf := f.seed(store.StateWorking, "")
			for _, link := range [][2]string{{oldParent, id}, {id, leaf}} {
				if err := apitest.SeedParentChild(f.path, link[0], link[1]); err != nil {
					t.Fatal(err)
				}
			}
			familyAt(t, f, oldParent, 0, 0, store.StateWorking)
			if mode == "resume" {
				prior := rvExamine(t, f, id)
				res, version, err := f.s.MoveToPending(id, prior.Snapshot, rvMoveStart, rvMoveToken, rvMoveSocket, newParent, store.LaunchOwner{})
				if err != nil || res != store.CondApplied {
					t.Fatalf("resume: %v %v", res, err)
				}
				familyAt(t, f, oldParent, 0, 0, store.StateWaiting)
				familyAt(t, f, newParent, 0, 0, store.StateWorking)
				if err := f.s.DeleteSpawn(leaf); err != nil {
					t.Fatal(err)
				}
				res, err = f.s.RestoreAfterFailedResume(id, version, store.ResumePrior{State: prior.State, EndedAtText: prior.EndedAtText, Identity: prior.Identity})
				if err != nil || res != store.CondApplied {
					t.Fatalf("restore resume: %v %v", res, err)
				}
				familyAt(t, f, newParent, 0, 0, store.StateWaiting)
			} else {
				prior := rvReuseRead(t, f, id)
				res, _, version, err := f.s.ResetForReuse(id, prior.Snapshot, prior.Agent, rvReuseFresh(newParent))
				if err != nil || res != store.CondApplied {
					t.Fatalf("reuse: %v %v", res, err)
				}
				familyAt(t, f, oldParent, 0, 0, store.StateWaiting)
				familyAt(t, f, newParent, 0, 0, store.StateWorking)
				res, err = f.s.RestoreAfterFailedReuse(id, version, prior.Life, time.Now())
				if err != nil || res != store.CondApplied {
					t.Fatalf("restore reuse: %v %v", res, err)
				}
				familyAt(t, f, oldParent, 0, 0, store.StateWorking)
				familyAt(t, f, newParent, 0, 0, store.StateWaiting)
				if err := f.s.DeleteSpawn(id); err != nil {
					t.Fatal(err)
				}
				familyAt(t, f, oldParent, 0, 0, store.StateWaiting)
				familyAt(t, f, leaf, 0, 0, store.StateWorking)
			}
		})
	}
	t.Run("insert initializes pending and changes parent once", func(t *testing.T) {
		f := newV5Store(t)
		parent := f.seed(store.StateWaiting, "")
		f.s.SetSettleMs(settleT)
		if err := f.s.InsertPending(store.Spawn{InstanceID: "family-new", ParentID: parent, CWD: "/tmp", TmuxSessionName: "family-new", Agent: store.AgentClaude}); err != nil {
			t.Fatal(err)
		}
		_, _, stamp := familyHistory(t, f, parent)
		familyAt(t, f, "family-new", settleT, stamp, store.StateWorking)
		familyAt(t, f, parent, settleT, stamp, store.StateWaiting)
		familyAt(t, f, parent, settleT, stamp+settleT, store.StateWorking)
	})
}
