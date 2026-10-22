package api_test

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestNeedsIntelligenceSettlementAndActualTransitionTime(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	for _, delay := range []time.Duration{0, 5 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			e := newRelayEnv(t, store.ProcessIdentity{})
			e.now = displayAt
			requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-30*time.Second), time.Time{}, time.Time{}, ""))
			c := needsIntelligenceClient(t, e, delay.Milliseconds(), 0)
			mark := trailMark(t)
			if delay > 0 {
				// The candidate must be committed here: its start is the settle clock.
				observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
				e.now = displayAt.Add(delay - time.Millisecond)
				assertDisplayState(t, c, e.id, store.StateCheckPermission)
				// A delayed first committing read must stamp itself, not the earlier deadline.
				e.now = displayAt.Add(delay + time.Hour)
			}
			entered := assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
			if entered.Since != e.now.Format(displayTS) {
				t.Errorf("entry backdated: %+v", entered)
			}
			assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
			entryTime := e.now
			requestFacts(t, e, apitest.PermissionAnswer(store.PaneAnswerOutside))
			if delay > 0 {
				// The clearing candidate is committed while the display holds.
				got := observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
				if got.StateDetail != entered.StateDetail {
					t.Errorf("clearing details changed: %+v", got)
				}
				e.now = e.now.Add(delay - time.Millisecond)
				assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
				e.now = e.now.Add(time.Millisecond)
			}
			observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
			for _, ev := range readAPITrailLines(t)[mark:] {
				if ev["instance_id"] == e.id && ev["event"] == "ad.spawn.state_transition" {
					t.Errorf("report impersonated raw transition: %v", ev)
				}
			}
			events := displayEvents(t, mark, e.id)
			if len(events) != 2 {
				t.Fatalf("events=%v; want entry/exit once", events)
			}
			assertDisplayEvent(t, events[0], store.StateCheckPermission, api.StateNeedsIntelligence, entryTime)
			assertDisplayEvent(t, events[1], api.StateNeedsIntelligence, store.StateCheckPermission, e.now)
		})
	}
}

// A change of representative request updates request_token and tool_name
// without settling, a new since or a record (SR-2.1, SR-5.1).
func TestNeedsIntelligenceRepresentativeContinuity(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	facts := apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, "")
	requestFacts(t, e, facts, apitest.PermissionToken(storefix.TestRequestTokenC))
	// Insert lexical B before A; insertion order must not decide the tie.
	for _, tok := range []string{storefix.TestRequestTokenB, storefix.TestRequestTokenA} {
		if _, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionToken(tok), facts); err != nil {
			t.Fatal(err)
		}
	}
	c := needsIntelligenceClient(t, e, 1000, 0)
	mark := trailMark(t)
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	// Removing the representative during the candidate must not restart settling.
	a, err := e.s.GetPermissionRequest(e.id, storefix.TestRequestTokenA)
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetPermissionFacts(e.dbPath, a.RequestID, apitest.PermissionProof(displayAt)); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Second)
	first := observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
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
	next := observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
	if next.RequestToken != storefix.TestRequestTokenC || next.Since != first.Since {
		t.Errorf("replacement restarted episode: first=%+v next=%+v", first, next)
	}
	if events := displayEvents(t, mark, e.id); len(events) != 1 {
		t.Errorf("events=%v; want only the entry, none for the representative change", events)
	}
}

// An independent request admitted mid-episode, once displayed (settle 0) or
// while the entry candidate is pending (settle 5 s), keeps the episode: its
// deadline, since, representative and one entry. The admitted request has the
// lexically first token but the later fallback clock, and once the first is
// answered it carries the same episode on.
func TestNeedsIntelligenceAdmissionMidEpisode(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	timeout := time.Duration(config.DefaultNeedsIntelligenceTimeoutSeconds) * time.Second
	for _, delay := range []time.Duration{0, 5 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			e := newRelayEnv(t, store.ProcessIdentity{})
			e.now = displayAt
			requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-timeout), time.Time{}, time.Time{}, ""), apitest.PermissionToken(storefix.TestRequestTokenC))
			c := needsIntelligenceClient(t, e, delay.Milliseconds(), 0)
			mark := trailMark(t)
			entry := displayAt.Add(delay)
			keeps := func(got api.StatusResult) {
				t.Helper()
				if got.RequestToken != storefix.TestRequestTokenC || got.ToolName != "Bash" || got.Since != entry.Format(displayTS) {
					t.Errorf("admission moved the episode: %+v; want C/Bash since %s", got.StateDetail, entry.Format(displayTS))
				}
			}
			if delay == 0 {
				assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
				e.now = entry.Add(time.Second)
			} else {
				observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
				e.now = displayAt.Add(delay / 2)
			}
			admitted := e.now
			if _, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionToken(storefix.TestRequestTokenA), apitest.PermissionTimes(admitted, time.Time{}, admitted, time.Time{}, time.Time{}, "")); err != nil {
				t.Fatal(err)
			}
			if delay == 0 {
				// Committing the admitted, still unqualified request's clock keeps the displayed episode.
				keeps(observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced))
			} else {
				// Committing the admitted request's clock must not restart the candidate.
				observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
				e.now = entry.Add(-time.Millisecond)
				assertDisplayState(t, c, e.id, store.StateCheckPermission)
				e.now = entry
				assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
			}
			// Once the admitted request qualifies too, the earlier clock still decides.
			e.now = admitted.Add(timeout)
			keeps(assertDisplayState(t, c, e.id, api.StateNeedsIntelligence))
			first, err := e.s.GetPermissionRequest(e.id, storefix.TestRequestTokenC)
			if err != nil || !first.AwaitsAnswer() {
				t.Fatalf("first request no longer awaits an answer: %+v %v", first, err)
			}
			// Answering the first request hands the episode to the admitted one.
			if err := apitest.SetPermissionFacts(e.dbPath, first.RequestID, apitest.PermissionAnswer(store.PaneAnswerOutside)); err != nil {
				t.Fatal(err)
			}
			next := observeStatus(t, c, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced)
			if next.RequestToken != storefix.TestRequestTokenA || next.ToolName != "Read" || next.Since != entry.Format(displayTS) {
				t.Errorf("admitted request did not continue the episode: %+v; want A/Read since %s", next.StateDetail, entry.Format(displayTS))
			}
			events := displayEvents(t, mark, e.id)
			if len(events) != 1 {
				t.Fatalf("events=%v; want the entry once", events)
			}
			assertDisplayEvent(t, events[0], store.StateCheckPermission, api.StateNeedsIntelligence, entry)
		})
	}
}

