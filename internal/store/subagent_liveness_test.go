package store_test

// A row that comes back after a liveness close (SR-8.3, b.f5h): the ended write
// and the find-missing mark close the row's running entries as lost, and
// neither the hook that brings the row back (it carries no subagent event) nor
// a later stop or start treats the close as more than it is. A closed entry
// stays closed until an event names it: a SubagentStop writes stopped (never
// running), a SubagentStart reopens it in the row's current launch, and a
// main-agent Stop's listing or a launch result leaves it not running. New work
// of the revived row (a start for a new id, a launch request) is recorded. The
// writes' own close is the close mix of subagent_close_helpers_test.go,
// asserted in hook_gate_test.go and find_missing_writes_test.go; the move's and
// the reset's coming back are in resume_move_test.go and reuse_test.go; the
// events against a lost entry as such are subagent_transitions_test.go's.

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// livenessClosers are the writes that close a live row's entries and leave its
// subagent_tracking and launch_token as they were.
var livenessClosers = []struct {
	name  string
	close func(f *saStore)
}{
	{"ended write", func(f *saStore) {
		if got := storefix.ApplyAgentHook(f.t, f.s, f.id, "SessionEnd", "sess-sa"); !got.Applied {
			f.t.Fatalf("SessionEnd = %+v; want applied", got)
		}
	}},
	{"find-missing mark", func(f *saStore) {
		if _, res, err := f.s.MarkMissingIfSameLife(f.id, rvExamine(f.t, f.v5Store, f.id).Snapshot); err != nil || res != store.CondApplied {
			f.t.Fatalf("MarkMissingIfSameLife = %v, %v; want CondApplied", res, err)
		}
	}},
}

// closedAndBack is a row whose close mix a liveness close turned lost and whose
// agent then came back with a UserPromptSubmit: the mix, the entries as the
// close left them, and a receipt time past every time the test wrote.
type closedAndBack struct {
	*saStore
	mix    closeMix
	closed apitest.SubagentRows
	later  int64
}

func newClosedAndBack(t *testing.T, closer func(f *saStore)) closedAndBack {
	t.Helper()
	f := newSAStore(t, store.StateWaiting)
	mix := f.seedCloseMix(f.id)
	lo, hi := cmWindow(func() { closer(f) })
	f.assertMixClosedInWindow(mix, lo, hi)
	closed := f.subagentRowsOf(f.id)
	if got := storefix.ApplyAgentHook(t, f.s, f.id, "UserPromptSubmit", "sess-sa"); !got.Applied {
		t.Fatalf("UserPromptSubmit = %+v; want applied", got)
	}
	if got := f.rawColumns(f.id).State; got != store.StateWorking {
		t.Fatalf("state after the row's agent came back = %v; want %q", got, store.StateWorking)
	}
	return closedAndBack{saStore: f, mix: mix, closed: closed, later: time.Now().UnixMilli() + 60_000}
}

// TestRowComesBackWithEntriesStillClosed checks the hook that brings a closed
// row back changes no entry and no claimable launch.
func TestRowComesBackWithEntriesStillClosed(t *testing.T) {
	for _, c := range livenessClosers {
		t.Run(c.name, func(t *testing.T) {
			f := newClosedAndBack(t, c.close)
			if got := f.subagentRowsOf(f.id); !reflect.DeepEqual(got, f.closed) {
				t.Errorf("entries and launches after the row came back:\n got  %+v\n want %+v", got, f.closed)
			}
			f.assertBystanderKept(f.mix)
		})
	}
}

// TestStopOfClosedEntryWritesStopped checks a SubagentStop for an entry a
// liveness close turned lost writes it stopped by the stop event, never running,
// and changes no other entry.
func TestStopOfClosedEntryWritesStopped(t *testing.T) {
	for _, c := range livenessClosers {
		t.Run(c.name, func(t *testing.T) {
			f := newClosedAndBack(t, c.close)
			f.stop(store.SubagentLifecycleEvent{SubagentID: cmRecord, ReceivedMs: f.later})
			got, ok := f.entryOf(f.id, cmRecord)
			if !ok {
				t.Fatalf("no entry %s after the stop", cmRecord)
			}
			saCheckInvariants(t, got)
			if got.State != store.SubagentStateStopped || got.CloseReason != store.SubagentCloseStopEvent || got.StoppedAt != f.later {
				t.Errorf("%s after the stop: state %v, close_reason %v, stopped_at %v; want stopped, stop_event, %d",
					cmRecord, got.State, got.CloseReason, got.StoppedAt, f.later)
			}
			after := f.subagentRowsOf(f.id)
			for i, e := range after.Entries {
				if e.SubagentID != cmRecord && !reflect.DeepEqual(e, f.closed.Entries[i]) {
					t.Errorf("entry %s changed by another entry's stop:\n before %+v\n after  %+v", e.SubagentID, f.closed.Entries[i], e)
				}
			}
		})
	}
}

