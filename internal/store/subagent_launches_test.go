package store_test

// Claimable launches (SR-4.5, SR-7.8; b.xjn) through the store's gated writes:
// what a launch request opens, which start claims which launch, what closes one
// (a launch result, a failure, a main-agent Stop past the grace, whose edges are
// pinned here only) and the Stop's skip read. The caps on real writes are in
// subagent_caps_test.go, the gate in subagent_gate_test.go. Fixture and helpers:
// subagent_transitions_test.go.

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// saToolIDs lists the tool_use_ids of the claimable launches, in created_seq order.
func saToolIDs(ls []apitest.SubagentLaunchColumns) []string {
	ids := []string{}
	for _, l := range ls {
		ids = append(ids, l.ToolUseID.(string))
	}
	return ids
}

func (f *saStore) wantLaunches(want ...string) {
	f.t.Helper()
	if got := saToolIDs(f.rows().Launches); !reflect.DeepEqual(got, append([]string{}, want...)) {
		f.t.Errorf("claimable launches %v; want %v", got, want)
	}
}

func TestSubagentLaunchRequestOpens(t *testing.T) {
	t.Run("opens one holding only the tool_use_id, the type and the receipt time", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-1", Type: "Explore", ReceivedMs: saE})
		rows := f.rows()
		if len(rows.Entries) != 0 || len(rows.Launches) != 1 {
			t.Fatalf("rows %+v; want one claimable launch and no entry", rows)
		}
		want := apitest.SubagentLaunchColumns{CreatedSeq: rows.Launches[0].CreatedSeq, InstanceID: f.id, LaunchToken: f.token(),
			ToolUseID: "toolu-1", SubagentType: "Explore", OpenedAt: saE}
		if rows.Launches[0] != want {
			t.Errorf("launch %+v; want %+v", rows.Launches[0], want)
		}
	})
	for _, tc := range []struct {
		name string
		req  store.SubagentLaunchRequest
	}{
		{"an empty tool_use_id opens none", store.SubagentLaunchRequest{Type: "Explore", ReceivedMs: saE}},
		{"an empty type opens none", store.SubagentLaunchRequest{ToolUseID: "toolu-1", ReceivedMs: saE}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting)
			f.request(tc.req)
			f.wantLaunches()
		})
	}
	t.Run("a repeated request changes nothing, its opened_at included", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-1", Type: "Explore", ReceivedMs: saAt(1)})
		before := f.rows()
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-1", Type: "Plan", ReceivedMs: saAt(2)})
		if got := f.rows(); !reflect.DeepEqual(got, before) {
			t.Errorf("rows %+v; want %+v unchanged", got, before)
		}
	})
	t.Run("a tool_use_id that is already an entry of the current launch opens none", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedEntry("sa-known", apitest.WithSubagentToolUseID("toolu-known"))
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-known", Type: "Explore", ReceivedMs: saE})
		f.wantLaunches()
	})
	t.Run("a tool_use_id of an earlier launch's entry opens one", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedEntry("sa-earlier", apitest.WithSubagentToolUseID("toolu-known"), apitest.WithSubagentLaunchToken("earlier-launch"))
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-known", Type: "Explore", ReceivedMs: saE})
		f.wantLaunches("toolu-known")
	})
	t.Run("a request writes no entry and leaves the entries as they were", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedEntry(saSub)
		before := f.rows().Entries
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-1", Type: "Explore", ReceivedMs: saE})
		if got := f.rows().Entries; !reflect.DeepEqual(got, before) {
			t.Errorf("entries %+v; want %+v unchanged", got, before)
		}
	})
}

// saStartOf starts sub with a type and returns whether it left the entry confirmed.
func saStartOf(f *saStore, sub, typ string) bool {
	f.t.Helper()
	f.start(store.SubagentLifecycleEvent{SubagentID: sub, Type: typ, SessionID: "sess-ev", ReceivedMs: saE})
	return f.mustEntry(sub).Confirmed == int64(1)
}

