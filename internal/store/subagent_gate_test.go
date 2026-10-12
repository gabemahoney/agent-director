package store_test

// The hook gate on every subagent entry and claimable-launch write, the
// SubagentStart/SubagentStop row write and the rollback of a failed entry write
// (SR-3.1, SR-3.2, SR-3.3; b.xjn). The writes run through the store's methods
// with an explicit gate, so a row that is ended, missing, untracked or has no
// launch token is reachable (the storefix helpers revive an ended row first).
// Fixture and helpers: subagent_transitions_test.go.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// saGateVariant is a row and a hook parent: control is the one case whose writes
// land; the others each break one condition of SR-3.1.
type saGateVariant struct {
	name     string
	state    string
	opts     []apitest.SpawnOption
	foreign  bool   // the hook's parent is another process than the pane's
	token    string // launch token of the seeded rows; "" is the row's own
	control  bool
	rowWrite string // lifecycle row write: "applied", a not-applied reason, or "" unchecked
}

func saPaneIdentity(token string) apitest.SpawnOption {
	return apitest.WithLaunchIdentity(store.LaunchIdentity{Token: token, Socket: apitest.TestSocket, PanePID: 4242, PaneID: "%1"})
}

var saGateVariants = []saGateVariant{
	{name: "own agent, live tracked row", state: store.StateWaiting, control: true, rowWrite: "applied"},
	{name: "another process", state: store.StateWaiting, foreign: true, rowWrite: store.HookReasonPIDMismatch},
	{name: "row with no pane", state: store.StateWaiting, opts: []apitest.SpawnOption{apitest.WithNoPane()}, rowWrite: store.HookReasonNoPaneRecorded},
	{name: "ended row", state: store.StateEnded, opts: []apitest.SpawnOption{saPaneIdentity(goodToken)}},
	{name: "missing row", state: store.StateMissing, opts: []apitest.SpawnOption{saPaneIdentity(goodToken)}},
	{name: "untracked row", state: store.StateWaiting, opts: []apitest.SpawnOption{apitest.WithNoSubagentTracking()}, rowWrite: "applied"},
	{name: "row with no launch token", state: store.StateWaiting, opts: []apitest.SpawnOption{saPaneIdentity("")}, token: "seed-launch", rowWrite: "applied"},
}

func (v saGateVariant) store(t *testing.T) *saStore { return newSAStore(t, v.state, v.opts...) }

func (v saGateVariant) entryOpts(extra ...apitest.SubagentOption) []apitest.SubagentOption {
	if v.token != "" {
		extra = append(extra, apitest.WithSubagentLaunchToken(v.token))
	}
	return extra
}

func (v saGateVariant) launchOpts(extra ...apitest.SubagentLaunchOption) []apitest.SubagentLaunchOption {
	if v.token != "" {
		extra = append(extra, apitest.WithClaimableLaunchToken(v.token))
	}
	return extra
}

// seedPre seeds a running entry, a stopped entry, an open launch of type T and an
// old launch of type U.
func (v saGateVariant) seedPre(f *saStore) {
	f.t.Helper()
	f.seedEntry("run", v.entryOpts()...)
	f.seedEntry("stopped", v.entryOpts(apitest.WithSubagentState(store.SubagentStateStopped), apitest.WithSubagentCloseReason(store.SubagentCloseStopEvent),
		apitest.WithSubagentStoppedAt(saAt(2)), apitest.WithSubagentStoppedAtSource(store.SubagentSourceStop))...)
	f.seedLaunch("toolu-open", v.launchOpts(apitest.WithClaimableType("T"), apitest.WithClaimableOpenedAt(saAt(1)))...)
	f.seedLaunch("toolu-old", v.launchOpts(apitest.WithClaimableType("U"), apitest.WithClaimableOpenedAt(saAt(-1000)))...)
}

// gate is the gate of a hook of event from the variant's parent process.
func (v saGateVariant) gate(f *saStore, event string) store.HookGate {
	f.t.Helper()
	pid, start, err := storefix.AgentHookParent(f.s, f.id)
	if err != nil {
		f.t.Fatalf("AgentHookParent: %v", err)
	}
	if v.foreign {
		pid = storefix.ForeignHookParentPID(pid)
	}
	return store.HookGate{Event: event, ParentPID: pid, ParentStart: start}
}

