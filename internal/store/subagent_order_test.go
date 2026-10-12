package store_test

// Repeats and arrival order of the subagent events (SR-7.7, N6, N12; b.xjn): an
// event applied again with its own receipt time writes nothing, and every order
// of the events Claude Code produces ends in the same fields, confirmation and
// state, with the times the rules assign. The fixture and helpers are in
// subagent_transitions_test.go.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSubagentRepeatsWriteNothing applies each event twice with its own receipt
// time, to an id with no entry and to an entry in each state: the second
// application leaves every column of every entry as the first left it,
// activity_at included (SR-7.7).
func TestSubagentRepeatsWriteNothing(t *testing.T) {
	priors := []struct {
		name string
		opts []apitest.SubagentOption // nil = no entry
	}{
		{"no entry", nil},
		{"running", saSaturated()},
		{"stopped", saStoppedSeed(store.SubagentSourceStop)},
		{"lost", saSaturated(apitest.WithSubagentState(store.SubagentStateLost), apitest.WithSubagentCloseReason(store.SubagentCloseAgentGone),
			apitest.WithSubagentLostAt(saAt(3)))},
	}
	for _, p := range priors {
		for _, ev := range saEvents {
			t.Run(p.name+"/"+ev.name, func(t *testing.T) {
				t.Parallel()
				f := newSAStore(t, store.StateWaiting)
				if p.opts != nil {
					f.seedEntry(saSub, p.opts...)
				}
				ev.apply(f)
				once := f.rows()

				ev.apply(f)

				if twice := f.rows(); !reflect.DeepEqual(twice, once) {
					t.Errorf("rows after the second application\n got %+v\nwant %+v", twice, once)
				}
			})
		}
	}
}

// TestSubagentRepeatsAtALaterTime checks the two repeats that are not no-ops
// (N12): a start after a stop cannot be told from a restart, so it reopens the
// entry, and a stop that carries stop_hook_active, delivered twice, moves
// stopped_at to the later delivery. A plain second stop moves nothing.
func TestSubagentRepeatsAtALaterTime(t *testing.T) {
	at := func(f *saStore, kind string, ms int64, supersedes bool) {
		switch kind {
		case "start":
			ev := saStartEv()
			ev.ReceivedMs = ms
			f.start(ev)
		default:
			ev := saStopEv(supersedes)
			ev.ReceivedMs = ms
			f.stop(ev)
		}
	}
	t.Run("a start after a stop reopens the entry", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		at(f, "start", saAt(1), false)
		at(f, "stop", saAt(2), false)
		at(f, "start", saAt(3), false)
		e := f.mustEntry(saSub)
		want := map[string]any{"state": store.SubagentStateRunning, "first": saAt(1), "started": saAt(3), "opened": saAt(3), "stopped": saAt(2)}
		got := map[string]any{"state": e.State, "first": e.FirstStartedAt, "started": e.StartedAt, "opened": e.OpenedAt, "stopped": e.StoppedAt}
		if !reflect.DeepEqual(got, want) || e.CloseReason != nil {
			t.Errorf("entry %v, close_reason %v; want %v with stopped_at kept and no close_reason", got, e.CloseReason, want)
		}
		saCheckInvariants(t, e)
	})
	t.Run("a stop superseding a blocked stop, delivered twice, takes the later time", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		at(f, "start", saAt(1), false)
		at(f, "stop", saAt(2), true)
		at(f, "stop", saAt(3), true)
		if e := f.mustEntry(saSub); e.StoppedAt != saAt(3) || e.ActivityAt != saAt(3) || e.State != store.SubagentStateStopped {
			t.Errorf("state %v stopped_at %v activity_at %v; want stopped, %d, %d", e.State, e.StoppedAt, e.ActivityAt, saAt(3), saAt(3))
		}
	})
	t.Run("a plain second stop moves nothing", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		at(f, "start", saAt(1), false)
		at(f, "stop", saAt(2), false)
		before := f.mustEntry(saSub)
		at(f, "stop", saAt(3), false)
		if got := f.mustEntry(saSub); !reflect.DeepEqual(got, before) {
			t.Errorf("entry %+v; want %+v unchanged", got, before)
		}
	})
	t.Run("a stop superseding a blocked stop, after the blocked one, takes its time", func(t *testing.T) {
		t.Parallel()
		f := newSAStore(t, store.StateWaiting)
		at(f, "start", saAt(1), false)
		at(f, "stop", saAt(2), false)
		at(f, "stop", saAt(3), true)
		if e := f.mustEntry(saSub); e.StoppedAt != saAt(3) {
			t.Errorf("stopped_at = %v; want %d", e.StoppedAt, saAt(3))
		}
	})
}

