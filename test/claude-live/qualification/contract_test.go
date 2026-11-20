package qualification

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Unit projection of the candidate table; the independent normative force
// rows and numeric prose come from its packaged caller contract.
func contractInputs(t *testing.T) ([]byte, api.DeliveryCapabilities) {
	t.Helper()
	doc, err := os.ReadFile("../../../docs/caller-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	c := store.BusySendCapabilitiesFor("claude")
	r := api.DeliveryCapabilities{HeldCount: c.HeldCount, HeldBytes: c.HeldBytes,
		ConfirmFromTranscriptsCapability: string(c.ConfirmFromTranscripts), ConfirmFromTranscriptsReason: string(c.ConfirmFromTranscriptsReason),
		Force: &api.DeliveryForceCapability{Admission: string(c.ForceAdmission)}}
	for _, m := range c.Modes {
		r.Modes = append(r.Modes, api.DeliveryModeLimits{SendMode: string(m.Mode), MessageBytes: m.MessageBytes, PromptBytes: m.PromptBytes, TextKind: string(m.TextKind)})
	}
	for _, l := range c.ForceLimits {
		r.Force.Limits = append(r.Force.Limits, string(l))
	}
	for _, s := range c.ForceStates {
		r.Force.States = append(r.Force.States, api.DeliveryForceState{State: string(s.State), Outcome: string(s.Outcome), Reason: string(s.Reason)})
	}
	return doc, r
}

func TestQualificationContractAgreement(t *testing.T) {
	for _, kind := range []string{"candidate", "missing-row", "unknown-row", "docs-runtime-agree-on-drift", "admission", "sighting-limit", "message-cap", "confirmation", "missing-table"} {
		t.Run(kind, func(t *testing.T) {
			doc, runtime := contractInputs(t)
			want := "fail"
			switch kind {
			case "candidate":
				want = "pass"
			case "missing-row":
				runtime.Force.States = runtime.Force.States[:7]
			case "unknown-row":
				runtime.Force.States[0].State = "invented"
			case "docs-runtime-agree-on-drift":
				runtime.Force.States[0].Reason = "invented"
				doc = []byte(strings.Replace(string(doc), "| `idle` | `handed_over` | `admitted` |", "| `idle` | `handed_over` | `invented` |", 1))
			case "admission":
				runtime.Force.Admission = "hook_exit"
			case "sighting-limit":
				runtime.Force.Limits = runtime.Force.Limits[1:]
			case "message-cap":
				runtime.Modes[0].MessageBytes++
			case "confirmation":
				runtime.ConfirmFromTranscriptsCapability = "supported"
			case "missing-table":
				runtime.Force.States = nil
				want = "not_completed"
			}
			b, _ := json.Marshal(runtime)
			got := CheckContractAgreement(doc, b)
			if got.SignalID != "contract" || got.Status != want {
				t.Fatalf("%+v; want %s", got, want)
			}
			if kind == "candidate" {
				contract, err := ParseContract(doc)
				if err != nil || len(contract.Force) != 8 || !reflect.DeepEqual(got.Expected, contract.Force) || !reflect.DeepEqual(got.Observed, contract.Force) || !reflect.DeepEqual(got.Evidence, []string{"raw/caller-contract.md", "raw/capabilities.json"}) {
					t.Fatalf("wrong normative rows/evidence: %+v, %v", got, err)
				}
			}
		})
	}
}

func TestQualificationForceState(t *testing.T) {
	doc, _ := contractInputs(t)
	contract, err := ParseContract(doc)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	for _, kind := range []string{"within-call-pending-row", "sighted-then-written", "late-idle", "not-exercised", "no-sighting", "refusal", "refusal-lost-custody", "wrong-reason", "unknown-state"} {
		t.Run(kind, func(t *testing.T) {
			o := ForceObservation{State: "tool_running", Outcome: "handed_over", Reason: "admitted", ForceID: "unit-force", Started: start,
				Returned: start.Add(10 * time.Second), Written: start.Add(time.Second), Sighted: start.Add(2 * time.Second), Ordered: start.Add(2 * time.Second), OrderProven: true, Exercised: true,
				HeldBefore: 1, Evidence: []string{"raw/unit-force.json"}}
			want := "pass"
			switch kind {
			case "sighted-then-written":
				o.Sighted = start.Add(time.Second)
				o.Written = start.Add(2 * time.Second)
				o.Ordered = o.Written
			case "late-idle":
				o.State = "idle"
				o.Sighted = start.Add(11 * time.Second)
				o.Ordered = o.Sighted
				o.Outcome = "pending"
				o.Reason = "awaiting_admission"
				o.LaterOutcome = "handed_over"
			case "not-exercised":
				o.Exercised = false
				want = "not_completed"
			case "no-sighting":
				o.Sighted = time.Time{}
				want = "fail"
			case "refusal", "refusal-lost-custody":
				o.State = "ended"
				o.Outcome = "refused"
				o.Reason = "launch_ended"
				o.HeldAfter = 1
				if kind == "refusal-lost-custody" {
					o.HeldAfter = 0
					want = "fail"
				}
			case "wrong-reason":
				o.Reason = "turn_running"
				want = "fail"
			case "unknown-state":
				o.State = "invented"
				want = "fail"
			}
			got := CheckForceState(contract, o)
			if got.SignalID != "force/"+o.State || got.Status != want || !reflect.DeepEqual(got.Evidence, o.Evidence) {
				t.Fatalf("%+v; want %s", got, want)
			}
		})
	}
}

func TestQualificationAcceptedLimitAllowlist(t *testing.T) {
	for _, tc := range []struct {
		code string
		refs []string
		want string
	}{
		{"never_sighted_hold", []string{"raw/hold.json"}, "pass"}, {"esc_gap", []string{"raw/esc.json"}, "pass"},
		{"invented", []string{"raw/claimed.json"}, "fail"}, {"user_hook_after_sighting", nil, "fail"},
	} {
		if got := accepted("unit", tc.code, "unit data", nil, tc.refs...); got.Status != tc.want {
			t.Fatalf("%+v", got)
		}
	}
}

func TestQualificationRecordedForceResult(t *testing.T) {
	data, err := os.ReadFile("testdata/recorded/force-idle.json")
	if err != nil {
		t.Fatal(err)
	}
	var clip struct {
		Public json.RawMessage
		Force  runner.Row
		Prompt runner.Row
	}
	if err := json.Unmarshal(data, &clip); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), clip.Public...)
	got, err := ParseForce(clip.Public)
	if err != nil {
		t.Fatal(err)
	}
	var public api.DeliveryResult
	_ = json.Unmarshal(clip.Public, &public)
	if got.Outcome != "handed_over" || got.Reason != "admitted" || got.ForceID != runner.Text(clip.Force, "force_id") || public.Class != "held" || public.Final || !bytes.Equal(before, clip.Public) {
		t.Fatalf("force admission rewrote immutable public Result: %+v / %+v", got, public)
	}
	if runner.Text(clip.Prompt, "order_kind") != "emission_complete" {
		t.Fatal("historical order provenance relabelled")
	}
	// The pre-revision order field is not passed off as today's wake-order
	// proof; revised two-fact ordering is exercised with explicit unit facts.
}

