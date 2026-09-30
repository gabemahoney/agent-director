package store

// Export-for-test shim: hands the store trail (trail_emit_test.go) to the
// external hook gate tests (hook_gate_test.go and siblings). It reads only.

import "testing"

// TrailMark returns how many store trail lines exist now, for TrailEventsSince.
func TrailMark(t *testing.T) int {
	t.Helper()
	return len(readStoreTrailLines(t))
}

// TrailEventsSince returns the event lines naming instanceID written after mark.
func TrailEventsSince(t *testing.T, mark int, event, instanceID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, row := range readStoreTrailLines(t)[mark:] {
		if row["event"] == event && row["claude_instance_id"] == instanceID {
			out = append(out, row)
		}
	}
	return out
}
