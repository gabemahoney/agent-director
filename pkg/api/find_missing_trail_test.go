package api_test

// find_missing_trail_test.go — SR-A-2.5: ad.find_missing.tick emission tests.
//
// Covers the two emit sites in findMissingImpl:
//   - proc_absent: one event per row written to missing (SR-A-2.5 per-row path).
//   - degraded_mode_skip: one event when probe returns 0 IDs but ≥1 live rows
//     exist; zero rows are mutated (SRD §14.6).
//
// Zero-emit paths are also pinned to guard against spurious trail writes.
//
// Trail infrastructure: TestMain (example_main_test.go) fixes
// AGENT_DIRECTOR_STATE_DIR to a temp dir before any test runs. The trail
// singleton (sync.Once) captures that path on first Emit. All trail reads in
// this file go through readAPITrailLines, which opens apiTrailDir/ad-trail.jsonl.
//
// Coordination: another test writer owns cmd/agent-director/find_missing_cli_test.go
// (or recovery_cmd_test.go) for the CLI surface. No overlap.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
)

var apiTSRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// apiTrailFilePath returns the trail file path used by the singleton in api tests.
func apiTrailFilePath() string { return filepath.Join(apiTrailDir, "ad-trail.jsonl") }

// readAPITrailLines parses every JSONL line from the api trail file.
// Returns nil when the file does not exist yet.
func readAPITrailLines(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(apiTrailFilePath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("readAPITrailLines: %v", err)
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("readAPITrailLines: unmarshal %q: %v", sc.Text(), err)
		}
		rows = append(rows, m)
	}
	if sc.Err() != nil {
		t.Fatalf("readAPITrailLines: scan: %v", sc.Err())
	}
	return rows
}

// apiFindMissingTicksAt returns ad.find_missing.tick lines added after prevCount
// total lines in the api trail file.
func apiFindMissingTicksAt(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	all := readAPITrailLines(t)
	var out []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.find_missing.tick" {
			out = append(out, row)
		}
	}
	return out
}

// assertAPITrailStr checks row[key] == want.
func assertAPITrailStr(t *testing.T, row map[string]any, key, want string) {
	t.Helper()
	got, ok := row[key]
	if !ok {
		t.Errorf("field %q missing", key)
		return
	}
	if got != want {
		t.Errorf("[%q] = %v; want %q", key, got, want)
	}
}

// TestFindMissingProcAbsentEmitsTrail verifies that findMissingImpl emits one
// ad.find_missing.tick per row transitioned to missing with:
//   - reconciliation_reason="proc_absent"
//   - claude_instance_id equal to the transitioned row's instance ID
//   - prior_state from MarkSpawnMissing RETURNING ("working" in the fake store)
//   - new_state="missing"
//   - source="ad_find_missing"
//
// Two rows are absent from the probe set (tick-a, tick-c); one is present (tick-b).
// The test asserts exactly two ticks, one per missing row.
func TestFindMissingProcAbsentEmitsTrail(t *testing.T) {
	st := &fakeFindMissingStore{liveIDs: []string{"tick-a", "tick-b", "tick-c"}}
	prober := &fakeProber{set: map[string]struct{}{"tick-b": {}}}
	before := len(readAPITrailLines(t))

	res, err := api.FindMissing(context.Background(), st, prober, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 2 {
		t.Fatalf("count = %d; want 2", res.Count)
	}

	ticks := apiFindMissingTicksAt(t, before)
	if len(ticks) != 2 {
		t.Fatalf("want 2 ad.find_missing.tick (proc_absent); got %d", len(ticks))
	}

	tickIDs := make(map[string]bool)
	for _, tick := range ticks {
		id, ok := tick["claude_instance_id"].(string)
		if !ok || id == "" {
			t.Errorf("[claude_instance_id] = %v; want non-empty string", tick["claude_instance_id"])
		}
		tickIDs[id] = true

		assertAPITrailStr(t, tick, "event", "ad.find_missing.tick")
		assertAPITrailStr(t, tick, "reconciliation_reason", "proc_absent")
		assertAPITrailStr(t, tick, "source", "ad_find_missing")
		assertAPITrailStr(t, tick, "new_state", "missing")
		// fakeFindMissingStore.MarkSpawnMissing always returns "working" as the
		// prior state — matching the representative live state documented in the fake.
		assertAPITrailStr(t, tick, "prior_state", "working")

		ts, ok := tick["ts"].(string)
		if !ok || !apiTSRe.MatchString(ts) {
			t.Errorf("[ts] = %v; want RFC3339Nano timestamp", tick["ts"])
		}
	}
	if !tickIDs["tick-a"] || !tickIDs["tick-c"] {
		t.Errorf("tick instance IDs = %v; want tick-a and tick-c", tickIDs)
	}
}

// TestFindMissingDegradedModeEmitsTrail verifies the SRD §14.6 degraded-mode
// guard: when the probe returns 0 IDs but ≥1 live rows exist, findMissingImpl
// must:
//   - emit exactly one ad.find_missing.tick with reconciliation_reason="degraded_mode_skip"
//   - set claude_instance_id to JSON null
//   - set live_row_count to the number of live rows
//   - mutate zero rows (MarkSpawnMissing must not be called)
func TestFindMissingDegradedModeEmitsTrail(t *testing.T) {
	st := &fakeFindMissingStore{liveIDs: []string{"dg-1", "dg-2"}}
	prober := &fakeProber{set: map[string]struct{}{}}
	before := len(readAPITrailLines(t))

	res, err := api.FindMissing(context.Background(), st, prober, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("degraded mode count = %d; want 0", res.Count)
	}
	if len(st.marked) != 0 {
		t.Errorf("degraded mode marked rows = %v; want none (zero rows mutated)", st.marked)
	}

	ticks := apiFindMissingTicksAt(t, before)
	if len(ticks) != 1 {
		t.Fatalf("want 1 ad.find_missing.tick (degraded_mode_skip); got %d", len(ticks))
	}
	tick := ticks[0]
	assertAPITrailStr(t, tick, "event", "ad.find_missing.tick")
	assertAPITrailStr(t, tick, "reconciliation_reason", "degraded_mode_skip")
	assertAPITrailStr(t, tick, "source", "ad_find_missing")

	// claude_instance_id must be JSON null (maps to nil in map[string]any).
	if v, exists := tick["claude_instance_id"]; !exists || v != nil {
		t.Errorf("[claude_instance_id] = %v; want null", tick["claude_instance_id"])
	}
	// live_row_count must equal len(liveIDs)=2. JSON numbers decode to float64.
	if v, ok := tick["live_row_count"].(float64); !ok || v != 2 {
		t.Errorf("[live_row_count] = %v (%T); want 2", tick["live_row_count"], tick["live_row_count"])
	}
}

// TestFindMissingZeroTouchEmitsNoTrail pins the normal-mode zero-touch path:
// when the probe covers every live row, findMissingImpl must emit zero
// ad.find_missing.tick lines.
func TestFindMissingZeroTouchEmitsNoTrail(t *testing.T) {
	st := &fakeFindMissingStore{liveIDs: []string{"alive-p", "alive-q"}}
	prober := &fakeProber{set: map[string]struct{}{"alive-p": {}, "alive-q": {}}}
	before := len(readAPITrailLines(t))

	if _, err := api.FindMissing(context.Background(), st, prober, &recordingLogger{}); err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if ticks := apiFindMissingTicksAt(t, before); len(ticks) != 0 {
		t.Errorf("zero-touch path emitted %d ad.find_missing.tick; want 0", len(ticks))
	}
}
