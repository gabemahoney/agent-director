package qualification

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Continuation preserves incomplete measurements while authorizing only the
// already selected independent phases. It never makes this capture importable.
type Continuation struct {
	Status              string            `json:"status"`
	ContinueIndependent bool              `json:"continue_independent"`
	BlockedImports      []string          `json:"blocked_imports"`
	Gaps                []runner.Result   `json:"b3_gaps"`
	Outcomes            []WorkloadOutcome `json:"outcomes"`
}

// CheckContinuation reads the sealed capture, rather than trusting an earlier
// verdict file or an error string. The current ledger must be its exact result.
// Future continuation callers must reuse this gate, not infer authority from
// individual outcomes; see the qualification reuse rules in docs/architecture.md.
func CheckContinuation(root string, ledger Ledger) (Continuation, error) {
	out := Continuation{Status: "not_completed"}
	c, err := LoadCapture(root)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(c.After, ledger) {
		return out, errors.New("continuation ledger is not the exact reconciled phase ledger")
	}
	if _, err = AccountTurns(ledger); err != nil {
		return out, err
	}
	v := AnalyzePhase(root)
	if v.Status != "not_completed" {
		return out, errors.New("continuation requires an incomplete phase without a failed verdict")
	}
	for _, id := range []string{"accounting", "cleanup", "contract"} {
		ok := false
		for _, r := range v.Results {
			if r.SignalID == id && r.Status == "pass" {
				ok = true
			}
		}
		if !ok {
			return out, errors.New("continuation requires passed " + id)
		}
	}
	out.Gaps = incompleteJudgments(v.Results)
	for _, r := range v.Results {
		if containsFailedVerdict(r) {
			return out, errors.New("failed component blocks continuation")
		}
	}
	for _, r := range c.Manifest.Problems {
		if !strings.HasPrefix(r.SignalID, "workload/") {
			// The runner also records the returned named error as the phase problem.
			if r.SignalID == "phase" && r.Status == "not_completed" {
				continue
			}
			return out, errors.New("unclassified phase problem blocks continuation")
		}
		var w WorkloadOutcome
		b, err := json.Marshal(r.Observed)
		if err != nil {
			return out, err
		}
		if err = json.Unmarshal(b, &w); err != nil {
			return out, err
		}
		if r.Status != "not_completed" || !slices.Contains([]string{"missing-next-tool", "missed-prose-window", "different-origin", "plan-not-present", "early-Stop-fallback", "response-still-active", "nonterminating-model", "precondition-not-established", "ordinary-early-F3", "insufficient-batches", "backlog-not-exercised"}, w.Code) || r.SignalID != "workload/"+w.Code {
			return out, errors.New("invalid named workload outcome")
		}
		bound := false
		for _, x := range c.Manifest.Cases {
			if x.ID == w.Case && x.InstanceID == w.Instance && runner.Digest([]byte(x.Text)) == w.InputSHA256 {
				for _, o := range c.Owners {
					if o.Owner.InstanceID == w.Instance && o.Owner.LaunchToken == w.Launch && o.Owner.SessionID == w.Session && !w.At.Before(o.Owner.Started) && !w.At.After(c.Manifest.Finished) {
						bound = true
					}
				}
			}
		}
		event := false
		for _, ref := range r.Evidence {
			for _, h := range c.Hooks {
				if h.Reference == ref && runner.Text(h.Payload, "session_id") == w.Session && runner.Text(h.Payload, "agent_id") == "" {
					event = true
				}
			}
		}
		if !bound || !event {
			return out, errors.New("workload outcome lacks frozen input and parent event binding")
		}
		out.Outcomes = append(out.Outcomes, w)
	}
	if len(out.Outcomes) == 0 {
		return out, errors.New("no named workload outcome permits continuation")
	}
	for _, problem := range c.Manifest.Problems {
		if problem.SignalID == "phase" {
			for _, line := range strings.Split(problem.Reason, "\n") {
				named := false
				for _, outcome := range workloadRows(c) {
					if strings.HasSuffix(line, outcome.Reason) {
						named = true
					}
				}
				if !named {
					return out, errors.New("unclassified returned phase error blocks continuation")
				}
			}
		}
	}
	out.BlockedImports = []string{c.Manifest.Phase}
	out.ContinueIndependent = true
	return out, nil
}

func containsFailedVerdict(r runner.Result) bool {
	b, err := json.Marshal(r)
	if err != nil {
		return true
	}
	var value any
	if json.Unmarshal(b, &value) != nil {
		return true
	}
	var failed func(any) bool
	failed = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			if x["status"] == "fail" {
				return true
			}
			for _, v := range x {
				if failed(v) {
					return true
				}
			}
		case []any:
			for _, v := range x {
				if failed(v) {
					return true
				}
			}
		}
		return false
	}
	return failed(value)
}