// The receipt times of the order sequences. A launch result's hook can start
// before the start's yet be applied after it, so the result's time is the
// earliest.
var (
	saTimeDetails = saAt(1)
	saTimeStart   = saAt(2)
	saTimeStop    = saAt(3)
)

// saOrderStep applies one named event of an order sequence at its own receipt
// time: S a SubagentStart, B a background launch result, F a foreground launch
// result, P a SubagentStop.
func saOrderStep(f *saStore, step string) {
	f.t.Helper()
	switch step {
	case "S":
		ev := saStartEv()
		ev.SessionID, ev.ReceivedMs = "sess-"+f.id, saTimeStart
		f.start(ev)
	case "B":
		d := saDetailsEv(store.SubagentLaunchBackgroundStarted)
		d.ReceivedMs = saTimeDetails
		f.details(d)
	case "F":
		d := saDetailsEv(store.SubagentLaunchForegroundFinished)
		d.ReceivedMs = saTimeDetails
		f.details(d)
	case "P":
		ev := saStopEv(false)
		ev.ReceivedMs = saTimeStop
		f.stop(ev)
	default:
		f.t.Fatalf("unknown order step %q", step)
	}
}

// TestSubagentOrderIndependence applies the same events of one subagent in each
// order Claude Code produces (a stop never before its own start; a result at any
// point, N6) and checks the entry ends with the same fields, confirmation and
// state as the first order, with first_started_at the earliest start receipt,
// stopped_at the stop's, and started_at the receipt of whichever start came first
// (N12, SR-7.7).
func TestSubagentOrderIndependence(t *testing.T) {
	cases := []struct {
		name        string
		orders      []string // each a sequence of steps
		wantFirst   int64
		wantStarted map[string]int64 // by order
		wantStopped int64
		wantType    string
		wantMode    string
	}{
		{"background", []string{"SBP", "BSP", "SPB"}, saTimeDetails,
			map[string]int64{"SBP": saTimeStart, "BSP": saTimeDetails, "SPB": saTimeStart}, saTimeStop, "start-type", store.SubagentRunModeBackground},
		{"foreground", []string{"SFP", "FSP", "SPF"}, saTimeStart,
			map[string]int64{"SFP": saTimeStart, "FSP": saTimeStart, "SPF": saTimeStart}, saTimeStop, "start-type", store.SubagentRunModeForeground},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var baseline *apitest.SubagentEntryColumns
			for _, order := range tc.orders {
				f := newSAStore(t, store.StateWaiting)
				for _, step := range strings.Split(order, "") {
					saOrderStep(f, step)
				}
				e := f.mustEntry(saSub)
				saCheckInvariants(t, e)
				if e.State != store.SubagentStateStopped || e.StoppedAt != tc.wantStopped || e.StoppedAtSource != store.SubagentSourceStop {
					t.Errorf("%s: state %v stopped_at %v from %v; want stopped, %d from stop", order, e.State, e.StoppedAt, e.StoppedAtSource, tc.wantStopped)
				}
				if e.FirstStartedAt != tc.wantFirst || e.StartedAt != tc.wantStarted[order] {
					t.Errorf("%s: first_started_at %v started_at %v; want %d, %d", order, e.FirstStartedAt, e.StartedAt, tc.wantFirst, tc.wantStarted[order])
				}
				if e.Confirmed != int64(1) || e.SubagentType != tc.wantType || e.RunMode != tc.wantMode {
					t.Errorf("%s: confirmed %v type %v run_mode %v; want 1, %s, %s", order, e.Confirmed, e.SubagentType, e.RunMode, tc.wantType, tc.wantMode)
				}
				if baseline == nil {
					baseline = &e
				} else if diff := saSameEntry(*baseline, e); diff != "" {
					t.Errorf("%s differs from %s: %s", order, tc.orders[0], diff)
				}
			}
		})
	}
}

