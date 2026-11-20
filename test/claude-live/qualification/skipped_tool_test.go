package qualification_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// a5/kit19/Claude 2.1.311: replay the recorded skipped-tool ordering through
// the real finalizer and typed analyzer. This is no claim of tool-boundary intake.
func TestQualificationA5SkippedTool(t *testing.T) {
	for _, kind := range []string{"stop-delivery", "missing-AD-marker", "wrong-session", "second-extra-start", "no-delivery"} {
		t.Run(kind, func(t *testing.T) {
			var clip struct {
				Capture    q.Capture
				Journals   map[string][]json.RawMessage
				Unexpected q.Charge `json:"original_unexpected_charge"`
			}
			raw, err := os.ReadFile("testdata/recorded/a5-skipped-tool.json")
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &clip); err != nil {
				t.Fatal(err)
			}
			c := clip.Capture
			f := qualificationfix.New(t, "typed-controls")
			evidence, err := runner.NewEvidence(filepath.Join(t.TempDir(), "evidence"))
			if err != nil {
				t.Fatal(err)
			}
			c.Root = evidence.Root
			put := func(ref string, value any) {
				t.Helper()
				if err := evidence.JSON(ref, value); err != nil {
					t.Fatal(err)
				}
			}
			for i := range c.Owners {
				o := &c.Owners[i]
				if kind == "no-delivery" && i == 0 {
					var prior []json.RawMessage
					for _, row := range clip.Journals[o.Owner.SessionID] {
						if !strings.Contains(string(row), "3183e260-c83c-4e17-ba2b-bc3433c3fdcf") && !strings.Contains(string(row), "TYPED-between-tools:") {
							prior = append(prior, row)
						}
					}
					clip.Journals[o.Owner.SessionID] = prior
					o.Snapshot.Tables["handovers"], o.Snapshot.Tables["prompts"], o.Snapshot.Tables["members"] = nil, nil, nil
					c.Snapshots[o.SnapshotRef] = o.Snapshot
				}
				o.Transcript = pasteTranscript(t, clip.Journals[o.Owner.SessionID], o.Owner.SessionID)
			}
			for ref, snapshot := range c.Snapshots {
				put(ref, snapshot)
			}
			put("raw/qualification-observations.json", c.Observations)
			const deliveryRef = "raw/hooks/invocation-3514751438/input.json"
			var delivery q.HookFact
			var hooks []q.HookFact
			for _, h := range c.Hooks {
				if h.Reference == deliveryRef {
					delivery = h
					if kind == "no-delivery" {
						continue
					}
					if kind == "missing-AD-marker" {
						h.Payload["prompt"] = strings.Replace(runner.Text(h.Payload, "prompt"), "[agent-director: message 1 of 1]", "", 1)
					}
					if kind == "wrong-session" {
						// A real owned sibling, so this occurrence must be counted
						// but cannot spend the AD case's reserved turn.
						h.Payload["session_id"] = c.Owners[1].Owner.SessionID
						h.Payload["cwd"] = c.Owners[1].Owner.CWD
					}
				}
				hooks = append(hooks, h)
			}
			if kind == "second-extra-start" {
				extra := delivery
				extra.Payload = runner.Row{}
				for key, value := range delivery.Payload {
					extra.Payload[key] = value
				}
				extra.Payload["prompt_id"] = "unit-second-extra-prompt"
				extra.Reference = "raw/hooks/invocation-unit-second-extra/input.json"
				extra.Receipt.Received = extra.Receipt.Received.Add(time.Millisecond)
				extra.Receipt.Finished = extra.Receipt.Finished.Add(time.Millisecond)
				hooks = append(hooks, extra)
			}
			for _, h := range hooks {
				put(h.Reference, h.Payload)
				b, err := runner.Read(c.Root, h.Reference)
				if err != nil {
					t.Fatal(err)
				}
				h.Receipt.Bytes, h.Receipt.SHA256 = len(b), runner.Digest(b)
				put(strings.TrimSuffix(h.Reference, "input.json")+"receipt.json", h.Receipt)
			}
			before := f.Ledger
			before.Charges = nil
			// One isolated case reservation; the whole typed phase's share is
			// deliberately left to the code-writer/MI plan, not guessed here.
			if err := q.Reserve(&before, q.Charge{ID: c.Manifest.Cases[0].ID, Phase: "typed-controls", Attempt: before.Identity.Attempt, Reserved: 1}); err != nil {
				t.Fatal(err)
			}
			c.Before = before
			put("unit-ledger.json", before)
			r := &runner.Run{Evidence: evidence, Manifest: c.Manifest}
			r.Manifest.TurnsReserved = 1
			r.Manifest.RecordingHook.Events = []string{"PreToolUse", "PostToolBatch", "Stop", "UserPromptSubmit"}
			if err := q.FinalizeExerciseForTest(context.Background(), q.NewExercise(r, filepath.Join(c.Root, "unit-ledger.json"))); err != nil {
				t.Fatal(err)
			}
			c.Manifest = r.Manifest
			c.After, err = q.LoadLedger(filepath.Join(c.Root, "unit-ledger.json"))
			if err != nil {
				t.Fatal(err)
			}
			c.Hooks, err = q.HookFacts(c.Root, c.Manifest.RunID, r.Manifest.RecordingHook.Events)
			if err != nil {
				t.Fatal(err)
			}
			planned, unexpected := 0, 0
			for _, charge := range c.After.Charges {
				if strings.HasPrefix(charge.ID, "unexpected-") {
					unexpected += charge.Actual
				} else if charge.Reserved == 1 && charge.Actual == 1 && charge.Reconciled {
					planned++
					if charge.Prompt != runner.Text(delivery.Payload, "prompt_id")+"@"+deliveryRef || charge.Launch != c.Owners[0].Owner.LaunchToken || !slices.Contains(charge.Evidence, deliveryRef) {
						t.Errorf("planned Stop turn lost exact owned occurrence: %+v", charge)
					}
				}
			}
			wantPlanned, wantUnexpected := 1, 0
			switch kind {
			case "missing-AD-marker", "wrong-session":
				wantPlanned, wantUnexpected = 0, 1
			case "second-extra-start":
				wantUnexpected = 1
			case "no-delivery":
				wantPlanned = 0
			}
			if planned != wantPlanned || unexpected != wantUnexpected || int(c.Manifest.ObservedTurns) != wantPlanned+wantUnexpected {
				t.Errorf("Stop fallback attribution: planned=%d unexpected=%d observed=%d; want %d/%d/%d", planned, unexpected, c.Manifest.ObservedTurns, wantPlanned, wantUnexpected, wantPlanned+wantUnexpected)
			}
			accounting := q.AccountingForTest(c)
			if wantUnexpected == 0 && accounting.Status != "pass" || wantUnexpected != 0 && accounting.Status == "pass" {
				t.Errorf("Stop fallback accounting: status=%s reason=%s; unexpected=%d", accounting.Status, accounting.Reason, wantUnexpected)
			}
			rows := q.AnalyzeTypedForTest(c)
			control := namedResult(t, rows, "typed-window/typed-between-tools-typed-input")
			if control.Status != "pass" {
				t.Fatalf("recorded second-sleep control: status=%s reason=%s", control.Status, control.Reason)
			}
			pair := namedResult(t, rows, "typed-between-tools")
			if kind == "stop-delivery" {
				ad := namedResult(t, rows, "typed-window/typed-between-tools-ad-input")
				timing, ok := ad.Observed.(q.Timing)
				if !ok || !timing.Whole || timing.Mode != "wait_turn" || timing.Request != "3183e260-c83c-4e17-ba2b-bc3433c3fdcf" || timing.Intake.Format(time.RFC3339Nano) != "2026-11-19T19:11:16.152Z" {
					t.Errorf("preserve a5 whole wait-turn intake despite absent tool boundary: %+v", ad.Observed)
				}
				for _, id := range []string{"typed-between-tools", "typed-window/typed-between-tools-ad-input"} {
					row := namedResult(t, rows, id)
					cause := strings.ToLower(row.Reason)
					if !strings.Contains(cause, "stop") || !strings.Contains(cause, "deliver") || !strings.Contains(cause, "tool") || !(strings.Contains(cause, "no ") || strings.Contains(cause, "skip")) || strings.Contains(cause, "comparable injection/model-input evidence absent") {
						t.Errorf("%s must name skipped tool and delivery at Stop: status=%s reason=%s", id, row.Status, row.Reason)
					}
					if row.Status == "pass" {
						t.Errorf("%s substituted fallback for missing tool-boundary measurement", id)
					}
				}
				// Accept the classification in a result ID, reason or observation;
				// avoid prescribing a new outcome field or identifier spelling.
				named := false
				for _, row := range rows {
					b, _ := json.Marshal(row)
					label := strings.ToLower(string(b))
					if row.Status == "not_completed" && strings.Contains(label, "stop") && (strings.Contains(label, "skip") || strings.Contains(label, "no tool boundary")) {
						named = true
					}
				}
				if !named {
					t.Error("planned Stop delivery after skipped tool lacks a named incomplete outcome")
				}
				idle := namedResult(t, rows, "typed-idle")
				if idle.Status != "not_completed" || !strings.Contains(strings.ToLower(idle.Reason), "stop") {
					t.Errorf("fallback must close typed branch and name the lost idle pair: status=%s reason=%s", idle.Status, idle.Reason)
				}
				summary := q.AggregatePhaseResults("shakedown", []string{"typed-controls"}, map[string][]runner.Result{"typed-controls": rows}, nil, nil)
				if summary.SelectedStatus == "pass" || summary.MeasurementStatus == "pass" {
					t.Errorf("named incomplete workload erased b3 GAP: selected=%s b3=%s", summary.SelectedStatus, summary.MeasurementStatus)
				}
			}
			if kind == "no-delivery" && pair.Status == "pass" {
				t.Error("skipped tool without AD delivery passed")
			}
		})
	}
}
