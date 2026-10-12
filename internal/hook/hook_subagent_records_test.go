package hook_test

// Subagent capture through hook.Handle (b.xjn; SRD SR-15.3, SR-2.2, SR-3.2,
// SR-3.3, SR-4.2, SR-4.3, SR-4.4, SR-6.2, SR-6.3, SR-7, SR-13.1): each payload
// reaches the right gated store write, with the receipt time of
// HandleConfig.Start. They show the wiring; the store's transition matrix is
// internal/store's. Every hook runs with empty stdout (fireFrom asserts it).
// The claims are hook_subagent_claims_test.go, the payload-shape cases
// hook_subagent_inputs_test.go and the safety cases hook_subagent_safety_test.go.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const subRowID = "ad-subagent-capture"

// TestSubagentCaptureBackground: a background launch (B9, B1, B5) is one
// running record with every field and the injected receipt times, and B3 stops
// it. Claude Code 2.1.296's canContinueAgent changes nothing.
func TestSubagentCaptureBackground(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result []payloadEdit
	}{
		{name: "B5", result: nil},
		{name: "B5 with canContinueAgent", result: []payloadEdit{setKey("tool_response.canContinueAgent", true)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSubRow(t, subRowID)
			claimed := running(subAgentID, subType, true, at(2))
			full := filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))
			r.script(t, []step{
				{name: "B9 opens a claimable launch", payload: variant(t, fxRequest), at: at(1),
					launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
				{name: "B1 claims it and fills nothing", payload: variant(t, fxStart), at: at(2), upsert: "updated",
					entries: []wantEntry{claimed}},
				{name: "B5 fills the record", payload: variant(t, fxResult, tc.result...), at: at(3),
					entries: []wantEntry{full}},
				{name: "B3 stops it", payload: variant(t, fxStop), at: at(4), upsert: "updated",
					entries: []wantEntry{full.stoppedAt(at(4))}},
			})
		})
	}
}

// TestSubagentCaptureForeground: a foreground launch in fg.jsonl's order. While
// a subagent runs its record has only its type (N11), nested ones too; its
// completed result sets every field and run_mode foreground; neither the
// parent's stop nor a main-agent Stop closes anything more.
func TestSubagentCaptureForeground(t *testing.T) {
	r := newSubRow(t, subRowID)
	parentRunning := running(subAgentID, subType, true, at(2))
	childRunning := running(subChildID, subType, true, at(4))
	childDone := filled(r, subChildID, subType, subNestedDesc, subModel, subNestedToolUseID, at(4))
	childDone.runMode, childDone.parent, childDone.parentID = "foreground", "subagent", subAgentID
	childDone = childDone.stoppedAt(at(5))
	parentDone := filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))
	parentDone.runMode = "foreground"
	parentDone = parentDone.stoppedAt(at(7))

	r.script(t, []step{
		{name: "B9", payload: variant(t, fxRequest), at: at(1),
			launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
		{name: "B1 running with its type only", payload: variant(t, fxStart), at: at(2),
			entries: []wantEntry{parentRunning}},
		{name: "nested request", payload: nestedRequest(t), at: at(3),
			entries: []wantEntry{parentRunning}, launches: []wantLaunch{{subNestedToolUseID, subType, at(3)}}},
		{name: "nested start, its type only", payload: nestedStart(t), at: at(4),
			entries: []wantEntry{parentRunning, childRunning}},
		{name: "nested stop", payload: nestedStop(t), at: at(5),
			entries: []wantEntry{parentRunning, childRunning.stoppedAt(at(5))}},
		{name: "nested completed result fills the child", payload: nestedCompleted(t), at: at(6),
			entries: []wantEntry{parentRunning, childDone}},
		{name: "B3 with an empty listing stops the parent", payload: foregroundStop(t), at: at(7), upsert: "updated",
			entries: []wantEntry{parentRunning.stoppedAt(at(7)), childDone}},
		{name: "B11 fills the parent", payload: variant(t, fxCompleted), at: at(8),
			entries: []wantEntry{parentDone, childDone}},
		{name: "main-agent Stop closes neither", payload: mainStop(t), at: at(9),
			entries: []wantEntry{parentDone, childDone}},
	})
}