// saGateWrite is one write kind: a lifecycle event or an after-write.
type saGateWrite struct {
	name  string
	life  *store.SubagentLifecycleEvent
	after store.SubagentAfterWrite
}

var saGateWrites = []saGateWrite{
	{name: "start creating an entry and claiming a launch", life: &store.SubagentLifecycleEvent{Kind: store.SubagentLifecycleStart, SubagentID: "new", Type: "T", ReceivedMs: saE}},
	{name: "start of a stopped entry", life: &store.SubagentLifecycleEvent{Kind: store.SubagentLifecycleStart, SubagentID: "stopped", ReceivedMs: saE}},
	{name: "stop of a running entry", life: &store.SubagentLifecycleEvent{Kind: store.SubagentLifecycleStop, SubagentID: "run", ReceivedMs: saE}},
	{name: "launch request", after: store.SubagentAfterWrite{LaunchRequest: &store.SubagentLaunchRequest{ToolUseID: "toolu-new", Type: "T", ReceivedMs: saE}}},
	{name: "background result creating an entry and closing its launch", after: store.SubagentAfterWrite{LaunchDetails: &store.SubagentLaunchDetails{
		ToolUseID: "toolu-open", SubagentID: "bg-new", Outcome: store.SubagentLaunchBackgroundStarted, ReceivedMs: saE}}},
	{name: "foreground result for an existing entry", after: store.SubagentAfterWrite{LaunchDetails: &store.SubagentLaunchDetails{
		ToolUseID: "toolu-old", SubagentID: "run", Outcome: store.SubagentLaunchForegroundFinished, ReceivedMs: saE}}},
	{name: "teammate result", after: store.SubagentAfterWrite{LaunchDetails: &store.SubagentLaunchDetails{ToolUseID: "toolu-open", ReceivedMs: saE}}},
	{name: "launch failure", after: store.SubagentAfterWrite{LaunchFailure: &store.SubagentLaunchFailure{ToolUseID: "toolu-open"}}},
	{name: "main agent's turn end", after: store.SubagentAfterWrite{TurnEnd: &store.SubagentTurnEnd{ReceivedMs: saE + 600_000}}},
}

// apply runs the write with the variant's gate; outcome and applied are the
// lifecycle's (zero for an after-write).
func (w saGateWrite) apply(f *saStore, v saGateVariant) (store.UpsertOutcome, store.HookApplied, error) {
	f.t.Helper()
	if w.life != nil {
		event := "SubagentStart"
		if w.life.Kind == store.SubagentLifecycleStop {
			event = "SubagentStop"
		}
		return f.s.ApplySubagentLifecycle(f.id, v.gate(f, event), *w.life, event, "", false)
	}
	return store.UpsertNoChange, store.HookApplied{}, f.s.ApplySubagentAfterWrite(f.id, v.gate(f, "PostToolUse"), w.after)
}

// TestSubagentGateRefusesEntryWrites checks that every kind of write changes no
// entry and no claimable launch unless the hook is the row's own agent on a live,
// tracked row with a launch token, and that the control row does change them.
func TestSubagentGateRefusesEntryWrites(t *testing.T) {
	for _, v := range saGateVariants {
		for _, w := range saGateWrites {
			t.Run(v.name+"/"+w.name, func(t *testing.T) {
				t.Parallel()
				f := v.store(t)
				v.seedPre(f)
				before := f.rows()

				outcome, applied, err := w.apply(f, v)

				if err != nil {
					t.Fatalf("write failed: %v; want it refused or applied without an error", err)
				}
				changed := !reflect.DeepEqual(f.rows(), before)
				if changed != v.control {
					t.Errorf("rows changed = %v; want %v\nbefore %+v\nafter  %+v", changed, v.control, before, f.rows())
				}
				if w.life == nil || v.rowWrite == "" {
					return
				}
				if v.rowWrite == "applied" {
					if outcome != store.UpsertUpdated || !applied.Applied {
						t.Errorf("outcome %v, applied %+v; want the row write applied", outcome, applied)
					}
				} else if outcome != store.UpsertNoChange || applied.Applied || applied.Reason != v.rowWrite {
					t.Errorf("outcome %v, applied %+v; want not applied with reason %q", outcome, applied, v.rowWrite)
				}
			})
		}
	}
}

