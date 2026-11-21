package qualification_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

func namedResult(t *testing.T, rows []runner.Result, id string) runner.Result {
	t.Helper()
	for _, r := range rows {
		if r.SignalID == id {
			return r
		}
	}
	t.Fatalf("missing result %s", id)
	return runner.Result{}
}

func putCommand(f *qualificationfix.Fixture, c *q.Capture, name string, value any) string {
	ref := "raw/unit-" + name + ".json"
	f.Put(ref, value)
	c.Commands[ref] = runner.Command{Name: name, Stdout: ref}
	return ref
}

// These are explicitly synthetic fact-order/output-shape observations. They
// do not qualify real native hook ordering or replace the deferred live phase.
func TestQualificationUserHookJudgments(t *testing.T) {
	for _, kind := range []string{"block-and-context", "pre-sighting-block", "invented-replacement", "confirmed", "missing-context-intake", "unverified-end", "foreign-parent"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "user-hooks")
			c := f.Capture()
			c.Hooks = nil
			c.Owners = nil
			c.Manifest.Cases = nil
			start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
			for i, action := range []string{"block", "additionalContext"} {
				id := fmt.Sprintf("H%d", i+1)
				iid := "unit-" + id
				sid := "session-" + id
				prompt := "prompt-" + id
				text := "controlled-" + id
				owner := q.OwnerEvidence{Owner: runner.Owned{InstanceID: iid, SessionID: sid, LaunchToken: "launch-" + id, CWD: "/unit/" + id, Exited: true, StoppedAt: start.Add(5 * time.Second)}, TranscriptRef: "raw/unit-transcript"}
				if i == 0 {
					owner.Snapshot = runner.Snapshot{InstanceID: iid, Tables: map[string][]runner.Row{
						"messages": {{"message_id": "m", "instance_id": iid, "text": text}}, "members": {{"message_id": "m", "handover_id": "h", "prompt_index": 0}},
						"handovers": {{"handover_id": "h", "launch_token": owner.Owner.LaunchToken}}, "prompts": {{"handover_id": "h", "prompt_index": 0, "emission": "issued", "admission_kind": "prompt_sighting", "admitted_at_ms": start.Add(time.Second).UnixMilli(), "order_at_ms": start.Add(time.Second).UnixMilli()}}}}
				} else {
					b, _ := json.Marshal(q.HookContext)
					owner.Transcript.Requests = []signals.Request{{Time: start.Add(3 * time.Second), ID: "unit-request", Messages: []signals.Message{{Role: "system", Content: b}}}}
				}
				if kind == "unverified-end" {
					owner.Owner.Exited = false
				}
				if kind == "missing-context-intake" {
					owner.Transcript.Requests = nil
				}
				c.Owners = append(c.Owners, owner)
				c.Manifest.Cases = append(c.Manifest.Cases, runner.Case{ID: id, InstanceID: iid, Text: text})
				input := "raw/unit-hook-" + id + "/input.json"
				output := "raw/unit-hook-" + id + "/output.json"
				native := runner.Row{"hook_event_name": "UserPromptSubmit", "session_id": sid, "prompt_id": prompt, "prompt": text, "cwd": owner.Owner.CWD}
				f.Put(input, native)
				out := runner.Row{"decision": "block"}
				if i == 1 {
					out = runner.Row{"hookSpecificOutput": runner.Row{"hookEventName": "UserPromptSubmit", "additionalContext": q.HookContext}}
				}
				if kind == "invented-replacement" {
					out["updatedPrompt"] = "invented"
				}
				f.Put(output, out)
				rec := q.UserHookReceipt{Instance: iid, Session: sid, Prompt: prompt, Action: action, Started: start.Add(2 * time.Second), Written: start.Add(2500 * time.Millisecond), Input: input, Output: output}
				if kind == "pre-sighting-block" && i == 0 {
					rec.Written = start.Add(500 * time.Millisecond)
					rec.Started = start
				}
				if kind == "foreign-parent" {
					rec.Session = "foreign"
				}
				f.Put("raw/user-hook-fixtures/unit-"+id+"/receipt.json", rec)
				c.Hooks = append(c.Hooks, q.HookFact{Receipt: runner.Receipt{Event: "UserPromptSubmit", Received: start.Add(time.Second)}, Payload: native, Reference: input})
				class := "sent_unconfirmed"
				if kind == "confirmed" {
					class = "delivered_confirmed"
				}
				putCommand(f, &c, id, api.DeliveryResult{Class: class})
			}
			rows := q.CheckLifecycleAndHookLimits(c)
			block, context := namedResult(t, rows, "hook-block"), namedResult(t, rows, "hook-context")
			wantBlock, wantContext := "pass", "pass"
			switch kind {
			case "invented-replacement", "confirmed", "foreign-parent":
				wantBlock = "fail"
				wantContext = "fail"
			case "missing-context-intake":
				wantContext = "not_completed"
			case "unverified-end":
				wantBlock = "not_completed"
			}
			if block.Status != wantBlock || context.Status != wantContext {
				t.Fatalf("block=%+v context=%+v", block, context)
			}
			if kind == "block-and-context" {
				post := namedResult(t, rows, "post-sighting-hook")
				if post.Status != "pass" || post.Observed.(map[string]any)["accepted_limit"] != "user_hook_after_sighting" {
					t.Fatalf("post sighting limit lost: %+v", post)
				}
			}
			if kind == "pre-sighting-block" {
				post := namedResult(t, rows, "post-sighting-hook")
				if post.Status != "not_completed" {
					t.Fatalf("pre-sighting fixture became accepted post-sighting limit: %+v", post)
				}
			}
		})
	}
}