// TestSubagentCaptureNestedParent: B6 (a launch result carrying its launcher's
// agent_id) creates the child's record with parent_type subagent and the
// launcher's id, though no entry has that id.
func TestSubagentCaptureNestedParent(t *testing.T) {
	r := newSubRow(t, subRowID)
	child := filled(r, subChildID, subType, subNestedDesc, subModel, subNestedToolUseID, at(1))
	child.parent, child.parentID = "subagent", subAgentID
	r.script(t, []step{
		{name: "B6", payload: variant(t, fxResultNested), at: at(1), entries: []wantEntry{child}},
	})
}

// TestSubagentCaptureNoRecord: teammates (B10, B2, B7) and the internal agent
// (B4) produce no record. A teammate's request opens no claimable launch, its
// start makes only an observation, and B4's stop has no entry to stop.
func TestSubagentCaptureNoRecord(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload func(*testing.T) []byte
		entries []wantEntry
	}{
		{name: "B10 teammate request", payload: func(t *testing.T) []byte { return variant(t, fxRequestTeammate) }},
		{name: "B2 teammate start", payload: func(t *testing.T) []byte { return variant(t, fxStartTeammate) },
			entries: []wantEntry{running(subTeammateID, subTeammateType, false, at(1))}},
		{name: "B7 teammate result", payload: func(t *testing.T) []byte { return variant(t, fxResultTeammate) }},
		{name: "B4 internal agent stop", payload: func(t *testing.T) []byte { return variant(t, fxStopInternal) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSubRow(t, subRowID)
			r.script(t, []step{{name: "fire", payload: tc.payload(t), at: at(1), entries: tc.entries}})
			if got := records(r); len(got) != 0 {
				t.Errorf("records = %v; want none", got)
			}
		})
	}
}

// TestSubagentCaptureNamedLaunch: a launch with tool_input.name opens no
// claimable launch, so its start is an observation until its completed result
// confirms it; the stop in between stops the observation.
func TestSubagentCaptureNamedLaunch(t *testing.T) {
	r := newSubRow(t, subRowID)
	observed := running(subAgentID, subType, false, at(2))
	done := filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))
	done.runMode = "foreground"
	r.script(t, []step{
		{name: "named B9 opens nothing", payload: namedRequest(t), at: at(1)},
		{name: "B1 observed, not claimed", payload: variant(t, fxStart), at: at(2),
			entries: []wantEntry{observed}},
		{name: "B3 with an empty listing stops the observation", payload: foregroundStop(t), at: at(3),
			entries: []wantEntry{observed.stoppedAt(at(3))}},
	})
	if got := records(r); len(got) != 0 {
		t.Errorf("records before B11 = %v; want none", got)
	}
	r.script(t, []step{
		{name: "B11 confirms and fills it", payload: variant(t, fxCompleted), at: at(4),
			entries: []wantEntry{done.stoppedAt(at(3))}},
	})
	if got := records(r); len(got) != 1 {
		t.Errorf("records after B11 = %v; want 1", got)
	}
}

// TestSubagentCaptureTypeFromAgentType: a completed result without
// tool_input.subagent_type types the new record from tool_response.agentType;
// when both are present tool_input.subagent_type wins.
func TestSubagentCaptureTypeFromAgentType(t *testing.T) {
	const resolved = "resolved-type"
	for _, tc := range []struct {
		name  string
		edits []payloadEdit
		want  string
	}{
		{name: "tool_input.subagent_type absent", want: resolved,
			edits: []payloadEdit{delKey("tool_input.subagent_type"), setKey("tool_response.agentType", resolved)}},
		{name: "both present", want: subType,
			edits: []payloadEdit{setKey("tool_response.agentType", resolved)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSubRow(t, subRowID)
			want := wantEntry{id: subAgentID, state: "stopped", confirmed: true, typ: tc.want,
				desc: subLaunchDesc, model: subModel, parent: "agent", parentID: r.id, toolUseID: subToolUseID,
				runMode: "foreground", stopped: at(1), closed: "stop_event", opened: at(1)}
			r.script(t, []step{
				{name: "B11", payload: variant(t, fxCompleted, tc.edits...), at: at(1), entries: []wantEntry{want}},
			})
		})
	}
}

