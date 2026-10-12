package hook_test

// What the subagent capture reads from a payload and what it does with a
// payload that lacks a field or carries one of the wrong type (b.xjn; SRD
// SR-15.1, SR-5.3, SR-12.1, N7): the session id is the payload's, a field the
// rule needs and the payload lacks writes nothing, and a field of the wrong
// type is no value, never a failed hook.

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// TestSubagentCaptureSessionID: the entry's session id is the payload's
// session_id, and a start or stop without an agent_id (a main-agent hook of
// the same name) is the ordinary soft refresh and writes no entry.
func TestSubagentCaptureSessionID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"SubagentStart without agent_id", variant(t, fxStart, delKey("agent_id"))},
		{"SubagentStop without agent_id", variant(t, fxStop, delKey("agent_id"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSubRow(t, subRowID)
			before := r.spawn()
			f := r.fire(tc.payload, at(1))
			after := r.spawn()
			assertEntries(t, r)
			if f.upsert() != "updated" {
				t.Errorf("upsert_outcome = %v; want updated", f.upsert())
			}
			if after.RowVersion != before.RowVersion.(int64)+1 {
				t.Errorf("row_version = %v; want %v (one soft refresh)", after.RowVersion, before.RowVersion.(int64)+1)
			}
			if after.SessionID != subSessionID {
				t.Errorf("session_id = %v; want %q", after.SessionID, subSessionID)
			}
			if want := payloadTranscript(t, tc.payload); after.JSONLPath != want {
				t.Errorf("jsonl_path = %v; want %q", after.JSONLPath, want)
			}
		})
	}

	t.Run("an entry takes the payload's session id and the row's stays unset", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		other := "5d3f8a21-7c40-4b96-a1e5-0f2b9c6d4e87"
		r.fire(variant(t, fxStart), at(1))
		r.fire(variant(t, fxStart, setKey("session_id", other), setKey("agent_id", subChildID)), at(2))
		got := map[string]any{}
		for _, e := range r.rows().Entries {
			got[e.SubagentID.(string)] = e.SessionID
		}
		if got[subAgentID] != subSessionID || got[subChildID] != other {
			t.Errorf("entry session ids = %v; want %q for %s and %q for %s", got, subSessionID, subAgentID, other, subChildID)
		}
		if sid := r.spawn().SessionID; sid != nil {
			t.Errorf("row session_id = %v; want NULL (a subagent's hook never records the row's)", sid)
		}
	})
}

// TestSubagentCaptureMissingFields: a launch request without a tool_use_id opens
// no launch, so a start has nothing to claim; a launch result with neither a
// tool_use_id nor an agentId runs no entry transaction.
func TestSubagentCaptureMissingFields(t *testing.T) {
	t.Run("request without tool_use_id", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.script(t, []step{
			{name: "request", payload: variant(t, fxRequest, delKey("tool_use_id")), at: at(1)},
			{name: "start claims nothing", payload: variant(t, fxStart), at: at(2),
				entries: []wantEntry{running(subAgentID, subType, false, at(2))}},
			{name: "result confirms it", payload: variant(t, fxResult), at: at(3),
				entries: []wantEntry{filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))}},
		})
	})

	t.Run("request without subagent_type", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.script(t, []step{
			{name: "request", payload: variant(t, fxRequest, delKey("tool_input.subagent_type")), at: at(1)},
		})
	})

	t.Run("result with neither tool_use_id nor agentId", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		leftover := wantLaunch{subToolUseID, subType, at(1)}
		r.script(t, []step{
			{name: "request", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{leftover}},
			{name: "result", payload: variant(t, fxResult, delKey("tool_use_id"), delKey("tool_response.agentId")), at: at(2),
				upsert: "updated", launches: []wantLaunch{leftover}},
		})
	})

	t.Run("result without agentId closes its launch and makes no record", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.script(t, []step{
			{name: "request", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
			{name: "result", payload: variant(t, fxResult, delKey("tool_response.agentId")), at: at(2)},
		})
	})
}