func TestSubagentClaims(t *testing.T) {
	t.Run("a new start claims the launch of its type: it confirms the entry and the launch is gone", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedLaunch("toolu-1", apitest.WithClaimableType("Explore"))
		if !saStartOf(f, saSub, "Explore") {
			t.Errorf("entry not confirmed")
		}
		f.wantLaunches()
	})
	for _, tc := range []struct{ name, launchType, startType string }{
		{"a start of another type claims nothing", "Explore", "Plan"},
		{"a start without a type claims nothing", "Explore", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting)
			f.seedLaunch("toolu-1", apitest.WithClaimableType(tc.launchType))
			if saStartOf(f, saSub, tc.startType) {
				t.Errorf("entry confirmed")
			}
			f.wantLaunches("toolu-1")
		})
	}
	t.Run("a launch of an earlier launch is never claimed", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedLaunch("toolu-1", apitest.WithClaimableType("Explore"), apitest.WithClaimableLaunchToken("earlier-launch"))
		if saStartOf(f, saSub, "Explore") {
			t.Errorf("entry confirmed")
		}
		f.wantLaunches("toolu-1")
	})
	t.Run("the oldest launch of the type goes: opened_at first, then created_seq", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedLaunch("toolu-late", apitest.WithClaimableType("Explore"), apitest.WithClaimableOpenedAt(saAt(5)))
		f.seedLaunch("toolu-other", apitest.WithClaimableType("Plan"), apitest.WithClaimableOpenedAt(saAt(1)))
		f.seedLaunch("toolu-early", apitest.WithClaimableType("Explore"), apitest.WithClaimableOpenedAt(saAt(3)))
		f.seedLaunch("toolu-tie-a", apitest.WithClaimableType("Explore"), apitest.WithClaimableOpenedAt(saAt(3)))
		saStartOf(f, "sa-1", "Explore")
		f.wantLaunches("toolu-late", "toolu-other", "toolu-tie-a")
		saStartOf(f, "sa-2", "Explore")
		f.wantLaunches("toolu-late", "toolu-other")
	})
	t.Run("one launch and two starts: the first start claims, the second does not, in both orders", func(t *testing.T) {
		t.Parallel()
		for _, order := range [][2]string{{"sa-a", "sa-b"}, {"sa-b", "sa-a"}} {
			f := newSAStore(t, store.StateWaiting)
			f.seedLaunch("toolu-1", apitest.WithClaimableType("Explore"))
			first, second := saStartOf(f, order[0], "Explore"), saStartOf(f, order[1], "Explore")
			if !first || second {
				t.Errorf("order %v: confirmed %v then %v; want true then false", order, first, second)
			}
			f.wantLaunches()
		}
	})
	t.Run("two launches of one type: one claim each, never two for one start", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedLaunch("toolu-1", apitest.WithClaimableType("Explore"), apitest.WithClaimableOpenedAt(saAt(1)))
		f.seedLaunch("toolu-2", apitest.WithClaimableType("Explore"), apitest.WithClaimableOpenedAt(saAt(2)))
		if !saStartOf(f, "sa-1", "Explore") {
			t.Errorf("first start not confirmed")
		}
		f.wantLaunches("toolu-2")
		if !saStartOf(f, "sa-2", "Explore") || saStartOf(f, "sa-3", "Explore") {
			t.Errorf("want the second start confirmed and the third not")
		}
		f.wantLaunches()
	})
	t.Run("a stale launch is taken by a later start of its type", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedLaunch("toolu-failed", apitest.WithClaimableType("Explore"), apitest.WithClaimableOpenedAt(saAt(-1000)))
		if !saStartOf(f, saSub, "Explore") {
			t.Errorf("entry not confirmed by the stale launch")
		}
		f.wantLaunches()
	})
	t.Run("a start for an existing entry claims nothing", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedEntry(saSub, apitest.WithSubagentConfirmed(false))
		f.seedLaunch("toolu-1", apitest.WithClaimableType("Explore"))
		if saStartOf(f, saSub, "Explore") {
			t.Errorf("entry confirmed")
		}
		f.wantLaunches("toolu-1")
	})
}

