package hook_test

// Stop snapshots and SubagentStop listings through hook.Handle (b.xjn; SRD
// SR-15.3, SR-5.1, SR-7.4, SR-7.5, SR-7.6, SR-3.2, N3, N13, Epic AC 3): a main
// agent Stop's background_tasks creates, confirms and fills entries and closes
// the unlisted background ones past the grace; a SubagentStop's listing only
// confirms. They show the wiring on the virtual clock; the store's matrix and
// the grace edges (exactly 2 s, +1 ms) are internal/store's. Every hook runs with
// empty stdout (fireFrom asserts it).

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// listedRecord is the expected record a snapshot or a listing makes of a
// subagent it alone saw: confirmed, running, background, the listed type (none
// when typ is empty), the Stop's session and nothing else.
func listedRecord(id, typ string, ms int64) wantEntry {
	w := wantEntry{id: id, state: "running", confirmed: true, runMode: "background", opened: ms}
	if typ != "" {
		w.typ = typ
	}
	return w
}

// lostAbsent is w closed by a snapshot that did not list it, at ms.
func (w wantEntry) lostAbsent(ms int64) wantEntry {
	w.state, w.lost, w.closed = "lost", ms, "absent_from_snapshot"
	return w
}

// b8Teammate is the id of the teammate entry of B8's background_tasks.
func b8Teammate(t *testing.T) string {
	t.Helper()
	return fixtureValue(t, fxMainStop, "background_tasks").([]any)[1].(map[string]any)["id"].(string)
}

// stopListing is a main-agent Stop (B8) listing running subagents ids.
func stopListing(t *testing.T, ids ...string) []byte {
	var entries []any
	for _, id := range ids {
		entries = append(entries, taskEntry(id, subType, nil))
	}
	return variant(t, fxMainStop, tasks(entries...))
}

// TestSubagentSnapshotMissedLaunch: a Stop's snapshot alone (B8) makes the record
// of a background launch whose own hooks were missed (N13): confirmed, running,
// background, typed by the listing and with no description, times, model,
// parent or tool_use_id; the teammate entry beside it makes none. The launch
// result (B5) later sets those fields.
func TestSubagentSnapshotMissedLaunch(t *testing.T) {
	r := newSubRow(t, subRowID)
	created := listedRecord(subAgentID, subType, at(1))
	filledLater := filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))
	filledLater.opened = at(1)
	r.script(t, []step{
		{name: "B8 creates the record", payload: variant(t, fxMainStop), at: at(1), upsert: "updated",
			entries: []wantEntry{created}},
		{name: "B5 fills it", payload: variant(t, fxResult), at: at(2), entries: []wantEntry{filledLater}},
	})
	for _, e := range r.rows().Entries {
		if e.SubagentID == b8Teammate(t) {
			t.Errorf("entry for the teammate entry's id %q; want none", e.SubagentID)
		}
	}
}

// TestSubagentListingNeverStoresDescription: no listing description, whether
// "(resumed)" (the probe's text for a resumed subagent) or the fixture's own, is
// stored by a Stop's snapshot or a SubagentStop's listing, for a record the
// listing made (null description) and for one a launch result described.
func TestSubagentListingNeverStoresDescription(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop bool // the listing is a SubagentStop's, not a main-agent Stop's
	}{
		{name: "main-agent Stop snapshot", stop: false},
		{name: "SubagentStop listing", stop: true},
	} {
		for _, text := range []string{"(resumed)", subListingDesc} {
			for _, described := range []bool{false, true} {
				name := tc.name + "/" + text + "/"
				if described {
					name += "record with a description"
				} else {
					name += "record without one"
				}
				t.Run(name, func(t *testing.T) {
					r := newSubRow(t, subRowID)
					listing := tasks(taskEntry(subAgentID, subType, map[string]any{"description": text}))
					fixture := fxMainStop
					if tc.stop {
						fixture = fxStop
					}
					payload := variant(t, fixture, listing)
					var want wantEntry
					var pre []step
					switch {
					case described:
						want = filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))
						pre = []step{
							{name: "B9", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
							{name: "B1", payload: variant(t, fxStart), at: at(2), entries: []wantEntry{running(subAgentID, subType, true, at(2))}},
							{name: "B5", payload: variant(t, fxResult), at: at(3), entries: []wantEntry{want}},
						}
						if tc.stop {
							want = want.stoppedAt(at(4))
						}
					case tc.stop:
						want = running(subAgentID, subType, false, at(1))
						want.confirmed, want.runMode = true, "background"
						pre = []step{{name: "B1 with no launch request", payload: variant(t, fxStart), at: at(1),
							entries: []wantEntry{running(subAgentID, subType, false, at(1))}}}
						want = want.stoppedAt(at(2))
					default:
						want = listedRecord(subAgentID, subType, at(1))
					}
					atMs := at(int64(len(pre)) + 1)
					r.script(t, append(pre, step{name: "the listing", payload: payload, at: atMs, entries: []wantEntry{want}}))
				})
			}
		}
	}
}

