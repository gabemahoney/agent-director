package signals

import (
	"encoding/json"
	"fmt"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/signalsfix"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"strings"
	"testing"
	"time"
)

// Independent qualification inventory from t3.xis.w3.ci.ai / SR-8.3 and O4.
// Do not derive it from CheckIDs or a saved analyzer verdict.
var mandatory = strings.Fields("recording-hook turn-budget owned-cleanup session-watch file-changed user-prompt-identity wake-wrapping plain-sighting async-rewake reserved-escaping reserved-no-sighting post-tool-batch whole-next-input turn-end-order wake-steer-overlap hook-completion-order p-b p-c wake-stop-continuation")

func clause(t *testing.T, v runner.Verdict, id, status string) runner.Result {
	t.Helper()
	for _, r := range v.Results {
		if r.SignalID == id {
			if r.Status != status {
				t.Fatalf("%s: want %s; got %+v", id, status, r)
			}
			if r.Expected == nil || r.Observed == nil || len(r.Evidence) == 0 {
				t.Fatalf("unattributed result: %+v", r)
			}
			return r
		}
	}
	t.Fatalf("missing clause %s: %+v", id, v)
	return runner.Result{}
}
func namedPremise(t *testing.T, v runner.Verdict) {
	t.Helper()
	if len(v.NamedPremises) != 1 || v.NamedPremises[0].ID != "steer-wake-order" || !strings.Contains(v.NamedPremises[0].Reference, "O4") || !strings.Contains(v.NamedPremises[0].Reason, "awaiting-take guard") {
		t.Fatalf("R-a missing: %+v", v.NamedPremises)
	}
	for _, r := range v.Results {
		if r.SignalID == "steer-wake-order" || r.SignalID == "wake-stop-no-tools" {
			t.Fatalf("retired/unmeasured mandatory result: %+v", r)
		}
	}
	if !strings.Contains(Format(v), "steer-wake-order: named, unmeasured") {
		t.Fatal("printed R-a missing")
	}
}
func TestClaudeLiveCapturedPassing(t *testing.T) {
	f := signalsfix.Passing(t)
	v := Analyze(f.Root)
	if v.Status != "pass" {
		t.Fatal(Format(v))
	}
	if len(v.Results) != len(mandatory) {
		t.Fatalf("mandatory inventory: %d", len(v.Results))
	}
	for _, id := range mandatory {
		clause(t, v, id, "pass")
	}
	namedPremise(t, v)
	obs := clause(t, v, "wake-stop-continuation", "pass").Observed.(map[string]any)
	if obs["wake_input_form"] != "system-stop-continuation" {
		t.Fatalf("provider A2 form lost: %+v", obs)
	}
}

