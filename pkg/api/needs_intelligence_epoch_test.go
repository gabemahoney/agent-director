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

// observationVersion is the committed observer version of id's row.
func observationVersion(t *testing.T, e *relayEnv) int64 {
	t.Helper()
	row, err := e.s.GetSpawn(e.id)
	if err != nil {
		t.Fatal(err)
	}
	return row.ObservationVersion
}

// committedNeedsIntelligence builds a relayEnv whose row is displayed
// needs_intelligence on request A, then returns it with the client. settle is
// the client's [state] settle_ms.
func committedNeedsIntelligence(t *testing.T, settle int64) (*relayEnv, *api.Client) {
	t.Helper()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := needsIntelligenceClient(t, e, settle, 0)
	if settle > 0 {
		observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
		e.now = e.now.Add(time.Duration(settle) * time.Millisecond)
	}
	observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
	return e, c
}

// A new permission interval qualifying for the displayed needs_intelligence
// never inherits its episode: the old one ends with its own exit at the read
// that detects the interval, without settling, and the new request starts a
// fresh candidate, with no since or token carried over. With no settle time
// both happen in one read; otherwise the row reads as its own state until the
// new candidate settles.
func TestNeedsIntelligenceNewIntervalEndsOldEpisodeAndStartsFresh(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	for _, settle := range []int64{0, 1000} {
		t.Run(fmt.Sprintf("settle_%dms", settle), func(t *testing.T) {
			e, c := committedNeedsIntelligence(t, settle)
			first := e.now
			old := observeStatus(t, c, e.id, api.StateNeedsIntelligence, nil)
			if old.RequestToken != storefix.TestRequestTokenA || old.Since != first.Format(displayTS) {
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
				apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, "")); err != nil {
				t.Fatal(err)
			}
			mark := trailMark(t)
			e.now = first.Add(time.Hour)
			secondRead := e.now
			if settle == 0 {
				got := observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
				if got.RequestToken != storefix.TestRequestTokenB || got.ToolName != "Read" || got.Since != secondRead.Format(displayTS) {
					t.Fatalf("new episode inherited the old one: old=%+v new=%+v", old, got)
				}
				events := displayEvents(t, mark, e.id)
				if len(events) != 2 {
					t.Fatalf("events=%v; want one exit and one entry", events)
				}
				assertDisplayEvent(t, events[0], api.StateNeedsIntelligence, store.StateCheckPermission, secondRead)
				assertDisplayEvent(t, events[1], store.StateCheckPermission, api.StateNeedsIntelligence, secondRead)
				return
			}
			// The old episode ends at once, the new one only after its settle time.
			observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
			events := displayEvents(t, mark, e.id)
			if len(events) != 1 {
				t.Fatalf("events=%v; want only the old episode's exit", events)
			}
			assertDisplayEvent(t, events[0], api.StateNeedsIntelligence, store.StateCheckPermission, secondRead)
			e.now = e.now.Add(time.Duration(settle)*time.Millisecond - time.Millisecond)
			observeStatus(t, c, e.id, store.StateCheckPermission, nil)
			e.now = e.now.Add(time.Millisecond)
			got := observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
			if got.RequestToken != storefix.TestRequestTokenB || got.Since != e.now.Format(displayTS) {
				t.Fatalf("new episode inherited the old one: old=%+v new=%+v", old, got)
			}
			events = displayEvents(t, mark, e.id)
			if len(events) != 2 {
				t.Fatalf("events=%v; want one exit and one entry", events)
			}
			assertDisplayEvent(t, events[1], store.StateCheckPermission, api.StateNeedsIntelligence, e.now)
		})
	}
}

