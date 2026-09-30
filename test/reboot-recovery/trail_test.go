package rebootrecovery_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trailRecords returns the records of event for instanceID in the trail the
// CLI and the stub's hook wrote under the test's isolated HOME.
func trailRecords(t *testing.T, home, event, instanceID string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".agent-director", "ad-trail.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read trail: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("trail line %q is not JSON: %v", line, err)
		}
		if rec["event"] == event && rec["claude_instance_id"] == instanceID {
			out = append(out, rec)
		}
	}
	return out
}

// assertHookApplied fails when the stub's SessionStart for instanceID was
// ignored by the hook gate (SR-22.9): any ad.hook.ignored record for the row.
func assertHookApplied(t *testing.T, home, instanceID string) {
	t.Helper()
	if got := trailRecords(t, home, "ad.hook.ignored", instanceID); len(got) != 0 {
		t.Fatalf("row %s: %d ad.hook.ignored records, want none: %v", instanceID, len(got), got)
	}
}

// assertMarkedProcAbsent fails unless find-missing ticked instanceID exactly
// once, as a mark to missing with reason proc_absent (SR-11.3, SR-11.4).
func assertMarkedProcAbsent(t *testing.T, home, instanceID string) {
	t.Helper()
	ticks := trailRecords(t, home, "ad.find_missing.tick", instanceID)
	if len(ticks) != 1 {
		t.Fatalf("row %s: %d ad.find_missing.tick records, want 1: %v", instanceID, len(ticks), ticks)
	}
	if got := ticks[0]["reconciliation_reason"]; got != "proc_absent" {
		t.Errorf("row %s: tick reconciliation_reason = %v, want proc_absent", instanceID, got)
	}
	if got := ticks[0]["new_state"]; got != "missing" {
		t.Errorf("row %s: tick new_state = %v, want missing", instanceID, got)
	}
}