func TestNeedsIntelligenceCandidateCanceledByInterveningTurn(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := needsIntelligenceClient(t, e, 1000, 0)
	mark := trailMark(t)
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	// No state read occurs during the authoritative move out and back.
	for _, state := range []string{store.StateWaiting, store.StateCheckPermission} {
		if err := seedAgentState(e.s, e.dbPath, e.id, state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, "")); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetSettle(e.dbPath, e.id, store.StateCheckPermission, 0); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	// The new interval starts its own candidate at this read.
	observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
	if events := displayEvents(t, mark, e.id); len(events) != 0 {
		t.Errorf("canceled candidate emitted %v", events)
	}
	e.now = e.now.Add(time.Second)
	assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
}

// An authoritative write that ends the displayed episode records the exit at
// its own commit time, not at a later read: a terminal write and the
// find-missing repair of a check_permission row nothing holds any more.
func TestNeedsIntelligenceTerminalExitIsImmediate(t *testing.T) {
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
			requestFacts(t, e, apitest.PermissionProof(displayAt))
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
			e.now = displayAt
			requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
			c := needsIntelligenceClient(t, e, 0, 0)
			assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
			c = needsIntelligenceClient(t, e, settleMax, 0)
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
			if after.DisplayObservation != "" || after.ObservationVersion <= before.ObservationVersion || after.PermissionEpoch != before.PermissionEpoch+1 {
				t.Errorf("write left the episode behind: before=%+v after=%+v", before, after)
			}
			assertDisplayState(t, c, e.id, tc.state)
			assertDisplayState(t, c, e.id, tc.state)
			_, stamp, err := apitest.ReadSettle(e.dbPath, e.id)
			if err != nil {
				t.Fatal(err)
			}
			events := displayEvents(t, mark, e.id)
			if len(events) != 1 {
				t.Fatalf("immediate exit attempts=%v", events)
			}
			// The exit must use the authoritative commit time.
			assertDisplayEvent(t, events[0], api.StateNeedsIntelligence, tc.state, time.UnixMilli(stamp))
		})
	}
}

func TestNeedsIntelligenceFamilyRemainsBinaryAndChildLocal(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	if _, err := apitest.SeedSpawn(e.dbPath, "parent", store.StateWaiting, "", "off", "", false); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedParentChild(e.dbPath, "parent", e.id); err != nil {
		t.Fatal(err)
	}
	c := needsIntelligenceClient(t, e, 0, 0)
	child := assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
	parent := assertDisplayState(t, c, "parent", store.StateWaiting)
	if child.FamilyState != store.StateWorking || parent.FamilyState != store.StateWorking {
		t.Errorf("family must count a live needs_intelligence member as working: child=%+v parent=%+v", child, parent)
	}
}

func TestNeedsIntelligenceFamilySettlesIndependently(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-time.Hour), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	c := needsIntelligenceClient(t, e, 0, 0)
	assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
	if err := apitest.SetFamilyHistory(e.dbPath, e.id, store.StateWorking, store.StateWaiting, displayAt.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	c = needsIntelligenceClient(t, e, 1000, 0)
	before := assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
	if before.FamilyState != store.StateWaiting {
		t.Fatalf("own display bypassed family settle: %+v", before)
	}
	e.now = e.now.Add(time.Second - time.Millisecond)
	if row := assertDisplayState(t, c, e.id, api.StateNeedsIntelligence); row.FamilyState != store.StateWaiting {
		t.Fatalf("family settled early: %+v", row)
	}
	e.now = e.now.Add(time.Millisecond)
	after := assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
	if after.FamilyState != store.StateWorking || after.Since != before.Since {
		t.Fatalf("independent family settlement=%+v", after)
	}
}

func TestNeedsIntelligenceEarliestFallbackPrecedesLexicalToken(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	e.now = displayAt
	requestFacts(t, e, apitest.PermissionTimes(displayAt.Add(-24*time.Hour), time.Time{}, displayAt.Add(-30*time.Second), time.Time{}, time.Time{}, ""))
	_, err := apitest.SeedPermissionRequest(e.dbPath, e.id, "Read", apitest.PermissionToken(storefix.TestRequestTokenC), apitest.PermissionTimes(displayAt.Add(-time.Minute), time.Time{}, displayAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
	if err != nil {
		t.Fatal(err)
	}
	c := needsIntelligenceClient(t, e, 0, 0)
	got := assertDisplayState(t, c, e.id, api.StateNeedsIntelligence)
	if got.RequestToken != storefix.TestRequestTokenC || got.ToolName != "Read" {
		t.Fatalf("creation age or lexical order displaced earlier fallback: %+v", got)
	}
}
