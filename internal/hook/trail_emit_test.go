package hook_test

// trail_emit_test.go — the hook's trail lines (SR-A-2.1 ad.hook.fired, SR-A-2.3
// ad.relay_attempt.completed, SR-A-2.7 ad.resume.observed) and the test
// binary's TestMain. trail.Emit pins its file on the first call, so TestMain
// sets HOME (os.Setenv, before m.Run) to an isolated temp home; each test
// reads only the lines written after its own checkpoint.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// trailTestHome is the isolated HOME TestMain sets for the test binary.
var trailTestHome string

func TestMain(m *testing.M) {
	sandboxguard.Require()
	d, err := os.MkdirTemp("", "ad-hook-trail-*")
	if err != nil {
		panic("TestMain: MkdirTemp: " + err.Error())
	}
	defer os.RemoveAll(d)
	trailTestHome = d
	if err := os.Setenv("HOME", d); err != nil {
		panic("TestMain: Setenv: " + err.Error())
	}
	os.Exit(m.Run())
}

// trailFile returns the trail file path used by the singleton.
func trailFile() string {
	return filepath.Join(trailTestHome, ".agent-director", "ad-trail.jsonl")
}

// readTrailLines parses every JSONL line from path; nil when it does not exist yet.
func readTrailLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("readTrailLines: %v", err)
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("readTrailLines: unmarshal %q: %v", sc.Text(), err)
		}
		rows = append(rows, m)
	}
	if sc.Err() != nil {
		t.Fatalf("readTrailLines: scan: %v", sc.Err())
	}
	return rows
}

// hookFiredAt returns the one ad.hook.fired line written after prevCount lines.
func hookFiredAt(t *testing.T, prevCount int) map[string]any {
	t.Helper()
	var fired []map[string]any
	for _, row := range readTrailLines(t, trailFile())[prevCount:] {
		if row["event"] == "ad.hook.fired" {
			fired = append(fired, row)
		}
	}
	if len(fired) != 1 {
		t.Fatalf("want 1 ad.hook.fired after offset %d; got %d", prevCount, len(fired))
	}
	return fired[0]
}

// assertStr checks row[key] equals want.
func assertStr(t *testing.T, row map[string]any, key, want string) {
	t.Helper()
	if got, ok := row[key]; !ok || got != want {
		t.Errorf("[%q] = %v (present %v); want %q", key, got, ok, want)
	}
}

// assertNull checks row[key] is present and JSON null.
func assertNull(t *testing.T, row map[string]any, key string) {
	t.Helper()
	if got, ok := row[key]; !ok || got != nil {
		t.Errorf("[%q] = %v (present %v); want null", key, got, ok)
	}
}

// assertNoToolInput checks that "tool_input" is absent from the line.
func assertNoToolInput(t *testing.T, row map[string]any) {
	t.Helper()
	if _, ok := row["tool_input"]; ok {
		t.Error(`trail line must not contain "tool_input"`)
	}
}

// envHook builds the env func Handle expects.
func envHook(instanceID, relayMode string) func(string) string {
	return func(k string) string {
		switch k {
		case "AGENT_DIRECTOR_INSTANCE_ID":
			return instanceID
		case hook.EnvRelayMode:
			return relayMode
		}
		return ""
	}
}

// TestTrailEmitHookFired: one ad.hook.fired per Handle with its fields, across
// (lifecycle × upsert_outcome) on a real store; never tool_input. Unreachable
// cells: (SessionStart, inserted) and (PermissionRequest, no_change).
func TestTrailEmitHookFired(t *testing.T) {
	const pr = `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"cmd":"echo hi"}}`
	cases := []struct {
		name, payload, id, relay string
		seed                     bool // a live row (pane recorded), fired by its agent
		outcome, event, tool     string
		token                    bool // a non-null request_token (relay path)
		sessID                   string
	}{
		{name: "pre_tool_use_no_change", payload: `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, id: "te-ptu-nc",
			outcome: "no_change", event: "PreToolUse", tool: "Bash"},
		{name: "pre_tool_use_updated", payload: `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, id: "te-ptu-up", seed: true,
			outcome: "updated", event: "PreToolUse", tool: "Bash"},
		{name: "session_start_updated", payload: `{"hook_event_name":"SessionStart","transcript_path":"/x/abc123.jsonl"}`, id: "te-ss-up", seed: true,
			outcome: "updated", event: "SessionStart", sessID: "abc123"},
		{name: "session_start_no_change", payload: `{"hook_event_name":"SessionStart","transcript_path":"/x/def456.jsonl"}`, id: "te-ss-nc",
			outcome: "no_change", event: "SessionStart", sessID: "def456"},
		// The relay's INSERT outcome overwrites the transition's; the poll times out on the virtual clock.
		{name: "permission_request_relay_inserted", payload: pr, id: "te-pr-ins", relay: hook.RelayModeOn, seed: true,
			outcome: "inserted", event: "PermissionRequest", tool: "Bash", token: true},
		{name: "session_end_compact_updated", payload: `{"hook_event_name":"SessionEnd","reason":"compact"}`, id: "te-se-up", seed: true,
			outcome: "updated", event: "SessionEnd"},
		{name: "session_end_compact_no_change", payload: `{"hook_event_name":"SessionEnd","reason":"compact"}`, id: "te-se-nc",
			outcome: "no_change", event: "SessionEnd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := len(readTrailLines(t, trailFile()))
			// SR-22.9: with no row the hook changes nothing (no_change) and logs no ad.hook.ignored.
			st, _ := storefix.OpenTempStore(t)
			parent := hookParent{PID: apitest.TestPanePID, Start: storefix.SeedPaneStarttime}
			if c.seed {
				st, _ = seedAgentRow(t, c.id, store.StateWorking)
				parent = agentParent(t, st, c.id)
			}
			now, restore := setupVirtualClock(t)
			defer restore()
			hc := hookConfig(envHook(c.id, c.relay), parent)
			hc.Cfg, hc.Clock = config.Relay{TimeoutSeconds: 1}, &advancingClock{now: now}
			if err := hook.Handle(context.Background(), strings.NewReader(c.payload), io.Discard, st, hc, nil); err != nil {
				t.Fatalf("Handle: %v", err)
			}

			row := hookFiredAt(t, before)
			for key, want := range map[string]string{"source": "ad_hook", "claude_instance_id": c.id, "event_name": c.event,
				"relay_mode": c.relay, "upsert_outcome": c.outcome, "session_id": c.sessID} {
				assertStr(t, row, key, want)
			}
			if m, _ := row["matcher"].([]any); len(m) != 1 || m[0] != "*" {
				t.Errorf("matcher = %v; want [*]", row["matcher"])
			}
			if ts, ok := row["ts"].(string); !ok || ts == "" {
				t.Error("ts missing or empty")
			}
			if c.tool == "" {
				assertNull(t, row, "tool_name")
			} else {
				assertStr(t, row, "tool_name", c.tool)
			}
			if !c.token {
				assertNull(t, row, "request_token")
			} else if tok, _ := row["request_token"].(string); tok == "" {
				t.Errorf("request_token = %v; want a non-empty string", row["request_token"])
			}
			assertNoToolInput(t, row)
		})
	}
}