// TestSubagentClaimFillsNothing checks a claim only confirms (SR-4.5, N11): after
// it every field but subagent_type and session_id is null until the entry's own
// launch details arrive, which then set them all.
func TestSubagentClaimFillsNothing(t *testing.T) {
	t.Parallel()
	f := newSAStore(t, store.StateWaiting)
	f.seedLaunch("toolu-1", apitest.WithClaimableType("Explore"))
	saStartOf(f, saSub, "Explore")

	e := f.mustEntry(saSub)
	if e.Description != nil || e.Model != nil || e.ParentType != nil || e.ParentID != nil || e.ToolUseID != nil || e.RunMode != nil ||
		e.SubagentType != "Explore" || e.Confirmed != int64(1) {
		t.Fatalf("claimed entry %+v; want only type, session and confirmation set", e)
	}
	d := saDetailsEv(store.SubagentLaunchForegroundFinished)
	d.ToolUseID = "toolu-1"
	f.details(d)
	e = f.mustEntry(saSub)
	if e.Description != "event description" || e.Model != "event-model" || e.ParentType != store.SubagentParentAgent ||
		e.ToolUseID != "toolu-1" || e.RunMode != store.SubagentRunModeForeground || e.SubagentType != "Explore" {
		t.Errorf("entry after its launch details %+v; want every field set and the start's type kept", e)
	}
}

func TestSubagentLaunchCloses(t *testing.T) {
	seed := func(f *saStore) {
		f.seedLaunch("toolu-1", apitest.WithClaimableType("Explore"))
		f.seedLaunch("toolu-2", apitest.WithClaimableType("Explore"))
	}
	for _, o := range []struct {
		name    string
		outcome store.SubagentLaunchOutcome
	}{
		{"other", store.SubagentLaunchOther}, {"background", store.SubagentLaunchBackgroundStarted}, {"foreground", store.SubagentLaunchForegroundFinished},
	} {
		t.Run("a "+o.name+" launch result closes the launch with its tool_use_id only", func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting)
			seed(f)
			d := saDetailsEv(o.outcome)
			d.ToolUseID = "toolu-1"
			f.details(d)
			f.wantLaunches("toolu-2")
		})
	}
	t.Run("a result with no subagent id (a teammate's) closes the launch and writes no entry", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		seed(f)
		d := saDetailsEv(store.SubagentLaunchBackgroundStarted)
		d.ToolUseID, d.SubagentID = "toolu-1", ""
		f.details(d)
		f.wantLaunches("toolu-2")
		if e := f.rows().Entries; len(e) != 0 {
			t.Errorf("entries %+v; want none", e)
		}
	})
	t.Run("a result for a tool_use_id with no open launch changes no launch", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		seed(f)
		d := saDetailsEv(store.SubagentLaunchOther)
		d.ToolUseID = "toolu-unknown"
		f.details(d)
		f.wantLaunches("toolu-1", "toolu-2")
	})
	t.Run("a failure closes the launch of its tool_use_id and writes no entry", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		seed(f)
		f.applied(applyFailure(f, "toolu-2"))
		f.wantLaunches("toolu-1")
		if e := f.rows().Entries; len(e) != 0 {
			t.Errorf("entries %+v; want none", e)
		}
	})
	t.Run("a failure leaves an entry with that tool_use_id as it is", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		f.seedEntry(saSub, apitest.WithSubagentToolUseID("toolu-1"))
		before := f.rows().Entries
		f.applied(applyFailure(f, "toolu-1"))
		if got := f.rows().Entries; !reflect.DeepEqual(got, before) {
			t.Errorf("entries %+v; want %+v unchanged", got, before)
		}
	})
}