// TestSubagentOrderSessionIDFollowsTheCreatingEvent checks the one field that
// may depend on the order (N12): session_id is the creating event's, the start's
// in one order and the launch result's in the other.
func TestSubagentOrderSessionIDFollowsTheCreatingEvent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ order, want string }{{"SB", "sess-sa-agent"}, {"BS", "sess-ev"}} {
		f := newSAStore(t, store.StateWaiting)
		for _, step := range strings.Split(tc.order, "") {
			saOrderStep(f, step)
		}
		if got := f.mustEntry(saSub).SessionID; got != tc.want {
			t.Errorf("order %s: session_id = %v; want %s", tc.order, got, tc.want)
		}
	}
}

// TestSubagentResultAloneAndStopWins checks the stopped_at rules of SR-6.5 that
// the orders above do not isolate: a completed result alone gives stopped_at its
// own receipt time, and a stop's time wins over the result's whichever came
// first.
func TestSubagentResultAloneAndStopWins(t *testing.T) {
	for _, tc := range []struct {
		order      string
		wantStop   int64
		wantSource string
	}{
		{"F", saTimeDetails, store.SubagentSourceDetails},
		{"SFP", saTimeStop, store.SubagentSourceStop},
		{"SPF", saTimeStop, store.SubagentSourceStop},
	} {
		t.Run(tc.order, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWaiting)
			for _, step := range strings.Split(tc.order, "") {
				saOrderStep(f, step)
			}
			if e := f.mustEntry(saSub); e.StoppedAt != tc.wantStop || e.StoppedAtSource != tc.wantSource {
				t.Errorf("stopped_at %v from %v; want %d from %s", e.StoppedAt, e.StoppedAtSource, tc.wantStop, tc.wantSource)
			}
		})
	}
}

// TestSubagentInterleavedSubagents checks the hooks of two subagents interleaved
// in any way give each the entry it has when applied alone (N6): one entry's
// events never touch the other's.
func TestSubagentInterleavedSubagents(t *testing.T) {
	t.Parallel()
	run := func(order []string) apitest.SubagentRows {
		f := newSAStore(t, store.StateWaiting)
		for _, step := range order {
			sub, kind := step[:1], step[1:]
			if kind == "S" {
				f.start(store.SubagentLifecycleEvent{SubagentID: "sa-" + sub, Type: "start-type", ReceivedMs: saTimeStart})
			} else {
				f.stop(store.SubagentLifecycleEvent{SubagentID: "sa-" + sub, ReceivedMs: saTimeStop})
			}
		}
		return f.rows()
	}
	alone := run([]string{"aS", "aP", "bS", "bP"})
	for _, order := range [][]string{{"aS", "bS", "bP", "aP"}, {"aS", "bS", "aP", "bP"}, {"bS", "aS", "aP", "bP"}} {
		got := run(order)
		for i, e := range got.Entries {
			var want apitest.SubagentEntryColumns
			for _, w := range alone.Entries {
				if w.SubagentID == e.SubagentID {
					want = w
				}
			}
			if diff := saSameEntry(want, e); diff != "" || e.StartedAt != want.StartedAt || e.StoppedAt != want.StoppedAt || e.FirstStartedAt != want.FirstStartedAt {
				t.Errorf("order %v entry %d (%v): %+v; want %+v", order, i, e.SubagentID, e, want)
			}
		}
		if len(got.Entries) != 2 {
			t.Errorf("order %v: %d entries; want 2", order, len(got.Entries))
		}
	}
}