func TestQualificationEscEvidence(t *testing.T) {
	for _, kind := range []string{"bounded-absence", "notification", "short-absence", "unobserved", "wrong-session", "parent-stop", "empty-held-refusal", "wrong-held-reason"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "esc-gap")
			c := f.Capture()
			c.Hooks = nil
			owner := c.Owners[0]
			start := time.Unix(1800000000, 0).UTC()
			for round := 1; round <= 2; round++ {
				id := fmt.Sprintf("esc-%d", round)
				held := "E3"
				if round == 2 {
					held = "E4"
				}
				rec := q.EscObservation{ID: id, Instance: owner.Owner.InstanceID, Steer: fmt.Sprintf("steer-%d", round), Escape: start, Held: held, Delivery: api.DeliveryFacts{AwaitingTurnStart: 1}, Result: api.DeliveryResult{Class: "held", Wait: &api.DeliveryWait{Reason: "readiness_unknown"}}, Notification: q.NotificationWindow{Session: owner.Owner.SessionID, Started: start, Ended: start.Add(20 * time.Second), Observed: true}, Force: fmt.Sprintf("raw/unit-force-%d.json", round)}
				force := runner.Row{"force_result": runner.Row{"force_id": "unit-force", "outcome": "refused", "reason": "input_unresolved"}}
				switch kind {
				case "short-absence":
					rec.Notification.Ended = start.Add(time.Second)
				case "unobserved":
					rec.Notification.Observed = false
				case "wrong-session":
					rec.Notification.Session = "other"
				case "wrong-held-reason":
					rec.Result.Wait.Reason = "other"
				case "empty-held-refusal":
					force["force_result"].(runner.Row)["reason"] = "no_held_messages"
				case "parent-stop":
					c.Hooks = append(c.Hooks, q.HookFact{Receipt: runner.Receipt{Event: "Stop", Received: start.Add(time.Second)}, Payload: runner.Row{"session_id": owner.Owner.SessionID}})
				case "notification":
					h := q.HookFact{Receipt: runner.Receipt{Event: "Notification", Received: start.Add(time.Second)}, Payload: runner.Row{"session_id": owner.Owner.SessionID, "notification_type": "idle_prompt"}}
					rec.Notification.Notifications = []q.HookFact{h}
					c.Hooks = []q.HookFact{h}
				}
				f.Put(rec.Force, force)
				f.Put("raw/"+id+"-gap.json", rec)
			}
			rows := q.CheckEscRecovery(c)
			if kind == "empty-held-refusal" || kind == "wrong-held-reason" {
				if namedResult(t, rows, "esc-typed-recovery").Status != "fail" || namedResult(t, rows, "esc-resume-recovery").Status != "fail" {
					t.Fatal("wrong refusal qualified")
				}
				return
			}
			want := "pass"
			if kind == "short-absence" || kind == "unobserved" || kind == "wrong-session" {
				want = "not_completed"
			}
			if kind == "parent-stop" {
				want = "fail"
			}
			if got := namedResult(t, rows, "esc-notification"); got.Status != want {
				t.Fatalf("%+v want %s", got, want)
			}
			// Notification is observation only. Without each actual recovery and
			// complete pre-Esc input, neither recovery may become an accepted limit.
			for _, id := range []string{"esc-typed-recovery", "esc-resume-recovery", "esc-input-placement"} {
				if got := namedResult(t, rows, id); got.Status == "pass" {
					t.Fatalf("missing %s qualified: %+v", id, got)
				}
			}
		})
	}
}