func applyFailure(f *saStore, toolUseID string) store.HookApplied {
	f.t.Helper()
	return storefix.ApplySubagentLaunchFailure(f.t, f.s, f.id, store.SubagentLaunchFailure{ToolUseID: toolUseID})
}

// TestSubagentTurnEndGrace pins the grace edges of SR-4.5 and SR-7.5 on a
// main-agent Stop: a claimable launch closes only when opened strictly more than
// 2000 ms before the Stop's receipt time, and the skip read agrees with it.
func TestSubagentTurnEndGrace(t *testing.T) {
	opened := saAt(5)
	for _, tc := range []struct {
		name     string
		stopAt   int64
		wantOpen bool
	}{
		{"before the launch was opened", opened - 5000, true},
		{"at its opening", opened, true},
		{"1 ms short of the grace", opened + store.SubagentGraceMs - 1, true},
		{"exactly at the grace", opened + store.SubagentGraceMs, true},
		{"1 ms past the grace", opened + store.SubagentGraceMs + 1, false},
		{"well past the grace", opened + 600_000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWorking)
			f.seedLaunch("toolu-1", apitest.WithClaimableOpenedAt(opened))
			f.seedEntry(saSub)
			entries := f.rows().Entries

			if work, err := f.s.SubagentTurnEndHasWork(f.id, store.SubagentTurnEnd{ReceivedMs: tc.stopAt}); err != nil || work == tc.wantOpen {
				t.Errorf("SubagentTurnEndHasWork = %v, %v; want %v", work, err, !tc.wantOpen)
			}
			f.mainStop(tc.stopAt)

			if tc.wantOpen {
				f.wantLaunches("toolu-1")
			} else {
				f.wantLaunches()
			}
			if got := f.rows().Entries; !reflect.DeepEqual(got, entries) {
				t.Errorf("entries %+v; want %+v: a Stop closes launches only", got, entries)
			}
		})
	}
	t.Run("one Stop closes the old launches and keeps the younger", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.seedLaunch("toolu-old", apitest.WithClaimableOpenedAt(opened))
		f.seedLaunch("toolu-young", apitest.WithClaimableOpenedAt(opened+60_000))
		f.seedLaunch("toolu-older", apitest.WithClaimableOpenedAt(opened-60_000))
		f.mainStop(opened + 61_000)
		f.wantLaunches("toolu-young")
	})
	t.Run("a launch of an earlier launch is left alone", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.seedLaunch("toolu-earlier", apitest.WithClaimableOpenedAt(opened), apitest.WithClaimableLaunchToken("earlier-launch"))
		f.mainStop(opened + 600_000)
		f.wantLaunches("toolu-earlier")
	})
}

// TestSubagentTurnEndHasWork checks the skip read of SR-12.2: it counts only the
// row's current launch's claimable launches, reads nothing for an unknown row or
// one with no launch token, and writes nothing.
func TestSubagentTurnEndHasWork(t *testing.T) {
	past := saAt(5) + 600_000
	t.Run("nothing open", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.seedEntry(saSub)
		saWantWork(t, f, f.id, past, false)
	})
	t.Run("another row's launches do not count", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		other := f.seed(store.StateWorking, "sess-other")
		if err := apitest.SeedSubagentLaunch(f.path, other, "toolu-other", apitest.WithClaimableOpenedAt(saAt(5))); err != nil {
			t.Fatal(err)
		}
		saWantWork(t, f, f.id, past, false)
		saWantWork(t, f, other, past, true)
	})
	t.Run("an earlier launch's do not count", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.seedLaunch("toolu-1", apitest.WithClaimableOpenedAt(saAt(5)), apitest.WithClaimableLaunchToken("earlier-launch"))
		saWantWork(t, f, f.id, past, false)
	})
	t.Run("an unknown row has none", func(t *testing.T) {
		t.Parallel()
		saWantWork(t, newSAStore(t, store.StateWorking), "no-such-row", past, false)
	})
	t.Run("a row with no launch token has none", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking, apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket}))
		f.seedLaunch("toolu-1", apitest.WithClaimableLaunchToken("seed-launch"), apitest.WithClaimableOpenedAt(saAt(5)))
		saWantWork(t, f, f.id, past, false)
	})
	t.Run("a Stop with nothing past the grace writes no launch or entry", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWorking)
		f.seedLaunch("toolu-1", apitest.WithClaimableOpenedAt(saAt(5)))
		f.seedEntry(saSub)
		before := f.rows()
		f.mainStop(saAt(5) + 1000)
		if got := f.rows(); !reflect.DeepEqual(got, before) {
			t.Errorf("rows %+v; want %+v unchanged", got, before)
		}
	})
}

