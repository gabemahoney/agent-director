package qualification

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Synthetic count-boundary data only: this does not claim a live backlog run,
// small-drain proof, scheduler fallback, or qualification of a Claude version.
func syntheticBacklogProof(t *testing.T) BacklogProof {
	t.Helper()
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	p := BacklogProof{
		Before: runner.Snapshot{InstanceID: "unit-backlog", Tables: map[string][]runner.Row{}},
		After:  runner.Snapshot{InstanceID: "unit-backlog", Tables: map[string][]runner.Row{}},
		Final:  runner.Snapshot{InstanceID: "unit-backlog", Tables: map[string][]runner.Row{}},
		Refusals: []RefusalObservation{
			{Case: "backlog-over-count", Reason: "held_count", Actual: 1001, Allowed: 1000, ResendSafe: true, RetryWith: "new_send", Command: "raw/over-count.json"},
			{Case: "backlog-over-text", Reason: "text_bytes", Actual: 8001, Allowed: 8000, Command: "raw/over-text.json"},
		},
		Lookups: map[string]bool{"backlog-0001": true, "backlog-0500": true, "backlog-1000": true},
		Exited:  true,
	}
	for i := 1; i <= 1000; i++ {
		id := fmt.Sprintf("backlog-%04d", i)
		text, err := Corpus(id, 1900)
		if err != nil {
			t.Fatal(err)
		}
		p.Accepted = append(p.Accepted, AdmissionAttempt{
			ID: id, TextBytes: len(text), Started: start, Ended: start.Add(time.Millisecond), Output: "raw/" + id + ".json",
			Result: api.DeliveryResult{CallerUUID: "unit-caller", RequestID: id, Class: "held"},
		})
		for _, s := range []*runner.Snapshot{&p.Before, &p.After, &p.Final} {
			row := runner.Row{"message_id": id, "instance_id": "unit-backlog", "accepted_seq": int64(i), "text": text, "text_sha256": runner.Digest([]byte(text)), "place": "held", "result_class": "held"}
			if s == &p.Final {
				row["place"], row["result_class"] = "final", "not_started"
			}
			s.Tables["messages"] = append(s.Tables["messages"], row)
		}
	}
	return p
}

func TestQualificationBacklogEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*BacklogProof)
		status string
	}{
		{"complete-unit-proof", func(*BacklogProof) {}, "pass"},
		{"missing-acceptance", func(p *BacklogProof) { p.Accepted = p.Accepted[:999] }, "not_completed"},
		{"duplicate-acceptance", func(p *BacklogProof) { p.Accepted[999] = p.Accepted[0] }, "fail"},
		{"wrong-body-size", func(p *BacklogProof) { p.Accepted[499].TextBytes-- }, "fail"},
		{"premature-final-result", func(p *BacklogProof) { p.Accepted[499].Result.Final = true }, "fail"},
		{"unexpected-emission", func(p *BacklogProof) { p.After.Tables["prompts"] = []runner.Row{{"emission": "issued"}} }, "fail"},
		{"custody-left-held", func(p *BacklogProof) { p.After.Tables["messages"][499]["place"] = "handover" }, "fail"},
		{"missing-count-refusal", func(p *BacklogProof) { p.Refusals = p.Refusals[1:] }, "not_completed"},
		{"unsafe-retry", func(p *BacklogProof) { p.Refusals[0].ResendSafe = false }, "fail"},
		{"wrong-retry", func(p *BacklogProof) { p.Refusals[0].RetryWith = "same_send" }, "fail"},
		{"unverified-exit", func(p *BacklogProof) { p.Exited = false }, "not_completed"},
		{"unfinalized-message", func(p *BacklogProof) { p.Final.Tables["messages"][499]["result_class"] = "held" }, "fail"},
		{"missing-final-lookup", func(p *BacklogProof) { delete(p.Lookups, "backlog-0500") }, "not_completed"},
		// Preserve row counts, text lengths and valid hashes: an unrelated
		// integrity failure must not substitute for detecting lost custody.
		{"duplicate-held-message", func(p *BacklogProof) { p.After.Tables["messages"][499] = p.After.Tables["messages"][0] }, "fail"},
		{"substituted-held-body", func(p *BacklogProof) {
			text := strings.Repeat("x", 1900)
			row := p.After.Tables["messages"][499]
			row["text"], row["text_sha256"] = text, runner.Digest([]byte(text))
		}, "fail"},
		{"duplicate-final-message", func(p *BacklogProof) { p.Final.Tables["messages"][499] = p.Final.Tables["messages"][0] }, "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := syntheticBacklogProof(t)
			tc.change(&p)
			got := CheckBacklog(p)
			if got.SignalID != "large-backlog" || got.Status != tc.status {
				t.Fatalf("CheckBacklog = %+v; want large-backlog %s", got, tc.status)
			}
			if tc.status == "pass" {
				if got.Expected != "all Held, next refused, verified exit, confirmed finalization" || got.Observed != 1000 || !reflect.DeepEqual(got.Evidence, []string{"raw/backlog-acceptance.json"}) {
					t.Fatalf("complete proof lost its exact observation/evidence: %+v", got)
				}
			}
		})
	}
}

