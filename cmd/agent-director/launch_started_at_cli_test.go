package main_test

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// launchShape is one seeded row shape for the launch_started_at CLI tests;
// want is the exact wire value, or "" when the key must be absent.
type launchShape struct {
	name, id, state string
	opt             apitest.SpawnOption
	want            string
}

// launchShapes covers a readable pending row (with and without a ms fraction),
// a non-pending row that has a stored launch start, and unreadable pending rows.
func launchShapes() []launchShape {
	return []launchShape{
		{"pending ms fraction", "ls-pending-frac", "pending", apitest.WithLaunchStartedAt(1790000000123), "2026-09-21T14:13:20.123Z"},
		{"pending whole second", "ls-pending-whole", "pending", apitest.WithLaunchStartedAt(1790000000000), "2026-09-21T14:13:20Z"},
		{"waiting with launch start", "ls-waiting", "waiting", apitest.WithLaunchStartedAt(1790000000123), ""},
		{"pending NULL", "ls-pending-null", "pending", apitest.WithNoLaunchStartedAt(), ""},
		{"pending non-integer", "ls-pending-raw", "pending", apitest.WithRawLaunchStartedAt("not-a-number"), ""},
	}
}

// seedLaunchShapes bootstraps home's store and seeds every launchShapes row.
func seedLaunchShapes(t *testing.T, home, fakeDir string) {
	t.Helper()
	if _, stderr, code := runSpawnCLI(t, home, fakeDir, "list"); code != 0 {
		t.Fatalf("bootstrap list exit = %d; stderr=%s", code, stderr)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	for _, sh := range launchShapes() {
		if _, err := apitest.SeedSpawn(dbPath, sh.id, sh.state, "/tmp", "off", "", false, sh.opt); err != nil {
			t.Fatalf("seed %s: %v", sh.id, err)
		}
	}
}

// launchField returns the raw launch_started_at string of obj and whether the
// key is present; a present non-string value (e.g. null) fails the test.
func launchField(t *testing.T, obj map[string]json.RawMessage) (string, bool) {
	t.Helper()
	raw, ok := obj["launch_started_at"]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("launch_started_at = %s; want a JSON string or absent", raw)
	}
	return s, true
}

// listRowsByID runs `list` with extra args and returns each row's raw keys by id.
func listRowsByID(t *testing.T, home, fakeDir string, extra ...string) map[string]map[string]json.RawMessage {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, append([]string{"list"}, extra...)...)
	if code != 0 {
		t.Fatalf("list %v exit = %d; stderr=%s", extra, code, stderr)
	}
	var res struct {
		Spawns []map[string]json.RawMessage `json:"spawns"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse list %q: %v", stdout, err)
	}
	out := make(map[string]map[string]json.RawMessage, len(res.Spawns))
	for _, row := range res.Spawns {
		var id string
		if err := json.Unmarshal(row["claude_instance_id"], &id); err != nil {
			t.Fatalf("list row without a string claude_instance_id: %v", row)
		}
		out[id] = row
	}
	return out
}

// verbObject runs status or get for id (exit 0 required) and returns its raw
// top-level keys; for list it returns id's row from an unfiltered list.
func verbObject(t *testing.T, home, fakeDir, verb, id string) map[string]json.RawMessage {
	t.Helper()
	if verb == "list" {
		row, ok := listRowsByID(t, home, fakeDir)[id]
		if !ok {
			t.Fatalf("list has no row %q", id)
		}
		return row
	}
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, verb, "--claude-instance-id", id)
	if code != 0 {
		t.Fatalf("%s exit = %d; stderr=%s", verb, code, stderr)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
		t.Fatalf("parse %s %q: %v", verb, stdout, err)
	}
	return obj
}

// assertLaunchField checks obj's launch_started_at against want ("" = absent).
func assertLaunchField(t *testing.T, obj map[string]json.RawMessage, want string) {
	t.Helper()
	got, present := launchField(t, obj)
	switch {
	case want == "" && present:
		t.Errorf("launch_started_at = %q; want key absent", got)
	case want != "" && !present:
		t.Errorf("launch_started_at absent; want %q", want)
	case got != want:
		t.Errorf("launch_started_at = %q; want %q", got, want)
	}
}

// TestLaunchStartedAtCLISeeded pins SR-22.2/SR-5.5 on the CLI: status, get and
// list print launch_started_at only for a readable pending row, exit 0 always.
func TestLaunchStartedAtCLISeeded(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	seedLaunchShapes(t, home, fakeDir)
	for _, verb := range []string{"status", "get", "list"} {
		for _, sh := range launchShapes() {
			t.Run(verb+"/"+sh.name, func(t *testing.T) {
				assertLaunchField(t, verbObject(t, home, fakeDir, verb, sh.id), sh.want)
			})
		}
	}
}

// TestLaunchStartedAtCLIListStatePending pins that `list --state pending` over a
// mixed store returns every pending row, with the field only on readable ones.
func TestLaunchStartedAtCLIListStatePending(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	seedLaunchShapes(t, home, fakeDir)
	rows := listRowsByID(t, home, fakeDir, "--state", "pending")
	var wantIDs, gotIDs []string
	for _, sh := range launchShapes() {
		if sh.state == "pending" {
			wantIDs = append(wantIDs, sh.id)
		}
	}
	for id := range rows {
		gotIDs = append(gotIDs, id)
	}
	sort.Strings(wantIDs)
	sort.Strings(gotIDs)
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("list --state pending ids = %v; want %v", gotIDs, wantIDs)
	}
	for _, sh := range launchShapes() {
		if row, ok := rows[sh.id]; ok {
			t.Run(sh.name, func(t *testing.T) { assertLaunchField(t, row, sh.want) })
		}
	}
}

// rfc3339UTCMillis is the wire shape: RFC3339 UTC, fraction of 1-3 digits or none.
var rfc3339UTCMillis = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{1,3})?Z$`)

// TestLaunchStartedAtCLISpawnEndToEnd pins that a fresh spawn (fake tmux) shows
// pending and a UTC launch start within wall-clock bounds on status, get and list.
func TestLaunchStartedAtCLISpawnEndToEnd(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	before := time.Now().Truncate(time.Millisecond)
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", t.TempDir())
	after := time.Now()
	if code != 0 {
		t.Fatalf("spawn exit = %d; stderr=%s", code, stderr)
	}
	var res spawnResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || res.ClaudeInstanceID == "" {
		t.Fatalf("parse spawn stdout %q: %v", stdout, err)
	}
	for _, verb := range []string{"status", "get", "list"} {
		t.Run(verb, func(t *testing.T) {
			obj := verbObject(t, home, fakeDir, verb, res.ClaudeInstanceID)
			var state string
			if err := json.Unmarshal(obj["state"], &state); err != nil || state != "pending" {
				t.Errorf("state = %s; want \"pending\"", obj["state"])
			}
			got, present := launchField(t, obj)
			if !present {
				t.Fatalf("launch_started_at absent on a freshly spawned pending row")
			}
			if !rfc3339UTCMillis.MatchString(got) {
				t.Errorf("launch_started_at = %q; want RFC3339 UTC with at most ms precision", got)
			}
			ts, err := time.Parse(time.RFC3339Nano, got)
			if err != nil {
				t.Fatalf("parse launch_started_at %q: %v", got, err)
			}
			if ts.Before(before) || ts.After(after) {
				t.Errorf("launch_started_at = %s; want within [%s, %s]", ts, before, after)
			}
		})
	}
}