func saWantWork(t *testing.T, f *saStore, id string, at int64, want bool) {
	t.Helper()
	saWantWorkOf(t, f, id, store.SubagentTurnEnd{ReceivedMs: at}, want)
}

func saWantWorkOf(t *testing.T, f *saStore, id string, te store.SubagentTurnEnd, want bool) {
	t.Helper()
	before := f.rows()
	if got, err := f.s.SubagentTurnEndHasWork(id, te); err != nil || got != want {
		t.Errorf("SubagentTurnEndHasWork(%q) = %v, %v; want %v", id, got, err, want)
	}
	if got := f.rows(); !reflect.DeepEqual(got, before) {
		t.Errorf("the read changed rows: %+v -> %+v", before, got)
	}
}

// TestSubagentStopSkipWithSnapshot checks the full skip read of SR-12.2: a Stop
// has work when its snapshot lists a subagent, when it has a snapshot and a
// running background entry of the current launch is past the grace, or when a
// claimable launch is; otherwise it writes nothing, and one that has work
// changes the rows.
func TestSubagentStopSkipWithSnapshot(t *testing.T) {
	bg := apitest.WithSubagentRunMode(store.SubagentRunModeBackground)
	stopped := []apitest.SubagentOption{bg, apitest.WithSubagentState(store.SubagentStateStopped), apitest.WithSubagentCloseReason(store.SubagentCloseStopEvent),
		apitest.WithSubagentStoppedAt(saAt(2)), apitest.WithSubagentStoppedAtSource(store.SubagentSourceStop)}
	empty := &store.SubagentSnapshot{}
	listing := func(l ...store.SubagentListed) *store.SubagentSnapshot { return &store.SubagentSnapshot{Listed: l} }
	old := apitest.WithSubagentOpenedAt(saE - store.SubagentGraceMs - 1)
	for _, tc := range []struct {
		name      string
		seed      func(f *saStore)
		snap      *store.SubagentSnapshot
		work      bool
		unchanged bool // a Stop that has work but finds nothing to change
	}{
		{"an empty snapshot and nothing to close", func(f *saStore) {}, empty, false, false},
		{"a listing of only empty ids and nothing to close", func(f *saStore) {}, listing(saL("", "T")), false, false},
		{"an empty snapshot and a background entry exactly at the grace", func(f *saStore) {
			f.seedEntry(saSub, bg, apitest.WithSubagentOpenedAt(saE-store.SubagentGraceMs))
		}, empty, false, false},
		{"an empty snapshot and a background entry opened after the Stop", func(f *saStore) {
			f.seedEntry(saSub, bg, apitest.WithSubagentOpenedAt(saE+5000))
		}, empty, false, false},
		{"an empty snapshot and a foreground entry past the grace", func(f *saStore) {
			f.seedEntry(saSub, apitest.WithSubagentRunMode(store.SubagentRunModeForeground), old)
		}, empty, false, false},
		{"an empty snapshot and a null-mode entry past the grace", func(f *saStore) { f.seedEntry(saSub, old) }, empty, false, false},
		{"an empty snapshot and a stopped background entry past the grace", func(f *saStore) { f.seedEntry(saSub, append(stopped, old)...) }, empty, false, false},
		{"an empty snapshot and an earlier launch's background entry past the grace", func(f *saStore) {
			f.seedEntry(saSub, bg, old, apitest.WithSubagentLaunchToken("earlier-launch"))
		}, empty, false, false},
		{"no snapshot and a background entry past the grace", func(f *saStore) { f.seedEntry(saSub, bg, old) }, nil, false, false},
		{"a listing that names a subagent", func(f *saStore) {}, listing(saL(saSub, "T")), true, false},
		{"a listing that names a subagent that is already as the listing leaves it", func(f *saStore) {
			f.seedEntry(saSub, bg)
		}, listing(saL(saSub, "")), true, true},
		{"an empty snapshot and a background entry 1 ms past the grace", func(f *saStore) { f.seedEntry(saSub, bg, old) }, empty, true, false},
		{"an empty snapshot and a claimable launch past the grace", func(f *saStore) {
			f.seedLaunch("toolu-1", apitest.WithClaimableOpenedAt(saE-store.SubagentGraceMs-1))
		}, empty, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWorking)
			tc.seed(f)
			before := f.rows()
			te := store.SubagentTurnEnd{ReceivedMs: saE, Snapshot: tc.snap}

			saWantWorkOf(t, f, f.id, te, tc.work)
			f.applied(storefix.ApplySubagentMainAgentStop(t, f.s, f.id, storefix.MainAgentStop{ReceivedMs: saE, Snapshot: tc.snap}))

			if changed := !reflect.DeepEqual(f.rows(), before); changed == (tc.unchanged || !tc.work) {
				t.Errorf("rows changed = %v; want %v", changed, !(tc.unchanged || !tc.work))
			}
		})
	}
}