// TestTrailEmitRelayLines: a relayed PermissionRequest that polls prints its
// acked answer (a verdict, or its timeout deny, SRD §6.4) and writes one
// ad.relay_attempt.completed (CASE B: the relay is DB-poll based, so its fields
// are degenerate) and, after its envelope, one ad.resume.observed with the
// verdict and a numeric elapsed_ms_from_row_open (non-negative and small for a
// row just opened); both carry ad.hook.fired's request_token. A poll whose
// reads keep failing answers nothing and writes no ad.resume.observed (b.146
// rule 3).
func TestTrailEmitRelayLines(t *testing.T) {
	cases := []struct {
		verdict          string
		rows             []store.PermissionRow
		errs             []error
		behavior, reason string // the envelope; "" = none
	}{
		{"allow", []store.PermissionRow{{Decision: "allow", DecisionReason: "ok", CreatedAt: time.Now()}}, []error{nil}, "allow", "ok"},
		{"deny", []store.PermissionRow{{Decision: "deny", DecisionReason: "nope"}}, []error{nil}, "deny", "nope"},
		{"timeout", []store.PermissionRow{{}}, []error{nil}, "deny", ""},
		{"error", make([]store.PermissionRow, 10), repeatErr(10, errors.New("db error")), "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.verdict, func(t *testing.T) {
			id := "te-relay-lines-" + tc.verdict
			before := len(readTrailLines(t, trailFile()))
			var stdout bytes.Buffer
			if err := hook.Handle(context.Background(), strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Write"}`),
				&stdout, &flakyRelayStore{getRows: tc.rows, getErrs: tc.errs}, relayDoubleConfig(id), nil); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			want := ""
			if tc.behavior != "" {
				want = hook.EncodeDecision(hook.EventNamePermissionRequest, tc.behavior, tc.reason) + "\n"
			}
			if stdout.String() != want {
				t.Errorf("stdout = %q; want %q", stdout.String(), want)
			}

			token, _ := hookFiredAt(t, before)["request_token"].(string)
			attempt, resume := linesAfter(t, before, "ad.relay_attempt.completed", id), linesAfter(t, before, "ad.resume.observed", id)
			if tc.behavior == "" {
				if token == "" || len(attempt) != 1 || len(resume) != 0 {
					t.Errorf("ad.hook.fired token %q, %d ad.relay_attempt.completed, %d ad.resume.observed; want a token, 1, 0", token, len(attempt), len(resume))
				}
				return
			}
			if token == "" || len(attempt) != 1 || len(resume) != 1 {
				t.Fatalf("ad.hook.fired token %q, %d ad.relay_attempt.completed, %d ad.resume.observed; want a token, 1, 1", token, len(attempt), len(resume))
			}
			for key, want := range map[string]any{"source": "relay_hook", "request_token": token, "target_endpoint": "db_poll",
				"outcome": "db_relay_active", "bytes_sent": 0.0, "bytes_received": 0.0} {
				if got := attempt[0][key]; got != want {
					t.Errorf("ad.relay_attempt.completed %s = %v; want %v", key, got, want)
				}
			}
			for key, want := range map[string]any{"source": "ad_polling", "request_token": token, "verdict": tc.verdict} {
				if got := resume[0][key]; got != want {
					t.Errorf("ad.resume.observed %s = %v; want %v", key, got, want)
				}
			}
			for _, line := range []map[string]any{attempt[0], resume[0]} {
				if ts, _ := line["ts"].(string); ts == "" {
					t.Errorf("%s ts missing or empty", line["event"])
				}
			}
			elapsed, ok := resume[0]["elapsed_ms_from_row_open"].(float64)
			if !ok || tc.verdict == "allow" && (elapsed < 0 || elapsed >= 60_000) {
				t.Errorf("elapsed_ms_from_row_open = %v; want a JSON number (0 <= n < 60000 for a row just opened)", resume[0]["elapsed_ms_from_row_open"])
			}
		})
	}
}
