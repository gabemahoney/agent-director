package signals

// t3.xis.w3.ci.ai: synthetic boundary controls carried from repairs 12/13.
import (
	"encoding/json"
	"errors"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"strings"
	"testing"
	"time"
)

// Repair 11: request order is conversation causality, not file order. The
// real request (line 46) precedes its terminal display row (line 47).
func TestClaudeLiveContinuationCausality(t *testing.T) {
	for _, kind := range []string{"request-before-terminal", "inherited-history", "old-pair", "duplicate-candidate", "third-copy", "two-feedbacks", "changed-reason", "foreign-command", "missing-terminal", "terminal-after-feedback", "duplicate-terminal", "missing-feedback", "missing-error", "wrong-role", "unknown-variant", "unbound-plan", "old-reason-new-variant"} {
		t.Run(kind, func(t *testing.T) {
			c, continued := diag14Capture(t)
			p, _, e := c.prompt(ContinuationWakeOrder)
			if e != nil {
				t.Fatal(e)
			}
			complete := runner.Number(p, "transport_at_ms")
			stop := c.nextStop(complete).UnixMilli()
			active, terminal, e := c.textWindow(complete, stop)
			if e != nil {
				t.Fatal(e)
			}
			x, _ := c.caseRecord(Continuation)
			ri := -1
			for i := range c.t.Requests {
				if c.t.Requests[i].ID == continued.ID {
					ri = i
				}
			}
			if ri < 0 {
				t.Fatal("missing continuation")
			}
			r := &c.t.Requests[ri]
			feedback, blocking, terminalIndex := -1, -1, -1
			for i, m := range r.Messages {
				texts, _ := Texts(m)
				for _, s := range texts {
					if strings.HasPrefix(s, "Stop hook feedback:") {
						feedback = i
					}
					if strings.HasPrefix(s, "Stop hook blocking error from command:") {
						blocking = i
					}
				}
				if m.Role == "assistant" {
					terminalIndex = i
				}
			}
			if feedback < 0 || blocking < 0 || terminalIndex < 0 || continued.Line >= terminal {
				t.Fatalf("captured causal pair missing: %d %d %d %d/%d", feedback, blocking, terminalIndex, continued.Line, terminal)
			}
			alter := func(index int, old, new string) {
				r.Messages[index].Content = json.RawMessage(strings.ReplaceAll(string(r.Messages[index].Content), old, new))
			}
			switch kind {
			case "request-before-terminal":
			case "inherited-history", "duplicate-candidate":
				duplicate := *r
				duplicate.ID = "synthetic-next"
				duplicate.Time = duplicate.Time.Add(time.Millisecond)
				duplicate.Line = 999
				keep := len(duplicate.Messages)
				if kind == "duplicate-candidate" {
					keep = 0
				}
				c.t.Requests = append(c.t.Requests, duplicate)
				c.t.Rows = append(c.t.Rows, TranscriptRow{Type: "api-request", ID: duplicate.ID, Keep: keep})
			case "old-pair":
				c.m.ContinuationHook.Variant = ""
				c.m.ProbePlan = nil
				for i := range c.t.Requests {
					for j := range c.t.Requests[i].Messages {
						m := &c.t.Requests[i].Messages[j]
						var content any
						if e = json.Unmarshal(m.Content, &content); e != nil {
							t.Fatal(e)
						}
						content = replaceText(content, runner.ContinuationToolReason, runner.ContinuationReason)
						content = replaceText(content, " --variant held-steer-v1", "")
						m.Content, _ = json.Marshal(content)
					}
				}
			case "third-copy":
				r.Messages = append(r.Messages, r.Messages[blocking])
			case "two-feedbacks":
				r.Messages = append(r.Messages, r.Messages[feedback])
			case "changed-reason":
				alter(feedback, "Continue this bounded", "CONTINUE this bounded")
			case "foreign-command":
				alter(blocking, "continue-stop", "foreign-stop")
			case "missing-terminal":
				r.Messages[terminalIndex].Content = json.RawMessage(`[{"type":"text","text":"different terminal"}]`)
			case "terminal-after-feedback":
				r.Messages[terminalIndex], r.Messages[feedback] = r.Messages[feedback], r.Messages[terminalIndex]
			case "duplicate-terminal":
				r.Messages = append(r.Messages, r.Messages[terminalIndex])
			case "missing-feedback":
				r.Messages[feedback].Content = json.RawMessage(`[]`)
			case "missing-error":
				r.Messages[blocking].Content = json.RawMessage(`[]`)
			case "wrong-role":
				r.Messages[feedback].Role = "system"
			case "unknown-variant":
				c.m.ContinuationHook.Variant = "unknown"
			case "unbound-plan":
				c.m.ProbePlan = nil
			case "old-reason-new-variant":
				for j := range r.Messages {
					var content any
					if e = json.Unmarshal(r.Messages[j].Content, &content); e != nil {
						t.Fatal(e)
					}
					r.Messages[j].Content, _ = json.Marshal(replaceText(content, runner.ContinuationToolReason, runner.ContinuationReason))
				}
			}
			got, obs, e := c.continuationRequest(active.ID, terminal, stop, x.ObservedUntil)
			good := kind == "request-before-terminal" || kind == "inherited-history" || kind == "old-pair"
			if good && (e != nil || got.ID != continued.ID) {
				t.Fatalf("causal pair rejected: %v %+v", e, obs)
			}
			if !good && e == nil {
				t.Fatalf("ambiguous/changed pair accepted: %+v", obs)
			}
		})
	}
}

