package qualification

import (
	"fmt"
	"slices"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type SizeBound struct {
	Route         string         `json:"route"`
	WholeThrough  int            `json:"whole_through"`
	FirstNonWhole int            `json:"first_non_whole,omitempty"`
	LowerBound    bool           `json:"lower_bound"`
	Samples       []NativeSample `json:"samples"`
}

// JudgeSizeLadder is file-only. A whole input means the complete known block
// in the first eligible model request, never an assistant's acknowledgment.
func JudgeSizeLadder(route string, samples []NativeSample) runner.Result {
	id := "native-wake-ladder"
	if route == "post_tool_batch" {
		id = "native-steer-ladder"
	}
	bound := SizeBound{Route: route, Samples: samples}
	expected, err := NativeRungs(route)
	if err != nil {
		return result(id, "fail", err.Error(), nil, nil)
	}
	var refs []string
	seenReceipts := map[string]bool{}
	for i, want := range expected {
		at := slices.IndexFunc(samples, func(s NativeSample) bool { return s.Rung.ID == want.ID })
		if i == 5 && at < 0 && bound.FirstNonWhole != 0 {
			break
		}
		if at < 0 {
			return result(id, "not_completed", "required native rung absent", want.ID, bound, refs...)
		}
		s := samples[at]
		if route == "post_tool_batch" && seenReceipts[s.Receipt] {
			return result(id, "not_completed", "insufficient-batches: one parent batch cannot supply multiple native rungs", want.ID, bound, refs...)
		}
		seenReceipts[s.Receipt] = true
		refs = append(refs, s.Receipt, s.Transcript)
		if s.Session == "" || s.Receipt == "" || s.Transcript == "" || !s.NextRequest || s.Emission.Written.IsZero() {
			return result(id, "not_completed", "native output or next model request missing", want.ID, bound, refs...)
		}
		if s.Rung.Route != route || s.Rung.Body != want.Body || s.Rung.ModelInput != want.ModelInput || s.Rung.ProviderCounts != want.ProviderCounts {
			return result(id, "fail", "native corpus/complete transformation differs from the candidate renderer", want, s.Rung, refs...)
		}
		if i <= 1 && !s.Whole {
			return result(id, "fail", "native route did not preserve corpus or published-cap-sized input whole", want.ProviderCounts, bound, refs...)
		}
		if s.Whole {
			if bound.FirstNonWhole != 0 {
				return result(id, "fail", "native size observations are non-monotone; no single bracket can be claimed", nil, bound, refs...)
			}
			bound.WholeThrough = max(bound.WholeThrough, want.ProviderCounts.Bytes)
		} else if bound.FirstNonWhole == 0 {
			bound.FirstNonWhole = want.ProviderCounts.Bytes
		}
	}
	bound.LowerBound = bound.FirstNonWhole == 0
	reason := fmt.Sprintf("native measurement only: whole through %d complete-input bytes", bound.WholeThrough)
	if bound.LowerBound {
		reason += "; lower bound, no maximum discovered"
	} else {
		reason += fmt.Sprintf("; first measured non-whole rung %d", bound.FirstNonWhole)
	}
	return result(id, "pass", reason, "required rungs with exact next-input evidence", bound, refs...)
}

type BacklogProof struct {
	Accepted []AdmissionAttempt   `json:"accepted"`
	Before   runner.Snapshot      `json:"before"`
	After    runner.Snapshot      `json:"after"`
	Final    runner.Snapshot      `json:"final"`
	Refusals []RefusalObservation `json:"refusals"`
	Lookups  map[string]bool      `json:"final_lookups"`
	Exited   bool                 `json:"exited"`
}

type RefusalObservation struct {
	Case       string `json:"case"`
	Reason     string `json:"reason"`
	Actual     int64  `json:"actual"`
	Allowed    int64  `json:"allowed"`
	ResendSafe bool   `json:"resend_safe"`
	RetryWith  string `json:"retry_with"`
	Command    string `json:"command"`
}

// CheckBacklog checks admission and lifecycle disposal; the separate exact
// scheduler full-drain proof remains a required coverage row in the summary.
func CheckBacklog(p BacklogProof) runner.Result {
	const id = "large-backlog"
	if len(p.Accepted) != 1000 || len(p.Before.Tables["messages"]) != 1000 || len(p.After.Tables["messages"]) != 1000 {
		return result(id, "not_completed", "1000 distinct accepted Messages not fully recorded", 1000, len(p.Accepted))
	}
	seen := map[string]bool{}
	expectedBodies := map[string]string{}
	for _, a := range p.Accepted {
		if seen[a.ID] || a.ID == "" || a.TextBytes != 1900 || a.Result.Class != "held" || a.Result.Final || a.Started.IsZero() || a.Ended.Before(a.Started) {
			return result(id, "fail", "acceptance/corpus/Result contradicts the Held backlog", nil, a, a.Output)
		}
		seen[a.ID] = true
		body, err := Corpus(a.ID, 1900)
		if err != nil || a.Result.RequestID != a.ID {
			return result(id, "fail", "acceptance identity differs from its controlled corpus/pair", nil, a, a.Output)
		}
		expectedBodies[body] = a.ID
	}
	if p.Before.InstanceID == "" || p.Before.InstanceID != p.After.InstanceID || p.Before.InstanceID != p.Final.InstanceID {
		return result(id, "fail", "backlog snapshots target different instances", p.Before.InstanceID, []string{p.After.InstanceID, p.Final.InstanceID})
	}
	for _, snapshot := range []runner.Snapshot{p.Before, p.After, p.Final} {
		ids, bodies, seqs := map[string]bool{}, map[string]bool{}, map[int64]bool{}
		for _, row := range snapshot.Tables["messages"] {
			mid, body, seq := runner.Text(row, "message_id"), runner.Text(row, "text"), runner.Number(row, "accepted_seq")
			if mid == "" || ids[mid] || bodies[body] || seq <= 0 || seqs[seq] || runner.Text(row, "instance_id") != p.Before.InstanceID || expectedBodies[body] == "" || runner.Text(row, "text_sha256") != runner.Digest([]byte(body)) {
				return result(id, "fail", "backlog custody lost a distinct controlled Message/body", nil, row)
			}
			ids[mid] = true
			bodies[body] = true
			seqs[seq] = true
		}
	}
	if !sameCustody(p.Before, p.After, false) || !sameCustody(p.Before, p.Final, false) {
		return result(id, "fail", "backlog custody identities/corpus changed across snapshots", nil, nil)
	}
	for _, s := range []runner.Snapshot{p.Before, p.After} {
		if len(s.Tables["prompts"]) != 0 {
			return result(id, "fail", "expensive backlog emitted instead of remaining Held", 0, len(s.Tables["prompts"]))
		}
		for _, m := range s.Tables["messages"] {
			if len(runner.Text(m, "text")) != 1900 || runner.Text(m, "place") != "held" {
				return result(id, "fail", "backlog Message changed or left Held", nil, m)
			}
		}
	}
	for _, want := range []string{"held_count", "text_bytes"} {
		at := slices.IndexFunc(p.Refusals, func(v RefusalObservation) bool { return v.Reason == want })
		if at < 0 {
			return result(id, "not_completed", "exact machine-readable limit refusal missing", want, p.Refusals)
		}
		refusal := p.Refusals[at]
		if refusal.Actual <= refusal.Allowed || refusal.Allowed <= 0 {
			return result(id, "fail", "refusal's actual/allowed limit is invalid", nil, refusal, refusal.Command)
		}
		if want == "held_count" && (!refusal.ResendSafe || refusal.RetryWith != "new_send" || refusal.Allowed != 1000) {
			return result(id, "fail", "held_count refusal lacks retry-after-drain contract", nil, refusal, refusal.Command)
		}
	}
	if !p.Exited || len(p.Final.Tables["messages"]) != 1000 || len(p.Final.Tables["prompts"]) != 0 {
		return result(id, "not_completed", "verified exit/fresh reuse disposal evidence missing", nil, p.Final)
	}
	for _, m := range p.Final.Tables["messages"] {
		if runner.Text(m, "result_class") != "not_started" {
			return result(id, "fail", "fresh reuse did not finalize a never-issued Message", nil, m)
		}
	}
	for _, id := range []string{"backlog-0001", "backlog-0500", "backlog-1000"} {
		if !p.Lookups[id] {
			return result("large-backlog", "not_completed", "confirmed final Lookup missing", id, p.Lookups)
		}
	}
	return result(id, "pass", "1000 x 1900-byte admission and no-drain lifecycle disposal recorded", "all Held, next refused, verified exit, confirmed finalization", len(p.Accepted), "raw/backlog-acceptance.json")
}

// A row count cannot establish custody continuity. Lifecycle state may change;
// immutable Message identity, target, accepted sequence and corpus may not.
func sameCustody(before, after runner.Snapshot, allowAdded bool) bool {
	if before.InstanceID != after.InstanceID {
		return false
	}
	project := func(s runner.Snapshot) (map[string]string, bool) {
		out := map[string]string{}
		for _, m := range s.Tables["messages"] {
			id := runner.Text(m, "message_id")
			if id == "" {
				return nil, false
			}
			if _, exists := out[id]; exists {
				return nil, false
			}
			out[id] = stringJSON([]any{runner.Text(m, "instance_id"), runner.Number(m, "accepted_seq"), runner.Text(m, "text"), runner.Text(m, "text_sha256")})
		}
		return out, true
	}
	a, aok := project(before)
	b, bok := project(after)
	if !aok || !bok || (!allowAdded && len(a) != len(b)) {
		return false
	}
	for id, body := range a {
		if b[id] != body {
			return false
		}
	}
	return true
}
