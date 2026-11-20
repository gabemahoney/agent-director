package qualification_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

func evidenceTime(ms int64) time.Time { return time.UnixMilli(1800000000000 + ms).UTC() }

func putOwnedForceCommand(f *qualificationfix.Fixture, c *q.Capture, name string, response api.ForceResult, start, end int64) {
	ended := evidenceTime(end)
	response.StartedAt, response.EndedAt = evidenceTime(start), &ended
	ref := putCommand(f, c, name, runner.Row{"force_result": response})
	command := c.Commands[ref]
	command.Argv = []string{"ad", "force-delivery", "--instance-id", c.Owners[len(c.Owners)-1].Owner.InstanceID}
	command.Started, command.Ended = evidenceTime(start), ended
	c.Commands[ref] = command
}

// Two small physical-output units with complete custody, callback, journal and
// start joins. Synthetic metadata prevents this being a passing live capture.
func unitPhysicalCapture(t *testing.T, phase, mode string, ordinary bool) (*qualificationfix.Fixture, q.Capture) {
	t.Helper()
	f := qualificationfix.New(t, phase)
	c := f.Capture()
	plan := q.GroupPlan{Mode: mode, Texts: []string{strings.Repeat("a", 8000), strings.Repeat("b", 8000)}, Cases: []string{"F3", "F4"}, Parts: []store.BusySendPromptPart{{First: 0, Count: 1}, {First: 1, Count: 1}}}
	if ordinary {
		var err error
		plan, err = q.PlanGroup(mode)
		if err != nil {
			t.Fatal(err)
		}
	}
	iid := "live-" + c.Manifest.RunID + "-unit-" + phase
	o := runner.Owned{InstanceID: iid, SessionID: "unit-" + phase, LaunchID: "unit-launch", LaunchToken: "unit-token", Transcript: "/unit/journal", CWD: "/unit/phase", Started: evidenceTime(0), StoppedAt: evidenceTime(100), Exited: true, PID: 1, ProcessStart: "unit-process"}
	s := runner.Snapshot{InstanceID: iid, Observed: evidenceTime(90), ReadCompleted: evidenceTime(91), Tables: map[string][]runner.Row{
		"spawns":    {{"session_id": o.SessionID, "launch_token": o.LaunchToken, "state": "waiting"}},
		"launches":  {{"launch_id": o.LaunchID, "launch_token": o.LaunchToken, "provider": "claude", "ended_at_ms": evidenceTime(100).UnixMilli()}},
		"handovers": {{"handover_id": "unit-h", "instance_id": iid, "session_id": o.SessionID, "launch_token": o.LaunchToken, "boundary": "force", "force_id": "unit-force"}},
		"forces":    {{"force_id": "unit-force", "handover_id": "unit-h", "instance_id": iid, "launch_id": o.LaunchID, "outcome": "handed_over"}},
	}}
	if ordinary {
		s.Tables["handovers"][0]["boundary"] = "turn_end"
	}
	if mode == "steer_turn" {
		s.Tables["handovers"][0]["boundary"] = "tool_boundary"
	}
	c.Manifest.Cases = nil
	for i, text := range plan.Texts {
		part := 0
		if i >= plan.Parts[1].First {
			part = 1
		}
		s.Tables["messages"] = append(s.Tables["messages"], runner.Row{"message_id": plan.Cases[i], "instance_id": iid, "accepted_seq": int64(i + 1), "text": text, "text_sha256": runner.Digest([]byte(text)), "place": "handover"})
		s.Tables["members"] = append(s.Tables["members"], runner.Row{"handover_id": "unit-h", "message_id": plan.Cases[i], "member_index": int64(i), "prompt_index": int64(part)})
		c.Manifest.Cases = append(c.Manifest.Cases, runner.Case{ID: plan.Cases[i], InstanceID: iid, Text: text, Mode: mode, Started: evidenceTime(1), Ended: evidenceTime(2)})
	}
	var journal bytes.Buffer
	for i, part := range plan.Parts {
		issue := int64(10 + i*20)
		p := runner.Row{"handover_id": "unit-h", "prompt_index": int64(i), "message_count": int64(part.Count), "emission": "issued", "transport": "written", "issued_at_ms": evidenceTime(issue).UnixMilli(), "transport_at_ms": evidenceTime(issue + 2).UnixMilli(), "admission_kind": "prompt_sighting", "admitted_at_ms": evidenceTime(issue + 3).UnixMilli(), "order_kind": "ordered_acceptance", "order_at_ms": evidenceTime(issue + 3).UnixMilli()}
		var texts [][]byte
		for _, text := range plan.Texts[part.First : part.First+part.Count] {
			texts = append(texts, []byte(text))
		}
		body := string(store.RenderBusySendPromptBody(texts))
		role, input := "user", signals.NativeWake(q.Transform(body, "wait_turn"))
		if mode == "steer_turn" {
			p["order_kind"] = "emission_complete"
			p["order_at_ms"] = p["transport_at_ms"]
			role, input = "system", signals.NativeContextBody(body)
		}
		s.Tables["prompts"] = append(s.Tables["prompts"], p)
		b, _ := unitJournal(t, o.SessionID, fmt.Sprintf("unit-request-%d", i), role, input, evidenceTime(issue+4))
		journal.Write(b)
		if mode != "steer_turn" {
			hook := unitHook(t, f, o, "UserPromptSubmit", evidenceTime(issue+3), runner.Row{"transcript_path": o.Transcript, "prompt_id": fmt.Sprintf("unit-prompt-%d", i), "prompt": q.Transform(body, "wait_turn")})
			c.Hooks = append(c.Hooks, hook)
			c.After.Charges = append(c.After.Charges, q.Charge{ID: plan.Cases[part.First], Phase: phase, Attempt: c.After.Identity.Attempt, Launch: o.LaunchToken, Prompt: "physical@" + hook.Reference, Actual: 1, Reconciled: true})
		}
	}
	if ordinary {
		c.Hooks = append(c.Hooks, unitHook(t, f, o, "Stop", evidenceTime(20), nil))
		f.Put("raw/group-plans.json", []q.GroupPlan{plan})
	}
	tr, err := signals.ParseTranscript(journal.Bytes(), o.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	prefix := iid + "-" + o.LaunchToken
	ref, jref := "raw/final-"+prefix+"-store.json", "raw/"+prefix+"-transcript.jsonl"
	trail := []runner.Row{{"instance_id": iid, "event": "ad.hook.fired", "event_name": "UserPromptSubmit", "session_id": o.SessionID, "upsert_outcome": "updated"}}
	c.Owners = append(c.Owners, q.OwnerEvidence{Owner: o, Snapshot: s, SnapshotRef: ref, TranscriptRef: jref, Transcript: tr, Trail: trail})
	c.Snapshots[ref] = s
	c.Manifest.Owned = append(c.Manifest.Owned, o)
	c.Manifest.Finished = evidenceTime(101)
	f.PutBytes(jref, journal.Bytes())
	b, _ := json.Marshal(trail[0])
	f.PutBytes("raw/"+prefix+"-trail.jsonl", append(b, '\n'))
	return f, c
}

// Persist only through the existing adapter/index and production loader.
func sealUnitCapture(t *testing.T, f *qualificationfix.Fixture, c q.Capture) {
	t.Helper()
	f.Manifest = c.Manifest
	for _, o := range c.Owners {
		if o.SnapshotRef != "" {
			f.Put(o.SnapshotRef, o.Snapshot)
		}
	}
	f.Put("raw/qualification-ledger-before.json", c.Before)
	f.Put("raw/qualification-ledger-after.json", c.After)
	f.Put("raw/qualification-observations.json", c.Observations)
	var cmds bytes.Buffer
	enc := json.NewEncoder(&cmds)
	keys := make([]string, 0, len(c.Commands))
	for key := range c.Commands {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := enc.Encode(c.Commands[key]); err != nil {
			t.Fatal(err)
		}
	}
	f.PutBytes("commands.jsonl", cmds.Bytes())
	inv, err := runner.InspectRecordings(f.Root, f.Manifest.RunID, f.Manifest.RecordingHook.Events)
	if err != nil {
		t.Fatal(err)
	}
	f.Put("raw/hook-inventory.json", inv)
	f.JSON("manifest.json", f.Manifest)
}

func unitContract(t *testing.T) q.Contract {
	t.Helper()
	b, err := os.ReadFile("../../../docs/caller-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	c, err := q.ParseContract(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestQualificationForceStateEvidence(t *testing.T) {
	for _, kind := range []string{"idle-established", "changed-public-force-id", "idle-issued-before", "idle-missed-window", "not-started-established", "not-started-SessionStart", "not-started-missed-window"} {
		t.Run(kind, func(t *testing.T) {
			f, c := unitPhysicalCapture(t, "force-states", "wait_turn", false)
			o := &c.Owners[len(c.Owners)-1]
			// State control: one Held Message and one force, independently of
			// the two-prompt group exercised by the other tests below.
			o.Snapshot.Tables["messages"] = o.Snapshot.Tables["messages"][:1]
			o.Snapshot.Tables["members"] = o.Snapshot.Tables["members"][:1]
			o.Snapshot.Tables["prompts"] = o.Snapshot.Tables["prompts"][:1]
			o.Transcript.Requests = o.Transcript.Requests[:1]
			var hooks []q.HookFact
			for _, h := range c.Hooks {
				if runner.Text(h.Payload, "session_id") != o.Owner.SessionID || runner.Text(h.Payload, "prompt_id") != "unit-prompt-1" {
					hooks = append(hooks, h)
				}
			}
			c.Hooks = hooks
			state, command := "idle", "F1-idle-force"
			outcome, reason := "handed_over", "admitted"
			// The unit Message is unconfirmed, so it has no public carried pair.
			response := api.ForceResult{ForceID: "unit-force", Outcome: outcome, Reason: reason}
			if strings.HasPrefix(kind, "not-started") {
				state, command, outcome, reason = "not_started", "not-started-force", "refused", "agent_starting"
				response = api.ForceResult{ForceID: "unit-force", Outcome: outcome, Reason: reason}
				o.Snapshot.Tables["prompts"] = nil
				o.Snapshot.Tables["members"] = nil
				o.Snapshot.Tables["handovers"] = nil
				o.Snapshot.Tables["forces"][0]["outcome"] = outcome
				o.Snapshot.Tables["forces"][0]["reason"] = reason
				delete(o.Snapshot.Tables["forces"][0], "handover_id")
				o.Snapshot.Tables["messages"][0]["place"] = "held"
				o.Transcript.Requests = nil
				c.Hooks = c.Hooks[:len(c.Hooks)-1]
			}
			ended := evidenceTime(15)
			response.StartedAt, response.EndedAt = evidenceTime(5), &ended
			o.Snapshot.Tables["forces"][0]["reason"] = reason
			o.Snapshot.Tables["forces"][0]["started_at_ms"] = evidenceTime(5).UnixMilli()
			o.Snapshot.Tables["forces"][0]["ended_at_ms"] = ended.UnixMilli()
			ref := putCommand(f, &c, command, runner.Row{"force_result": response})
			cmd := c.Commands[ref]
			cmd.Argv = []string{"ad", "force-delivery", "--instance-id", o.Owner.InstanceID}
			cmd.Started = evidenceTime(5)
			cmd.Ended = evidenceTime(15)
			c.Commands[ref] = cmd
			body := c.Manifest.Cases[0].Text
			held := runner.Row{"message_id": "F3", "instance_id": o.Owner.InstanceID, "accepted_seq": int64(1), "text": body, "text_sha256": runner.Digest([]byte(body)), "place": "held"}
			before := runner.Snapshot{InstanceID: o.Owner.InstanceID, Observed: evidenceTime(3), ReadCompleted: evidenceTime(4), Tables: map[string][]runner.Row{"spawns": {{"state": "waiting", "session_id": o.Owner.SessionID}}, "launches": {{"launch_id": o.Owner.LaunchID}}, "messages": {held}}}
			if state == "not_started" {
				before.Tables["spawns"][0]["state"] = "pending"
			}
			after := before
			after.Observed = evidenceTime(16)
			after.ReadCompleted = evidenceTime(17)
			after.Tables = map[string][]runner.Row{"spawns": before.Tables["spawns"], "launches": before.Tables["launches"], "messages": {{}}}
			for key, value := range held {
				after.Tables["messages"][0][key] = value
			}
			if state == "idle" {
				after.Tables["messages"][0]["place"] = "handover"
			}
			c.Snapshots = map[string]runner.Snapshot{"unit/before": before, "unit/after": after}
			c.Manifest.Cases = append(c.Manifest.Cases, runner.Case{ID: "F1", InstanceID: o.Owner.InstanceID, Text: c.Manifest.Cases[0].Text, Mode: "wait_turn"})
			want, established := "pass", true
			if kind == "changed-public-force-id" {
				response.ForceID = "another-nonempty-force"
				f.Put(ref, runner.Row{"force_result": response})
				want = "fail"
			}
			if kind == "idle-issued-before" {
				o.Snapshot.Tables["prompts"][0]["issued_at_ms"] = evidenceTime(1).UnixMilli()
				want, established = "not_completed", false
			}
			if kind == "not-started-SessionStart" {
				c.Hooks = append(c.Hooks, q.HookFact{Receipt: runner.Receipt{Event: "SessionStart", Received: evidenceTime(1)}, Payload: runner.Row{"session_id": o.Owner.SessionID}})
				want, established = "not_completed", false
			}
			if strings.HasSuffix(kind, "missed-window") {
				f.Put("raw/F1-missed-window.json", runner.Row{"State": state})
				want = "not_completed"
			}
			if got := q.ForceStateEstablishedForTest(c, state, cmd, before, true); got != established {
				t.Fatalf("state derivation=%t want %t", got, established)
			}
			got := q.ForceStateForTest(c, unitContract(t), state, command, "F1")
			if got.Status != want {
				t.Fatalf("derived force: %+v want %s", got, want)
			}
		})
	}
}

func TestQualificationPendingForceEvidence(t *testing.T) {
	for _, kind := range []string{"released-tail-first-F3", "wrong-pending", "wrong-concurrent-refusal", "tail-not-held", "released-tail-written"} {
		t.Run(kind, func(t *testing.T) {
			f, c := unitPhysicalCapture(t, "force-states", "wait_turn", false)
			pending := api.ForceResult{ForceID: "old-force", Outcome: "pending", Reason: "awaiting_admission"}
			second := api.ForceResult{ForceID: "second-force", Outcome: "refused", Reason: "handover_in_progress"}
			o := &c.Owners[len(c.Owners)-1]
			// The pending F2 and refused concurrent call are distinct from the
			// later unit-force that first physically issues the returned F3.
			o.Snapshot.Tables["forces"] = append(o.Snapshot.Tables["forces"],
				runner.Row{"force_id": "old-force", "handover_id": "old-F2-tail", "instance_id": o.Owner.InstanceID, "launch_id": o.Owner.LaunchID, "outcome": "pending", "reason": "awaiting_admission"},
				runner.Row{"force_id": "second-force", "instance_id": o.Owner.InstanceID, "launch_id": o.Owner.LaunchID, "outcome": "refused", "reason": "handover_in_progress"})
			o.Snapshot.Tables["handovers"] = append(o.Snapshot.Tables["handovers"], runner.Row{"handover_id": "old-F2-tail", "force_id": "old-force", "instance_id": o.Owner.InstanceID, "session_id": o.Owner.SessionID, "launch_token": o.Owner.LaunchToken, "boundary": "force"})
			if kind == "wrong-pending" {
				pending.Reason = "another-wait"
			}
			if kind == "wrong-concurrent-refusal" {
				second.Reason = "no_held_messages"
			}
			putOwnedForceCommand(f, &c, "F2-force", pending, 1, 3)
			putOwnedForceCommand(f, &c, "second-force-refusal", second, 4, 5)
			putOwnedForceCommand(f, &c, "F3-F4-force", api.ForceResult{ForceID: "unit-force", Outcome: "handed_over", Reason: "admitted"}, 8, 35)
			tail := runner.Snapshot{Tables: map[string][]runner.Row{"messages": {{"text": c.Manifest.Cases[0].Text, "place": "held"}}, "prompts": {{"emission": "released", "transport": ""}}}}
			if kind == "tail-not-held" {
				tail.Tables["messages"][0]["place"] = "handover"
			}
			if kind == "released-tail-written" {
				tail.Tables["prompts"][0]["transport"] = "written"
			}
			c.Observations = append(c.Observations, q.Observation{Case: "F2-tail-return", Kind: "snapshot", Snapshot: "unit/tail"})
			c.Snapshots["unit/tail"] = tail
			want := "fail"
			if kind == "released-tail-first-F3" {
				want = "pass"
			}
			if kind == "tail-not-held" {
				want = "not_completed"
			}
			if got := q.PendingForceForTest(c); got.Status != want {
				t.Fatalf("pending join: %+v want %s", got, want)
			}
			if kind == "released-tail-first-F3" {
				o.Snapshot.Tables["members"] = append(o.Snapshot.Tables["members"], runner.Row{"handover_id": "old-F2-tail", "message_id": "F3", "member_index": int64(0), "prompt_index": int64(0)})
				o.Snapshot.Tables["prompts"] = append(o.Snapshot.Tables["prompts"], runner.Row{"handover_id": "old-F2-tail", "prompt_index": int64(0), "message_count": int64(1), "emission": "released", "transport": ""})
				if got := q.ForceGroupForTest(c); got.Status != "pass" {
					t.Fatalf("first physical F3 after release: %+v", got)
				}
			}
		})
	}
}

// R13-1: an early-finished workload cannot manufacture a pending-force
// requirement; an actually eligible force missing its sighting still fails.
func TestQualificationForceWorkloadPreconditions(t *testing.T) {
	for _, kind := range []string{"tool-finished-early", "eligible-no-sighting"} {
		t.Run(kind, func(t *testing.T) {
			f, c := unitPhysicalCapture(t, "force-states", "wait_turn", false)
			o := &c.Owners[len(c.Owners)-1]
			c.Hooks = append(c.Hooks, unitHook(t, f, o.Owner, "PreToolUse", evidenceTime(1), runner.Row{"tool_name": "Bash", "tool_use_id": "unit-long", "tool_input": runner.Row{"command": strings.Split(q.LongToolForTest, "`")[1]}}))
			if kind == "tool-finished-early" {
				c.Hooks = append(c.Hooks, unitHook(t, f, o.Owner, "PostToolBatch", evidenceTime(2), runner.Row{"tool_calls": []runner.Row{{"tool_use_id": "unit-long", "tool_name": "Bash"}}}))
				putOwnedForceCommand(f, &c, "F2-force", api.ForceResult{ForceID: "unit-force", Outcome: "handed_over", Reason: "admitted"}, 3, 15)
				got := q.PendingForceForTest(c)
				if got.Status != "not_completed" || !(strings.Contains(strings.ToLower(got.Reason), "precondition") || strings.Contains(strings.ToLower(got.Reason), "tool")) {
					t.Errorf("finished workload must name precondition-not-established, not pending failure: status=%s reason=%s", got.Status, got.Reason)
				}
				return
			}
			for _, p := range o.Snapshot.Tables["prompts"] {
				delete(p, "admitted_at_ms")
				delete(p, "order_at_ms")
			}
			var hooks []q.HookFact
			for _, h := range c.Hooks {
				if runner.Text(h.Payload, "session_id") != o.Owner.SessionID || h.Receipt.Event != "UserPromptSubmit" {
					hooks = append(hooks, h)
				}
			}
			c.Hooks = hooks
			putOwnedForceCommand(f, &c, "F2-force", api.ForceResult{ForceID: "unit-force", Outcome: "pending", Reason: "awaiting_admission"}, 3, 15)
			got := q.ForceStateForTest(c, unitContract(t), "tool_running", "F2-force", "F3")
			if got.Status != "fail" || !strings.Contains(got.Reason, "stop for MI") {
				t.Fatalf("eligible force product failure hidden: %+v", got)
			}
		})
	}
}

func TestQualificationForceGroupEvidence(t *testing.T) {
	for _, kind := range []string{"one-force-two-physical-prompts", "missing-sighting", "missing-whole", "missing-start", "duplicate-output"} {
		t.Run(kind, func(t *testing.T) {
			f, c := unitPhysicalCapture(t, "force-states", "wait_turn", false)
			putOwnedForceCommand(f, &c, "F3-F4-force", api.ForceResult{ForceID: "unit-force", Outcome: "handed_over", Reason: "admitted"}, 8, 35)
			o := &c.Owners[len(c.Owners)-1]
			want := "fail"
			switch kind {
			case "one-force-two-physical-prompts":
				want = "pass"
			case "missing-sighting":
				o.Snapshot.Tables["prompts"][1]["admission_kind"] = ""
			case "missing-whole":
				o.Transcript.Requests = o.Transcript.Requests[:1]
				want = "not_completed"
			case "missing-start":
				c.After.Charges = c.After.Charges[:1]
			case "duplicate-output":
				body, err := q.PromptBody(o.Snapshot, o.Snapshot.Tables["prompts"][0])
				if err != nil {
					t.Fatal(err)
				}
				c.Hooks = append(c.Hooks, unitHook(t, f, o.Owner, "UserPromptSubmit", evidenceTime(53), runner.Row{"transcript_path": o.Owner.Transcript, "prompt_id": "unit-duplicate-output", "prompt": q.Transform(body, "wait_turn")}))
				_, again := unitJournal(t, o.Owner.SessionID, "unit-duplicate-request", "user", signals.NativeWake(q.Transform(body, "wait_turn")), evidenceTime(54))
				o.Transcript.Requests = append(o.Transcript.Requests, again.Requests...)
			}
			if got := q.ForceGroupForTest(c); got.Status != want {
				t.Fatalf("physical group: %+v want %s", got, want)
			}
		})
	}
}

func TestQualificationPublishedCapEvidence(t *testing.T) {
	for _, mode := range []string{"wait_turn", "steer_turn"} {
		for _, kind := range []string{"whole-published-cap", "wrong-body-size", "missing-next-input"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				_, c := unitPhysicalCapture(t, "force-states", mode, false)
				want := "pass"
				if kind == "wrong-body-size" {
					c.Manifest.Cases[0].Text += "x"
					o := &c.Owners[len(c.Owners)-1]
					text := c.Manifest.Cases[0].Text
					o.Snapshot.Tables["messages"][0]["text"] = text
					o.Snapshot.Tables["messages"][0]["text_sha256"] = runner.Digest([]byte(text))
					body, err := q.PromptBody(o.Snapshot, o.Snapshot.Tables["prompts"][0])
					if err != nil {
						t.Fatal(err)
					}
					role, input := "user", signals.NativeWake(q.Transform(body, "wait_turn"))
					if mode == "steer_turn" {
						role, input = "system", signals.NativeContextBody(body)
					}
					_, whole := unitJournal(t, o.Owner.SessionID, "unit-request-0", role, input, evidenceTime(14))
					o.Transcript.Requests[0] = whole.Requests[0]
					want = "fail"
				}
				if kind == "missing-next-input" {
					c.Owners[len(c.Owners)-1].Transcript.Requests = nil
					want = "not_completed"
				}
				if got := q.CheckCapForTest(c, "F3", mode); got.Status != want {
					t.Fatalf("AD cap: %+v want %s", got, want)
				}
			})
		}
	}
}

