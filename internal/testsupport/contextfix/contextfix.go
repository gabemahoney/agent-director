// Package contextfix provides sanitized native context captures and explicitly
// synthetic reliability variants. It never imports product API types.
package contextfix

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

//go:embed testdata
var data embed.FS

// Sample names one captured source and its independently recorded expectation.
type Sample struct {
	Name         string
	Agent        string
	SessionID    string
	Transcript   []byte
	Expected     json.RawMessage
	ChildID      string
	ResponseUUID string
	Callback     json.RawMessage
	ObservedAt   time.Time
}

// UnknownJSON is the required explicit-null representation.
const UnknownJSON = `{"used_tokens":null,"window_tokens":null,"fraction":null,"source":null,"as_of":null}`

// Load returns a named capture; missing and unsupported cases are synthetic.
func Load(name string) (Sample, error) {
	s := Sample{Name: name, Agent: "codex", Expected: json.RawMessage(UnknownJSON)}
	path := "codex/0.159.2/" + name
	switch name {
	case "claude-parent", "claude-child":
		return loadClaude(name)
	case "writer", "reviewer":
		var provenance struct {
			SessionID string `json:"session_id"`
		}
		b, err := data.ReadFile("testdata/" + path + ".provenance.json")
		if err != nil {
			return s, err
		}
		if err = json.Unmarshal(b, &provenance); err != nil {
			return s, err
		}
		s.SessionID = provenance.SessionID
		s.Expected, err = data.ReadFile("testdata/" + path + ".expected.json")
		if err != nil {
			return s, err
		}
	case "claude-missing-capacity":
		s.Agent = "claude"
		path = "claude/2.1.280/missing-capacity"
		s.SessionID = "ecc35d7b-a6fc-42fa-9460-e2f1899b9b66"
	case "cursor-2026.10.01-e373342", "cursor-2026.10.12-d47ca18":
		s.Agent = "cursor"
		s.SessionID = "unqualified-cursor"
		return s, nil
	default:
		return s, fmt.Errorf("unknown context sample %q", name)
	}
	var err error
	s.Transcript, err = data.ReadFile("testdata/" + path + ".jsonl")
	return s, err
}

// Must loads a capture, reporting fixture errors through the owning test.
func Must(t testing.TB, name string) Sample {
	t.Helper()
	s, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Variant changes captured metadata/accounting for an explicitly synthetic case.
func Variant(t testing.TB, name string) Sample {
	t.Helper()
	s := Must(t, "writer")
	s.Name = "synthetic-" + name
	lines := strings.Split(strings.TrimSuffix(string(s.Transcript), "\n"), "\n")
	records := make([]map[string]any, len(lines))
	for i, line := range lines {
		if err := json.Unmarshal([]byte(line), &records[i]); err != nil {
			t.Fatal(err)
		}
	}
	meta := records[0]["payload"].(map[string]any)
	last := -1
	turn := -1
	start := -1
	for i, r := range records {
		p := r["payload"].(map[string]any)
		if r["type"] == "turn_context" {
			turn = i
		}
		if p["type"] == "task_started" {
			start = i
		}
		if p["type"] == "token_count" {
			last = i
		}
	}
	usage := records[last]["payload"].(map[string]any)
	info := usage["info"].(map[string]any)
	current := info["last_token_usage"].(map[string]any)
	unknown := true
	switch name {
	case "untested-version":
		meta["cli_version"] = "0.159.3"
	case "wrong-provider":
		meta["model_provider"] = "other"
	case "missing-provider":
		delete(meta, "model_provider")
	case "wrong-history":
		meta["history_mode"] = "unknown"
	case "fork":
		meta["forked_from_id"] = "parent-session"
	case "history-base":
		meta["history_base"] = map[string]any{"offset": 1}
	case "wrong-session":
		s.SessionID = "other-session"
	case "missing-session":
		s.SessionID = ""
	case "missing-usage":
		usage["info"] = nil
	case "missing-current":
		delete(info, "last_token_usage")
	case "missing-capacity":
		delete(info, "model_context_window")
	case "zero-capacity":
		info["model_context_window"] = 0
	case "wrong-capacity":
		info["model_context_window"] = 1
	case "negative-usage":
		current["total_tokens"] = -1
	case "missing-total":
		delete(current, "total_tokens")
	case "missing-time":
		delete(records[last], "timestamp")
	case "regressing-time":
		records[last]["timestamp"] = records[0]["timestamp"]
	case "wrong-turn":
		records[turn]["payload"].(map[string]any)["turn_id"] = "other-turn"
	case "missing-model":
		delete(records[turn]["payload"].(map[string]any), "model")
	case "missing-start-window":
		delete(records[start]["payload"].(map[string]any), "model_context_window")
	case "compaction-gap", "response-gap", "unknown-event":
		r := map[string]any{"timestamp": records[len(records)-1]["timestamp"], "type": "compacted", "payload": map[string]any{}}
		if name == "response-gap" {
			r["type"] = "response_item"
		}
		if name == "unknown-event" {
			r["type"] = "event_msg"
			r["payload"] = map[string]any{"type": "unqualified_event"}
		}
		records = append(records, r)
	case "post-compaction":
		records = append(records[:last], append([]map[string]any{{"timestamp": records[last]["timestamp"], "type": "compacted", "payload": map[string]any{}}}, records[last:]...)...)
		unknown = false
	case "zero-usage":
		current["total_tokens"] = 0
		unknown = false
		var expected map[string]any
		_ = json.Unmarshal(s.Expected, &expected)
		expected["used_tokens"] = 0
		expected["fraction"] = 0
		s.Expected, _ = json.Marshal(expected)
	default:
		t.Fatalf("unknown synthetic context variant %q", name)
	}
	var out strings.Builder
	for _, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(b)
		out.WriteByte('\n')
	}
	s.Transcript = []byte(out.String())
	if unknown {
		s.Expected = Must(t, "claude-missing-capacity").Expected
	}
	return s
}
