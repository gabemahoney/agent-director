package contextfix

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func loadClaude(name string) (Sample, error) {
	const root = "testdata/claude/2.1.280/"
	s := Sample{Name: name, Agent: "claude"}
	subject, source := "parent", "claude_jsonl_usage_statusline_capacity"
	callbackSource := "statusLine"
	if name == "claude-child" {
		subject, source, callbackSource = "child", "claude_jsonl_usage_subagent_statusline_capacity", "subagentStatusLine"
	}
	var provenance struct {
		Pairs []struct {
			Agent     string  `json:"agent"`
			SessionID string  `json:"session_id"`
			AgentID   string  `json:"agent_id"`
			Used      int64   `json:"used_tokens"`
			Window    int64   `json:"window_tokens"`
			Fraction  float64 `json:"fraction"`
			At        string  `json:"usage_as_of"`
		} `json:"pairs"`
	}
	b, err := data.ReadFile(root + "live-provenance.json")
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(b, &provenance); err != nil {
		return s, err
	}
	for _, p := range provenance.Pairs {
		if p.Agent == subject {
			s.SessionID, s.ChildID = p.SessionID, p.AgentID
			s.Expected, err = json.Marshal(struct {
				Used     int64   `json:"used_tokens"`
				Window   int64   `json:"window_tokens"`
				Fraction float64 `json:"fraction"`
				Source   string  `json:"source"`
				At       string  `json:"as_of"`
			}{p.Used, p.Window, p.Fraction, source, p.At})
			if err != nil {
				return s, err
			}
		}
	}
	s.Transcript, err = data.ReadFile(root + "live-" + subject + "-transcript.jsonl")
	if err != nil {
		return s, err
	}
	for _, line := range bytes.Split(bytes.TrimSpace(s.Transcript), []byte{'\n'}) {
		var r struct{ Type, UUID string }
		if err = json.Unmarshal(line, &r); err != nil {
			return s, err
		}
		if r.Type == "assistant" {
			s.ResponseUUID = r.UUID
		}
	}
	b, err = data.ReadFile(root + "live-statuslines.jsonl")
	if err != nil {
		return s, err
	}
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte{'\n'}) {
		var r struct {
			Source     string
			ObservedAt time.Time `json:"observed_at"`
		}
		if err = json.Unmarshal(line, &r); err != nil {
			return s, err
		}
		if r.Source == callbackSource {
			s.Callback = append(json.RawMessage(nil), line...)
			s.ObservedAt = r.ObservedAt
		}
	}
	return s, nil
}

