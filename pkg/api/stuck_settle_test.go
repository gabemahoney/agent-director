package api_test

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestPermissionStuckSettlementAndActualTransitionTime(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	for _, delay := range []time.Duration{0, 5 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			e := newRelayEnv(t, store.ProcessIdentity{})
			e.now = stuckAt
			stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-30*time.Second), time.Time{}, time.Time{}, ""))
			c := stuckClient(t, e, delay.Milliseconds(), 0)
			mark := trailMark(t)
			if delay > 0 {
				// The candidate must be committed here: its start is the settle clock.
				observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
				e.now = stuckAt.Add(delay - time.Millisecond)
				assertStuckState(t, c, e.id, store.StateCheckPermission)
				// A delayed first committing read must stamp itself, not the earlier deadline.
				e.now = stuckAt.Add(delay + time.Hour)
			}
			entered := assertStuckState(t, c, e.id, "stuck")
			if entered.Since != e.now.Format("2006-01-02T15:04:05.000Z") {
				t.Errorf("entry backdated: %+v", entered)
			}
			assertStuckState(t, c, e.id, "stuck")
			entryTime := e.now
			stuckFacts(t, e, apitest.PermissionAnswer(store.PaneAnswerOutside))
			if delay > 0 {
				// The clearing candidate is committed while the stuck display holds.
				got := observeStatus(t, c, e.id, "stuck", apitest.VersionAdvanced)
				if got.StuckInfo != entered.StuckInfo {
					t.Errorf("clearing details changed: %+v", got)
				}
				e.now = e.now.Add(delay - time.Millisecond)
				assertStuckState(t, c, e.id, "stuck")
				e.now = e.now.Add(time.Millisecond)
			}
			observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
			var events []map[string]any
			for _, ev := range readAPITrailLines(t)[mark:] {
				if ev["instance_id"] != e.id {
					continue
				}
				if ev["event"] == "ad.spawn.state_transition" {
					t.Errorf("report impersonated raw transition: %v", ev)
				}
				if ev["event"] == "ad.spawn.stuck_transition" {
					events = append(events, ev)
				}
			}
			if len(events) != 2 {
				t.Fatalf("events=%v; want entry/exit once", events)
			}
			for i, want := range []struct {
				prior, next string
				at          time.Time
			}{{store.StateCheckPermission, "stuck", entryTime}, {"stuck", store.StateCheckPermission, e.now}} {
				ev := events[i]
				if ev["prior_state"] != want.prior || ev["new_state"] != want.next || ev["stuck_reason"] != "permission_unanswered" || ev["ts"] != want.at.Format("2006-01-02T15:04:05.000Z") {
					t.Errorf("transition=%v; want %+v", ev, want)
				}
			}
		})
	}
}

func TestPermissionStuckRepresentativeContinuity(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt
	facts := apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, "")
	stuckFacts(t, e, facts, apitest.PermissionToken(storefix.TestRequestTokenC))
	// Insert lexical B before A; insertion order must not decide the tie.
	for _, tok := range []string{storefix.TestRequestTokenB, storefix.TestRequestTokenA} {
		if _, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionToken(tok), facts); err != nil {
			t.Fatal(err)
		}
	}
	c := stuckClient(t, e, 1000, 0)
	mark := trailMark(t)
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	// Removing the representative during the candidate must not restart settling.
	a, err := e.s.GetPermissionRequest(e.id, storefix.TestRequestTokenA)
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetPermissionFacts(e.dbPath, a.RequestID, apitest.PermissionProof(stuckAt)); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
	first := observeStatus(t, c, e.id, "stuck", apitest.VersionAdvanced)
	if first.RequestToken != storefix.TestRequestTokenB {
		t.Fatalf("tie representative=%+v", first)
	}
	b, err := e.s.GetPermissionRequest(e.id, storefix.TestRequestTokenB)
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetPermissionFacts(e.dbPath, b.RequestID, apitest.PermissionAnswer(store.PaneAnswerOutside)); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Millisecond)
	next := observeStatus(t, c, e.id, "stuck", apitest.VersionAdvanced)
	if next.RequestToken != storefix.TestRequestTokenC || next.Since != first.Since {
		t.Errorf("replacement restarted episode: first=%+v next=%+v", first, next)
	}
	var n int
	for _, ev := range readAPITrailLines(t)[mark:] {
		if ev["instance_id"] == e.id && ev["event"] == "ad.spawn.stuck_transition" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("representative change emitted %d transitions", n)
	}
}

func TestPermissionStuckCandidateCanceledByInterveningTurn(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := stuckClient(t, e, 1000, 0)
	mark := trailMark(t)
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	// No state read occurs during the authoritative move out and back.
	for _, state := range []string{store.StateWaiting, store.StateCheckPermission} {
		if err := seedAgentState(e.s, e.dbPath, e.id, state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, "")); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetSettle(e.dbPath, e.id, store.StateCheckPermission, 0); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	// The new interval starts its own candidate at this read.
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	for _, ev := range readAPITrailLines(t)[mark:] {
		if ev["instance_id"] == e.id && ev["event"] == "ad.spawn.stuck_transition" {
			t.Errorf("canceled candidate emitted %v", ev)
		}
	}
	e.now = e.now.Add(time.Second)
	assertStuckState(t, c, e.id, "stuck")
}

