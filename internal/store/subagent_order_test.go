package store_test

// Repeats and arrival order of the subagent events (SR-7.7, N6, N12; b.xjn): an
// event applied again with its own receipt time writes nothing, and every order
// of the events Claude Code produces ends in the same fields, confirmation and
// state, with the times the rules assign, listings included. The fixture and
// helpers are in subagent_helpers_test.go.

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

// saOrderTimes are the receipt times of the order sequences. A launch result's
// hook can start before the start's yet be applied after it, so the result's
// time can be the earliest. request is the time of the launch request that
// precedes a background launch (t0 of SR-15.2's sequence).
type saOrderTimes struct{ request, details, start, stop, mainStop int64 }

var saTimes = saOrderTimes{request: saAt(0), details: saAt(1), start: saAt(2), stop: saAt(3), mainStop: saAt(4)}

var (
	saTimeDetails = saTimes.details
	saTimeStart   = saTimes.start
	saTimeStop    = saTimes.stop
)

// saOrderStep applies one named event of an order sequence at its own receipt
// time (saTimes): S a SubagentStart, B a background launch result, F a foreground
// launch result, P a SubagentStop with an empty listing, M a main-agent Stop with
// an empty snapshot.
func saOrderStep(f *saStore, step string) { f.t.Helper(); saOrderStepAt(f, step, saTimes) }

// saOrderStepAt is saOrderStep at the receipt times tm, with three more steps: R
// the launch request that opens the claimable launch a start or a background
// result then claims or closes, L a main-agent Stop whose snapshot lists the
// subagent and I its SubagentStop, which lists itself.
func saOrderStepAt(f *saStore, step string, tm saOrderTimes) {
	f.t.Helper()
	switch step {
	case "R":
		f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-ev", Type: "start-type", ReceivedMs: tm.request})
	case "S":
		ev := saStartEv()
		ev.SessionID, ev.ReceivedMs = "sess-"+f.id, tm.start
		f.start(ev)
	case "B":
		d := saDetailsEv(store.SubagentLaunchBackgroundStarted)
		d.ReceivedMs = tm.details
		f.details(d)
	case "F":
		d := saDetailsEv(store.SubagentLaunchForegroundFinished)
		d.ReceivedMs = tm.details
		f.details(d)
	case "P":
		ev := saStopEv(false)
		ev.ReceivedMs, ev.Listing = tm.stop, []store.SubagentListed{}
		f.stop(ev)
	case "M":
		f.snapStop(tm.mainStop)
	case "L":
		f.snapStop(tm.mainStop, saL(saSub, "list-type"))
	case "I":
		f.listStop(saSub, tm.stop, saL(saSub, "list-type"))
	default:
		f.t.Fatalf("unknown order step %q", step)
	}
}

// saForegroundTimes are the receipt times of SR-15.2's foreground sequence: the
// launch request at t0, the start at t1, its SubagentStop at t2, the completed
// result at t3 (after the stop) and the main-agent Stop at t4. The Stop is far
// past the 2 s grace of the request, so it closes the claimable launch when
// neither the start nor the result has yet.
var saForegroundTimes = saOrderTimes{request: saAt(0), start: saAt(1), stop: saAt(2), details: saAt(3), mainStop: saAt(4)}

