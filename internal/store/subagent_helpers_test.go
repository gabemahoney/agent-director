package store_test

// The shared fixture of the subagent store tests (b.xjn): the saStore store with
// one live row and its event, seed and read helpers, the entry invariants and
// the saEvents table (the six events of SR-7.1 to SR-7.3 at one receipt time).
// Used by subagent_transitions_test.go, subagent_order_test.go,
// subagent_launches_test.go, subagent_caps_test.go and subagent_gate_test.go.

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	saAgentID = "sa-agent" // fixed so entries compare across stores (parent_id)
	saSub     = "sa-sub"
)

// saAt is the n-th ten-second step past the seed epoch; saE is the receipt time
// of the event under test, past every seeded time.
func saAt(n int64) int64 { return apitest.SeededSubagentMs + n*10_000 }

var saE = saAt(10)

// saStore is a store with one live row, "sa-agent", whose own agent applies the
// subagent hook events.
type saStore struct {
	*v5Store
	id string
}

func newSAStore(t *testing.T, state string, opts ...apitest.SpawnOption) *saStore {
	t.Helper()
	f := newV5Store(t)
	id, err := apitest.SeedSpawn(f.path, saAgentID, state, "/tmp", "", "sess-sa", false, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn(%s): %v", state, err)
	}
	return &saStore{v5Store: f, id: id}
}

func (f *saStore) applied(a store.HookApplied) {
	f.t.Helper()
	if !a.Applied {
		f.t.Fatalf("the row write was not applied: %+v; want applied", a)
	}
}

func (f *saStore) start(ev store.SubagentLifecycleEvent) {
	f.t.Helper()
	f.applied(storefix.ApplySubagentStart(f.t, f.s, f.id, ev))
}

func (f *saStore) stop(ev store.SubagentLifecycleEvent) {
	f.t.Helper()
	f.applied(storefix.ApplySubagentStop(f.t, f.s, f.id, ev))
}

func (f *saStore) details(d store.SubagentLaunchDetails) {
	f.t.Helper()
	f.applied(storefix.ApplySubagentLaunchDetails(f.t, f.s, f.id, d))
}

func (f *saStore) request(r store.SubagentLaunchRequest) {
	f.t.Helper()
	f.applied(storefix.ApplySubagentLaunchRequest(f.t, f.s, f.id, r))
}

func (f *saStore) mainStop(ms int64) {
	f.t.Helper()
	f.applied(storefix.ApplySubagentMainAgentStop(f.t, f.s, f.id, storefix.MainAgentStop{ReceivedMs: ms}))
}

func (f *saStore) rows() apitest.SubagentRows { f.t.Helper(); return f.subagentRowsOf(f.id) }

// entry returns the current-life entry with subagent id sub.
func (f *saStore) entry(sub string) (apitest.SubagentEntryColumns, bool) {
	f.t.Helper()
	return f.entryInLife(sub, f.rawColumns(f.id).LifeNumber.(int64))
}

func (f *saStore) entryInLife(sub string, life int64) (apitest.SubagentEntryColumns, bool) {
	f.t.Helper()
	for _, e := range f.rows().Entries {
		if e.SubagentID == sub && e.LifeNumber == life {
			return e, true
		}
	}
	return apitest.SubagentEntryColumns{}, false
}

func (f *saStore) mustEntry(sub string) apitest.SubagentEntryColumns {
	f.t.Helper()
	e, ok := f.entry(sub)
	if !ok {
		f.t.Fatalf("no entry %q; rows %+v", sub, f.rows().Entries)
	}
	return e
}

func (f *saStore) seedEntry(sub string, opts ...apitest.SubagentOption) {
	f.t.Helper()
	if err := apitest.SeedSubagent(f.path, f.id, sub, opts...); err != nil {
		f.t.Fatalf("SeedSubagent(%s): %v", sub, err)
	}
}

func (f *saStore) seedLaunch(toolUseID string, opts ...apitest.SubagentLaunchOption) {
	f.t.Helper()
	if err := apitest.SeedSubagentLaunch(f.path, f.id, toolUseID, opts...); err != nil {
		f.t.Fatalf("SeedSubagentLaunch(%s): %v", toolUseID, err)
	}
}

// token is the row's current launch_token as stored.
func (f *saStore) token() any { return f.rawColumns(f.id).LaunchToken }

// saSameEntry reports how two entries differ, field by field, except the
// columns that depend on the row or on arrival: created_seq, launch_token, the
// six times and session_id (N12). "" means they are the same.
func saSameEntry(a, b apitest.SubagentEntryColumns) string {
	for _, e := range []*apitest.SubagentEntryColumns{&a, &b} {
		e.CreatedSeq, e.LaunchToken, e.SessionID = 0, nil, nil
		e.FirstStartedAt, e.StartedAt, e.StoppedAt, e.LostAt, e.OpenedAt, e.ActivityAt = nil, nil, nil, nil, nil, nil
	}
	if reflect.DeepEqual(a, b) {
		return ""
	}
	return fmt.Sprintf("%+v vs %+v", a, b)
}