// TestSubagentGateAtTheCap checks the gated evictions delete nothing for a
// refused hook (and exactly one row for the control), and that a refused creating
// start, creating result or launch request on a full row changes nothing, an
// eviction included.
func TestSubagentGateAtTheCap(t *testing.T) {
	for _, v := range saGateVariants {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			f := v.store(t)
			saFillEntries(f, v.entryOpts()...)
			saFillLaunches(f, v.launchOpts()...)
			before := f.rows()
			gate := v.gate(f, "SubagentStart")

			for name, evict := range map[string]func(*store.Store, string, store.HookGate) (bool, error){
				"entry": store.EvictSubagentEntryGatedForTest, "launch": store.EvictSubagentLaunchGatedForTest,
			} {
				if got, err := evict(f.s, f.id, gate); err != nil || got != v.control {
					t.Errorf("gated %s eviction = %v, %v; want %v", name, got, err, v.control)
				}
			}
			if v.control {
				f.wantCounts(store.SubagentCap-1, store.SubagentCap-1)
				return
			}
			if got := f.rows(); !reflect.DeepEqual(got, before) {
				t.Errorf("gated evictions changed rows")
			}
			for _, w := range []saGateWrite{saGateWrites[0], saGateWrites[3], saGateWrites[4]} {
				if _, _, err := w.apply(f, v); err != nil {
					t.Errorf("%s: %v", w.name, err)
				}
			}
			if got := f.rows(); !reflect.DeepEqual(got, before) {
				f.wantCounts(len(before.Entries), len(before.Launches))
				t.Errorf("a refused write on a full row changed rows")
			}
		})
	}
}