func TestQualificationBacklogPublicRecords(t *testing.T) {
	for _, kind := range []string{"bound-originals", "changed-public-acceptance", "changed-public-refusal", "found-refused-pair", "unknown-with-stored-ID", "missing-lookup"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "text-limits-backlog")
			c := f.Capture()
			p := q.SyntheticBacklogForTest(t)
			for i := range p.Accepted {
				a := &p.Accepted[i]
				a.Result.CallerUUID = c.Manifest.RunID
				text, err := q.Corpus(a.ID, 1900)
				if err != nil {
					t.Fatal(err)
				}
				ref := putCommand(f, &c, a.ID, a.Result)
				a.Output = ref
				cmd := c.Commands[ref]
				cmd.Argv = []string{"ad", "send-keys", "--text", text, "--delivery-request-id", a.ID}
				c.Commands[ref] = cmd
			}
			for _, r := range p.Refusals {
				details := runner.Row{"class": "cant_send", "reason": r.Reason, "limit": runner.Row{"actual": r.Actual, "allowed": r.Allowed}, "retry_with": r.RetryWith, "resend_safe": r.ResendSafe}
				ref := putCommand(f, &c, r.Case, runner.Row{"err_details": details})
				cmd := c.Commands[ref]
				cmd.ExitCode = 1
				c.Commands[ref] = cmd
				putCommand(f, &c, r.Case+"-unclaimed-lookup", api.GetDeliveryResult{Lookup: "unknown"})
			}
			want := "fail"
			switch kind {
			case "bound-originals":
				want = "pass"
			case "changed-public-acceptance":
				value := p.Accepted[0].Result
				value.Wait = &api.DeliveryWait{Reason: "another-wait"}
				f.Put(p.Accepted[0].Output, value)
			case "changed-public-refusal":
				r := p.Refusals[0]
				f.Put("raw/unit-"+r.Case+".json", runner.Row{"err_details": runner.Row{"class": "cant_send", "reason": r.Reason, "limit": runner.Row{"actual": r.Actual + 1, "allowed": r.Allowed}, "retry_with": r.RetryWith, "resend_safe": r.ResendSafe}})
			case "found-refused-pair", "unknown-with-stored-ID":
				value := api.GetDeliveryResult{Lookup: "unknown", Result: &api.DeliveryResult{RequestID: "invented-stored-ID", Class: "held"}}
				if kind == "found-refused-pair" {
					value.Lookup = "found"
				}
				ref := "raw/unit-" + p.Refusals[0].Case + "-unclaimed-lookup.json"
				f.Put(ref, value)
			case "missing-lookup":
				delete(c.Commands, "raw/unit-"+p.Refusals[0].Case+"-unclaimed-lookup.json")
				want = "not_completed"
			}
			if got := q.VerifyBacklogRecordsForTest(c, p); got.Status != want {
				t.Fatalf("original public proof join: %+v want %s", got, want)
			}
		})
	}
}
