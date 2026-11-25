package hook_test

// Regression coverage for b.3mx: Claude's non-classifier fields must never
// prevent a hook write; Codex and Cursor retain their own strict decoding.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
)

type payloadTypeCase struct {
	name  string
	value any
}

var nonStringPayloadValues = []payloadTypeCase{
	{"number", 12345},
	{"bool", true},
	{"object", map[string]any{"value": "compact"}},
	{"array", []any{"compact"}},
}

func TestClaudeNonClassifierFieldsTreatNonStringsAsAbsent(t *testing.T) {
	values := append(append([]payloadTypeCase{}, nonStringPayloadValues...), payloadTypeCase{"null", json.RawMessage("null")})
	for _, fixture := range []string{
		fxStart, fxRequest, "permission-request.json", "user-prompt-submit.json", "stop.json",
		// Exercise source's compact branch and file_path's owned-wake value too.
		"session-start-compact.json", "file-changed.json",
	} {
		for _, key := range hook.ClaudeLenientPayloadFieldsForTest {
			t.Run(fixture+"/"+key, func(t *testing.T) {
				absent := payloadWith(t, fixture, map[string]any{key: nil})
				want, err := hook.ClassifyEvent("claude", absent)
				if err != nil {
					t.Fatalf("classify absent field: %v", err)
				}
				for _, value := range values {
					t.Run(value.name, func(t *testing.T) {
						raw := payloadWith(t, fixture, map[string]any{key: value.value})
						got, err := hook.ClassifyEvent("claude", raw)
						if err != nil {
							t.Errorf("ClassifyEvent: %v; want no error", err)
						} else if !reflect.DeepEqual(got, want) {
							t.Errorf("ClassifyEvent = %+v; want field absent: %+v", got, want)
						}
						if got := hook.PeekEventName(raw); got != want.EventName {
							t.Errorf("PeekEventName = %q; want %q", got, want.EventName)
						}
					})
				}
			})
		}
	}
}

func TestClassifierFieldsRejectNonStrings(t *testing.T) {
	for _, key := range hook.StrictPayloadFieldsForTest {
		for _, value := range nonStringPayloadValues {
			t.Run(key+"/"+value.name, func(t *testing.T) {
				raw := payloadWith(t, "permission-request.json", map[string]any{key: value.value})
				_, err := hook.ClassifyEvent("claude", raw)
				want := fmt.Sprintf("json: cannot unmarshal %s into Go struct field payload.%s of type string", value.name, key)
				if err == nil || err.Error() != want {
					t.Errorf("ClassifyEvent error = %v; want %q", err, want)
				}
			})
		}
	}
}

func TestOtherAgentFieldsRetainStrictTypes(t *testing.T) {
	for _, tc := range []struct{ agent, key string }{
		{"codex", "session_id"}, {"cursor", "session_id"}, {"cursor", "origin"},
	} {
		for _, value := range nonStringPayloadValues {
			t.Run(tc.agent+"/"+tc.key+"/"+value.name, func(t *testing.T) {
				raw := payloadWith(t, "stop.json", map[string]any{"origin": "main", tc.key: value.value})
				_, err := hook.ClassifyEvent(tc.agent, raw)
				want := fmt.Sprintf("json: cannot unmarshal %s into Go struct field payload.%s of type string", value.name, tc.key)
				if err == nil || err.Error() != want {
					t.Errorf("ClassifyEvent error = %v; want %q", err, want)
				}
			})
		}
	}
}

func TestHandleSubagentStartNumericSessionID(t *testing.T) {
	r := newSubRow(t, subRowID)
	before := r.spawn()
	f := r.fire(variant(t, fxStart, setKey("session_id", 12345)), at(1))
	after := r.spawn()
	rows := r.rows()
	if strings.Contains(f.Log, "hook: classify") {
		t.Errorf("classify failure: %s", f.Log)
	}
	if after.RowVersion != before.RowVersion.(int64)+1 {
		t.Errorf("row_version = %v; want %v (one soft refresh)", after.RowVersion, before.RowVersion.(int64)+1)
	}
	if len(rows.Entries) != 1 {
		t.Fatalf("entries = %d; want 1", len(rows.Entries))
	}
	if sid := rows.Entries[0].SessionID; sid != nil {
		t.Errorf("entry session_id = %v; want NULL", sid)
	}
}

func TestHandleStopNumericSessionID(t *testing.T) {
	for _, tc := range []struct {
		name string
		sid  any
	}{
		{"string control", subSessionID},
		{"number", 12345},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSubRow(t, subRowID)
			f := r.fire(variant(t, "stop.json",
				setKey("transcript_path", writeTranscript(t, subSessionID)), setKey("session_id", tc.sid)), at(1))
			if strings.Contains(f.Log, "hook: classify") {
				t.Errorf("classify failure: %s", f.Log)
			}
			if got := r.spawn().State; got != "waiting" {
				t.Errorf("state = %v; want waiting", got)
			}
		})
	}
}