// TestSubagentLifecycleRowWrite checks the row write inside SubagentStart and
// SubagentStop (SR-3.2): a soft refresh of the row (state kept, last_seen_at
// bumped, liveness markers cleared, pane start recorded, row_version +1, no
// idle_since or settle write) with one trail record after the commit, and for an
// event with no subagent id only that.
func TestSubagentLifecycleRowWrite(t *testing.T) {
	const unverified = "2026-01-02 03:04:05"
	setup := func(t *testing.T) *saStore {
		f := newV5Store(t)
		id := f.seed(store.StateWaiting, "sess-rw", apitest.WithLaunchIdentity(store.LaunchIdentity{Token: goodToken, Socket: apitest.TestSocket, PanePID: 4242, PaneID: "%1"}),
			apitest.WithIdleSince(rvIdleSince), apitest.WithSettle(store.StateWaiting, 777),
			apitest.WithLivenessUnverifiedSince(unverified), apitest.WithLivenessNote("note"))
		sf := &saStore{v5Store: f, id: id}
		sf.seedEntry("run")
		sf.seedLaunch("toolu-open", apitest.WithClaimableType("T"))
		if err := store.BackdateLastSeenForTest(f.s, id); err != nil {
			t.Fatal(err)
		}
		return sf
	}
	own := func(f *saStore, event string) store.HookGate {
		f.t.Helper()
		pid, start, err := storefix.AgentHookParent(f.s, f.id)
		if err != nil {
			f.t.Fatal(err)
		}
		return store.HookGate{Event: event, ParentPID: pid, ParentStart: start}
	}
	for _, tc := range []struct {
		name      string
		event     string
		ev        store.SubagentLifecycleEvent
		wantEntry bool // the event writes an entry
	}{
		{"start", "SubagentStart", store.SubagentLifecycleEvent{Kind: store.SubagentLifecycleStart, SubagentID: "new", Type: "T", ReceivedMs: saE}, true},
		{"stop", "SubagentStop", store.SubagentLifecycleEvent{Kind: store.SubagentLifecycleStop, SubagentID: "run", ReceivedMs: saE}, true},
		{"start with no subagent id", "SubagentStart", store.SubagentLifecycleEvent{Kind: store.SubagentLifecycleStart, Type: "T", ReceivedMs: saE}, false},
		{"stop with no subagent id", "SubagentStop", store.SubagentLifecycleEvent{Kind: store.SubagentLifecycleStop, ReceivedMs: saE}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := setup(t)
			before, rowsBefore := f.rawColumns(f.id), f.rows()
			mark := store.TrailMark(t)

			outcome, applied, err := f.s.ApplySubagentLifecycle(f.id, own(f, tc.event), tc.ev, tc.event, "", false)

			if err != nil || outcome != store.UpsertUpdated || !applied.Applied {
				t.Fatalf("ApplySubagentLifecycle = %v, %+v, %v; want updated and applied", outcome, applied, err)
			}
			after := f.rawColumns(f.id)
			if after.LastSeenAt == before.LastSeenAt {
				t.Errorf("last_seen_at %v not bumped", after.LastSeenAt)
			}
			want := before
			want.RowVersion, want.LastSeenAt = before.RowVersion.(int64)+1, after.LastSeenAt
			want.LivenessUnverifiedSince, want.LivenessNote, want.PaneStarttime = nil, nil, storefix.SeedPaneStarttime
			if !reflect.DeepEqual(after, want) {
				t.Errorf("row after %+v; want only the soft refresh over %+v", after, want)
			}
			if settled, stamp, err := apitest.ReadSettle(f.path, f.id); err != nil || settled != store.StateWaiting || stamp != 777 {
				t.Errorf("settle = %q, %d, %v; want the seeded settle untouched", settled, stamp, err)
			}
			if changed := !reflect.DeepEqual(f.rows(), rowsBefore); changed != tc.wantEntry {
				t.Errorf("entry rows changed = %v; want %v (a claimable launch of type T included)", changed, tc.wantEntry)
			}
			events := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", f.id)
			if len(events) != 1 || events[0]["prior_state"] != store.StateWaiting || events[0]["new_state"] != store.StateWaiting ||
				events[0]["soft_refresh"] != true || events[0]["triggering_event_name"] != tc.event {
				t.Errorf("trail records %v; want one soft refresh of waiting to waiting for %s", events, tc.event)
			}
		})
	}
	t.Run("a refused hook writes no row and no trail record", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		before, rowsBefore := f.rawColumns(f.id), f.rows()
		mark := store.TrailMark(t)
		gate := own(f, "SubagentStart")
		gate.ParentPID++

		outcome, applied, err := f.s.ApplySubagentLifecycle(f.id, gate, *saGateWrites[0].life, "SubagentStart", "", false)

		if err != nil || outcome != store.UpsertNoChange || applied.Applied || applied.Reason != store.HookReasonPIDMismatch {
			t.Errorf("ApplySubagentLifecycle = %v, %+v, %v; want no change, pid_mismatch", outcome, applied, err)
		}
		if got := f.rawColumns(f.id); !reflect.DeepEqual(got, before) {
			t.Errorf("row after %+v; want %+v unchanged", got, before)
		}
		if got := f.rows(); !reflect.DeepEqual(got, rowsBefore) {
			t.Errorf("entry rows changed")
		}
		if events := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", f.id); len(events) != 0 {
			t.Errorf("trail records %v; want none", events)
		}
	})
	t.Run("an entry transaction after a hook writes no trail record", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		mark := store.TrailMark(t)
		w := store.SubagentAfterWrite{LaunchRequest: &store.SubagentLaunchRequest{ToolUseID: "toolu-new", Type: "T", ReceivedMs: saE}}
		if err := f.s.ApplySubagentAfterWrite(f.id, own(f, "PostToolUse"), w); err != nil {
			t.Fatal(err)
		}
		if events := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", f.id); len(events) != 0 {
			t.Errorf("trail records %v; want none", events)
		}
	})
}

