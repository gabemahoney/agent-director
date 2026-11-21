package qualification_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// Small synthetic fact sequence for the schedule and injection-window seams.
// It has no ledger/capture envelope and cannot be exported as a live run.
func unitGroupFacts() (q.Capture, []q.GroupPlan) {
	at := func(ms int64) time.Time { return time.UnixMilli(1800000000000 + ms).UTC() }
	s := runner.Snapshot{InstanceID: "unit-parent", Tables: map[string][]runner.Row{"spawns": {{"session_id": "unit-session", "launch_token": "unit-launch"}}}}
	owner := q.OwnerEvidence{Owner: runner.Owned{InstanceID: "unit-parent", SessionID: "unit-session", LaunchToken: "unit-launch", Started: at(1)}, SnapshotRef: "unit/store", TranscriptRef: "unit/journal"}
	c := q.Capture{Manifest: runner.Manifest{Cases: []runner.Case{{ID: "G0", InstanceID: "unit-parent", Text: "unit trigger", Started: at(1)}}}, Snapshots: map[string]runner.Snapshot{}}
	for i, id := range []string{"S1", "S2", "O1", "O2"} {
		mode, boundary := "steer_turn", "tool_boundary"
		issued := []int64{10, 20, 40, 70}[i]
		intake := []int64{13, 23, 46, 76}[i]
		if i > 1 {
			mode, boundary = "wait_turn", "turn_end"
		}
		text := "unit body " + id
		body := "[agent-director: message 1 of 1]\n" + text
		c.Manifest.Cases = append(c.Manifest.Cases, runner.Case{ID: id, InstanceID: "unit-parent", Text: text, Mode: mode, Started: at(5), Ended: at(6)})
		s.Tables["messages"] = append(s.Tables["messages"], runner.Row{"message_id": id, "instance_id": "unit-parent", "text": text, "text_sha256": runner.Digest([]byte(text)), "accepted_seq": int64(i + 1)})
		s.Tables["handovers"] = append(s.Tables["handovers"], runner.Row{"handover_id": id, "instance_id": "unit-parent", "launch_token": "unit-launch", "session_id": "unit-session", "boundary": boundary})
		s.Tables["members"] = append(s.Tables["members"], runner.Row{"handover_id": id, "message_id": id, "prompt_index": 0, "member_index": 0})
		p := runner.Row{"handover_id": id, "prompt_index": 0, "message_count": int64(1), "emission": "issued", "transport": "written", "issued_at_ms": at(issued).UnixMilli(), "transport_at_ms": at(issued + 2).UnixMilli(), "order_at_ms": at(issued + 2).UnixMilli(), "order_kind": "emission_complete"}
		role, want := "system", signals.NativeContextBody(body)
		if i > 1 {
			p["admission_kind"] = "prompt_sighting"
			p["admitted_at_ms"] = at(issued + 5).UnixMilli()
			p["order_at_ms"] = at(issued + 5).UnixMilli()
			p["order_kind"] = "ordered_acceptance"
			role, want = "user", signals.NativeWake(q.Transform(body, "wait_turn"))
		}
		s.Tables["prompts"] = append(s.Tables["prompts"], p)
		b, _ := json.Marshal(want)
		owner.Transcript.Requests = append(owner.Transcript.Requests, signals.Request{ID: "unit-request-" + id, Time: at(intake), Messages: []signals.Message{{Role: role, Content: b}}})
	}
	owner.Snapshot = s
	c.Owners = []q.OwnerEvidence{owner}
	for i, event := range []string{"PreToolUse", "PostToolBatch", "PostToolBatch", "Stop", "Stop"} {
		ms := []int64{4, 11, 21, 30, 60}[i]
		c.Hooks = append(c.Hooks, q.HookFact{Receipt: runner.Receipt{Event: event, Received: at(ms)}, Reference: fmt.Sprintf("unit/hook-%d", i), Payload: runner.Row{"session_id": "unit-session", "tool_name": "Bash"}})
	}
	parts := []store.BusySendPromptPart{{First: 0, Count: 1}, {First: 1, Count: 1}}
	plans := []q.GroupPlan{{Mode: "steer_turn", Texts: []string{"unit body S1", "unit body S2"}, Cases: []string{"S1", "S2"}, Parts: parts}, {Mode: "wait_turn", Texts: []string{"unit body O1", "unit body O2"}, Cases: []string{"O1", "O2"}, Parts: parts}}
	return c, plans
}