// TestSubagentCaptureHeldWorkingTransition: a row in check_permission with an
// open permission request stays there through B9, B1 and B5 (its working
// transition is held) and the entry writes still happen (SR-3.3).
func TestSubagentCaptureHeldWorkingTransition(t *testing.T) {
	r := newSubRowIn(t, subRowID, store.StateCheckPermission)
	storefix.SeedOpenPermissionRequests(t, r.st, r.id, []string{storefix.TestRequestTokenA})
	if got := r.spawn().State; got != store.StateCheckPermission {
		t.Fatalf("state after seeding the request = %v; want %s", got, store.StateCheckPermission)
	}
	full := filled(r, subAgentID, subType, subLaunchDesc, subModel, subToolUseID, at(2))
	steps := []step{
		{name: "B9", payload: variant(t, fxRequest), at: at(1), launches: []wantLaunch{{subToolUseID, subType, at(1)}}},
		{name: "B1", payload: variant(t, fxStart), at: at(2), entries: []wantEntry{running(subAgentID, subType, true, at(2))}},
		{name: "B5", payload: variant(t, fxResult), at: at(3), entries: []wantEntry{full}},
	}
	for _, s := range steps {
		r.script(t, []step{s})
		if got := r.spawn().State; got != store.StateCheckPermission {
			t.Errorf("state after %s = %v; want %s", s.name, got, store.StateCheckPermission)
		}
	}
}

// TestSubagentCaptureGateRefusals: a hook of another process, and a hook on a
// row that records no pane, writes no entry and closes no claimable launch,
// and each leaves exactly one ad.hook.ignored with the gate's reason. A
// claimable launch seeded first stays.
func TestSubagentCaptureGateRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		opts    []apitest.SpawnOption
		foreign bool
		reason  string
	}{
		{name: "another process", state: store.StateWorking, foreign: true, reason: store.HookReasonPIDMismatch},
		{name: "row with no pane", state: store.StatePending, opts: []apitest.SpawnOption{gateNoPane()},
			reason: store.HookReasonNoPaneRecorded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSubRowIn(t, subRowID, tc.state, tc.opts...)
			if err := apitest.SeedSubagentLaunch(r.dbPath, r.id, "toolu_seeded"); err != nil {
				t.Fatalf("SeedSubagentLaunch: %v", err)
			}
			seeded := wantLaunch{"toolu_seeded", "general-purpose", apitest.SeededSubagentMs}
			fire := r.fire
			if tc.foreign {
				fire = r.fireForeign
			}
			for i, h := range []struct {
				name    string
				payload []byte
			}{
				{"B9", variant(t, fxRequest)},
				{"B1", variant(t, fxStart)},
				{"B5", variant(t, fxResult)},
				{"B3", variant(t, fxStop)},
				{"main-agent Stop", mainStop(t)},
			} {
				f := fire(h.payload, at(int64(i)+1))
				if len(f.Ignored) != 1 || f.Ignored[0]["reason"] != tc.reason {
					t.Errorf("%s: ad.hook.ignored = %v; want exactly one with reason %q", h.name, f.Ignored, tc.reason)
				}
				assertEntries(t, r)
				assertLaunches(t, r, seeded)
			}
		})
	}
}

// TestSubagentCaptureNoEntryTransaction: a PreToolUse or PostToolUse of
// another tool (the name matched exactly), however launch-shaped, writes no
// entry and opens or closes no claimable launch, and a Stop with an agent_id
// closes none.
func TestSubagentCaptureNoEntryTransaction(t *testing.T) {
	seeded := wantLaunch{subToolUseID, subType, apitest.SeededSubagentMs}
	for _, tool := range []string{"Bash", "agent", "AgentTool", "Task"} {
		t.Run("tool "+tool, func(t *testing.T) {
			r := newSubRow(t, subRowID)
			if err := apitest.SeedSubagentLaunch(r.dbPath, r.id, subToolUseID); err != nil {
				t.Fatalf("SeedSubagentLaunch: %v", err)
			}
			r.script(t, []step{
				{name: "request", payload: variant(t, fxRequest, setKey("tool_name", tool), setKey("tool_use_id", subSecondToolUseID)),
					at: at(1), launches: []wantLaunch{seeded}},
				{name: "result", payload: variant(t, fxResult, setKey("tool_name", tool)), at: at(2),
					launches: []wantLaunch{seeded}},
				{name: "failure", payload: agentFailure(t, subToolUseID, setKey("tool_name", tool)), at: at(3),
					launches: []wantLaunch{seeded}},
			})
		})
	}
	t.Run("Stop with an agent_id", func(t *testing.T) {
		r := newSubRow(t, subRowID)
		if err := apitest.SeedSubagentLaunch(r.dbPath, r.id, subToolUseID); err != nil {
			t.Fatalf("SeedSubagentLaunch: %v", err)
		}
		r.script(t, []step{
			{name: "Stop of a subagent", payload: variant(t, fxMainStop, setKey("agent_id", subAgentID), emptyListing()),
				at: at(1), launches: []wantLaunch{seeded}},
		})
	})
}