// Leaving check_permission by a raw state change, with no new interval
// qualifying, still settles out like any other state: the display holds until
// the settle time.
func TestNeedsIntelligenceRawChangeOutOfCheckPermissionSettlesOut(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	e, c := committedNeedsIntelligence(t, 1000)
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
	observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
	e.now = e.now.Add(999 * time.Millisecond)
	observeStatus(t, c, e.id, api.StateNeedsIntelligence, nil)
	if events := displayEvents(t, mark, e.id); len(events) != 0 {
		t.Fatalf("exit before the settle time: %v", events)
	}
	e.now = e.now.Add(time.Millisecond)
	observeStatus(t, c, e.id, store.StateWaiting, apitest.VersionAdvanced)
	events := displayEvents(t, mark, e.id)
	if len(events) != 1 {
		t.Fatalf("events=%v; want the exit once", events)
	}
	assertDisplayEvent(t, events[0], api.StateNeedsIntelligence, store.StateWaiting, e.now)
}

// While the raw check_permission state is still settling in, every reader
// reports the state it settled on, and the entry record's prior state is the
// state the row reports when the entry settles.
func TestNeedsIntelligenceEntrySettleReadersAgreeWithReportedState(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := needsIntelligenceClient(t, e, 1000, 0)
	// The raw state is check_permission; it has held for no time, so it
	// reports the state it replaced until it has held the settle time.
	if err := apitest.SetSettle(e.dbPath, e.id, store.StateWorking, e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	mark := trailMark(t)
	observeStatus(t, c, e.id, store.StateWorking, apitest.VersionAdvanced)
	got, err := c.Get(e.id)
	if err != nil || got.State != store.StateWorking || got.StateDetail != (api.StateDetail{}) {
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
			if row.State != store.StateWorking {
				t.Errorf("List row=%+v; want the reported state", row)
			}
		}
	}
	if !seen {
		t.Fatalf("List lost the row: %+v", listed)
	}
	if events := displayEvents(t, mark, e.id); len(events) != 0 {
		t.Fatalf("events before the entry settled: %v", events)
	}
	// By the settle time the raw state reports as itself, and that is the prior.
	e.now = e.now.Add(time.Second)
	entered := observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
	if entered.Since != e.now.Format(displayTS) {
		t.Errorf("entry=%+v; want the entry read's time", entered)
	}
	events := displayEvents(t, mark, e.id)
	if len(events) != 1 {
		t.Fatalf("events=%v; want the entry once", events)
	}
	assertDisplayEvent(t, events[0], store.StateCheckPermission, api.StateNeedsIntelligence, e.now)
}

// A read that would change nothing writes nothing: not while a candidate
// waits for its settle time, and not while an episode is displayed. A due
// transition still commits at its boundary.
func TestNeedsIntelligenceReadsThatChangeNothingWriteNothing(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := needsIntelligenceClient(t, e, 1000, 0)
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	version := observationVersion(t, e)
	// Waiting candidate: repeated and later reads before the boundary.
	for _, offset := range []time.Duration{0, 1, 500 * time.Millisecond, 999 * time.Millisecond} {
		e.now = displayAt.Add(offset)
		observeStatus(t, c, e.id, store.StateCheckPermission, nil)
		if got := observationVersion(t, e); got != version {
			t.Fatalf("read at +%s wrote: observation_version %d -> %d", offset, version, got)
		}
	}
	// The boundary commits the entry.
	e.now = displayAt.Add(time.Second)
	observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
	if got := observationVersion(t, e); got != version+1 {
		t.Fatalf("entry observation_version %d; want %d", got, version+1)
	}
	version++
	// Displayed episode: later reads, and every other reader, write nothing.
	for _, offset := range []time.Duration{0, 1, time.Minute, time.Hour} {
		e.now = displayAt.Add(time.Second + offset)
		observeStatus(t, c, e.id, api.StateNeedsIntelligence, nil)
		if row, err := c.Get(e.id); err != nil || row.State != api.StateNeedsIntelligence {
			t.Fatalf("Get=%+v %v", row, err)
		}
		if got := observationVersion(t, e); got != version {
			t.Fatalf("read of a displayed episode at +%s wrote: observation_version %d -> %d", offset, version, got)
		}
	}
}