func TestQualificationGroupPhysicalSchedule(t *testing.T) {
	for _, kind := range []string{"G0-S1-S2-O1-O2", "missing-second-boundary", "child-stop", "missing-whole-S2", "O1-before-S2", "duplicate-physical-O2", "relabelled-ordinary"} {
		t.Run(kind, func(t *testing.T) {
			c, plans := unitGroupFacts()
			want := "fail"
			switch kind {
			case "G0-S1-S2-O1-O2":
				want = "pass"
			case "missing-second-boundary":
				c.Hooks = append(c.Hooks[:2], c.Hooks[3:]...)
				want = "not_completed"
			case "child-stop":
				c.Hooks[4].Payload["agent_id"] = "child"
				want = "not_completed"
			case "missing-whole-S2":
				c.Owners[0].Transcript.Requests[1].Messages = nil
				want = "not_completed"
			case "O1-before-S2":
				c.Owners[0].Snapshot.Tables["prompts"][2]["issued_at_ms"] = time.UnixMilli(1800000000015).UnixMilli()
			case "duplicate-physical-O2":
				c.Manifest.Cases[4].Text = "unit body O1"
			case "relabelled-ordinary":
				c.Manifest.Cases[3].ID = "F3"
				plans[1].Cases[0] = "F3"
			}
			got := q.CheckGroupSchedule(c, plans)
			if got.Status != want {
				t.Fatalf("%+v; want %s", got, want)
			}
		})
	}
}

func TestQualificationFirstEligibleBoundary(t *testing.T) {
	for _, kind := range []string{"first-boundary", "delayed-output", "missing-trigger", "wrong-state"} {
		t.Run(kind, func(t *testing.T) {
			c, _ := unitGroupFacts()
			x := c.Manifest.Cases[1]
			x.ID = "typed-long-tool-ad-input"
			c.Manifest.Cases = []runner.Case{x}
			before := runner.Snapshot{InstanceID: "unit-parent", ReadCompleted: time.UnixMilli(1800000000002), Tables: map[string][]runner.Row{"spawns": {{"session_id": "unit-session", "launch_token": "unit-launch"}}}}
			c.Snapshots["unit/before"] = before
			c.Observations = []q.Observation{{Case: x.ID, Kind: "send", State: "long-tool", Trigger: "unit/hook-0"}, {Case: x.ID + "-before", Kind: "snapshot", Snapshot: "unit/before"}}
			if kind == "delayed-output" {
				c.Owners[0].Snapshot.Tables["prompts"][0]["transport_at_ms"] = int64(1800000000022)
				c.Owners[0].Snapshot.Tables["prompts"][0]["order_at_ms"] = int64(1800000000022)
				c.Owners[0].Transcript.Requests[0].Messages = nil
				c.Owners[0].Transcript.Requests[1].Messages = c.Owners[0].Transcript.Requests[2].Messages
				body := "[agent-director: message 1 of 1]\nunit body S1"
				b, _ := json.Marshal(signals.NativeContextBody(body))
				c.Owners[0].Transcript.Requests[1].Messages = []signals.Message{{Role: "system", Content: b}}
			}
			if kind == "missing-trigger" {
				c.Observations[0].Trigger = "absent"
			}
			if kind == "wrong-state" {
				c.Observations[0].State = "between-tools"
			}
			got := q.ExtractTimings(c)
			if len(got) != 1 {
				t.Fatalf("%+v", got)
			}
			xgot := got[0]
			want := "not_completed"
			if kind == "first-boundary" {
				want = "pass"
			}
			if kind == "delayed-output" {
				want = "fail"
			}
			if xgot.BoundaryStatus != want || (kind == "first-boundary" && (!xgot.Whole || xgot.Boundary != 1)) || (kind == "delayed-output" && (xgot.Whole || xgot.Boundary != 2)) {
				t.Fatalf("eventual output moved eligible boundary: %+v; want %s", xgot, want)
			}
		})
	}
}