// TestSubagentWriteFailureRollsBack checks a failed entry write leaves nothing
// behind: the SubagentStart's row write is rolled back with it, a result's launch
// close with its entry, and a write with no valid shape writes nothing.
func TestSubagentWriteFailureRollsBack(t *testing.T) {
	v := saGateVariants[0]
	seed := func(t *testing.T) *saStore {
		f := v.store(t)
		v.seedPre(f)
		if err := store.BackdateLastSeenForTest(f.s, f.id); err != nil {
			t.Fatal(err)
		}
		return f
	}
	t.Run("a start whose entry write fails rolls its row write back", func(t *testing.T) {
		t.Parallel()
		f := seed(t)
		storefix.InjectWriteFailure(t, f.path, storefix.WriteFailSubagentRecord, f.id)
		before, rowsBefore := f.rawColumns(f.id), f.rows()

		outcome, applied, err := saGateWrites[0].apply(f, v)

		if err == nil || outcome != store.UpsertError || applied.Applied {
			t.Errorf("ApplySubagentLifecycle = %v, %+v, %v; want an error and no applied row write", outcome, applied, err)
		}
		if got := f.rawColumns(f.id); !reflect.DeepEqual(got, before) {
			t.Errorf("row after %+v; want %+v: row_version and last_seen_at unchanged", got, before)
		}
		if got := f.rows(); !reflect.DeepEqual(got, rowsBefore) {
			t.Errorf("entry rows changed")
		}
	})
	t.Run("a result whose entry write fails keeps its launch open", func(t *testing.T) {
		t.Parallel()
		f := seed(t)
		storefix.InjectWriteFailure(t, f.path, storefix.WriteFailSubagentRecord, f.id)
		rowsBefore := f.rows()

		got := f.s.ApplySubagentAfterWrite(f.id, v.gate(f, "PostToolUse"), saGateWrites[4].after)

		if got == nil || !strings.Contains(got.Error(), "store: subagent entry transaction") {
			t.Errorf("error %v; want one wrapped as a subagent entry transaction", got)
		}
		if rows := f.rows(); !reflect.DeepEqual(rows, rowsBefore) || !f.hasLaunch("toolu-open") {
			t.Errorf("rows %+v; want %+v unchanged, the launch still open", rows, rowsBefore)
		}
	})
	t.Run("an unknown kind with a subagent id is an error and writes nothing", func(t *testing.T) {
		t.Parallel()
		f := seed(t)
		before, rowsBefore := f.rawColumns(f.id), f.rows()

		outcome, applied, err := f.s.ApplySubagentLifecycle(f.id, v.gate(f, "SubagentStart"),
			store.SubagentLifecycleEvent{Kind: "pause", SubagentID: "new", ReceivedMs: saE}, "SubagentStart", "", false)

		if err == nil || outcome != store.UpsertError || applied.Applied {
			t.Errorf("ApplySubagentLifecycle = %v, %+v, %v; want an error", outcome, applied, err)
		}
		if got := f.rawColumns(f.id); !reflect.DeepEqual(got, before) {
			t.Errorf("row after %+v; want %+v unchanged", got, before)
		}
		if got := f.rows(); !reflect.DeepEqual(got, rowsBefore) {
			t.Errorf("entry rows changed")
		}
	})
	for _, tc := range []struct {
		name string
		w    store.SubagentAfterWrite
	}{
		{"none", store.SubagentAfterWrite{}},
		{"two", store.SubagentAfterWrite{LaunchRequest: saGateWrites[3].after.LaunchRequest, LaunchFailure: saGateWrites[7].after.LaunchFailure}},
	} {
		t.Run("an after-write with "+tc.name+" of its fields set is an error and writes nothing", func(t *testing.T) {
			t.Parallel()
			f := seed(t)
			rowsBefore := f.rows()
			if err := f.s.ApplySubagentAfterWrite(f.id, v.gate(f, "PostToolUse"), tc.w); err == nil {
				t.Errorf("ApplySubagentAfterWrite returned no error")
			}
			if got := f.rows(); !reflect.DeepEqual(got, rowsBefore) {
				t.Errorf("rows changed")
			}
		})
	}
}