// An authoritative write that ends the displayed episode records the exit at
// its own commit time, not at a later read: a terminal write and the
// find-missing repair of a check_permission row nothing holds any more.
func TestPermissionStuckTerminalExitIsImmediate(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	for _, tc := range []struct {
		name, state string
		write       func(*testing.T, *relayEnv)
	}{
		{"ended", store.StateEnded, func(t *testing.T, e *relayEnv) {
			if err := seedAgentState(e.s, e.dbPath, e.id, store.StateEnded); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing", store.StateMissing, func(t *testing.T, e *relayEnv) {
			row, err := e.s.GetSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			if _, res, err := e.s.MarkMissingIfSameLife(e.id, row.Snapshot); err != nil || res != store.CondApplied {
				t.Fatalf("missing=%v %v", res, err)
			}
		}},
		{"repair", store.StateWorking, func(t *testing.T, e *relayEnv) {
			// Nothing holds the row once its only request is proven gone.
			stuckFacts(t, e, apitest.PermissionProof(stuckAt))
			row, err := e.s.GetSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			if state, res, err := e.s.RepairCheckPermissionIfSameLife(e.id, row.Snapshot); err != nil || res != store.CondApplied || state != store.StateWorking {
				t.Fatalf("repair=%q %v %v", state, res, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelayEnv(t, store.ProcessIdentity{})
			e.now = stuckAt
			stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
			c := stuckClient(t, e, 0, 0)
			assertStuckState(t, c, e.id, "stuck")
			c = stuckClient(t, e, settleMax, 0)
			before, err := e.s.GetSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			mark := trailMark(t)
			tc.write(t, e)
			// The write itself ended the episode: no read has happened yet.
			after, err := e.s.GetSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			if after.StuckObservation != "" || after.StuckVersion <= before.StuckVersion || after.PermissionEpoch != before.PermissionEpoch+1 {
				t.Errorf("write left the episode behind: before=%+v after=%+v", before, after)
			}
			assertStuckState(t, c, e.id, tc.state)
			assertStuckState(t, c, e.id, tc.state)
			_, stamp, err := apitest.ReadSettle(e.dbPath, e.id)
			if err != nil {
				t.Fatal(err)
			}
			var events []map[string]any
			for _, ev := range readAPITrailLines(t)[mark:] {
				if ev["instance_id"] == e.id && ev["event"] == "ad.spawn.stuck_transition" {
					events = append(events, ev)
				}
			}
			if len(events) != 1 {
				t.Fatalf("immediate exit attempts=%v", events)
			}
			ev := events[0]
			if ev["prior_state"] != "stuck" || ev["new_state"] != tc.state || ev["stuck_reason"] != "permission_unanswered" || ev["ts"] != time.UnixMilli(stamp).UTC().Format("2006-01-02T15:04:05.000Z") {
				t.Errorf("exit must use authoritative commit time: %v stamp=%d", ev, stamp)
			}
		})
	}
}

func TestPermissionStuckFamilyRemainsBinaryAndChildLocal(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	if _, err := apitest.SeedSpawn(e.dbPath, "parent", store.StateWaiting, "", "off", "", false); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedParentChild(e.dbPath, "parent", e.id); err != nil {
		t.Fatal(err)
	}
	c := stuckClient(t, e, 0, 0)
	child := assertStuckState(t, c, e.id, "stuck")
	parent := assertStuckState(t, c, "parent", store.StateWaiting)
	if child.FamilyState != store.StateWorking || parent.FamilyState != store.StateWorking {
		t.Errorf("family must count live stuck as working: child=%+v parent=%+v", child, parent)
	}
}

func TestPermissionStuckFamilySettlesIndependently(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := stuckClient(t, e, 0, 0)
	assertStuckState(t, c, e.id, "stuck")
	if err := apitest.SetFamilyHistory(e.dbPath, e.id, store.StateWorking, store.StateWaiting, stuckAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	c = stuckClient(t, e, 1000, 0)
	before := assertStuckState(t, c, e.id, "stuck")
	if before.FamilyState != store.StateWaiting {
		t.Fatalf("own stuck bypassed family settle: %+v", before)
	}
	e.now = e.now.Add(time.Second - time.Millisecond)
	if row := assertStuckState(t, c, e.id, "stuck"); row.FamilyState != store.StateWaiting {
		t.Fatalf("family settled early: %+v", row)
	}
	e.now = e.now.Add(time.Millisecond)
	after := assertStuckState(t, c, e.id, "stuck")
	if after.FamilyState != store.StateWorking || after.Since != before.Since {
		t.Fatalf("independent family settlement=%+v", after)
	}
}

func TestPermissionStuckEarliestFallbackPrecedesLexicalToken(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = stuckAt
	stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-24*time.Hour), time.Time{}, stuckAt.Add(-30*time.Second), time.Time{}, time.Time{}, ""))
	_, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionToken(storefix.TestRequestTokenC), apitest.PermissionTimes(stuckAt.Add(-time.Minute), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	if err != nil {
		t.Fatal(err)
	}
	c := stuckClient(t, e, 0, 0)
	got := assertStuckState(t, c, e.id, "stuck")
	if got.RequestToken != storefix.TestRequestTokenC || got.ToolName != "Read" {
		t.Fatalf("creation age or lexical order displaced earlier fallback: %+v", got)
	}
}
