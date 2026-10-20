package api_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const stuckTS = "2006-01-02T15:04:05.000Z"

// stuckEventsSince returns id's stuck transitions after mark, in order. Its
// callers are serial: the fixture row ids are reused by other tests.
func stuckEventsSince(t *testing.T, mark int, id string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, ev := range readAPITrailLines(t)[mark:] {
		if ev["instance_id"] == id && ev["event"] == "ad.spawn.stuck_transition" {
			events = append(events, ev)
		}
	}
	return events
}

func assertStuckEvent(t *testing.T, ev map[string]any, prior, next string, at time.Time) {
	t.Helper()
	if ev["prior_state"] != prior || ev["new_state"] != next || ev["stuck_reason"] != "permission_unanswered" || ev["ts"] != at.UTC().Format(stuckTS) {
		t.Errorf("transition=%v; want %s -> %s at %s", ev, prior, next, at.UTC().Format(stuckTS))
	}
}

// stuckVersion is the committed observer version of id's row.
func stuckVersion(t *testing.T, e *relayEnv) int64 {
	t.Helper()
	row, err := e.s.GetSpawn(e.id)
	if err != nil {
		t.Fatal(err)
	}
	return row.StuckVersion
}

// committedStuck builds a relayEnv whose row is displayed stuck on request A,
// then returns it with the client. settle is the client's [state] settle_ms.
func committedStuck(t *testing.T, settle int64) (*relayEnv, *api.Client) {
	t.Helper()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := stuckClient(t, e, settle, 0)
	if settle > 0 {
		observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
		e.now = e.now.Add(time.Duration(settle) * time.Millisecond)
	}
	observeStatus(t, c, e.id, "stuck", apitest.VersionAdvanced)
	return e, c
}

// A new permission interval never inherits the displayed episode: the old
// one ends with its own exit and the new request starts a fresh candidate,
// with no since or token carried over. With no settle time both happen in
// one read; otherwise the row reads as its own state until the new candidate
// settles.
func TestPermissionStuckNewIntervalEndsOldEpisodeAndStartsFresh(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	for _, settle := range []int64{0, 1000} {
		t.Run(fmt.Sprintf("settle_%dms", settle), func(t *testing.T) {
			e, c := committedStuck(t, settle)
			first := e.now
			old := observeStatus(t, c, e.id, "stuck", nil)
			if old.RequestToken != storefix.TestRequestTokenA || old.Since != first.Format(stuckTS) {
				t.Fatalf("setup episode=%+v", old)
			}
			// The agent leaves check_permission and comes back: a new interval.
			// No read happens in between.
			for _, state := range []string{store.StateWaiting, store.StateCheckPermission} {
				if err := seedAgentState(e.s, e.dbPath, e.id, state); err != nil {
					t.Fatal(err)
				}
			}
			if err := apitest.SetSettle(e.dbPath, e.id, store.StateCheckPermission, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionToken(storefix.TestRequestTokenB),
				apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, "")); err != nil {
				t.Fatal(err)
			}
			mark := trailMark(t)
			e.now = first.Add(time.Hour)
			secondRead := e.now
			if settle == 0 {
				got := observeStatus(t, c, e.id, "stuck", apitest.VersionAdvanced)
				if got.RequestToken != storefix.TestRequestTokenB || got.ToolName != "Read" || got.Since != secondRead.Format(stuckTS) {
					t.Fatalf("new episode inherited the old one: old=%+v new=%+v", old, got)
				}
				events := stuckEventsSince(t, mark, e.id)
				if len(events) != 2 {
					t.Fatalf("events=%v; want one exit and one entry", events)
				}
				assertStuckEvent(t, events[0], "stuck", store.StateCheckPermission, secondRead)
				assertStuckEvent(t, events[1], store.StateCheckPermission, "stuck", secondRead)
				return
			}
			// The old episode ends at once, the new one only after its settle time.
			observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
			events := stuckEventsSince(t, mark, e.id)
			if len(events) != 1 {
				t.Fatalf("events=%v; want only the old episode's exit", events)
			}
			assertStuckEvent(t, events[0], "stuck", store.StateCheckPermission, secondRead)
			e.now = e.now.Add(time.Duration(settle)*time.Millisecond - time.Millisecond)
			observeStatus(t, c, e.id, store.StateCheckPermission, nil)
			e.now = e.now.Add(time.Millisecond)
			got := observeStatus(t, c, e.id, "stuck", apitest.VersionAdvanced)
			if got.RequestToken != storefix.TestRequestTokenB || got.Since != e.now.Format(stuckTS) {
				t.Fatalf("new episode inherited the old one: old=%+v new=%+v", old, got)
			}
			events = stuckEventsSince(t, mark, e.id)
			if len(events) != 2 {
				t.Fatalf("events=%v; want one exit and one entry", events)
			}
			assertStuckEvent(t, events[1], store.StateCheckPermission, "stuck", e.now)
		})
	}
}