func TestSubagentEarlierLifeClaimAndResult(t *testing.T) {
	for _, outcome := range []store.SubagentLaunchOutcome{store.SubagentLaunchOther, store.SubagentLaunchBackgroundStarted, store.SubagentLaunchForegroundFinished} {
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting, apitest.WithLifeNumber(4))
			f.seedEntry(saSub, saSaturated(apitest.WithSubagentLife(3))...)
			hidden, _ := f.entryInLife(saSub, 3)
			f.seedLaunch("toolu-ev", apitest.WithClaimableType("start-type"))
			f.details(saDetailsEv(outcome))
			f.wantLaunches()
			if old, ok := f.entryInLife(saSub, 3); !ok || !reflect.DeepEqual(old, hidden) {
				t.Fatalf("hidden record changed: %+v -> %+v", hidden, old)
			}
			if outcome == store.SubagentLaunchOther {
				if _, ok := f.entry(saSub); ok {
					t.Fatal("other result created a current entry")
				}
			} else {
				f.start(saStartEv())
				if e := f.mustEntry(saSub); e.LifeNumber != 4 || e.SubagentType != "start-type" {
					t.Errorf("current entry after its start: %+v", e)
				}
				if old, _ := f.entryInLife(saSub, 3); !reflect.DeepEqual(old, hidden) {
					t.Errorf("current-life update changed hidden entry: %+v -> %+v", hidden, old)
				}
			}
		})
	}
	t.Run("start claims despite earlier-only id", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting, apitest.WithLifeNumber(4))
		f.seedEntry(saSub, saSaturated(apitest.WithSubagentLife(3))...)
		hidden, _ := f.entryInLife(saSub, 3)
		f.seedLaunch("toolu-1", apitest.WithClaimableType("Explore"))
		if !saStartOf(f, saSub, "Explore") {
			t.Fatal("current entry was not confirmed by claim")
		}
		f.wantLaunches()
		if old, _ := f.entryInLife(saSub, 3); !reflect.DeepEqual(old, hidden) {
			t.Errorf("hidden entry changed: %+v -> %+v", hidden, old)
		}
	})
}