// A wholly synthetic continuationHold input, with real prompt/Message links,
// two completed tools, indexed read intervals and exact whole request inputs.
// It exercises the step-7 predicate; it is not a claimed provider observation.
func diagHeldContinuation(t *testing.T, kind string) (capture, Request, time.Time) {
	t.Helper()
	base := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return base.Add(time.Duration(seconds) * time.Second) }
	ms := func(seconds int) int64 { return at(seconds).UnixMilli() }
	raw := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	text := func(role, s string) Message {
		return Message{Role: role, Content: raw([]Block{{Type: "text", Text: s}})}
	}
	o := runner.Owned{InstanceID: "synthetic", LaunchID: "launch", LaunchToken: "token", SessionID: "session", Transcript: "/synthetic/transcript"}
	c := capture{root: t.TempDir(), commands: map[string]runner.Command{}, hooks: map[string][]runner.Row{}}
	c.m = runner.Manifest{Started: base, Finished: at(100), Owned: []runner.Owned{o}, Artifacts: map[string]runner.Artifact{}}
	c.s = runner.Snapshot{Observed: at(90), ReadCompleted: at(91), InstanceID: o.InstanceID, Tables: map[string][]runner.Row{
		"spawns":   {{"session_id": o.SessionID, "launch_token": o.LaunchToken}},
		"launches": {{"launch_id": o.LaunchID, "launch_token": o.LaunchToken, "provider": "claude"}},
		"forces":   {{"force_id": "force", "handover_id": "wake", "instance_id": o.InstanceID, "launch_id": o.LaunchID}},
	}}
	for i, item := range []struct {
		id, text, mode, handover, boundary string
		accepted, issued, written          int
	}{
		{Continuation, ContinuationWakeOrder, "wait_turn", "wake", "force", 5, 6, 7},
		{ContinuationSteerCase, ContinuationSteer, "steer_turn", "steer", "tool_boundary", 10, 71, 72},
	} {
		c.m.Cases = append(c.m.Cases, runner.Case{ID: item.id, RequestID: item.id, Command: item.id, InstanceID: o.InstanceID, Text: item.text, Mode: item.mode, Ready: true, Started: at(item.accepted - 1), ObservedUntil: at(100)})
		c.commands[item.id] = runner.Command{Name: item.id, Started: at(item.accepted - 1), Ended: at(item.accepted + 1), Argv: []string{"send-keys", "--instance-id", o.InstanceID, "--text", item.text, "--delivery-request-id", item.id}}
		c.s.Tables["messages"] = append(c.s.Tables["messages"], runner.Row{"message_id": item.handover, "instance_id": o.InstanceID, "text": item.text, "text_sha256": runner.Digest([]byte(item.text)), "accepted_seq": int64(i + 1), "accepted_at_ms": ms(item.accepted), "send_mode": item.mode, "place": "handover"})
		c.s.Tables["handovers"] = append(c.s.Tables["handovers"], runner.Row{"handover_id": item.handover, "instance_id": o.InstanceID, "launch_id": o.LaunchID, "launch_token": o.LaunchToken, "session_id": o.SessionID, "boundary": item.boundary, "force_id": "force"})
		c.s.Tables["members"] = append(c.s.Tables["members"], runner.Row{"message_id": item.handover, "handover_id": item.handover, "prompt_index": int64(0)})
		c.s.Tables["prompts"] = append(c.s.Tables["prompts"], runner.Row{"handover_id": item.handover, "prompt_index": int64(0), "emission": "issued", "message_count": int64(1), "issued_at_ms": ms(item.issued), "transport": "written", "transport_at_ms": ms(item.written), "order_kind": "emission_complete", "order_at_ms": ms(item.written)})
	}
	wake := c.s.Tables["prompts"][0]
	wake["admission_kind"], wake["admitted_at_ms"], wake["order_kind"], wake["order_at_ms"] = "prompt_sighting", ms(60), "ordered_acceptance", ms(60)
	hook := func(event, parent string, seconds int) runner.Row {
		return runner.Row{"hook_event_name": event, "session_id": o.SessionID, "transcript_path": o.Transcript, "prompt_id": parent, "_received": at(seconds).Format(time.RFC3339Nano)}
	}
	trail := func(event string, seconds int) runner.Row {
		return runner.Row{"event": "ad.hook.fired", "event_name": event, "instance_id": o.InstanceID, "session_id": o.SessionID, "upsert_outcome": "no_change", "ts": at(seconds).Format(time.RFC3339Nano)}
	}
	ups := hook("UserPromptSubmit", "wake-parent", 60)
	ups["prompt"] = Render(ContinuationWakeOrder, "wait_turn")
	c.hooks["UserPromptSubmit"] = []runner.Row{ups}
	c.trail = []runner.Row{trail("Stop", 25), trail("UserPromptSubmit", 60)}
	for i, item := range []struct {
		id, parent, program                        string
		request, start, completed, batch, ad, next int
	}{
		{"sibling", "original-parent", runner.ContinuationToolProgram, 26, 30, 40, 42, 43, 45},
		{"wake", "wake-parent", BusyProgram, 61, 62, 70, 72, 74, 76},
	} {
		result := raw([]runner.Row{{"type": "tool_result", "tool_use_id": item.id, "content": ""}})
		req := Request{ID: item.id, Line: 10 + i*10, Time: at(item.request)}
		if i == 1 {
			req.Messages = []Message{text("user", NativeWake(Render(ContinuationWakeOrder, "wait_turn")))}
		}
		next := Request{ID: item.id + "-next", Line: req.Line + 3, Time: at(item.next), Messages: []Message{{Role: "user", Content: result}}}
		if i == 1 {
			next.Messages = append(next.Messages, text("system", NativeContext(ContinuationSteer)))
		}
		c.t.Requests = append(c.t.Requests, req, next)
		c.t.Rows = append(c.t.Rows,
			TranscriptRow{Type: "assistant", Line: req.Line + 1, RequestRef: req.ID, Timestamp: at(item.start), Message: Message{Content: raw([]runner.Row{{"type": "tool_use", "id": item.id, "name": "Bash", "input": runner.Row{"command": item.program}}})}},
			TranscriptRow{Type: "user", Line: req.Line + 2, Timestamp: at(item.completed), Message: Message{Content: result}},
			TranscriptRow{Type: "assistant", Line: req.Line + 4, RequestRef: next.ID, Timestamp: at(item.next + 1), Message: Message{StopReason: "end_turn"}})
		pre, batch := hook("PreToolUse", item.parent, item.start), hook("PostToolBatch", item.parent, item.batch)
		pre["tool_use_id"], pre["tool_name"], pre["tool_input"] = item.id, "Bash", map[string]any{"command": item.program}
		batch["tool_calls"] = []any{map[string]any{"tool_use_id": item.id, "tool_name": "Bash", "tool_input": map[string]any{"command": item.program}}}
		c.hooks["PreToolUse"] = append(c.hooks["PreToolUse"], pre)
		c.hooks["PostToolBatch"] = append(c.hooks["PostToolBatch"], batch)
		c.trail = append(c.trail, trail("PostToolBatch", item.ad))
	}
	c.t.Rows = append(c.t.Rows, TranscriptRow{Timestamp: at(75), Attachment: TranscriptAttachment{Event: "PostToolBatch", Stdout: string(raw(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "PostToolBatch", "additionalContext": Prompt(ContinuationSteer)}}))}})
	initial := diagCopy(c.s)
	initial.Observed, initial.ReadCompleted = at(12), at(13)
	initial.Tables["messages"][1]["place"] = "held"
	initial.Tables["prompts"] = initial.Tables["prompts"][:1]
	initial.Tables["handovers"] = initial.Tables["handovers"][:1]
	initial.Tables["members"] = initial.Tables["members"][:1]
	hw := initial.Tables["prompts"][0]
	hw["order_kind"], hw["order_at_ms"], hw["admission_kind"], hw["admitted_at_ms"] = nil, nil, nil, nil
	after := diagCopy(initial)
	after.Observed, after.ReadCompleted = at(44), at(44).Add(100*time.Millisecond)
	switch kind {
	case "late-acceptance":
		// Terminal=20, Stop begins=21, acceptance=22, read=23..24,
		// trail=25: all old predicates accepted this missing pre-Stop case.
		for _, s := range []runner.Snapshot{c.s, initial, after} {
			s.Tables["messages"][1]["accepted_at_ms"] = ms(22)
		}
		cmd := c.commands[ContinuationSteerCase]
		cmd.Started, cmd.Ended = at(21), at(23)
		c.commands[ContinuationSteerCase] = cmd
		initial.Observed, initial.ReadCompleted = at(23), at(24)
	case "read-finishes-after-terminal":
		initial.ReadCompleted = at(23)
	case "read-finishes-in-terminal-ms":
		initial.ReadCompleted = at(20).Add(-100 * time.Microsecond)
	case "read-finishes-at-terminal":
		initial.ReadCompleted = at(20)
	case "missing-read-upper-bound":
		initial.ReadCompleted = time.Time{}
	case "invalid-read-interval":
		initial.ReadCompleted = at(11)
	case "early-passive-batch":
		c.hooks["PostToolBatch"][0]["_received"] = at(31).Format(time.RFC3339Nano)
	case "early-ad-batch":
		c.trail[2]["ts"] = at(32).Format(time.RFC3339Nano)
	case "passive-in-completion-ms":
		c.hooks["PostToolBatch"][0]["_received"] = at(40).Add(100 * time.Microsecond).Format(time.RFC3339Nano)
	case "ad-at-completion":
		c.trail[2]["ts"] = at(40).Format(time.RFC3339Nano)
	case "snapshot-before-completion":
		after.Observed, after.ReadCompleted = at(39), at(44)
	case "early-batches-and-snapshot":
		c.hooks["PostToolBatch"][0]["_received"] = at(31).Format(time.RFC3339Nano)
		c.trail[2]["ts"] = at(32).Format(time.RFC3339Nano)
		after.Observed, after.ReadCompleted = at(33), at(34)
	case "snapshot-in-ad-ms":
		after.Observed, after.ReadCompleted = at(43).Add(100*time.Microsecond), at(44)
	case "issue-during-wake-tool":
		c.s.Tables["prompts"][1]["issued_at_ms"] = ms(63)
	case "issue-at-completion":
		c.s.Tables["prompts"][1]["issued_at_ms"] = ms(70)
	case "early-wake-passive-batch":
		c.hooks["PostToolBatch"][1]["_received"] = at(63).Format(time.RFC3339Nano)
	case "early-wake-ad-batch":
		c.trail[3]["ts"] = at(64).Format(time.RFC3339Nano)
	case "envelope-during-wake-tool":
		c.t.Rows[len(c.t.Rows)-1].Timestamp = at(63)
	case "envelope-at-next-request":
		c.t.Rows[len(c.t.Rows)-1].Timestamp = at(76)
	case "different-request-same-time":
		next := c.t.Requests[3]
		c.t.Requests = append(c.t.Requests[:3], Request{ID: "unrelated", Line: 22, Time: next.Time, Messages: []Message{text("system", NativeContext(ContinuationSteer))}}, next)
	case "passive-after-ad":
		c.hooks["PostToolBatch"][0]["_received"] = at(43).Add(10 * time.Millisecond).Format(time.RFC3339Nano)
	}
	firstSighted := diagCopy(c.s)
	firstSighted.Observed, firstSighted.ReadCompleted = at(65), at(66)
	firstSighted.Tables["messages"][1]["place"] = "held"
	firstSighted.Tables["prompts"] = firstSighted.Tables["prompts"][:1]
	firstSighted.Tables["handovers"] = firstSighted.Tables["handovers"][:1]
	firstSighted.Tables["members"] = firstSighted.Tables["members"][:1]
	diagSave(t, &c, "raw/first-sighted-store.json", firstSighted)
	diagSave(t, &c, "raw/initial-held-store.json", initial)
	diagSave(t, &c, "raw/after-tool-held-store.json", after)
	finish := at(20)
	if kind == "read-finishes-in-terminal-ms" {
		finish = at(20).Add(-50 * time.Microsecond)
	}
	return c, c.t.Requests[0], finish
}

