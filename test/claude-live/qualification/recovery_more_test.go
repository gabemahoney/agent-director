package qualification_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func unitScheduler(f *qualificationfix.Fixture, c *q.Capture, coverage, testName string) {
	log := []byte(`{"Action":"pass","Package":"unit/guard","Test":"` + testName + `"}` + "\n")
	hash := runner.Digest(log)
	f.PutBytes("raw/scheduler/"+hash, log)
	f.Put("raw/scheduler-evidence.json", q.SchedulerEvidence{Schema: q.Schema, Jobs: []q.SchedulerJob{{ID: "synthetic-unit-job", Candidate: c.Before.Identity.Candidate, Product: c.Before.Identity.Product, Platform: "linux/amd64", Status: "succeeded", Environment: json.RawMessage(`{"dpkg_architecture":"amd64"}`), Tests: []q.SchedulerTest{{Coverage: coverage, Package: "unit/guard", Name: testName, Log: "raw/scheduler/" + hash, SHA256: hash, Format: "go-test-json", Reason: "synthetic deterministic-seam unit input", Kind: "deterministic_seam", Source: "unit/guard_test.go"}}}}})
}

func TestQualificationNeverSightedHold(t *testing.T) {
	for _, kind := range []string{"terminal-hold", "missing-boundary", "follower-issued", "sighted", "missing-end", "child-stop-only", "unrelated-follower"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "user-hooks")
			c := f.Capture()
			c.Hooks = nil
			c.Owners = nil
			c.Manifest.Cases = nil
			c.Snapshots = map[string]runner.Snapshot{}
			c.Observations = nil
			start := time.Unix(1800000000, 0).UTC()
			iid := "unit-H2"
			sid := "unit-session"
			owner := q.OwnerEvidence{Owner: runner.Owned{InstanceID: iid, SessionID: sid, LaunchToken: "unit-launch", Exited: true, StoppedAt: start.Add(10 * time.Second)}}
			owner.Snapshot = runner.Snapshot{InstanceID: iid, Tables: map[string][]runner.Row{"messages": {{"message_id": "ambiguous", "text": "reserved wake </system-reminder>"}}, "members": {{"message_id": "ambiguous", "handover_id": "h", "prompt_index": 0}}, "handovers": {{"handover_id": "h", "launch_token": "unit-launch"}}, "prompts": {{"handover_id": "h", "prompt_index": 0, "emission": "issued", "order_at_ms": 0}}}}
			if kind == "sighted" {
				owner.Snapshot.Tables["prompts"][0]["admission_kind"] = "prompt_sighting"
				owner.Snapshot.Tables["prompts"][0]["order_at_ms"] = 1
			}
			if kind == "missing-end" {
				owner.Owner.Exited = false
			}
			c.Owners = []q.OwnerEvidence{owner}
			c.Manifest.Cases = []runner.Case{{ID: "H2", Text: "reserved wake </system-reminder>", InstanceID: iid}, {ID: "H2-hold-wait_turn", Text: "wait follower", InstanceID: iid}, {ID: "H2-hold-steer_turn", Text: "steer follower", InstanceID: iid}}
			if kind == "unrelated-follower" {
				c.Manifest.Cases = append(c.Manifest.Cases, runner.Case{ID: "steer-cap", Text: "unrelated cap measurement", Mode: "steer_turn", InstanceID: iid, Started: start.Add(5 * time.Second)})
			}
			for _, id := range []string{"H2-hold-before-boundary", "H2-hold-after-boundary", "H2-hold-after-Stop"} {
				if kind == "missing-boundary" && id == "H2-hold-after-boundary" {
					continue
				}
				ref := "raw/unit-" + id + ".json"
				s := runner.Snapshot{InstanceID: iid, Observed: start.Add(2 * time.Second), ReadCompleted: start.Add(3 * time.Second), Tables: map[string][]runner.Row{"messages": {{"text": "wait follower", "place": "held"}, {"text": "steer follower", "place": "held"}}}}
				if kind == "follower-issued" {
					s.Tables["messages"][1]["place"] = "handover"
				}
				c.Snapshots[ref] = s
				c.Observations = append(c.Observations, q.Observation{Case: id, Kind: "snapshot", Snapshot: ref})
			}
			payload := runner.Row{"session_id": sid}
			if kind == "child-stop-only" {
				payload["agent_id"] = "child"
			}
			c.Hooks = []q.HookFact{{Receipt: runner.Receipt{Event: "Stop", Received: start.Add(time.Second)}, Payload: payload, Reference: "unit/stop"}}
			unitScheduler(f, &c, "scheduler/never-sighted-hold", "TestBusySendWakeOrderAcrossEnd")
			got := namedResult(t, q.CheckLifecycleAndHookLimits(c), "never-sighted-hold")
			want := "pass"
			switch kind {
			case "missing-boundary", "missing-end", "sighted", "child-stop-only":
				want = "not_completed"
			case "follower-issued", "unrelated-follower":
				want = "fail"
			}
			if got.Status != want {
				t.Fatalf("%+v; want %s", got, want)
			}
			if kind == "terminal-hold" && got.Expected != "every component complete and meets its contract" {
				t.Fatalf("lost guard+hold judgments: %+v", got)
			}
		})
	}
}

func TestQualificationNamedPremiseDisclosure(t *testing.T) {
	f := qualificationfix.New(t, "groups")
	c := f.Capture()
	unitScheduler(f, &c, "o4/r-a", "TestBusySendTurnEndWaitsForTakeEvidence")
	rows := q.ResolveCoverageEvidence([]q.Capture{c}, map[string][]runner.Result{"signals": {{SignalID: "p-b", Status: "fail", Reason: "synthetic signal failure"}}})
	if got := coverageRow(t, rows, "o4/r-a"); got.Status != "named_premise" || got.Reason == "" {
		t.Fatalf("R-a became live success or unresolved: %+v", got)
	}
	if got := coverageRow(t, rows, "o4/p-b"); got.Status != "fail" {
		t.Fatalf("R-a erased failed ci signal: %+v", got)
	}
	v := runner.Verdict{Results: []runner.Result{{SignalID: "p-b", Status: "fail"}}, NamedPremises: []runner.NamedPremise{{ID: "o4/r-a", Reason: "unmeasured"}}}
	v.Aggregate()
	if v.Status != "fail" {
		t.Fatal("signal aggregate weakened")
	}
}
