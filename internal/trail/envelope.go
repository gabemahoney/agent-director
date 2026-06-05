package trail

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"time"
)

// tsRegex matches the SR-A-7.9 timestamp format: ISO-8601 / RFC-3339 at
// millisecond or finer precision, always UTC (trailing Z).
var tsRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// buildEnvelope constructs a JSONL envelope for a single audit event.
//
// Fields are placed at the top level of the JSON object; the "tool_input" key
// is silently dropped (binding invariant — tool_input must never appear in
// the trail).
//
// ts resolution (SR-A-7.9):
//   - Absent or nil → substituted silently with time.Now().UTC() at ms precision.
//   - Present but malformed → substituted with a warning emitted via l (when non-nil).
//   - Present and valid → used as-is.
//
// Returns the JSON-encoded envelope with a trailing '\n'.
func buildEnvelope(event string, fields map[string]any, l *log.Logger) ([]byte, error) {
	// Copy fields to avoid mutating the caller's map; drop tool_input.
	env := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		if k == "tool_input" {
			continue
		}
		env[k] = v
	}
	env["event"] = event

	// Resolve ts per SR-A-7.9.
	rawTS, hasTSKey := env["ts"]
	switch {
	case !hasTSKey || rawTS == nil:
		// Absent or nil — substitute silently.
		env["ts"] = nowUTCMillis()
	default:
		s, ok := rawTS.(string)
		if !ok || !tsRegex.MatchString(s) {
			// Non-string or malformed string — warn and substitute.
			if l != nil {
				l.Printf("trail: malformed ts %q on event %s; substituting time.Now()", rawTS, event)
			}
			env["ts"] = nowUTCMillis()
		}
		// else: valid string, leave as-is.
	}

	b, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	return append(b, '\n'), nil
}

// nowUTCMillis returns the current UTC time formatted to millisecond precision,
// matching the SR-A-7.9 regex (e.g. "2026-06-05T01:23:45.678Z").
func nowUTCMillis() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
