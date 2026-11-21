package qualification_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

type pasteHook struct {
	Prompt   string    `json:"prompt"`
	PromptID string    `json:"prompt_id"`
	Session  string    `json:"session_id"`
	Received time.Time `json:"received"`
}

type pasteClip struct {
	PasteID   string            `json:"paste_id"`
	Text      string            `json:"text"`
	TypedCase runner.Case       `json:"typed_case"`
	StartCase runner.Case       `json:"start_case"`
	Running   pasteHook         `json:"running"`
	Submit    pasteHook         `json:"submit"`
	Journal   []json.RawMessage `json:"journal"`
	Pair      pastePairClip     `json:"long_tool_pair"`
}

type pastePairClip struct {
	Capture q.Capture
	Journal []json.RawMessage
}

func recordedPaste(t *testing.T) (pasteClip, signals.Transcript) {
	t.Helper()
	var clip pasteClip
	b, err := os.ReadFile("testdata/recorded/a2-typed-paste.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &clip); err != nil {
		t.Fatal(err)
	}
	return clip, pasteTranscript(t, clip.Journal, clip.Submit.Session)
}

func pasteTranscript(t *testing.T, rows []json.RawMessage, session string) signals.Transcript {
	t.Helper()
	var journal bytes.Buffer
	enc := json.NewEncoder(&journal)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	tr, err := signals.ParseTranscript(journal.Bytes(), session)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// TR5-1: compose both retained a2 sides through the actual typed analyzer.
// The clip labels the unrelated account/tool context replaced for privacy.
func TestQualificationRecordedLongToolPair(t *testing.T) {
	for _, kind := range []string{"retained-pair", "wait-before-injection", "missing-control-trigger"} {
		t.Run(kind, func(t *testing.T) {
			clip, control := recordedPaste(t)
			c := clip.Pair.Capture
			c.Root = qualificationfix.New(t, "typed-controls").Root
			for i := range c.Owners {
				o := &c.Owners[i]
				if o.Owner.SessionID == clip.Submit.Session {
					o.Transcript = control
				} else {
					o.Transcript = pasteTranscript(t, clip.Pair.Journal, o.Owner.SessionID)
				}
			}
			const adTrigger = "raw/hooks/invocation-1189811259/input.json"
			const controlTrigger = "raw/hooks/invocation-2379420033/input.json"
			if kind == "missing-control-trigger" {
				for i := range c.Observations {
					if c.Observations[i].Kind == "typed-control" {
						c.Observations[i].Trigger = ""
					}
				}
			}
			if kind == "wait-before-injection" {
				// R9-1: waitForCase records the same case before its pre-send
				// snapshot and injection. These unit wait spans add no intake.
				var ordered []q.Observation
				for _, observation := range c.Observations {
					if observation.Kind == "snapshot" {
						for _, injection := range c.Observations {
							if observation.Case == injection.Case+"-before" && (injection.Kind == "send" || injection.Kind == "typed-control") {
								ordered = append(ordered, q.Observation{Case: injection.Case, Instance: injection.Instance, Kind: "wait", State: injection.State, Started: observation.Started.Add(-time.Millisecond), Ended: observation.Started.Add(-time.Nanosecond)})
							}
						}
					}
					ordered = append(ordered, observation)
				}
				if len(ordered) != len(c.Observations)+2 {
					t.Fatal("recorded pair must retain one same-case wait before each injection")
				}
				c.Observations = ordered
			}
			observations := slices.Clone(c.Observations)
			rows := q.AnalyzeTypedForTest(c)
			if !slices.Equal(c.Observations, observations) {
				t.Error("typed analysis changed retained wait/injection observations needed for waiting_case diagnostics")
			}
			for _, want := range []struct {
				id      string
				mode    string
				request string
				intake  string
			}{
				{"typed-long-tool-ad-input", "steer_turn", "8817fc5e-a97e-4d66-9d8b-20e68fbd0115", "2026-11-19T07:41:52.894Z"},
				{"typed-long-tool-typed-input", "typed", "573fe110-c811-4116-9ba4-3dfd8a420782", "2026-11-19T07:41:52.961Z"},
			} {
				r := namedResult(t, rows, "typed-window/"+want.id)
				if kind == "missing-control-trigger" && want.mode == "typed" {
					if r.Status != "not_completed" || r.Reason != "send missed its trigger tool interval" {
						t.Fatalf("missing control trigger: status=%s reason=%s", r.Status, r.Reason)
					}
					continue
				}
				got, ok := r.Observed.(q.Timing)
				if !ok || r.Status != "pass" || got.Mode != want.mode || got.State != "long-tool" || !got.Whole || got.Boundary != 1 || got.BoundaryStatus != "pass" || got.Request != want.request || got.Intake.Format(time.RFC3339Nano) != want.intake {
					t.Errorf("%s: status=%s timing=%+v", want.id, r.Status, got)
				}
				if want.mode == "steer_turn" && (got.Issue.UnixMilli() != 1795074112864 || got.Complete.UnixMilli() != 1795074112866 || !got.Order.Equal(got.Complete)) {
					t.Fatalf("retained AD issue/transport/order lost: %+v", got)
				}
			}
			r := namedResult(t, rows, "typed-long-tool")
			pair, ok := r.Observed.(q.TypedPair)
			if !ok {
				t.Fatalf("pair observation is %T", r.Observed)
			}
			if kind == "missing-control-trigger" {
				if r.Status != "not_completed" {
					t.Fatalf("incomplete pair status=%s reason=%s", r.Status, r.Reason)
				}
			} else if r.Status != "pass" || pair.State != "long-tool" || pair.TextAD != clip.Text || pair.TextControl != clip.Text || len(pair.TextAD) != 8000 || pair.ADTrigger != adTrigger || pair.ControlTrigger != controlTrigger || pair.ADOffset != 68_918_429*time.Nanosecond || pair.ControlOffset != 88_221_626*time.Nanosecond || pair.ADBoundary != 1 || pair.ControlBoundary != 1 || !pair.ADWhole || !pair.ControlWhole {
				t.Fatalf("retained pair: status=%s state=%s bytes=%d/%d triggers=%s/%s offsets=%s/%s boundaries=%d/%d whole=%t/%t", r.Status, pair.State, len(pair.TextAD), len(pair.TextControl), pair.ADTrigger, pair.ControlTrigger, pair.ADOffset, pair.ControlOffset, pair.ADBoundary, pair.ControlBoundary, pair.ADWhole, pair.ControlWhole)
			}
			cap := namedResult(t, rows, "steer-cap")
			intake, ok := cap.Observed.(signals.Intake)
			if !ok || cap.Status != "pass" || intake.Count != 1 || intake.Typed != nil || intake.Time.Format(time.RFC3339Nano) != "2026-11-19T07:41:52.894Z" {
				t.Fatalf("recorded 8000-byte steer-cap: status=%s reason=%s intake=%+v", cap.Status, cap.Reason, intake)
			}
		})
	}
}

// R6-1: the input's provenance starts at injection, while the first eligible
// request starts after the running tool. Exercise the real boundary recheck.
func TestQualificationPasteTypedBoundary(t *testing.T) {
	for _, kind := range []string{"recorded-order", "queued-before-send"} {
		t.Run(kind, func(t *testing.T) {
			clip, tr := recordedPaste(t)
			at := func(clock string) time.Time {
				t.Helper()
				v, err := time.Parse(time.RFC3339Nano, "2026-11-19T"+clock+"Z")
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
			for i := range tr.Rows {
				if tr.Rows[i].Attachment.Type == "queued_command" {
					if !tr.Rows[i].Timestamp.Equal(at("07:40:52.739")) {
						t.Fatal("recorded queued attachment time changed")
					}
					if kind == "queued-before-send" {
						tr.Rows[i].Timestamp = clip.TypedCase.Started.Add(-time.Millisecond)
					}
				}
			}
			f := qualificationfix.New(t, "typed-controls")
			x := clip.TypedCase
			x.Text, x.InstanceID = clip.Text, "unit-a2-control"
			o := runner.Owned{InstanceID: x.InstanceID, LaunchID: "unit-a2-launch", LaunchToken: "unit-a2-token", SessionID: clip.Submit.Session, CWD: "/unit/a2-control", Started: clip.StartCase.Started, StoppedAt: at("07:42:12.180"), Exited: true}
			pre := unitHook(t, f, o, "PreToolUse", at("07:40:52.640068018"), runner.Row{"tool_name": "Bash", "prompt_id": clip.Submit.PromptID})
			post := unitHook(t, f, o, "PostToolBatch", at("07:41:52.934108180"), runner.Row{"prompt_id": clip.Submit.PromptID})
			submit := unitHook(t, f, o, "UserPromptSubmit", clip.Submit.Received, runner.Row{"prompt_id": clip.Submit.PromptID, "prompt": clip.Submit.Prompt})
			before := runner.Snapshot{InstanceID: o.InstanceID, Observed: x.Started.Add(-time.Millisecond), ReadCompleted: x.Started.Add(-time.Nanosecond), Tables: map[string][]runner.Row{"spawns": {{"session_id": o.SessionID, "launch_token": o.LaunchToken, "state": "working"}}}}
			const beforeRef = "raw/unit-a2-before.json"
			f.Put(beforeRef, before)
			c := q.Capture{Root: f.Root, Manifest: runner.Manifest{ProviderVersion: "2.1.311 (Claude Code)", Cases: []runner.Case{x}, Owned: []runner.Owned{o}},
				Owners: []q.OwnerEvidence{{Owner: o, Snapshot: before, SnapshotRef: beforeRef, TranscriptRef: "testdata/recorded/a2-typed-paste.json", Transcript: tr}},
				Hooks:  []q.HookFact{pre, submit, post}, Snapshots: map[string]runner.Snapshot{beforeRef: before},
				Observations: []q.Observation{{Case: x.ID, Instance: o.InstanceID, Kind: "typed-control", State: "long-tool", Started: x.Started, Ended: x.Ended, Trigger: pre.Reference}, {Case: x.ID + "-before", Kind: "snapshot", Snapshot: beforeRef}}}
			initial := q.CheckWholeInput(q.WholeInput{Body: x.Text, Mode: "typed", After: x.Started.UnixMilli(), Transcript: tr})
			wantInitial, wantBoundary := "pass", "pass"
			if kind == "queued-before-send" {
				wantInitial, wantBoundary = "fail", "fail"
			}
			if initial.Status != wantInitial {
				t.Fatalf("injection-bound whole check: %+v; want %s", initial, wantInitial)
			}
			timings := q.ExtractTimings(c)
			if len(timings) != 1 || timings[0].State != "long-tool" || timings[0].Boundary != 1 {
				t.Fatalf("real hook/snapshot boundary was not derived: %+v", timings)
			}
			got := timings[0]
			if kind == "recorded-order" && (got.Request != "573fe110-c811-4116-9ba4-3dfd8a420782" || !got.Intake.Equal(at("07:41:52.961"))) {
				t.Fatalf("initial recorded intake lost before boundary recheck: %+v", got)
			}
			if got.Whole != (kind == "recorded-order") || got.BoundaryStatus != wantBoundary {
				t.Errorf("queued paste after real boundary recheck: Whole=%t BoundaryStatus=%s Boundary=%d Reason=%s; want Whole=%t BoundaryStatus=%s", got.Whole, got.BoundaryStatus, got.Boundary, got.BoundaryReason, kind == "recorded-order", wantBoundary)
			}
		})
	}
}

// Finalize the real start ledger using the shared recorder-format helper.
// Only the two relevant passive hooks enter this private unit Run.
func pasteStarts(t *testing.T, clip pasteClip, prompt string, running bool) (q.Capture, string) {
	t.Helper()
	f := qualificationfix.New(t, "typed-controls")
	evidence, err := runner.NewEvidence(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	o := runner.Owned{InstanceID: "unit-paste-parent", LaunchID: "unit-paste-launch", LaunchToken: "unit-paste-token", SessionID: clip.Submit.Session, CWD: "/unit/paste", Started: clip.StartCase.Started, StoppedAt: clip.Submit.Received.Add(time.Minute), Exited: true}
	r := &runner.Run{Evidence: evidence, Manifest: runner.Manifest{Schema: runner.Schema, RunID: f.Manifest.RunID, Phase: "typed-controls", Owned: []runner.Owned{o}, TurnBudget: 6}}
	r.Manifest.RecordingHook.Events = []string{"UserPromptSubmit"}
	l := f.Ledger
	l.Charges = nil
	add := func(x runner.Case, hook pasteHook) string {
		x.InstanceID = o.InstanceID
		r.Manifest.Cases = append(r.Manifest.Cases, x)
		if err := q.Reserve(&l, q.Charge{ID: x.ID, Phase: "typed-controls", Attempt: l.Identity.Attempt, Reserved: 1}); err != nil {
			t.Fatal(err)
		}
		h := unitHook(t, f, o, "UserPromptSubmit", hook.Received, runner.Row{"prompt_id": hook.PromptID, "prompt": hook.Prompt})
		for _, ref := range []string{h.Reference, strings.TrimSuffix(h.Reference, "input.json") + "receipt.json"} {
			if err := evidence.Put(ref, f.Read(ref)); err != nil {
				t.Fatal(err)
			}
		}
		return h.Reference
	}
	if running {
		x := clip.StartCase
		x.Text = clip.Running.Prompt
		add(x, clip.Running)
	}
	x := clip.TypedCase
	x.Text = clip.Text
	h := clip.Submit
	h.Prompt = prompt
	ref := add(x, h)
	if err := evidence.JSON("raw/qualification-observations.json", []q.Observation{{Case: x.ID, Instance: o.InstanceID, Kind: "typed-control", Text: clip.Text, Started: x.Started, Ended: x.Ended}}); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(evidence.Root, "unit-ledger.json")
	if err := evidence.JSON("unit-ledger.json", l); err != nil {
		t.Fatal(err)
	}
	if err := q.FinalizeExerciseForTest(context.Background(), q.NewExercise(r, ledgerPath)); err != nil {
		t.Fatal(err)
	}
	l, err = q.LoadLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := q.HookFacts(evidence.Root, r.Manifest.RunID, r.Manifest.RecordingHook.Events)
	if err != nil {
		t.Fatal(err)
	}
	return q.Capture{Root: evidence.Root, Manifest: r.Manifest, After: l, Owners: []q.OwnerEvidence{{Owner: o}}, Hooks: facts}, ref
}

func assertPasteCharge(t *testing.T, c q.Capture, clip pasteClip, ref string, actual int) {
	t.Helper()
	var planned q.Charge
	unexpected := 0
	for _, charge := range c.After.Charges {
		if charge.ID == clip.TypedCase.ID {
			planned = charge
		}
		if strings.HasPrefix(charge.ID, "unexpected-") {
			unexpected += charge.Actual
		}
	}
	if planned.Actual != actual || !planned.Reconciled || unexpected != 1-actual {
		t.Errorf("paste start attribution: planned actual=%d reconciled=%t unexpected=%d; want %d/true/%d", planned.Actual, planned.Reconciled, unexpected, actual, 1-actual)
	}
	if actual == 1 && planned.Actual == 1 {
		if planned.Prompt != clip.Submit.PromptID+"@"+ref || !slices.Contains(planned.Evidence, ref) {
			t.Errorf("planned start lost its exact passive occurrence: %+v", planned)
		}
		if err := q.RequirePhysicalCaseForTest(c, clip.TypedCase.ID, "typed-controls"); err != nil {
			t.Errorf("planned typed physical start rejected: %v", err)
		}
	}
}

func TestQualificationPasteStartAttribution(t *testing.T) {
	for _, kind := range []string{"exact-wrapper", "unwrapped", "other-tag", "missing-id", "wrong-closing-id", "outside-text"} {
		t.Run(kind, func(t *testing.T) {
			clip, _ := recordedPaste(t)
			prompt := clip.Submit.Prompt
			actual := 0
			switch kind {
			case "exact-wrapper":
				actual = 1
			case "unwrapped":
				prompt, actual = clip.Text, 1
			case "other-tag":
				prompt = strings.ReplaceAll(prompt, "pasted_content", "other_content")
			case "missing-id":
				prompt = strings.ReplaceAll(prompt, ` id="c043"`, "")
			case "wrong-closing-id":
				prompt = strings.Replace(prompt, `</pasted_content id="c043">`, `</pasted_content id="other">`, 1)
			case "outside-text":
				prompt = "other text\n" + prompt
			}
			c, ref := pasteStarts(t, clip, prompt, false)
			assertPasteCharge(t, c, clip, ref, actual)
			if kind == "exact-wrapper" {
				b, err := runner.Read(c.Root, ref)
				var recorded pasteHook
				if err != nil || json.Unmarshal(b, &recorded) != nil || recorded.Prompt != clip.Submit.Prompt || !strings.HasPrefix(recorded.Prompt, `<pasted_content id="`+clip.PasteID+`">`+"\n") || !strings.HasSuffix(recorded.Prompt, "\n"+`</pasted_content id="`+clip.PasteID+`">`) {
					t.Fatalf("charged raw evidence lost exact wrapper/id: %v", err)
				}
			}
		})
	}
}

func TestQualificationPasteReusedPromptID(t *testing.T) {
	clip, _ := recordedPaste(t)
	if clip.Running.PromptID != clip.Submit.PromptID {
		t.Fatal("a2 reused prompt_id is required for this regression")
	}
	c, ref := pasteStarts(t, clip, clip.Submit.Prompt, true)
	assertPasteCharge(t, c, clip, ref, 1)
	if c.Manifest.ObservedTurns != 2 || len(c.After.Charges) != 2 {
		t.Errorf("reused prompt_id must retain the startup and one planned mid-turn parent start: observed=%d charges=%d", c.Manifest.ObservedTurns, len(c.After.Charges))
	}
}

func TestQualificationPasteTypedInput(t *testing.T) {
	for _, kind := range []string{"recorded-queued-wrapper", "unwrapped", "truncated-inner", "altered-inner", "other-tag", "missing-id", "outside-text"} {
		t.Run(kind, func(t *testing.T) {
			clip, tr := recordedPaste(t)
			want := "fail"
			replacement := clip.Submit.Prompt
			switch kind {
			case "recorded-queued-wrapper":
				want = "pass"
			case "unwrapped":
				_, tr = unitJournal(t, clip.Submit.Session, tr.Requests[0].ID, "user", clip.Text, tr.Requests[0].Time)
				want = "pass"
			case "truncated-inner":
				replacement = strings.Replace(replacement, clip.Text, clip.Text[:len(clip.Text)-1], 1)
			case "altered-inner":
				replacement = strings.Replace(replacement, "TYPED-long-tool", "TYPED-long-TOOL", 1)
			case "other-tag":
				replacement = strings.ReplaceAll(replacement, "pasted_content", "other_content")
			case "missing-id":
				replacement = strings.ReplaceAll(replacement, ` id="c043"`, "")
			case "outside-text":
				replacement = "other text\n" + replacement
			}
			// Narrow mutations of the parsed recorded request and its independent
			// queued attachment agree. The expected planned text stays untouched.
			if replacement != clip.Submit.Prompt {
				for i := range tr.Requests[0].Messages {
					m := &tr.Requests[0].Messages[i]
					var blocks []map[string]any
					if err := json.Unmarshal(m.Content, &blocks); err != nil {
						t.Fatal(err)
					}
					for _, block := range blocks {
						if text, ok := block["text"].(string); ok {
							block["text"] = strings.Replace(text, clip.Submit.Prompt, replacement, 1)
						}
					}
					m.Content, _ = json.Marshal(blocks)
				}
				for i := range tr.Rows {
					if tr.Rows[i].Attachment.Type == "queued_command" {
						tr.Rows[i].Attachment.Prompt = replacement
					}
				}
			}
			control := q.CheckWholeInput(q.WholeInput{Body: clip.Text, Mode: "typed", After: clip.TypedCase.Started.UnixMilli(), Transcript: tr})
			if control.Status != want {
				t.Errorf("a2 typed whole input: status=%s reason=%s; want %s", control.Status, control.Reason, want)
			}
			body := string(store.RenderBusySendPromptBody([][]byte{[]byte(clip.Text)}))
			_, ad := unitJournal(t, "unit-ad", "unit-ad-request", "system", signals.NativeContextBody(body), tr.Requests[0].Time)
			adWhole := q.CheckWholeInput(q.WholeInput{Body: body, Mode: "steer_turn", After: clip.TypedCase.Started.UnixMilli(), Transcript: ad})
			if adWhole.Status != "pass" {
				t.Fatalf("independent AD comparison control: %+v", adWhole)
			}
			pair := q.CheckTypedPair(q.TypedPair{State: "long-tool", TextAD: clip.Text, TextControl: clip.Text, ADTrigger: "unit/AD-trigger", ControlTrigger: "a2/PreToolUse", ADBoundary: 1, ControlBoundary: 1, ADWhole: adWhole.Status == "pass", ControlWhole: control.Status == "pass"})
			if pair.Status != want {
				t.Errorf("a2 typed pair using the planned inner text: status=%s reason=%s; want %s", pair.Status, pair.Reason, want)
			}
		})
	}
}

func TestQualificationPasteIsNotAD(t *testing.T) {
	for _, mode := range []string{"wait_turn", "steer_turn"} {
		t.Run(mode, func(t *testing.T) {
			clip, _ := recordedPaste(t)
			_, c := unitPhysicalCapture(t, "force-states", mode, false)
			o := &c.Owners[len(c.Owners)-1]
			body, err := q.PromptBody(o.Snapshot, o.Snapshot.Tables["prompts"][0])
			if err != nil {
				t.Fatal(err)
			}
			inner, role := signals.NativeWake(q.Transform(body, "wait_turn")), "user"
			if mode == "steer_turn" {
				inner, role = signals.NativeContextBody(body), "system"
			}
			// Stronger than matching a raw Message: even an exact AD native
			// frame inside a human paste is not an AD-issued input or sighting.
			wrapped := strings.Replace(clip.Submit.Prompt, clip.Text, inner, 1)
			callbackWrapped := strings.Replace(clip.Submit.Prompt, clip.Text, q.Transform(body, "wait_turn"), 1)
			_, typed := unitJournal(t, o.Owner.SessionID, "unit-wrapped-AD-text", role, wrapped, evidenceTime(14))
			o.Transcript.Requests[0] = typed.Requests[0]
			if got := q.CheckWholeInput(q.WholeInput{Body: body, Mode: mode, After: evidenceTime(12).UnixMilli(), Transcript: o.Transcript}); got.Status != "fail" {
				t.Errorf("human paste supplied AD whole input: %+v", got)
			}
			in := q.PackingEvidence{Owner: o.Owner, Snapshot: o.Snapshot, Transcript: o.Transcript, Trail: o.Trail}
			for _, h := range c.Hooks {
				if runner.Text(h.Payload, "session_id") == o.Owner.SessionID {
					if runner.Text(h.Payload, "prompt_id") == "unit-prompt-0" {
						h.Payload["prompt"] = callbackWrapped
					}
					h.Payload["_received_ms"] = h.Receipt.Received.UnixMilli()
					in.Hooks = append(in.Hooks, h.Payload)
				}
			}
			if got := q.CheckPackingAndOrder(in); got.Status != "fail" {
				t.Errorf("human paste supplied AD custody/emission/order proof: %+v", got)
			}
			if mode == "wait_turn" {
				if err := q.RequirePhysicalCaseForTest(c, "F3", "force-states"); err == nil {
					t.Error("human paste supplied an AD physical custody/start identity")
				}
				if _, err := signals.WakeOrderBody(o.Owner, o.Snapshot, o.Snapshot.Tables["prompts"][0], o.Snapshot.Tables["handovers"][0], body, in.Hooks, in.Trail, nil); err == nil {
					t.Error("human paste supplied AD wake admission/order")
				}
			}
			// A typed observation cannot create AD custody or confirmation.
			c.Manifest.Cases = []runner.Case{{ID: "unit-paste", InstanceID: o.Owner.InstanceID, Mode: "typed", Text: inner, Started: evidenceTime(10), Ended: evidenceTime(11)}}
			o.Snapshot.Tables = map[string][]runner.Row{}
			o.Transcript = typed
			timing := q.ExtractTimings(c)[0]
			if !timing.Issue.IsZero() || !timing.Complete.IsZero() || !timing.Sighting.IsZero() || !timing.Order.IsZero() || len(o.Snapshot.Tables["messages"]) != 0 || timing.ResponseMeaning != "first assistant text only; semantic acknowledgment and delivery confirmation are not inferred" {
				t.Errorf("typed wrapper acquired AD custody/admission/confirmation: %+v", timing)
			}
		})
	}
}