func TestQualificationRecordedResumeGuards(t *testing.T) {
	data, err := os.ReadFile("testdata/recorded/pause-resume.json")
	if err != nil {
		t.Fatal(err)
	}
	var clip struct {
		Before, After struct{ Spawns, Launches []runner.Row }
	}
	if err := json.Unmarshal(data, &clip); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"same-conversation-no-intake-proof", "startup-replay", "wrong-conversation", "old-process-not-exited"} {
		t.Run(kind, func(t *testing.T) {
			oldRow, newRow := clip.Before.Spawns[0], clip.After.Spawns[0]
			var ended, recorded int64
			for _, l := range clip.After.Launches {
				if runner.Text(l, "launch_token") == runner.Text(oldRow, "launch_token") {
					ended = runner.Number(l, "ended_at_ms")
				}
				if runner.Text(l, "launch_token") == runner.Text(newRow, "launch_token") {
					recorded = runner.Number(l, "recorded_at_ms")
				}
			}
			iid := runner.Text(oldRow, "instance_id")
			sid := runner.Text(oldRow, "session_id")
			old := OwnerEvidence{Owner: runner.Owned{InstanceID: iid, SessionID: sid, LaunchToken: runner.Text(oldRow, "launch_token"), Started: time.UnixMilli(ended - 10000), Exited: true, StoppedAt: time.UnixMilli(ended)}}
			fresh := OwnerEvidence{Owner: runner.Owned{InstanceID: iid, SessionID: runner.Text(newRow, "session_id"), LaunchToken: runner.Text(newRow, "launch_token"), Started: time.UnixMilli(recorded)}}
			c := Capture{Manifest: runner.Manifest{Cases: []runner.Case{{ID: "L2", InstanceID: iid, Text: "unit carried body"}}}, Commands: map[string]runner.Command{
				"stop":   {Name: "stop", Stdout: "stop", Started: time.UnixMilli(ended - 1), Ended: time.UnixMilli(ended + 1)},
				"resume": {Name: "resume", Stdout: "resume", Argv: []string{"ad", "resume"}, Started: time.UnixMilli(recorded - 1), Ended: time.UnixMilli(recorded + 1)}}, Owners: []OwnerEvidence{old, fresh}}
			if kind == "startup-replay" {
				cmd := c.Commands["resume"]
				cmd.Argv = append(cmd.Argv, "--prompt", "replay")
				c.Commands["resume"] = cmd
			}
			if kind == "wrong-conversation" {
				c.Owners[1].Owner.SessionID = "other"
			}
			if kind == "old-process-not-exited" {
				c.Owners[0].Owner.Exited = false
			}
			want := "fail"
			if kind == "same-conversation-no-intake-proof" {
				want = "not_completed"
			}
			if got := checkResume(c, "resume", "stop", "resume", "L2"); got.Status != want {
				t.Fatalf("%+v want %s", got, want)
			}
		})
	}
}