// TestSubagentCaptureWrongTypes: a field of the wrong type is no value (the
// subagent reader is lenient and never fails the hook), except where the event
// classification itself needs the field, where it is the classify failure the
// ordinary hooks already have: one log line, nothing written.
func TestSubagentCaptureWrongTypes(t *testing.T) {
	t.Run("numeric agent_id fails classification", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.fire(variant(t, fxRequest), at(1))
		before := r.spawn()
		f := r.fire(variant(t, fxStart, setKey("agent_id", 7)), at(2))
		if !strings.Contains(f.Log, "hook: classify") || f.LogLines != 1 {
			t.Errorf("log = %q; want one hook: classify line", f.Log)
		}
		if after := r.spawn(); after != before {
			t.Errorf("row after = %+v; want unchanged %+v", after, before)
		}
		assertEntries(t, r)
		assertLaunches(t, r, wantLaunch{subToolUseID, subType, at(1)})
	})

	t.Run("numeric agent_type is no type, so the start claims nothing", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.script(t, []step{
			{name: "request", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
			{name: "start", payload: variant(t, fxStart, setKey("agent_type", 7)), at: at(2),
				entries:  []wantEntry{running(subAgentID, "", false, at(2))},
				launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
			{name: "result confirms and types it", payload: variant(t, fxResult), at: at(3),
				entries: []wantEntry{filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))}},
		})
	})

	t.Run("a tool_response that is not an object is absent", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		started := running(subAgentID, subType, true, at(2))
		r.script(t, []step{
			{name: "request", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
			{name: "start", payload: variant(t, fxStart), at: at(2), entries: []wantEntry{started}},
			{name: "string response", payload: variant(t, fxResult, setKey("tool_response", "done")), at: at(3),
				entries: []wantEntry{started}},
			{name: "list response", payload: variant(t, fxResult, setKey("tool_response", []any{"done"})), at: at(4),
				entries: []wantEntry{started}},
		})
	})

	t.Run("a tool_input that is not an object is absent", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		r.script(t, []step{
			{name: "string input", payload: variant(t, fxRequest, setKey("tool_input", "go")), at: at(1)},
			{name: "list input", payload: variant(t, fxRequest, setKey("tool_input", []any{1})), at: at(2)},
		})
	})

	t.Run("a tool_use_id that is not a string fails classification", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		before := r.spawn()
		f := r.fire(variant(t, fxRequest, setKey("tool_use_id", 7)), at(1))
		if !strings.Contains(f.Log, "hook: classify") {
			t.Errorf("log = %q; want a hook: classify line", f.Log)
		}
		if after := r.spawn(); after != before {
			t.Errorf("row after = %+v; want unchanged %+v", after, before)
		}
		assertLaunches(t, r)
	})
}

// TestSubagentCaptureStopHookActive: a SubagentStop on a record already stopped
// keeps its stopped_at unless the payload's stop_hook_active is the boolean
// true, which replaces it with the stop's time (SR-6.4).
func TestSubagentCaptureStopHookActive(t *testing.T) {
	observed := running(subAgentID, subType, false, at(1))
	stopped := observed.stoppedAt(at(2))
	for _, tc := range []struct {
		name string
		edit payloadEdit
		want wantEntry
	}{
		{"absent", delKey("stop_hook_active"), stopped},
		{"false", setKey("stop_hook_active", false), stopped},
		{"a string", setKey("stop_hook_active", "true"), stopped},
		{"true", setKey("stop_hook_active", true), stopped.stoppedAt(at(3))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSubRow(t, subRowID)
			r.script(t, []step{
				{name: "start", payload: variant(t, fxStart), at: at(1), entries: []wantEntry{observed}},
				{name: "stop", payload: variant(t, fxStop), at: at(2), entries: []wantEntry{stopped}},
				{name: "second stop", payload: variant(t, fxStop, tc.edit), at: at(3), entries: []wantEntry{tc.want}},
			})
			if sid := r.spawn().State; sid != store.StateWorking {
				t.Errorf("row state = %v; want %s", sid, store.StateWorking)
			}
		})
	}
}