func TestClaudeLiveContinuationBoundaries(t *testing.T) {
	for _, kind := range []string{"positive", "passive-after-ad", "late-acceptance", "read-finishes-after-terminal", "read-finishes-in-terminal-ms", "read-finishes-at-terminal", "missing-read-upper-bound", "invalid-read-interval", "early-passive-batch", "early-ad-batch", "passive-in-completion-ms", "ad-at-completion", "snapshot-before-completion", "early-batches-and-snapshot", "snapshot-in-ad-ms", "issue-during-wake-tool", "issue-at-completion", "early-wake-passive-batch", "early-wake-ad-batch", "envelope-during-wake-tool", "envelope-at-next-request", "different-request-same-time"} {
		t.Run(kind, func(t *testing.T) {
			c, next, finish := diagHeldContinuation(t, kind)
			obs, err := c.continuationHold(map[string]any{}, next, "original-parent", finish)
			if kind == "positive" || kind == "passive-after-ad" || kind == "snapshot-in-ad-ms" {
				if err != nil {
					t.Fatalf("valid step 7: %v, %+v", err, obs)
				}
				return
			}
			if err == nil {
				t.Fatalf("counter-history qualified step 7: %+v", obs)
			}
			if kind == "late-acceptance" || strings.HasPrefix(kind, "read-finishes") || kind == "missing-read-upper-bound" || kind == "snapshot-before-completion" || kind == "snapshot-in-ad-ms" {
				if !errors.Is(err, errHeldContinuation) {
					t.Fatalf("want not_completed: %v, %+v", err, obs)
				}
			} else if kind == "invalid-read-interval" {
				if !strings.Contains(err.Error(), "invalid store observation read interval") {
					t.Fatal(err)
				}
			} else if strings.Contains(kind, "batch") || kind == "passive-in-completion-ms" || kind == "ad-at-completion" {
				if !strings.Contains(err.Error(), "PostToolBatch is not strictly after") {
					t.Fatalf("wrong boundary failed: %v", err)
				}
			} else if !strings.Contains(err.Error(), "distinct post-sighting tool boundary") {
				t.Fatalf("wrong issue/envelope failure: %v", err)
			}
		})
	}
}