// TestStartOfClosedEntryReopensIt checks a SubagentStart for an entry a liveness
// close turned lost reopens it as running in the row's current launch, with no
// close recorded, and touches no other entry or claimable launch.
func TestStartOfClosedEntryReopensIt(t *testing.T) {
	for _, c := range livenessClosers {
		t.Run(c.name, func(t *testing.T) {
			f := newClosedAndBack(t, c.close)
			token, _ := f.token().(string)
			if token == "" {
				t.Fatalf("the row has no launch token; the reopen check would be vacuous")
			}
			f.assertStartReopens(f.id, token, f.later)
			f.assertBystanderKept(f.mix)
		})
	}
}

// TestListingOrLaunchResultLeavesClosedEntryNotRunning checks a main-agent Stop
// whose snapshot lists a closed entry, and a launch result naming it, never turn
// it running again (SR-8.3); the per-field effects are
// subagent_transitions_test.go's.
func TestListingOrLaunchResultLeavesClosedEntryNotRunning(t *testing.T) {
	events := []struct {
		name  string
		apply func(f closedAndBack)
	}{
		{"main-agent Stop listing it", func(f closedAndBack) { f.snapStop(f.later, saL(cmRecord, "")) }},
		{"foreground-finished launch result", func(f closedAndBack) {
			f.details(store.SubagentLaunchDetails{ToolUseID: "toolu-cm-fg", SubagentID: cmRecord, Outcome: store.SubagentLaunchForegroundFinished, ReceivedMs: f.later})
		}},
		{"background-started launch result", func(f closedAndBack) {
			f.details(store.SubagentLaunchDetails{ToolUseID: "toolu-cm-bg", SubagentID: cmRecord, Outcome: store.SubagentLaunchBackgroundStarted, ReceivedMs: f.later})
		}},
	}
	for _, c := range livenessClosers {
		for _, ev := range events {
			t.Run(c.name+"/"+ev.name, func(t *testing.T) {
				f := newClosedAndBack(t, c.close)
				ev.apply(f)
				got, ok := f.entryOf(f.id, cmRecord)
				if !ok {
					t.Fatalf("no entry %s after the event", cmRecord)
				}
				saCheckInvariants(t, got)
				if got.State == store.SubagentStateRunning {
					t.Errorf("%s after the event: state %v; want it not running", cmRecord, got.State)
				}
			})
		}
	}
}

// TestRevivedRowRecordsNewWork checks a revived row records a start for a new
// subagent id as a running observation in its current launch, and a launch
// request as a claimable launch of that launch (SR-8.3).
func TestRevivedRowRecordsNewWork(t *testing.T) {
	for _, c := range livenessClosers {
		t.Run(c.name, func(t *testing.T) {
			f := newClosedAndBack(t, c.close)
			token, _ := f.token().(string)
			if token == "" {
				t.Fatalf("the row has no launch token; the check would be vacuous")
			}

			f.start(store.SubagentLifecycleEvent{SubagentID: "cm-new", ReceivedMs: f.later})
			got, ok := f.entryOf(f.id, "cm-new")
			if !ok {
				t.Fatalf("no entry cm-new after the start")
			}
			saCheckInvariants(t, got)
			if got.State != store.SubagentStateRunning || got.Confirmed != int64(0) || got.LaunchToken != token {
				t.Errorf("cm-new after the start: state %v, confirmed %v, launch %v; want a running observation in launch %q", got.State, got.Confirmed, got.LaunchToken, token)
			}

			f.request(store.SubagentLaunchRequest{ToolUseID: "toolu-cm-new", Type: "cm-type", ReceivedMs: f.later})
			launches := f.rows().Launches
			if len(launches) != 1 || launches[0].ToolUseID != "toolu-cm-new" || launches[0].LaunchToken != token {
				t.Errorf("claimable launches after the request = %+v; want one, toolu-cm-new in launch %q", launches, token)
			}
		})
	}
}
