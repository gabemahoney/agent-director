package signals

import (
	"slices"
	"time"
)

// ProviderRefusal is a machine diagnostic, never input or delivery evidence.
// Reuse this extractor for live progress checks and sealed-file analysis.
type ProviderRefusal struct {
	Code    string    `json:"code"`
	Request string    `json:"request"`
	Time    time.Time `json:"timestamp"`
	Line    int       `json:"line"`
}

// ProviderRefusals requires a current main request, including for a system
// marker whose parent UUID supplies the request binding. requestId and
// refusedUserMessageUuid are not aliases for the native request journal.
func (t Transcript) ProviderRefusals(after, before time.Time) []ProviderRefusal {
	requests := map[string]Request{}
	for _, q := range t.Requests {
		requests[q.ID] = q
	}
	rows := map[string]TranscriptRow{}
	duplicate := map[string]bool{}
	for _, r := range t.Rows {
		if r.UUID == "" {
			continue
		}
		if _, ok := rows[r.UUID]; ok {
			duplicate[r.UUID] = true
		}
		rows[r.UUID] = r
	}
	var out []ProviderRefusal
	for _, r := range t.Rows {
		if r.Sidechain || r.Timestamp.IsZero() || r.Timestamp.Before(after) || (!before.IsZero() && !r.Timestamp.Before(before)) {
			continue
		}
		code := ""
		if r.Type == "assistant" && r.Message.Role == "assistant" && r.Message.StopReason == "refusal" {
			code = "refusal"
		}
		if r.Type == "system" && r.Subtype == "model_refusal_no_fallback" {
			code = r.Subtype
		}
		if code == "" {
			continue
		}
		current := r
		seen := map[string]bool{}
		for {
			if current.RequestRef != "" {
				q, ok := requests[current.RequestRef]
				if ok && !q.Time.Before(after) && !q.Time.After(current.Timestamp) && !q.Time.After(r.Timestamp) && (before.IsZero() || q.Time.Before(before)) {
					out = append(out, ProviderRefusal{Code: code, Request: q.ID, Time: r.Timestamp, Line: r.Line})
				}
				break
			}
			id := current.ParentUUID
			if id == "" || seen[id] || duplicate[id] {
				break
			}
			seen[id] = true
			p, ok := rows[id]
			if !ok || p.Sidechain || p.Timestamp.Before(after) || p.Timestamp.After(r.Timestamp) {
				break
			}
			current = p
		}
	}
	slices.SortStableFunc(out, func(a, b ProviderRefusal) int { return a.Time.Compare(b.Time) })
	return out
}