// Synthetic physical identity facts exercise ownership joins, not accounting
// arithmetic or a purported live group schedule.
func TestQualificationPhysicalStartIdentity(t *testing.T) {
	for _, kind := range []string{"owned", "missing-start", "duplicate-start", "wrong-launch", "wrong-body", "child-start", "wrong-case"} {
		t.Run(kind, func(t *testing.T) {
			c := Capture{Manifest: runner.Manifest{RunID: "unit", Cases: []runner.Case{{ID: "O1", InstanceID: "unit-parent", Text: "unit body", Mode: "typed"}}},
				After:  Ledger{Identity: Identity{Attempt: "a1"}, Charges: []Charge{{ID: "O1", Phase: "groups", Attempt: "a1", Launch: "launch", Prompt: "physical@raw/parent.json", Actual: 1, Reconciled: true, Consumers: []string{"text-limits-backlog"}}}},
				Owners: []OwnerEvidence{{Owner: runner.Owned{InstanceID: "unit-parent", LaunchToken: "launch", SessionID: "session"}}},
				Hooks:  []HookFact{{Reference: "raw/parent.json", Receipt: runner.Receipt{Event: "UserPromptSubmit"}, Payload: runner.Row{"session_id": "session", "prompt": "unit body"}}}}
			switch kind {
			case "missing-start":
				c.After.Charges = nil
			case "duplicate-start":
				c.After.Charges = append(c.After.Charges, c.After.Charges[0])
			case "wrong-launch":
				c.After.Charges[0].Launch = "other"
			case "wrong-body":
				c.Hooks[0].Payload["prompt"] = "other"
			case "child-start":
				c.Hooks[0].Payload["agent_id"] = "child"
			case "wrong-case":
				c.Manifest.Cases[0].ID = "O2"
			}
			if err := requirePhysicalCase(c, "O1", "groups"); (err == nil) != (kind == "owned") {
				t.Fatalf("%s: %v", kind, err)
			}
		})
	}
}

func TestQualificationLaunchSequenceGates(t *testing.T) {
	for _, kind := range []string{"ordered", "mixed-mode-ordered", "cross-mode-inversion", "reversed-order", "before-order", "missing-stop"} {
		t.Run(kind, func(t *testing.T) {
			in := PackingEvidence{References: []string{"unit/sequence"}, Hooks: []runner.Row{{"hook_event_name": "Stop", "_received_ms": int64(40)}}}
			items := []physicalEmission{
				{Handover: runner.Row{"boundary": "turn_end"}, Prompt: runner.Row{"issued_at_ms": int64(10), "order_at_ms": int64(20)}, Mode: "wait_turn", Sequences: []int64{1}, Intake: 30},
				{Handover: runner.Row{"boundary": "turn_end"}, Prompt: runner.Row{"issued_at_ms": int64(50), "order_at_ms": int64(60)}, Mode: "wait_turn", Sequences: []int64{2}, Intake: 70},
			}
			switch kind {
			case "mixed-mode-ordered", "cross-mode-inversion":
				items[1].Mode = "steer_turn"
				items[1].Handover["boundary"] = "tool_boundary"
				if kind == "cross-mode-inversion" {
					items[0].Sequences, items[1].Sequences = []int64{2}, []int64{1}
				}
			case "reversed-order":
				items[1].Sequences = []int64{1}
			case "before-order":
				items[0].Prompt["order_at_ms"] = int64(55)
			case "missing-stop":
				in.Hooks = nil
			}
			want := "fail"
			if kind == "ordered" || kind == "mixed-mode-ordered" {
				want = "pass"
			}
			if got := checkLaunchSequence(in, items); got.Status != want {
				t.Fatalf("%+v; want %s", got, want)
			}
		})
	}
}

func TestQualificationForceGroupIdentity(t *testing.T) {
	for _, kind := range []string{"separate-forces", "one-prompt-two-labels", "ordinary-relabelled", "missing-force-id", "missing-F4"} {
		t.Run(kind, func(t *testing.T) {
			owner := runner.Owned{InstanceID: "unit-force-parent", LaunchToken: "unit-launch", SessionID: "unit-session"}
			s := runner.Snapshot{InstanceID: owner.InstanceID, Tables: map[string][]runner.Row{}}
			c := Capture{Owners: []OwnerEvidence{{Owner: owner}}, Manifest: runner.Manifest{Cases: []runner.Case{
				{ID: "F3", InstanceID: owner.InstanceID, Text: "force body F3"}, {ID: "F4", InstanceID: owner.InstanceID, Text: "force body F4"},
			}}}
			for i, id := range []string{"F3", "F4"} {
				hid := "one-force"
				if kind == "separate-forces" {
					hid = id
				}
				boundary, forceID := "force", "unit-force"
				if kind == "ordinary-relabelled" {
					boundary = "turn_end"
				}
				if kind == "missing-force-id" {
					forceID = ""
				}
				pi, count := i, 1
				if kind == "one-prompt-two-labels" {
					pi, count = 0, 2
				}
				s.Tables["messages"] = append(s.Tables["messages"], runner.Row{"message_id": id, "text": "force body " + id})
				s.Tables["members"] = append(s.Tables["members"], runner.Row{"message_id": id, "handover_id": hid, "member_index": int64(i), "prompt_index": int64(pi)})
				if i == 0 || kind == "separate-forces" {
					s.Tables["handovers"] = append(s.Tables["handovers"], runner.Row{"handover_id": hid, "launch_token": owner.LaunchToken, "boundary": boundary, "force_id": forceID})
				}
				if i == 0 || kind != "one-prompt-two-labels" {
					s.Tables["prompts"] = append(s.Tables["prompts"], runner.Row{"handover_id": hid, "prompt_index": int64(pi), "emission": "issued", "message_count": int64(count)})
				}
			}
			c.Owners[0].Snapshot = s
			if kind == "missing-F4" {
				c.Manifest.Cases = c.Manifest.Cases[:1]
			}
			got := forceGroup(c)
			if kind == "missing-F4" {
				if got.Status != "not_completed" {
					t.Fatalf("%+v", got)
				}
				return
			}
			if got.Status != "fail" || got.Reason != "F3/F4 are not consecutive prompts of one force" {
				t.Fatalf("physical identities were conflated: %+v", got)
			}
		})
	}
}