// Leaving check_permission by a raw state change, with no new interval, still
// settles out like any other state: the display holds until the settle time.
func TestPermissionStuckRawChangeOutOfCheckPermissionSettlesOut(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	e, c := committedStuck(t, 1000)
	mark := trailMark(t)
	// Waiting, not working: an open request holds the row against a move to
	// working (SR-5.1), and this test is about a state change that is not a
	// new interval.
	if err := seedAgentState(e.s, e.dbPath, e.id, store.StateWaiting); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetSettle(e.dbPath, e.id, store.StateWaiting, 0); err != nil {
		t.Fatal(err)
	}
	// The settle candidate starts at this read and the episode stays displayed.
	observeStatus(t, c, e.id, "stuck", apitest.VersionAdvanced)
	e.now = e.now.Add(999 * time.Millisecond)
	observeStatus(t, c, e.id, "stuck", nil)
	if events := stuckEventsSince(t, mark, e.id); len(events) != 0 {
		t.Fatalf("exit before the settle time: %v", events)
	}
	e.now = e.now.Add(time.Millisecond)
	observeStatus(t, c, e.id, store.StateWaiting, apitest.VersionAdvanced)
	events := stuckEventsSince(t, mark, e.id)
	if len(events) != 1 {
		t.Fatalf("events=%v; want the exit once", events)
	}
	assertStuckEvent(t, events[0], "stuck", store.StateWaiting, e.now)
}

// While the raw check_permission state is still settling in, every reader
// reports the state it settled on, and the entry event's prior state is the
// state the row reports when the entry settles.
func TestPermissionStuckEntrySettleReadersAgreeWithReportedState(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := stuckClient(t, e, 1000, 0)
	// The raw state is check_permission; it has held for no time, so it
	// reports the state it replaced until it has held the settle time.
	if err := apitest.SetSettle(e.dbPath, e.id, store.StateWorking, e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	mark := trailMark(t)
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	got, err := c.Get(e.id)
	if err != nil || got.State != store.StateWorking || got.StuckInfo != (api.StuckInfo{}) {
		t.Fatalf("Get=%+v %v; want the reported state", got, err)
	}
	listed, err := c.List(api.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, row := range listed.Spawns {
		if row.InstanceID == e.id {
			seen = true
			if row.State != store.StateWorking || row.StuckReason != "" {
				t.Errorf("List row=%+v; want the reported state", row)
			}
		}
	}
	if !seen {
		t.Fatalf("List lost the row: %+v", listed)
	}
	if events := stuckEventsSince(t, mark, e.id); len(events) != 0 {
		t.Fatalf("events before the entry settled: %v", events)
	}
	// By the settle time the raw state reports as itself, and that is the prior.
	e.now = e.now.Add(time.Second)
	entered := observeStatus(t, c, e.id, "stuck", apitest.VersionAdvanced)
	if entered.Since != e.now.Format(stuckTS) {
		t.Errorf("entry=%+v; want the entry read's time", entered)
	}
	events := stuckEventsSince(t, mark, e.id)
	if len(events) != 1 {
		t.Fatalf("events=%v; want the entry once", events)
	}
	assertStuckEvent(t, events[0], store.StateCheckPermission, "stuck", e.now)
}

// A read that would change nothing writes nothing: not while a candidate
// waits for its settle time, and not while an episode is displayed. A due
// transition still commits at its boundary.
func TestPermissionStuckReadsThatChangeNothingWriteNothing(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := stuckClient(t, e, 1000, 0)
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	version := stuckVersion(t, e)
	// Waiting candidate: repeated and later reads before the boundary.
	for _, offset := range []time.Duration{0, 1, 500 * time.Millisecond, 999 * time.Millisecond} {
		e.now = stuckAt.Add(offset)
		observeStatus(t, c, e.id, store.StateCheckPermission, nil)
		if got := stuckVersion(t, e); got != version {
			t.Fatalf("read at +%s wrote: stuck_version %d -> %d", offset, version, got)
		}
	}
	// The boundary commits the entry.
	e.now = stuckAt.Add(time.Second)
	observeStatus(t, c, e.id, "stuck", apitest.VersionAdvanced)
	if got := stuckVersion(t, e); got != version+1 {
		t.Fatalf("entry stuck_version %d; want %d", got, version+1)
	}
	version++
	// Displayed episode: later reads, and every other reader, write nothing.
	for _, offset := range []time.Duration{0, 1, time.Minute, time.Hour} {
		e.now = stuckAt.Add(time.Second + offset)
		observeStatus(t, c, e.id, "stuck", nil)
		if row, err := c.Get(e.id); err != nil || row.State != "stuck" {
			t.Fatalf("Get=%+v %v", row, err)
		}
		if got := stuckVersion(t, e); got != version {
			t.Fatalf("read of a displayed episode at +%s wrote: stuck_version %d -> %d", offset, version, got)
		}
	}
}