// The authorized witness is conjunctive. No sampled post-tool unsighted
// interval is supplied; the gap must be secured by the immutable issue log.
func TestClaudeLiveContinuationGap(t *testing.T) {
	for _, kind := range []string{"green", "missing-pre-result", "last-pre-result-not-held", "sighting-before-ad", "sighting-equal-ad", "first-sighted-not-held", "missing-first-sighted", "duplicate-issue", "missing-issue", "issue-inside-gap", "issue-at-sighting"} {
		t.Run(kind, func(t *testing.T) {
			c, next, _ := diagHeldContinuation(t, "positive")
			tool, err := c.requestTool(next, runner.ContinuationToolProgram, "original-parent")
			if err != nil {
				t.Fatal(err)
			}
			wake, _, err := c.prompt(ContinuationWakeOrder)
			if err != nil {
				t.Fatal(err)
			}
			sm, err := c.message(ContinuationSteer)
			if err != nil {
				t.Fatal(err)
			}
			history, err := c.history()
			if err != nil {
				t.Fatal(err)
			}
			pre := history[0]
			pre.Observed = tool.Completed.Add(-200 * time.Millisecond)
			pre.ReadCompleted = tool.Completed.Add(-199 * time.Millisecond)
			first := history[len(history)-1]
			later := diagCopy(first)
			later.Observed = first.ReadCompleted.Add(time.Second)
			later.ReadCompleted = later.Observed.Add(time.Millisecond)
			history = []runner.Snapshot{pre, first, later}
			steer, _, err := c.prompt(ContinuationSteer)
			if err != nil {
				t.Fatal(err)
			}
			if err = c.continuationToolHold(map[string]any{}, history, wake, sm, tool); err != nil {
				t.Fatalf("green setup: %v", err)
			}
			want := ""
			switch kind {
			case "missing-pre-result":
				history = history[1:]
				want = errHeldContinuation.Error()
			case "last-pre-result-not-held":
				earlier := diagCopy(pre)
				earlier.Observed = pre.Observed.Add(-time.Second)
				earlier.ReadCompleted = pre.ReadCompleted.Add(-time.Second)
				pre.Tables["messages"][1]["place"] = "handover"
				history = append([]runner.Snapshot{earlier}, history...)
				want = errHeldContinuation.Error()
			case "sighting-before-ad":
				wake["admitted_at_ms"] = tool.ADBatch.UnixMilli() - 1
				want = "sighting did not follow"
			case "sighting-equal-ad":
				wake["admitted_at_ms"] = tool.ADBatch.UnixMilli()
				want = "sighting did not follow"
			case "first-sighted-not-held":
				first.Tables["messages"][1]["place"] = "handover"
				want = "not Held in the first sighted"
			case "missing-first-sighted":
				history = history[:1]
				want = errHeldContinuation.Error()
			case "duplicate-issue":
				c.s.Tables["prompts"] = append(c.s.Tables["prompts"], steer)
				want = "exactly one logged STEER issue"
			case "missing-issue":
				c.s.Tables["prompts"] = c.s.Tables["prompts"][:1]
				want = "exactly one logged STEER issue"
			case "issue-inside-gap":
				steer["issued_at_ms"] = tool.Completed.Add(time.Second).UnixMilli()
				want = "not strictly after the unsighted gap"
			case "issue-at-sighting":
				steer["issued_at_ms"] = wake["admitted_at_ms"]
				want = "not strictly after the unsighted gap"
			}
			err = c.continuationToolHold(map[string]any{}, history, wake, sm, tool)
			if want == "" && err != nil {
				t.Fatal(err)
			}
			if want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				t.Fatalf("want %q, got %v", want, err)
			}
		})
	}
}
