package qualification_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Recorded parent occurrences remain charged; the typed workload itself is
// deliberately absent. Only authority/case/outcome metadata is synthetic.
func incompleteContinuation(t *testing.T) (*qualificationfix.Fixture, q.Ledger) {
	t.Helper()
	f := qualificationfix.New(t, "typed-controls")
	c := f.Capture()
	c.Before.Selected = []string{"typed-controls", "force-states"}
	c.After.Selected = []string{"typed-controls", "force-states"}
	x := c.Manifest.Cases[0]
	c.Manifest.Cases = []runner.Case{x}
	c.Manifest.ObservedTurns = 0
	c.Manifest.TurnBudget = 6
	for i := range c.Manifest.Owned {
		c.Manifest.Owned[i].StoppedAt = c.Manifest.Finished.Add(-time.Millisecond)
	}
	var cause q.HookFact
	for _, h := range c.Hooks {
		if h.Receipt.Event == "Stop" && runner.Text(h.Payload, "session_id") == c.Owners[0].Owner.SessionID && runner.Text(h.Payload, "agent_id") == "" {
			cause = h
		}
		if h.Receipt.Event != "UserPromptSubmit" || runner.Text(h.Payload, "agent_id") != "" {
			continue
		}
		for _, o := range c.Owners {
			if o.Owner.SessionID != runner.Text(h.Payload, "session_id") {
				continue
			}
			id := "unit-recorded-start-" + runner.Digest([]byte(h.Reference))[:16]
			c.After.Charges = append(c.After.Charges, q.Charge{ID: id, Phase: "typed-controls", Attempt: c.After.Identity.Attempt, Reserved: 1, Actual: 1, Launch: o.Owner.LaunchToken, Prompt: runner.Text(h.Payload, "prompt_id") + "@" + h.Reference, Reconciled: true, Evidence: []string{h.Reference}})
			c.Manifest.ObservedTurns++
		}
	}
	if cause.Reference == "" || c.Manifest.ObservedTurns != 6 {
		t.Fatalf("recorded continuation fixture lost Stop/start occurrences: cause=%s starts=%d", cause.Reference, c.Manifest.ObservedTurns)
	}
	o := c.Owners[0].Owner
	w := q.WorkloadOutcome{Code: "missing-next-tool", Case: x.ID, Instance: o.InstanceID, Launch: o.LaunchToken, Session: o.SessionID, At: cause.Receipt.Received, InputSHA256: runner.Digest([]byte(x.Text))}
	c.Manifest.Problems = []runner.Result{{SignalID: "workload/missing-next-tool", Status: "not_completed", Reason: "missing-next-tool: requested next tool absent after parent Stop", Observed: w, Evidence: []string{cause.Reference}}}
	doc, caps := q.ContractInputsForTest(t)
	f.PutBytes("raw/caller-contract.md", doc)
	f.Put("raw/capabilities.json", caps)
	sealUnitCapture(t, f, c)
	return f, c.After
}

func TestQualificationContinuationEntryPoint(t *testing.T) {
	f, ledger := incompleteContinuation(t)
	loaded, err := q.LoadCapture(f.Root)
	if err != nil {
		t.Fatalf("sealed fixture must load: %v", err)
	}
	if !reflect.DeepEqual(loaded.After, ledger) {
		t.Fatal("fixture ledger changed across sealed load")
	}
	v := q.AnalyzePhase(f.Root)
	if v.Status != "not_completed" {
		t.Fatalf("fixture must reach continuation without a failed verdict: %+v", v)
	}
	for _, id := range []string{"accounting", "cleanup", "contract"} {
		if row := namedResult(t, v.Results, id); row.Status != "pass" {
			t.Fatalf("continuation prerequisite %s: %+v", id, row)
		}
	}
	got, err := q.CheckContinuation(f.Root, ledger)
	if err != nil {
		t.Fatalf("sealed incomplete workload with reconciled accounting and cleanup must permit independent continuation: %v", err)
	}
	if got.Status != "not_completed" || !got.ContinueIndependent || !slices.Contains(got.BlockedImports, "typed-controls") || len(got.Gaps) == 0 || len(got.Outcomes) != 1 || got.Outcomes[0].Code != "missing-next-tool" {
		t.Fatalf("continuation lost GAP/import boundary: %+v", got)
	}
	changed := ledger
	changed.Identity.Attempt = "different"
	if out, err := q.CheckContinuation(f.Root, changed); err == nil || out.ContinueIndependent || !strings.Contains(err.Error(), "exact reconciled") {
		t.Fatalf("different ledger authorized continuation: %+v %v", out, err)
	}
}