// TestSubagentStopListingConfirms: a SubagentStop's own listing confirms an
// observation no launch request or result confirmed and fills its run mode
// (SR-7.6): B1 with no launch request, then B3, is a stopped record in the
// verb's list.
func TestSubagentStopListingConfirms(t *testing.T) {
	t.Run("B3 confirms its own entry", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		confirmed := listedRecord(subAgentID, subType, at(1))
		confirmed.first, confirmed.started = at(1), at(1)
		confirmed = confirmed.stoppedAt(at(2))
		r.script(t, []step{
			{name: "B1 with no launch request", payload: variant(t, fxStart), at: at(1),
				entries: []wantEntry{running(subAgentID, subType, false, at(1))}},
			{name: "B3", payload: variant(t, fxStop), at: at(2), upsert: "updated", entries: []wantEntry{confirmed}},
		})
		if got := records(r); len(got) != 1 || got[0] != subAgentID {
			t.Errorf("records = %v; want [%s]", got, subAgentID)
		}
	})

	t.Run("a listing never confirms or creates another subagent's entry", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		observed := running(subAgentID, subType, false, at(1))
		other := running(subSecondAgentID, subType, false, at(2))
		r.script(t, []step{
			{name: "B1 observed", payload: variant(t, fxStart), at: at(1), entries: []wantEntry{observed}},
			{name: "second start observed", payload: secondStart(t), at: at(2), entries: []wantEntry{observed, other}},
			{name: "the second's stop lists the first and an unseen id", payload: variant(t, fxStop,
				setKey("agent_id", subSecondAgentID), tasks(taskEntry(subAgentID, subType, nil), taskEntry(subChildID, subType, nil))),
				at: at(3), entries: []wantEntry{observed.withListing(), other.stoppedAt(at(3))}},
		})
	})
}

// withListing is an observation after a listing named it: confirmed, with the
// run mode background.
func (w wantEntry) withListing() wantEntry {
	w.confirmed, w.runMode = true, "background"
	return w
}

// TestSubagentSnapshotShapes: a record a Stop's snapshot created and that is
// past the grace by the next Stop. A background_tasks that is not an array is no
// snapshot, so the record stays running; an array that lists no subagent (even
// by elements that look like one) is a snapshot, so the record turns lost and
// nothing is created.
func TestSubagentSnapshotShapes(t *testing.T) {
	created := listedRecord(subAgentID, subType, at(1))
	for _, tc := range []struct {
		name string
		edit payloadEdit
		want wantEntry // the record after the second Stop
	}{
		{"absent", delKey("background_tasks"), created},
		{"null", setKey("background_tasks", nil), created},
		{"an object", setKey("background_tasks", map[string]any{"id": subAgentID}), created},
		{"a string", setKey("background_tasks", subAgentID), created},
		{"empty array", emptyListing(), created.lostAbsent(at(2))},
		{"a string element", tasks(subAgentID), created.lostAbsent(at(2))},
		{"a null element", tasks(nil), created.lostAbsent(at(2))},
		{"a number element", tasks(7), created.lostAbsent(at(2))},
		{"a list element", tasks([]any{subAgentID}), created.lostAbsent(at(2))},
		{"a teammate entry with the record's id", tasks(taskEntry(subAgentID, subType, map[string]any{"type": "teammate"})), created.lostAbsent(at(2))},
		{"a status other than running", tasks(taskEntry(subAgentID, subType, map[string]any{"status": "completed"})), created.lostAbsent(at(2))},
		{"an empty id", tasks(taskEntry("", subType, nil)), created.lostAbsent(at(2))},
		{"a missing id", tasks(map[string]any{"type": "subagent", "status": "running", "agent_type": subType}), created.lostAbsent(at(2))},
		{"a numeric id", tasks(taskEntry(subAgentID, subType, map[string]any{"id": 7})), created.lostAbsent(at(2))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSubRow(t, subRowID)
			r.script(t, []step{
				{name: "B8 creates the record", payload: stopListing(t, subAgentID), at: at(1), entries: []wantEntry{created}},
				{name: "the Stop under test", payload: variant(t, fxMainStop, tc.edit), at: at(2), entries: []wantEntry{tc.want}},
			})
		})
	}

	t.Run("a numeric agent_type for an unseen id creates a record with no type", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.script(t, []step{
			{name: "listing with agent_type 7", payload: variant(t, fxMainStop,
				tasks(taskEntry(subAgentID, "", map[string]any{"agent_type": 7}))),
				at: at(1), entries: []wantEntry{listedRecord(subAgentID, "", at(1))}},
		})
	})
}