func TestQualificationLifecycleReadOnly(t *testing.T) {
	for _, kind := range []string{"unchanged", "claimed-input"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "lifecycle")
			c := f.Capture()
			for _, id := range []string{"lifecycle-busy-held", "lifecycle-final-held"} {
				for _, side := range []string{"before", "after"} {
					ref := "raw/unit-" + id + "-" + side + ".json"
					s := runner.Snapshot{InstanceID: "unit", Tables: map[string][]runner.Row{}}
					if kind == "claimed-input" && side == "after" {
						s.Tables["handovers"] = []runner.Row{{"handover_id": "unexpected"}}
					}
					c.Snapshots[ref] = s
					c.Observations = append(c.Observations, q.Observation{Case: id + "-reads-" + side, Kind: "snapshot", Snapshot: ref})
				}
			}
			rows := q.CheckLifecycleAndHookLimits(c)
			want := "pass"
			if kind == "claimed-input" {
				want = "fail"
			}
			if got := namedResult(t, rows, "read-only"); got.Status != want {
				t.Fatalf("%+v", got)
			}
			for _, id := range []string{"lost-reply", "crash-resume", "fresh-reuse", "concurrent-delivery"} {
				if got := namedResult(t, rows, id); got.Status == "pass" {
					t.Fatalf("ordinary pause or missing callers invented lifecycle proof: %+v", got)
				}
			}
		})
	}
}

func TestQualificationLostReplyCustody(t *testing.T) {
	for _, kind := range []string{"unit-interrupted-caller", "wrong-repeat-pair", "duplicate-custody", "uninterrupted-reply", "receipt-mismatch", "missing-repeat"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "lifecycle")
			c := f.Capture()
			c.Manifest.Cases = []runner.Case{{ID: "L2", InstanceID: "unit-parent", Text: "unit lost-reply body"}}
			receipt := q.LostClientReceipt{PID: 1234, ProcessStart: "unit-process-start", Pair: "L2", Accepted: time.Unix(1800000000, 0).UTC()}
			interrupted := receipt
			lost := true
			if kind == "receipt-mismatch" {
				interrupted.ProcessStart = "other-process"
			}
			if kind == "uninterrupted-reply" {
				lost = false
			}
			f.Put("raw/lost-client-accepted.json", receipt)
			f.Put("raw/lost-client-interrupted.json", map[string]any{"Receipt": interrupted, "Command": runner.Command{ExitCode: 1}, "response_lost": lost})
			s := runner.Snapshot{InstanceID: "unit-parent", Tables: map[string][]runner.Row{"messages": {{"text": "unit lost-reply body"}}}}
			if kind == "duplicate-custody" {
				s.Tables["messages"] = append(s.Tables["messages"], s.Tables["messages"][0])
			}
			c.Snapshots["unit/accepted"] = s
			c.Observations = append(c.Observations, q.Observation{Case: "lost-client-durable-acceptance", Kind: "snapshot", Snapshot: "unit/accepted"})
			for i, name := range []string{"L2-repeat-before-crash", "L2-repeat-during-resume"} {
				pair := "L2"
				if kind == "wrong-repeat-pair" && i == 1 {
					pair = "other"
				}
				ref := putCommand(f, &c, name, api.DeliveryResult{CallerUUID: c.Manifest.RunID, RequestID: pair, Class: "held"})
				if kind == "missing-repeat" && i == 1 {
					delete(c.Commands, ref)
				}
			}
			var checks []runner.Result
			for _, r := range q.CheckLifecycleAndHookLimits(c) {
				if r.SignalID == "lost-reply" || strings.HasPrefix(r.SignalID, "lost-reply/") {
					checks = append(checks, r)
				}
			}
			v := runner.Verdict{Results: checks}
			v.Aggregate()
			want := "fail"
			if kind == "unit-interrupted-caller" {
				want = "pass"
			}
			if kind == "missing-repeat" {
				want = "not_completed"
			}
			if v.Status != want {
				t.Fatalf("lost-reply identity/custody: %+v; want %s", v, want)
			}
		})
	}
}