// TestSubagentOrderIndependence applies the same events of one subagent in each
// order Claude Code produces (a stop never before its own start; a result at any
// point, N6) and checks the entry ends with the same fields, confirmation and
// state as the first order, with first_started_at the earliest start receipt,
// stopped_at the stop's, and started_at the receipt of whichever start came first
// (N12, SR-7.7). Each sequence opens with its launch request (SR-15.2), which the
// start claims or the result closes, whichever comes first, or, in an order
// where a main-agent Stop past the grace comes first (MSFP), that Stop closes:
// no claimable launch is left in any order, and the entry's fields are the same
// either way. The foreground orders include a main-agent Stop with an empty
// snapshot, which closes nothing of a foreground entry, and the SRD's times: the
// completed result comes after the stop, yet stopped_at is the stop's.
func TestSubagentOrderIndependence(t *testing.T) {
	cases := []struct {
		name        string
		tm          saOrderTimes
		orders      []string // each a sequence of steps
		wantFirst   int64
		wantStarted map[string]int64 // by order
		wantStopped int64
		wantType    string
		wantMode    string
	}{
		{"background", saTimes, []string{"SBP", "BSP", "SPB"}, saTimeDetails,
			map[string]int64{"SBP": saTimeStart, "BSP": saTimeDetails, "SPB": saTimeStart}, saTimeStop, "start-type", store.SubagentRunModeBackground},
		{"foreground", saForegroundTimes, []string{"SFP", "FSP", "SPF", "SFPM", "SFMP", "FSMP", "MSFP"}, saForegroundTimes.start,
			map[string]int64{"SFP": saForegroundTimes.start, "FSP": saForegroundTimes.start, "SPF": saForegroundTimes.start, "SFPM": saForegroundTimes.start,
				"SFMP": saForegroundTimes.start, "FSMP": saForegroundTimes.start, "MSFP": saForegroundTimes.start},
			saForegroundTimes.stop, "start-type", store.SubagentRunModeForeground},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var baseline *apitest.SubagentEntryColumns
			for _, order := range tc.orders {
				f := newSAStore(t, store.StateWaiting)
				saOrderStepAt(f, "R", tc.tm)
				f.wantLaunches("toolu-ev")
				for _, step := range strings.Split(order, "") {
					saOrderStepAt(f, step, tc.tm)
				}
				f.wantLaunches()
				e := f.mustEntry(saSub)
				saCheckInvariants(t, e)
				if e.State != store.SubagentStateStopped || e.StoppedAt != tc.wantStopped || e.StoppedAtSource != store.SubagentSourceStop {
					t.Errorf("%s: state %v stopped_at %v from %v; want stopped, %d from stop", order, e.State, e.StoppedAt, e.StoppedAtSource, tc.wantStopped)
				}
				if e.LostAt != nil {
					t.Errorf("%s: lost_at %v; want null", order, e.LostAt)
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

// saPermutations returns every ordering of the distinct characters of steps, as
// strings, that valid accepts, in lexicographic order of the input's positions.
func saPermutations(steps string, valid func(string) bool) []string {
	var out []string
	var walk func(prefix string, rest []byte)
	walk = func(prefix string, rest []byte) {
		if len(rest) == 0 {
			if valid(prefix) {
				out = append(out, prefix)
			}
			return
		}
		for i := range rest {
			next := append(append([]byte{}, rest[:i]...), rest[i+1:]...)
			walk(prefix+string(rest[i]), next)
		}
	}
	walk("", []byte(steps))
	return out
}

// TestSubagentOrderIndependenceWithListings applies a background subagent's
// start, launch result, stop and the two listings of it (a main-agent Stop's
// snapshot and its own SubagentStop's) in every order Claude Code can produce
// (the 12 with the start before its own SubagentStop)
// and checks each ends as the first does (N6, N12, SR-7.4, SR-7.6). The times
// are inside the 2 s grace, so no order closes anything, and the launch request
// that opens the sequence is claimed by the start or closed by the result. Whichever of the start
// and the result is applied first gives started_at; the listing never moves a
// time, a type the start gave or the state the stop gave.
func TestSubagentOrderIndependenceWithListings(t *testing.T) {
	base := saAt(1)
	timings := []struct {
		name string
		tm   saOrderTimes
	}{
		{"result received before the start", saOrderTimes{request: base, details: base + 1, start: base + 2, mainStop: base + 50, stop: base + 100}},
		{"result received after the start", saOrderTimes{request: base, start: base + 1, details: base + 2, mainStop: base + 50, stop: base + 100}},
	}
	// Every order of the four events but the ones with the SubagentStop's listing
	// (I) before the start (S): a stop never comes before its own start. 12 of 24.
	orders := saPermutations("SBLI", func(order string) bool { return strings.Index(order, "S") < strings.Index(order, "I") })
	if len(orders) != 12 {
		t.Fatalf("generated %d orders %v; want the 12 with S before I", len(orders), orders)
	}
	for _, tt := range timings {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var baseline *apitest.SubagentEntryColumns
			for _, order := range orders {
				f := newSAStore(t, store.StateWaiting)
				saOrderStepAt(f, "R", tt.tm)
				f.wantLaunches("toolu-ev")
				for _, step := range strings.Split(order, "") {
					saOrderStepAt(f, step, tt.tm)
				}
				e := f.mustEntry(saSub)
				saCheckInvariants(t, e)
				wantStarted := tt.tm.start
				if strings.Index(order, "B") < strings.Index(order, "S") {
					wantStarted = tt.tm.details
				}
				wantFirst := min(tt.tm.start, tt.tm.details)
				if e.State != store.SubagentStateStopped || e.StoppedAt != tt.tm.stop || e.StoppedAtSource != store.SubagentSourceStop || e.LostAt != nil {
					t.Errorf("%s: state %v stopped_at %v from %v lost_at %v; want stopped, %d from stop, no lost_at", order, e.State, e.StoppedAt, e.StoppedAtSource, e.LostAt, tt.tm.stop)
				}
				if e.FirstStartedAt != wantFirst || e.StartedAt != wantStarted {
					t.Errorf("%s: first_started_at %v started_at %v; want %d, %d", order, e.FirstStartedAt, e.StartedAt, wantFirst, wantStarted)
				}
				if e.Confirmed != int64(1) || e.SubagentType != "start-type" || e.SubagentTypeSource != store.SubagentSourceStart || e.RunMode != store.SubagentRunModeBackground {
					t.Errorf("%s: confirmed %v type %v from %v run_mode %v; want 1, start-type from start, background", order, e.Confirmed, e.SubagentType, e.SubagentTypeSource, e.RunMode)
				}
				if baseline == nil {
					baseline = &e
				} else if diff := saSameEntry(*baseline, e); diff != "" {
					t.Errorf("%s differs from %s: %s", order, orders[0], diff)
				}
				f.wantLaunches()
			}
		})
	}
}

// TestSubagentTwoListingsOfDifferentTypes checks two listings of one id with
// different types, in both orders (SR-6.3, SR-7.4, SR-7.6): a start's type is
// never replaced by either, and with no entry only the main-agent Stop's
// snapshot creates one, so its type is the one kept whichever came first.
func TestSubagentTwoListingsOfDifferentTypes(t *testing.T) {
	both := []struct {
		name string
		main int64
		sub  int64
	}{
		{"main-agent Stop then SubagentStop", saAt(3), saAt(4)},
		{"SubagentStop then main-agent Stop", saAt(4), saAt(3)},
	}
	apply := func(f *saStore, mainFirst bool, mainAt, subAt int64) {
		main := func() { f.snapStop(mainAt, saL(saSub, "main-type")) }
		sub := func() { f.listStop("sa-stopper", subAt, saL(saSub, "stopper-type")) }
		if mainFirst {
			main()
			sub()
		} else {
			sub()
			main()
		}
	}
	for i, o := range both {
		mainFirst := i == 0
		t.Run("a start-created entry/"+o.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWorking)
			ev := saStartEv()
			ev.ReceivedMs = saAt(2)
			f.start(ev)

			apply(f, mainFirst, o.main, o.sub)

			e := f.mustEntry(saSub)
			if e.SubagentType != "start-type" || e.SubagentTypeSource != store.SubagentSourceStart || e.RunMode != store.SubagentRunModeBackground || e.RunModeSource != store.SubagentSourceSnapshot {
				t.Errorf("type %v from %v run_mode %v from %v; want start-type from start, background from snapshot", e.SubagentType, e.SubagentTypeSource, e.RunMode, e.RunModeSource)
			}
			if e.State != store.SubagentStateRunning || e.Description != nil {
				t.Errorf("state %v description %v; want running, nil", e.State, e.Description)
			}
		})
		t.Run("no entry/"+o.name, func(t *testing.T) {
			t.Parallel()
			f := newSAStore(t, store.StateWorking)

			apply(f, mainFirst, o.main, o.sub)

			e := f.mustEntry(saSub)
			if e.SubagentType != "main-type" || e.SubagentTypeSource != store.SubagentSourceSnapshot || e.Description != nil ||
				e.RunMode != store.SubagentRunModeBackground || e.RunModeSource != store.SubagentSourceSnapshot {
				t.Errorf("type %v from %v description %v run_mode %v from %v; want main-type from snapshot, nil, background from snapshot",
					e.SubagentType, e.SubagentTypeSource, e.Description, e.RunMode, e.RunModeSource)
			}
			if n := len(f.rows().Entries); n != 1 {
				t.Errorf("%d entries; want 1", n)
			}
		})
	}
}
