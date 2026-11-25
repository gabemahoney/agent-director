package hook

import (
	"reflect"
	"strings"
	"testing"
)

// These test-only lists also drive the external behavioral tables (b.3mx).
// SR-5.1 closes the strict classifier list; other Claude fields are absent
// when non-string. Keep additions coupled to their behavioral coverage.
var StrictPayloadFieldsForTest = []string{
	"hook_event_name", "event_name", "tool_name", "reason", "matcher",
	"endReason", "transcript_path", "agent_id", "notification_type", "tool_use_id",
}

var ClaudeLenientPayloadFieldsForTest = []string{
	"session_id", "source", "origin", "turn_id", "prompt", "file_path",
}

// TestPayloadFieldsHaveTypePolicy guards new shared decode fields against
// silently extending Claude's fail-on-wrong-type list again (b.3mx).
func TestPayloadFieldsHaveTypePolicy(t *testing.T) {
	policies := map[string]string{}
	for policy, fields := range map[string][]string{
		"strict classifier": StrictPayloadFieldsForTest,
		"Claude lenient":    ClaudeLenientPayloadFieldsForTest,
	} {
		for _, name := range fields {
			if previous, ok := policies[name]; ok {
				t.Errorf("field %q has duplicate policies: %s and %s", name, previous, policy)
			}
			policies[name] = policy
		}
	}
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Anonymous {
				embedded := field.Type
				if embedded.Kind() == reflect.Pointer {
					embedded = embedded.Elem()
				}
				if embedded.Kind() == reflect.Struct {
					walk(embedded)
					continue
				}
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if _, ok := policies[name]; !ok {
				t.Errorf("payload field %s (json %q) needs an SR-5.1 strict or Claude lenient policy and behavioral coverage", field.Name, name)
			}
		}
	}
	walk(reflect.TypeOf(payload{}))
}
