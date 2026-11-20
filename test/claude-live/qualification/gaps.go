package qualification

import (
	"encoding/json"
	"slices"
	"sort"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// incompleteJudgments includes incomplete children under failed combinations,
// in both typed live results and decoded sealed evidence. It never changes status.
// Reuse it for summary and continuation gap collection; see docs/architecture.md.
func incompleteJudgments(rows []runner.Result) []runner.Result {
	var out []runner.Result
	seen := map[string]int{}
	var walk func(any, []string)
	walk = func(value any, inherited []string) {
		switch v := value.(type) {
		case []any:
			for _, child := range v {
				walk(child, inherited)
			}
		case map[string]any:
			if id, ok := v["signal_id"].(string); ok && id != "" {
				b, _ := json.Marshal(v)
				var r runner.Result
				if json.Unmarshal(b, &r) == nil && r.Status != "" {
					refs := append([]string{}, inherited...)
					for _, ref := range r.Evidence {
						if !slices.Contains(refs, ref) {
							refs = append(refs, ref)
						}
					}
					if r.Status == "not_completed" {
						keyRow := r
						keyRow.Evidence = nil
						key, _ := json.Marshal(keyRow)
						if at, ok := seen[string(key)]; ok {
							for _, ref := range refs {
								if !slices.Contains(out[at].Evidence, ref) {
									out[at].Evidence = append(out[at].Evidence, ref)
								}
							}
						} else {
							seen[string(key)] = len(out)
							r.Evidence = refs
							out = append(out, r)
						}
					}
					walk(v["observed"], refs)
					return
				}
			}
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(v[key], inherited)
			}
		}
	}
	b, err := json.Marshal(rows)
	var decoded any
	if err != nil || json.Unmarshal(b, &decoded) != nil {
		return []runner.Result{result("gap-export", "not_completed", "component judgments could not be decoded for the gap handoff", "serializable judgments", nil)}
	}
	walk(decoded, nil)
	return out
}

func measurementGaps(phases map[string][]runner.Result, coverage []CoverageEvidence, prerequisites []runner.Result) []CheckRecord {
	var out []CheckRecord
	add := func(phase string, rows []runner.Result) {
		for _, r := range incompleteJudgments(rows) {
			out = append(out, CheckRecord{Phase: phase, Result: r})
		}
	}
	add("qualification", prerequisites)
	for phase, rows := range phases {
		add(phase, rows)
	}
	for _, row := range coverage {
		if row.Status == "named_premise" || row.Status == "claude_inapplicable" {
			continue
		}
		// A missing premise guard is coverage, never a live premise measurement.
		r := result(row.ID, row.Status, row.Reason, row.CoverageRow, row.Results)
		components := incompleteJudgments(row.Results)
		for _, child := range components {
			for _, ref := range child.Evidence {
				if !slices.Contains(r.Evidence, ref) {
					r.Evidence = append(r.Evidence, ref)
				}
			}
		}
		if row.Status == "not_completed" {
			out = append(out, CheckRecord{Phase: "coverage/" + row.ID, Result: r})
		}
		for _, child := range components {
			if child.SignalID != row.ID || row.Status != "not_completed" {
				out = append(out, CheckRecord{Phase: "coverage/" + row.ID, Result: child})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Phase+"/"+out[i].SignalID < out[j].Phase+"/"+out[j].SignalID
	})
	return out
}