// saCheckInvariants fails the test when e breaks an invariant of SR-6.5.
func saCheckInvariants(t *testing.T, e apitest.SubagentEntryColumns) {
	t.Helper()
	switch e.State {
	case store.SubagentStateRunning:
		if e.CloseReason != nil || e.LostAt != nil {
			t.Errorf("running entry has close_reason %v, lost_at %v; want both null", e.CloseReason, e.LostAt)
		}
	case store.SubagentStateStopped:
		if e.CloseReason != store.SubagentCloseStopEvent || e.StoppedAt == nil || e.LostAt != nil {
			t.Errorf("stopped entry has close_reason %v, stopped_at %v, lost_at %v; want stop_event, set, null", e.CloseReason, e.StoppedAt, e.LostAt)
		}
	case store.SubagentStateLost:
		if e.LostAt == nil {
			t.Errorf("lost entry has no lost_at")
		}
	}
	first, fok := e.FirstStartedAt.(int64)
	started, sok := e.StartedAt.(int64)
	if fok != sok || (fok && started < first) {
		t.Errorf("first_started_at %v, started_at %v; want both set with started_at not before first_started_at, or both null", e.FirstStartedAt, e.StartedAt)
	}
}

// saSaturated is a confirmed running entry with every field set, so an event
// shows only its state, launch and time effects.
func saSaturated(extra ...apitest.SubagentOption) []apitest.SubagentOption {
	return append([]apitest.SubagentOption{
		apitest.WithSubagentType("seed-type"), apitest.WithSubagentTypeSource(store.SubagentSourceStart),
		apitest.WithSubagentDescription("seed description"), apitest.WithSubagentModel("seed-model"),
		apitest.WithSubagentParent(store.SubagentParentAgent, saAgentID), apitest.WithSubagentToolUseID("toolu-seed"),
		apitest.WithSubagentRunMode(store.SubagentRunModeBackground), apitest.WithSubagentRunModeSource(store.SubagentSourceDetails),
		apitest.WithSubagentSessionID("sess-seed"),
		apitest.WithSubagentFirstStartedAt(saAt(1)), apitest.WithSubagentStartedAt(saAt(1)),
		apitest.WithSubagentOpenedAt(saAt(1)), apitest.WithSubagentActivityAt(saAt(1)),
	}, extra...)
}

func saStoppedSeed(source string, extra ...apitest.SubagentOption) []apitest.SubagentOption {
	return saSaturated(append([]apitest.SubagentOption{
		apitest.WithSubagentState(store.SubagentStateStopped), apitest.WithSubagentCloseReason(store.SubagentCloseStopEvent),
		apitest.WithSubagentStoppedAt(saAt(2)), apitest.WithSubagentStoppedAtSource(source), apitest.WithSubagentActivityAt(saAt(2)),
	}, extra...)...)
}

// The events under test, each at saE, with every payload value different from
// the saturated seed's.
func saStartEv() store.SubagentLifecycleEvent {
	return store.SubagentLifecycleEvent{SubagentID: saSub, SessionID: "sess-ev", Type: "start-type", ReceivedMs: saE}
}

func saStopEv(supersedes bool) store.SubagentLifecycleEvent {
	return store.SubagentLifecycleEvent{SubagentID: saSub, SupersedesBlockedStop: supersedes, ReceivedMs: saE}
}

func saDetailsEv(outcome store.SubagentLaunchOutcome) store.SubagentLaunchDetails {
	return store.SubagentLaunchDetails{ToolUseID: "toolu-ev", SubagentID: saSub, SessionID: "sess-ev", Type: "details-type",
		Description: "event description", Model: "event-model", Outcome: outcome, ReceivedMs: saE}
}

type saEvent struct {
	name  string
	apply func(f *saStore)
}

var saEvents = []saEvent{
	{"start", func(f *saStore) { f.start(saStartEv()) }},
	{"stop", func(f *saStore) { f.stop(saStopEv(false)) }},
	{"stop superseding a blocked stop", func(f *saStore) { f.stop(saStopEv(true)) }},
	{"background launch result", func(f *saStore) { f.details(saDetailsEv(store.SubagentLaunchBackgroundStarted)) }},
	{"foreground launch result", func(f *saStore) { f.details(saDetailsEv(store.SubagentLaunchForegroundFinished)) }},
	{"other launch result", func(f *saStore) { f.details(saDetailsEv(store.SubagentLaunchOther)) }},
}