// ClaudeModelVariant changes model/window metadata in a real Haiku pair;
// it is synthetic coverage, never evidence of a native alternate-model capture.
func ClaudeModelVariant(t testing.TB, name, model string, window int64) Sample {
	t.Helper()
	s := Must(t, name)
	s.Name = "synthetic-" + name + "-" + model
	var transcript bytes.Buffer
	for _, line := range bytes.Split(bytes.TrimSpace(s.Transcript), []byte{'\n'}) {
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		if r["type"] == "assistant" {
			r["message"].(map[string]any)["model"] = model
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		transcript.Write(b)
		transcript.WriteByte('\n')
	}
	s.Transcript = transcript.Bytes()
	var callback map[string]any
	if err := json.Unmarshal(s.Callback, &callback); err != nil {
		t.Fatal(err)
	}
	if s.ChildID == "" {
		callback["model"].(map[string]any)["id"] = model
		callback["context_window"].(map[string]any)["context_window_size"] = window
	} else {
		for _, task := range callback["tasks"].([]any) {
			p := task.(map[string]any)
			if p["id"] == s.ChildID {
				p["model"], p["contextWindowSize"] = model, window
			}
		}
	}
	var err error
	s.Callback, err = json.Marshal(callback)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal(s.Expected, &expected); err != nil {
		t.Fatal(err)
	}
	expected["window_tokens"] = window
	expected["fraction"] = expected["used_tokens"].(float64) / float64(window)
	s.Expected, err = json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ClaudeVariant makes synthetic adversaries without changing native captures.
func ClaudeVariant(t testing.TB, name string) Sample {
	t.Helper()
	s := Must(t, "claude-parent")
	s.Name = "synthetic-claude-" + name
	var records []map[string]any
	last := 0
	for _, line := range bytes.Split(bytes.TrimSpace(s.Transcript), []byte{'\n'}) {
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
		if r["type"] == "assistant" {
			last = len(records) - 1
		}
	}
	r := records[last]
	m := r["message"].(map[string]any)
	u := m["usage"].(map[string]any)
	unknown := true
	switch name {
	case "wrong-session":
		s.SessionID = "foreign-session"
	case "wrong-child":
		s.ChildID = "foreign-child"
	case "untested-version":
		r["version"] = "2.1.281"
	case "wrong-model":
		m["model"] = "unsupported"
	case "missing-model":
		delete(m, "model")
	case "missing-usage":
		delete(m, "usage")
	case "missing-time":
		delete(r, "timestamp")
	case "regressing-time":
		r["timestamp"] = "2000-01-01T00:00:00Z"
	case "missing-category":
		delete(u, "cache_read_input_tokens")
	case "negative-usage":
		u["input_tokens"] = -1
	case "overflow":
		u["input_tokens"] = json.Number("9223372036854775807")
	case "fractional-usage":
		u["input_tokens"] = 0.5
	case "zero-usage":
		for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens"} {
			u[key] = 0
		}
		var expected map[string]any
		if err := json.Unmarshal(s.Expected, &expected); err != nil {
			t.Fatal(err)
		}
		expected["used_tokens"], expected["fraction"] = 0, 0
		s.Expected, _ = json.Marshal(expected)
		unknown = false
	case "branch":
		r["parentUuid"] = "other-branch"
	case "missing-uuid":
		delete(r, "uuid")
	case "last-ordinary-iteration", "invalid-ordinary-iteration":
		counts := map[string]any{"type": "fallback_message"}
		for _, k := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens"} {
			counts[k] = u[k]
		}
		u["input_tokens"] = 123456
		u["iterations"] = []any{map[string]any{"type": "message", "input_tokens": 1, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 1}, counts, map[string]any{"type": "advisor_message", "input_tokens": 999}, map[string]any{"type": "compaction", "input_tokens": 999}}
		if name == "invalid-ordinary-iteration" {
			delete(counts, "input_tokens")
		} else {
			unknown = false
		}
	case "compaction-gap", "post-compaction", "new-response":
		copyRecord := func(v map[string]any) map[string]any {
			b, _ := json.Marshal(v)
			var out map[string]any
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		boundary := copyRecord(r)
		boundary["type"], boundary["subtype"], boundary["uuid"] = "system", "compact_boundary", "synthetic-boundary"
		delete(boundary, "message")
		boundary["parentUuid"] = records[len(records)-1]["uuid"]
		records = append(records, boundary)
		if name != "compaction-gap" {
			next := copyRecord(r)
			next["uuid"], next["parentUuid"] = "synthetic-response", "synthetic-boundary"
			records = append(records, next)
			s.ResponseUUID = "synthetic-response"
			unknown = false
		}
	case "malformed-tail":
		s.Transcript = append(s.Transcript, []byte("invalid\n")...)
		s.Expected = json.RawMessage(UnknownJSON)
		return s
	case "partial-tail":
		s.Transcript = append(s.Transcript, []byte("{\"type\":")...)
		s.Expected = json.RawMessage(UnknownJSON)
		return s
	case "unterminated":
		s.Transcript = bytes.TrimSuffix(s.Transcript, []byte{'\n'})
		s.Expected = json.RawMessage(UnknownJSON)
		return s
	default:
		t.Fatalf("unknown Claude variant %q", name)
	}
	s.Transcript = nil
	for _, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		s.Transcript = append(s.Transcript, b...)
		s.Transcript = append(s.Transcript, '\n')
	}
	if unknown {
		s.Expected = json.RawMessage(UnknownJSON)
	}
	return s
}