func TestClaudeLiveSignalMutations(t *testing.T) {
	hook := func(event string, change func(runner.Row)) func(*signalsfix.Fixture) {
		return func(f *signalsfix.Fixture) { f.Hooks(event, 0, change) }
	}
	plainHook := func(change func(runner.Row)) func(*signalsfix.Fixture) {
		return hook("UserPromptSubmit", func(r runner.Row) {
			if r["prompt"] == Render(Plain, "wait_turn") {
				change(r)
			}
		})
	}
	blob := func(old, new string) func(*signalsfix.Fixture) {
		return func(f *signalsfix.Fixture) {
			mutateBlobs(t, f, 0, func(r runner.Row) { r["message"] = replaceText(r["message"], old, new) })
		}
	}
	prompt := func(text string, change func(runner.Row)) func(*signalsfix.Fixture) {
		return func(f *signalsfix.Fixture) { mutatePrompt(f, 0, text, change) }
	}
	cases := []struct {
		name, id string
		change   func(*signalsfix.Fixture)
	}{
		{"recorder-removed", "recording-hook", func(f *signalsfix.Fixture) { f.Manifest.RecordingHook.Removed = false }},
		{"eighth-turn", "turn-budget", func(f *signalsfix.Fixture) { f.Manifest.ObservedTurns = 8 }},
		{"owner-alive", "owned-cleanup", func(f *signalsfix.Fixture) { f.Manifest.Owned[1].Exited = false }},
		{"session-source", "session-watch", hook("SessionStart", func(r runner.Row) { r["source"] = "resume" })},
		{"watch-path", "session-watch", func(f *signalsfix.Fixture) {
			rows := f.Rows(f.Transcript(0))
			for _, r := range rows {
				a, ok := r["attachment"].(map[string]any)
				if ok && a["hookEvent"] == "SessionStart" {
					a["stdout"] = strings.ReplaceAll(runner.Text(a, "stdout"), "watchPaths", "otherPaths")
				}
			}
			f.WriteRows(f.Transcript(0), rows)
		}},
		{"file-path", "file-changed", hook("FileChanged", func(r runner.Row) { r["file_path"] = "/different/wake" })},
		{"file-event", "file-changed", hook("FileChanged", func(r runner.Row) { r["event"] = "rename" })},
		{"async-input", "async-rewake", blob(NativeWake(Render(Plain, "wait_turn")), "changed whole input")},
		{"ups-parent", "user-prompt-identity", plainHook(func(r runner.Row) { r["agent_id"] = "foreign-child" })},
		{"ups-prompt-id", "user-prompt-identity", plainHook(func(r runner.Row) { r["prompt_id"] = "" })},
		{"wake-wrapper", "wake-wrapping", plainHook(func(r runner.Row) {
			r["prompt"] = strings.Replace(runner.Text(r, "prompt"), "<task-notification>", "<task-notification changed>", 1)
		})},
		{"plain-ad-sighting", "plain-sighting", prompt(Plain, func(p runner.Row) {
			if p["admission_kind"] != nil {
				p["admission_kind"] = "native_signal"
			}
		})},
		{"reserved-reminder-transform", "reserved-escaping", hook("UserPromptSubmit", func(r runner.Row) {
			if r["prompt"] == Render(Reserved, "wait_turn") {
				r["prompt"] = strings.ReplaceAll(runner.Text(r, "prompt"), "&lt;/system-reminder>", "</system-reminder>")
			}
		})},
		{"reserved-task-transform", "reserved-escaping", hook("UserPromptSubmit", func(r runner.Row) {
			if r["prompt"] == Render(Reserved, "wait_turn") {
				r["prompt"] = strings.ReplaceAll(runner.Text(r, "prompt"), `<\/task-notification>`, "</task-notification>")
			}
		})},
		{"invented-reserved-sighting", "reserved-no-sighting", prompt(Reserved, func(p runner.Row) {
			p["admission_kind"] = "prompt_sighting"
			p["admitted_at_ms"] = runner.Number(p, "transport_at_ms") + 1
		})},
		{"batch-tool-calls", "post-tool-batch", hook("PostToolBatch", func(r runner.Row) { r["tool_calls"] = []any{} })},
		{"batch-parent", "post-tool-batch", hook("PostToolBatch", func(r runner.Row) { r["prompt_id"] = "foreign-parent" })},
		{"whole-context", "post-tool-batch", func(f *signalsfix.Fixture) {
			rows := f.Rows(f.Transcript(0))
			for _, r := range rows {
				a, ok := r["attachment"].(map[string]any)
				if !ok || a["hookEvent"] != "PostToolBatch" {
					continue
				}
				var out map[string]any
				if json.Unmarshal([]byte(runner.Text(a, "stdout")), &out) != nil {
					continue
				}
				s, ok := out["hookSpecificOutput"].(map[string]any)
				if ok && s["additionalContext"] == Prompt(Steer) {
					s["additionalContext"] = Prompt(Steer) + " "
					b, _ := json.Marshal(out)
					a["stdout"] = string(b)
				}
			}
			f.WriteRows(f.Transcript(0), rows)
		}},
		{"input-separator", "whole-next-input", blob("[agent-director: message 1 of 1]", "[agent-director: message 1/1]")},
		{"input-suffix", "whole-next-input", blob(NativeWake(Render(Plain, "wait_turn")), NativeWake(Render(Plain, "wait_turn"))+" ")},
		{"input-provider-framing", "whole-next-input", blob("[SYSTEM NOTIFICATION - NOT USER INPUT]", "[SYSTEM NOTIFICATION]")},
		{"early-native-take", "turn-end-order", prompt(Steer, func(p runner.Row) {
			if p["admission_kind"] == "native_signal" {
				p["admitted_at_ms"] = p["transport_at_ms"]
			}
		})},
		{"missing-parent-stop", "turn-end-order", hook("Stop", func(r runner.Row) { r["agent_id"] = "foreign-child" })},
		{"stop-only-order", "hook-completion-order", prompt(WakeOrder, func(p runner.Row) {
			if p["order_kind"] != nil {
				p["order_at_ms"] = runner.Number(p, "transport_at_ms") + 1
			}
		})},
		{"missing-written", "whole-next-input", prompt(WakeOrder, func(p runner.Row) { p["transport"] = "writing" })},
		{"missing-sighting", "hook-completion-order", prompt(WakeOrder, func(p runner.Row) { p["admission_kind"] = nil; p["admitted_at_ms"] = nil })},
		{"early-steer-issue", "hook-completion-order", prompt(Steer, func(p runner.Row) { p["issued_at_ms"] = int64(1795022320000) })},
		{"accepted-outside-command", "wake-steer-overlap", func(f *signalsfix.Fixture) {
			f.Snapshots(0, func(s *runner.Snapshot) {
				for _, m := range s.Tables["messages"] {
					if m["text"] == Steer {
						m["accepted_at_ms"] = runner.Number(m, "accepted_at_ms") - 1000
					}
				}
			})
		}},
		{"duplicate-steer-issue", "hook-completion-order", func(f *signalsfix.Fixture) {
			var s runner.Snapshot
			f.Decode(f.Store(0), &s)
			c := capture{m: f.Manifest, s: s}
			p, _, e := c.prompt(Steer)
			if e != nil {
				t.Fatal(e)
			}
			s.Tables["prompts"] = append(s.Tables["prompts"], p)
			f.JSON(f.Store(0), s)
		}},
		{"receipt-without-ad-sighting", "hook-completion-order", func(f *signalsfix.Fixture) {
			name := "raw/" + f.Manifest.Owned[0].InstanceID + "-trail.jsonl"
			rows := f.Rows(name)
			for _, r := range rows {
				if r["event_name"] == "UserPromptSubmit" {
					r["upsert_outcome"] = "ignored"
				}
			}
			f.WriteRows(name, rows)
		}},
		{"continuation-tool-id", "wake-stop-continuation", func(f *signalsfix.Fixture) {
			f.Hooks("PreToolUse", 1, func(r runner.Row) { r["tool_use_id"] = "unrelated-tool" })
		}},
		{"continuation-tool-parent", "wake-stop-continuation", func(f *signalsfix.Fixture) {
			f.Hooks("PostToolBatch", 1, func(r runner.Row) { r["prompt_id"] = "unrelated-parent" })
		}},
		{"continuation-owner", "wake-stop-continuation", func(f *signalsfix.Fixture) {
			var o runner.Owned
			f.Decode("raw/continuation-owner.json", &o)
			o.SessionID = "foreign-owner"
			f.JSON("raw/continuation-owner.json", o)
		}},
		{"continuation-second-block", "wake-stop-continuation", func(f *signalsfix.Fixture) {
			for name := range f.Manifest.Artifacts {
				if !strings.HasPrefix(name, "raw/continuation/events/") || !strings.HasSuffix(name, "/receipt.json") {
					continue
				}
				var r runner.Row
				f.Decode(name, &r)
				if r["decision"] == "block" {
					continue
				}
				r["decision"] = "block"
				f.JSON(name, r)
				f.JSON(strings.TrimSuffix(name, "receipt.json")+"output.json", map[string]string{"decision": "block", "reason": runner.ContinuationToolReason})
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := signalsfix.Copy(t)
			tc.change(f)
			f.Reindex()
			v := Analyze(f.Root)
			for _, r := range v.Results {
				if r.SignalID == "evidence-integrity" {
					t.Fatalf("mutation failed mechanically: %+v", r)
				}
			}
			clause(t, v, tc.id, "fail")
			if v.Status != "fail" {
				t.Fatalf("failure hidden: %s", Format(v))
			}
			namedPremise(t, v)
		})
	}
}

func TestClaudeLiveContinuationStopLinkage(t *testing.T) {
	for _, kind := range []string{"ambiguous-passive-stop", "ambiguous-ad-stop", "missed-original-window"} {
		t.Run(kind, func(t *testing.T) {
			c, _ := diag14Capture(t)
			switch kind {
			case "ambiguous-passive-stop":
				// Inventory order is directory order, not callback time. Duplicate the
				// first chronological parent Stop, rather than an unrelated later Stop.
				var first runner.Row
				var at time.Time
				for _, p := range c.hooks["Stop"] {
					stamp, e := time.Parse(time.RFC3339Nano, runner.Text(p, "_received"))
					if e != nil {
						t.Fatal(e)
					}
					if at.IsZero() || stamp.Before(at) {
						first, at = p, stamp
					}
				}
				if first == nil {
					t.Fatal("no captured Stop")
				}
				c.hooks["Stop"] = append(c.hooks["Stop"], first)
			case "ambiguous-ad-stop":
				for _, r := range c.trail {
					if r["event_name"] == "Stop" {
						c.trail = append(c.trail, r)
						break
					}
				}
			case "missed-original-window":
				p, _, e := c.prompt(ContinuationWakeOrder)
				if e != nil {
					t.Fatal(e)
				}
				var window struct {
					RequestID string `json:"request_id"`
				}
				b, e := c.indexed("raw/text-finish-window-1.json")
				if e != nil {
					t.Fatal(e)
				}
				if e = json.Unmarshal(b, &window); e != nil {
					t.Fatal(e)
				}
				for i := range c.t.Rows {
					r := &c.t.Rows[i]
					if r.RequestRef == window.RequestID && r.Message.StopReason == "end_turn" {
						r.Timestamp = time.UnixMilli(runner.Number(p, "transport_at_ms") - 1)
					}
				}
			}
			_, e := c.continuationEvidence()
			if kind == "missed-original-window" {
				if e != errContinuationWindow {
					t.Fatalf("missed original window: %v", e)
				}
			} else if e == nil || !strings.Contains(e.Error(), "Stop linkage") {
				t.Fatalf("ambiguous Stop accepted or wrong failure: %v", e)
			}
		})
	}
}

func TestClaudeLiveIndependentPremiseReversals(t *testing.T) {
	for _, id := range []string{"p-b", "p-c"} {
		t.Run(id, func(t *testing.T) {
			f := signalsfix.Copy(t)
			c, e := load(f.Root)
			if e != nil {
				t.Fatal(e)
			}
			text := func(role, s string) Message {
				b, e := json.Marshal([]Block{{Type: "text", Text: s}})
				if e != nil {
					t.Fatal(e)
				}
				return Message{Role: role, Content: b}
			}
			anchor, _ := c.intake(WakeOrder, "wait_turn")
			steer, _, _ := c.prompt(Steer)
			at := time.UnixMilli(runner.Number(steer, "transport_at_ms") + 1)
			messages := []Message{text("system", NativeContext(Steer)), text("user", NativeWake(Render(WakeOrder, "wait_turn")))}
			if id == "p-c" {
				anchor, _ = c.intake(Steer, "steer_turn")
				plain, _, _ := c.prompt(Plain)
				at = time.UnixMilli(runner.Number(plain, "admitted_at_ms") + 1)
				messages = []Message{text("user", NativeWake(Render(Plain, "wait_turn"))), text("system", NativeContext(Steer))}
			}
			req := c.t.Requests[anchor.Request]
			// Retain the existing conversation prefix and suffix. Only inject the
			// reversed input before the measured input, so downstream keep/base counts
			// remain valid and the independently completed exercise is unchanged.
			messages = append(append(append([]Message(nil), req.Messages[:anchor.Message]...), messages[0]), req.Messages[anchor.Message:]...)
			rewriteRequest(t, f, 0, req.ID, at, messages)
			// Keep an independently completed response after the delayed input. Do not
			// turn the intended semantic reversal into a missing-exercise fixture.
			if id == "p-c" {
				rows := f.Rows(f.Transcript(0))
				for _, r := range rows {
					m, ok := r["message"].(map[string]any)
					if ok && r["requestRef"] == req.ID && m["stop_reason"] == "end_turn" {
						r["timestamp"] = at.Add(time.Millisecond)
					}
				}
				f.WriteRows(f.Transcript(0), rows)
				for i := range f.Manifest.Cases {
					x := &f.Manifest.Cases[i]
					if x.ObservedUntil.Before(at.Add(2 * time.Millisecond)) {
						x.ObservedUntil = at.Add(2 * time.Millisecond)
					}
				}
			}
			f.Reindex()
			altered, e := load(f.Root)
			if e != nil || altered.transcriptErr != nil {
				t.Fatalf("mechanically invalid reversal: %v / %v", e, altered.transcriptErr)
			}
			held, e := altered.heldOverlap(WakeOrder, Steer, Plain)
			if e != nil || !held {
				t.Fatalf("overlap lost: %v %v", held, e)
			}
			if _, e = altered.wakeOrder(WakeOrder); e != nil {
				t.Fatalf("wake order lost: %v", e)
			}
			v := Analyze(f.Root)
			r := clause(t, v, id, "fail")
			obs := r.Observed.(map[string]any)
			if id == "p-b" && (obs["steer_after_wake_order"] != true || obs["steer_emission_order"] != true || obs["wake_before_steer"] != false) {
				t.Fatalf("wrong P-b failure: %+v", obs)
			}
			if id == "p-c" && (obs["issue_and_take_order"] != true || obs["plain_held_during_steer_take"] != true || obs["steer_input_error"] != "" || obs["plain_input_error"] != "") {
				t.Fatalf("wrong P-c failure: %+v", obs)
			}
		})
	}
}

func TestClaudeLiveIncompleteAndEvidenceControls(t *testing.T) {
	for _, kind := range []string{"missed-overlap", "later-incomplete-earlier-failure", "unsafe-reference", "provider-extra-field"} {
		t.Run(kind, func(t *testing.T) {
			f := signalsfix.Copy(t)
			switch kind {
			case "missed-overlap":
				f.Snapshots(0, func(s *runner.Snapshot) {
					for _, m := range s.Tables["messages"] {
						if m["text"] == Plain {
							m["place"] = "closed"
						}
					}
				})
			case "later-incomplete-earlier-failure":
				mutateBlobs(t, f, 0, func(r runner.Row) {
					r["message"] = replaceText(r["message"], NativeWake(Render(Plain, "wait_turn")), "missing whole plain")
				})
				for i := range f.Manifest.Cases {
					if f.Manifest.Cases[i].ID == "reserved-tags" {
						f.Manifest.Cases[i].Ready = false
					}
				}
			case "unsafe-reference":
				f.Manifest.Artifacts["../outside"] = runner.Artifact{}
				f.JSON("manifest.json", f.Manifest)
			case "provider-extra-field":
				// Label-only mutation is explicitly synthetic, never qualification of a
				// different provider version. All three identity witnesses agree.
				f.Manifest.ProviderVersion += " (synthetic-label)"
				var proof runner.CandidateEvidence
				f.Decode("raw/candidate-installed.json", &proof)
				proof.ProviderVersion = f.Manifest.ProviderVersion
				f.JSON("raw/candidate-installed.json", proof)
				f.Write(proof.ProviderCommand, []byte(f.Manifest.ProviderVersion+"\n"))
				f.Hooks("FileChanged", 0, func(r runner.Row) { r["additional_future_field"] = map[string]any{"value": true} })
			}
			if kind != "unsafe-reference" {
				f.Reindex()
			}
			v := Analyze(f.Root)
			namedPremise(t, v)
			switch kind {
			case "missed-overlap":
				clause(t, v, "p-c", "not_completed")
				if v.Status == "pass" {
					t.Fatal("missed overlap qualified")
				}
			case "later-incomplete-earlier-failure":
				clause(t, v, "whole-next-input", "fail")
				if v.Status != "fail" {
					t.Fatal("later interruption erased failure")
				}
			case "unsafe-reference":
				clause(t, v, "evidence-integrity", "fail")
			case "provider-extra-field":
				if v.Status != "pass" {
					t.Fatal(Format(v))
				}
			}
		})
	}
}
func TestClaudeLiveWakeFactOrders(t *testing.T) {
	for _, kind := range []string{"written-first", "sighting-first", "equal-causal", "equal-unknown", "missing-sighting", "stale-sighting", "unmatched-sighting", "missing-written", "ambiguous-sighting", "early-order", "late-complete", "foreign-launch", "duplicate-issue", "missing-force"} {
		t.Run(kind, func(t *testing.T) {
			f := signalsfix.Copy(t)
			c, err := load(f.Root)
			if err != nil {
				t.Fatal(err)
			}
			p, h, _ := c.prompt(c.wakeText())
			written := runner.Number(p, "transport_at_ms")
			want := true
			switch kind {
			case "sighting-first":
				p["issued_at_ms"] = written - 20
				p["admitted_at_ms"] = written - 10
				p["order_at_ms"] = written
			case "equal-causal":
				p["admitted_at_ms"] = written
				p["order_at_ms"] = written
			case "equal-unknown":
				p["admitted_at_ms"] = written
				p["order_at_ms"] = written
				c.m.Artifacts = map[string]runner.Artifact{}
				want = false
			case "missing-sighting":
				p["admission_kind"], p["admitted_at_ms"] = nil, nil
				want = false
			case "stale-sighting":
				p["admitted_at_ms"] = runner.Number(p, "issued_at_ms") - 1
				want = false
			case "unmatched-sighting":
				for _, row := range c.hooks["UserPromptSubmit"] {
					if row["prompt"] == Render(c.wakeText(), "wait_turn") {
						row["prompt"] = "different exact input"
					}
				}
				want = false
			case "missing-written":
				p["transport"] = "writing"
				want = false
			case "ambiguous-sighting":
				for _, r := range c.hooks["UserPromptSubmit"] {
					if runner.Text(r, "prompt") == Render(c.wakeText(), "wait_turn") {
						c.hooks["UserPromptSubmit"] = append(c.hooks["UserPromptSubmit"], r)
						break
					}
				}
				want = false
			case "early-order":
				p["order_at_ms"] = written + 1
				want = false
			case "late-complete":
				c.s.Tables["launches"][0]["ended_at_ms"] = written - 1
				want = false
			case "foreign-launch":
				h["launch_id"] = "foreign"
				want = false
			case "duplicate-issue":
				c.s.Tables["prompts"] = append(c.s.Tables["prompts"], p)
				want = false
			case "missing-force":
				c.s.Tables["forces"] = nil
				want = false
			}
			if kind == "sighting-first" {
				s := diagCopy(c.s)
				s.Observed = time.UnixMilli(written + 1000)
				row := snapshotPrompt(s, p)
				row["transport"], row["transport_at_ms"], row["order_kind"], row["order_at_ms"] = "writing", nil, nil, nil
				c.m.Artifacts = map[string]runner.Artifact{}
				diagSave(t, &c, "raw/first-fact-store.json", s)
			}
			if kind == "equal-causal" {
				history, err := c.history()
				if err != nil {
					t.Fatal(err)
				}
				c.m.Artifacts = map[string]runner.Artifact{}
				for i, snapshot := range history {
					row := snapshotPrompt(snapshot, p)
					if runner.Text(row, "admission_kind") != "" {
						row["admitted_at_ms"] = written
					}
					if runner.Text(row, "order_kind") != "" {
						row["order_at_ms"] = written
					}
					diagSave(t, &c, fmt.Sprintf("raw/equal-%d-store.json", i), snapshot)
				}
			}
			order, err := c.wakeOrder(c.wakeText())
			if (err == nil) != want {
				t.Fatalf("order=%d err=%v want-valid=%v", order, err, want)
			}
		})
	}
}