// TestSubagentSnapshotGrace: a record the snapshot created stays running through
// a Stop whose array omits it within the grace and turns lost, absent_from_snapshot
// at that Stop's receipt time, past the grace (the receipt time is the hook's
// HandleConfig.Start).
func TestSubagentSnapshotGrace(t *testing.T) {
	r := newSubRow(t, subRowID)
	created := listedRecord(subAgentID, subType, at(1))
	r.script(t, []step{
		{name: "B8 creates it", payload: stopListing(t, subAgentID), at: at(1), entries: []wantEntry{created}},
		{name: "omitting Stop within the grace", payload: mainStop(t), at: at(1) + 1000, entries: []wantEntry{created}},
		{name: "omitting Stop past the grace", payload: mainStop(t), at: at(1) + 5000,
			entries: []wantEntry{created.lostAbsent(at(1) + 5000)}},
	})
}

// TestSubagentSnapshotNeverClosesForegroundOrUnknown (Epic AC 3): a main-agent
// Stop with an empty listing past the grace leaves running a record whose run
// mode is not background: a running foreground subagent still unfilled (run_mode
// null) and one restarted after its completed result (run_mode foreground).
func TestSubagentSnapshotNeverClosesForegroundOrUnknown(t *testing.T) {
	t.Run("claimed and running, run mode unknown", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		claimed := running(subAgentID, subType, true, at(2))
		r.script(t, []step{
			{name: "B9", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
			{name: "B1", payload: variant(t, fxStart), at: at(2), entries: []wantEntry{claimed}},
			{name: "Stop with an empty listing", payload: mainStop(t), at: at(3), entries: []wantEntry{claimed}},
		})
	})

	t.Run("restarted after a completed foreground result", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		done := filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))
		done.runMode = "foreground"
		done = done.stoppedAt(at(3))
		restarted := done
		restarted.state, restarted.closed = "running", nil // a restart keeps stopped_at
		restarted.started, restarted.opened = at(5), at(5)
		r.script(t, []step{
			{name: "B9", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
			{name: "B1", payload: variant(t, fxStart), at: at(2), entries: []wantEntry{running(subAgentID, subType, true, at(2))}},
			{name: "B3 with an empty listing", payload: foregroundStop(t), at: at(3),
				entries: []wantEntry{running(subAgentID, subType, true, at(2)).stoppedAt(at(3))}},
			{name: "B11", payload: variant(t, fxCompleted), at: at(4), entries: []wantEntry{done}},
			{name: "B1 restarts it", payload: variant(t, fxStart), at: at(5), entries: []wantEntry{restarted}},
			{name: "Stop with an empty listing", payload: mainStop(t), at: at(6), entries: []wantEntry{restarted}},
		})
	})
}

// TestSubagentSnapshotAfterClear (Epic AC 3, N3): /clear ends the session and
// starts another, but the launch goes on, and the first Stop of the new session
// is a main-agent Stop whose snapshot lists one of the old session's background
// subagents: the omitted one is lost, the listed one stays running with its
// old session id.
func TestSubagentSnapshotAfterClear(t *testing.T) {
	r := newSubRow(t, subRowID)
	newSession := fixtureValue(t, "session-start-clear.json", "session_id").(string)
	first := filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))
	second := filled(r, subSecondAgentID, subType, subSecondDesc, subSecondModel, subSecondToolUseID, at(5))
	r.script(t, []step{
		{name: "first launch", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
		{name: "first start", payload: variant(t, fxStart), at: at(2),
			entries: []wantEntry{running(subAgentID, subType, true, at(2))}, launches: nil},
		{name: "first result", payload: variant(t, fxResult), at: at(3), entries: []wantEntry{first}},
		{name: "second launch", payload: secondRequest(t), at: at(4), entries: []wantEntry{first},
			launches: []wantLaunch{{subSecondToolUseID, subType, at(4)}}},
		{name: "second start", payload: secondStart(t), at: at(5),
			entries: []wantEntry{first, running(subSecondAgentID, subType, true, at(5))}},
		{name: "second result", payload: secondResult(t), at: at(6), entries: []wantEntry{first, second}},
		{name: "SessionEnd clear", payload: variant(t, "session-end-clear.json"), at: at(7), entries: []wantEntry{first, second}},
		{name: "SessionStart clear", payload: variant(t, "session-start-clear.json"), at: at(8), entries: []wantEntry{first, second}},
		{name: "new-session Stop lists the first only", payload: variant(t, fxMainStop, setKey("session_id", newSession),
			tasks(taskEntry(subAgentID, subType, nil))), at: at(9),
			entries: []wantEntry{first, second.lostAbsent(at(9))}},
	})
	if got := r.spawn().State; got == store.StateEnded {
		t.Errorf("row state = %v after /clear; want it still live", got)
	}
}
