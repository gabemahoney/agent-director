package hook_test

// trail_emit_test.go — table-driven tests for ad.hook.fired emission
// across (lifecycle × upsert_outcome) combinations.
//
// Singleton note: trail.Emit uses a process-level sync.Once singleton
// whose file path is locked in on the first call. TestMain fixes
// AGENT_DIRECTOR_STATE_DIR via os.Setenv (not t.Setenv) before any test
// runs so all Handle invocations write to a single known file.
// Individual tests capture a line-count checkpoint before calling Handle
// and assert only on lines added by their own invocation.

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

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// trailTestDir is the persistent AGENT_DIRECTOR_STATE_DIR for the test
// binary. Set by TestMain before any test function runs.
var trailTestDir string

// TestMain fixes AGENT_DIRECTOR_STATE_DIR for the whole test binary.
// The trail singleton initialises on the first trail.Emit call and stays
// pointed at this directory for the process lifetime.
func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "ad-hook-trail-*")
	if err != nil {
		panic("TestMain: MkdirTemp: " + err.Error())
	}
	defer os.RemoveAll(d)
	trailTestDir = d
	if err := os.Setenv("AGENT_DIRECTOR_STATE_DIR", d); err != nil {
		panic("TestMain: Setenv: " + err.Error())
	}
	os.Exit(m.Run())
}

// trailFile returns the trail file path used by the singleton.
func trailFile() string { return filepath.Join(trailTestDir, "ad-trail.jsonl") }

// readTrailLines parses every JSONL line from path into []map[string]any.
// Returns nil when the file does not exist yet.
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

// hookFiredAt reads all trail lines added after prevCount and returns the
// single ad.hook.fired line among them. Fails the test if not exactly one.
func hookFiredAt(t *testing.T, prevCount int) map[string]any {
	t.Helper()
	all := readTrailLines(t, trailFile())
	var fired []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.hook.fired" {
			fired = append(fired, row)
		}
	}
	if len(fired) != 1 {
		t.Fatalf("want 1 ad.hook.fired after offset %d; got %d (total lines now %d)",
			prevCount, len(fired), len(all))
	}
	return fired[0]
}

// assertStr checks row[key] equals want.
func assertStr(t *testing.T, row map[string]any, key, want string) {
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

// assertNull checks row[key] is present and JSON null (nil in Go).
func assertNull(t *testing.T, row map[string]any, key string) {
	t.Helper()
	got, ok := row[key]
	if !ok {
		t.Errorf("field %q missing (want null)", key)
		return
	}
	if got != nil {
		t.Errorf("[%q] = %v; want null", key, got)
	}
}

// assertMatcher checks "matcher" is a JSON array equal to want.
func assertMatcher(t *testing.T, row map[string]any, want []any) {
	t.Helper()
	raw, ok := row["matcher"]
	if !ok {
		t.Error(`field "matcher" missing`)
		return
	}
	got, ok := raw.([]any)
	if !ok {
		t.Errorf("matcher type = %T; want []any", raw)
		return
	}
	if len(got) != len(want) {
		t.Errorf("matcher = %v; want %v", got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("matcher[%d] = %v; want %v", i, got[i], want[i])
		}
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

// TestTrailEmitHookFired is a table-driven test covering representative
// (lifecycle × upsert_outcome) combinations using a real *store.Store.
// Mock-based tests elsewhere only ever observe no_change because those
// doubles do not implement outcomeTransitioner/outcomeUpserter.
//
// Unreachable cells (documented):
//   - (SessionStart, inserted): ApplyHookTransitionResult never INSERTs
//     spawns rows — it returns updated (row present) or no_change (absent).
//   - (PermissionRequest, no_change): UpsertOpenPermissionRequestResult
//     returns only UpsertInserted or UpsertError; UpsertNoChange is not
//     in its contract.
func TestTrailEmitHookFired(t *testing.T) {
	type tc struct {
		name    string
		payload string
		id      string
		relay   string // "" or hook.RelayModeOn
		seed    bool   // seed a live spawn row in the store before Handle
		outcome string
		event   string
		tool    string // "" → expect null in trail
		token   bool   // expect non-null request_token (relay path only)
		sessID  string // expected session_id value ("" if none)
	}
	cases := []tc{
		{
			name:    "pre_tool_use_no_change",
			payload: `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
			id:      "te-ptu-nc", relay: "", seed: false,
			outcome: "no_change", event: "PreToolUse", tool: "Bash",
		},
		{
			name:    "pre_tool_use_updated",
			payload: `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
			id:      "te-ptu-up", relay: "", seed: true,
			outcome: "updated", event: "PreToolUse", tool: "Bash",
		},
		{
			name:    "session_start_updated",
			payload: `{"hook_event_name":"SessionStart","transcript_path":"/x/abc123.jsonl"}`,
			id:      "te-ss-up", relay: "", seed: true,
			outcome: "updated", event: "SessionStart", sessID: "abc123",
		},
		{
			name:    "session_start_no_change",
			payload: `{"hook_event_name":"SessionStart","transcript_path":"/x/def456.jsonl"}`,
			id:      "te-ss-nc", relay: "", seed: false,
			outcome: "no_change", event: "SessionStart", sessID: "def456",
		},
		{
			// relay path: upsert_outcome comes from UpsertOpenPermissionRequestResult
			// (overwrites the ApplyHookTransitionResult value). Polling times out via
			// virtual clock — deny envelope lands in stdout, trail records "inserted".
			name:    "permission_request_relay_inserted",
			payload: `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`,
			id:      "te-pr-ins", relay: hook.RelayModeOn, seed: true,
			outcome: "inserted", event: "PermissionRequest", tool: "Bash", token: true,
		},
		{
			name:    "session_end_compact_updated",
			payload: `{"hook_event_name":"SessionEnd","reason":"compact"}`,
			id:      "te-se-up", relay: "", seed: true,
			outcome: "updated", event: "SessionEnd",
		},
		{
			name:    "session_end_compact_no_change",
			payload: `{"hook_event_name":"SessionEnd","reason":"compact"}`,
			id:      "te-se-nc", relay: "", seed: false,
			outcome: "no_change", event: "SessionEnd",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			before := len(readTrailLines(t, trailFile()))

			st, _ := storefix.OpenTempStore(t)
			if c.seed {
				storefix.SeedSpawn(t, st, c.id)
			}

			var stdout bytes.Buffer
			cfg := config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0}
			var clock hook.PollClock
			if c.relay == hook.RelayModeOn {
				now, restore := setupVirtualClock(t)
				defer restore()
				clock = &advancingClock{now: now}
			}

			if err := hook.Handle(
				context.Background(),
				strings.NewReader(c.payload),
				&stdout,
				st,
				hook.HandleConfig{Env: envHook(c.id, c.relay), Cfg: cfg, Clock: clock},
				nil,
			); err != nil {
				t.Fatalf("Handle: %v", err)
			}

			row := hookFiredAt(t, before)

			assertStr(t, row, "event", "ad.hook.fired")
			assertStr(t, row, "source", "ad_hook")
			assertStr(t, row, "claude_instance_id", c.id)
			assertStr(t, row, "event_name", c.event)
			assertStr(t, row, "relay_mode", c.relay)
			assertStr(t, row, "upsert_outcome", c.outcome)
			assertStr(t, row, "session_id", c.sessID)
			assertMatcher(t, row, []any{"*"})

			if ts, ok := row["ts"].(string); !ok || ts == "" {
				t.Error("ts missing or empty")
			}
			if c.tool == "" {
				assertNull(t, row, "tool_name")
			} else {
				assertStr(t, row, "tool_name", c.tool)
			}
			if c.token {
				tok, ok := row["request_token"].(string)
				if !ok || tok == "" {
					t.Errorf("request_token want non-empty string; got %v", row["request_token"])
				}
			} else {
				assertNull(t, row, "request_token")
			}
			assertNoToolInput(t, row)
		})
	}
}

// TestSpawnStateTransitionSequence drives a full worker lifecycle through
// five consecutive Handle calls on a single shared store and asserts that the
// ad.spawn.state_transition lines emitted to the trail are:
//
//  1. Present for every step that transitions state (>= 6: seed + 5 handles).
//  2. Time-ordered (ts values non-decreasing in insertion order).
//  3. Internally consistent: each line's new_state equals the next line's
//     prior_state (allowing for no-op lines where prior == new).
//
// A unique claude_instance_id is used so the test can filter the shared trail
// file by ID and isolate its own lines from the rest of the test binary.
//
// Lifecycle: SessionStart → PreToolUse → PermissionRequest (no-relay) →
// PostToolUse → SessionEnd(user_quit).
// Expected state chain (including seed): pending→working→waiting→working→
// check_permission→working→ended.
func TestSpawnStateTransitionSequence(t *testing.T) {
	const testID = "seq-sst-b4uk-unique"

	// Snapshot the trail before we start so we can slice out our own lines.
	before := len(readTrailLines(t, trailFile()))

	// One store shared across all five Handle calls so state persists.
	st, _ := storefix.OpenTempStore(t)

	// Seed pending → working (also emits one ad.spawn.state_transition).
	storefix.SeedSpawn(t, st, testID)

	// callHandle drives one Handle invocation without relay; any error is fatal.
	cfg := config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0}
	callHandle := func(payload string) {
		t.Helper()
		if err := hook.Handle(
			context.Background(),
			strings.NewReader(payload),
			io.Discard,
			st,
			hook.HandleConfig{Env: envHook(testID, ""), Cfg: cfg},
			nil,
		); err != nil {
			t.Fatalf("Handle(%q): %v", payload, err)
		}
	}

	// 1. SessionStart: working → waiting
	callHandle(`{"hook_event_name":"SessionStart","transcript_path":"/x/seqtest-sess.jsonl"}`)
	// 2. PreToolUse(Bash): waiting → working
	callHandle(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	// 3. PermissionRequest (no-relay): working → check_permission
	callHandle(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`)
	// 4. PostToolUse: check_permission → working (open permission_requests=0, guard passes)
	callHandle(`{"hook_event_name":"PostToolUse","tool_name":"Bash"}`)
	// 5. SessionEnd(user_quit): working → ended
	callHandle(`{"hook_event_name":"SessionEnd","reason":"user_quit"}`)

	// Collect ad.spawn.state_transition lines for our instance ID only.
	all := readTrailLines(t, trailFile())
	var transitions []map[string]any
	for _, row := range all[before:] {
		if row["event"] == "ad.spawn.state_transition" &&
			row["claude_instance_id"] == testID {
			transitions = append(transitions, row)
		}
	}

	// At least 6 lines: 1 from the seed + 5 from Handle calls.
	if len(transitions) < 6 {
		t.Fatalf("want >= 6 ad.spawn.state_transition lines for %q; got %d",
			testID, len(transitions))
	}

	// Assert time order: ts values must be non-decreasing (ISO-8601 UTC strings
	// sort lexicographically in time order).
	for i := 1; i < len(transitions); i++ {
		tsA, _ := transitions[i-1]["ts"].(string)
		tsB, _ := transitions[i]["ts"].(string)
		if tsA == "" || tsB == "" {
			t.Errorf("transition[%d] or [%d] has missing/non-string ts", i-1, i)
			continue
		}
		if tsB < tsA {
			t.Errorf("trail not time-ordered: transitions[%d].ts=%q > transitions[%d].ts=%q",
				i-1, tsA, i, tsB)
		}
	}

	// Assert chain consistency: new_state[i] == prior_state[i+1].
	// No-op lines (prior == new) satisfy this automatically because the
	// invariant holds across them: ...→X, X→X, X→Y... all pair correctly.
	for i := 1; i < len(transitions); i++ {
		prevNew, _ := transitions[i-1]["new_state"].(string)
		curPrior, _ := transitions[i]["prior_state"].(string)
		if prevNew == "" || curPrior == "" {
			t.Errorf("transition[%d] or [%d] has missing/non-string state field", i-1, i)
			continue
		}
		if prevNew != curPrior {
			t.Errorf("chain break at index %d: transitions[%d].new_state=%q != transitions[%d].prior_state=%q",
				i, i-1, prevNew, i, curPrior)
		}
	}
}

// TestTrailEmitNoToolInput confirms tool_input is stripped even when
// present in the raw PermissionRequest payload.
func TestTrailEmitNoToolInput(t *testing.T) {
	before := len(readTrailLines(t, trailFile()))

	st, _ := storefix.OpenTempStore(t)
	storefix.SeedSpawn(t, st, "te-ti-id")

	now, restore := setupVirtualClock(t)
	defer restore()

	if err := hook.Handle(
		context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"cmd":"echo hi"}}`),
		io.Discard,
		st,
		hook.HandleConfig{
			Env:   envHook("te-ti-id", hook.RelayModeOn),
			Cfg:   config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0},
			Clock: &advancingClock{now: now},
		},
		nil,
	); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	row := hookFiredAt(t, before)
	assertNoToolInput(t, row)
}

// TestTrailEmitRelayAttemptCompleted verifies that a relay-mode
// PermissionRequest Handle call emits exactly one ad.relay_attempt.completed
// line carrying the CASE B degenerate fields (target_endpoint="db_poll",
// outcome="db_relay_active", bytes_sent/received=0, source="relay_hook") and
// that its request_token matches the one recorded in ad.hook.fired.
//
// This test documents the CASE B determination: the relay is DB-poll-based
// (no outbound HTTP shim), so in-process trail.Emit is used rather than
// exec'ing the trail-emit sub-verb (which is for external processes).
func TestTrailEmitRelayAttemptCompleted(t *testing.T) {
	const id = "te-relay-attempt-b4uk"
	before := len(readTrailLines(t, trailFile()))

	st, _ := storefix.OpenTempStore(t)
	storefix.SeedSpawn(t, st, id)

	now, restore := setupVirtualClock(t)
	defer restore()

	var stdout bytes.Buffer
	if err := hook.Handle(
		context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Write"}`),
		&stdout,
		st,
		hook.HandleConfig{
			Env:   envHook(id, hook.RelayModeOn),
			Cfg:   config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0},
			Clock: &advancingClock{now: now},
		},
		nil,
	); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	all := readTrailLines(t, trailFile())
	var relayLines []map[string]any
	for _, row := range all[before:] {
		if row["event"] == "ad.relay_attempt.completed" {
			relayLines = append(relayLines, row)
		}
	}
	if len(relayLines) != 1 {
		t.Fatalf("want 1 ad.relay_attempt.completed after offset %d; got %d",
			before, len(relayLines))
	}
	r := relayLines[0]

	assertStr(t, r, "event", "ad.relay_attempt.completed")
	assertStr(t, r, "source", "relay_hook")
	assertStr(t, r, "claude_instance_id", id)
	assertStr(t, r, "target_endpoint", "db_poll")
	assertStr(t, r, "outcome", "db_relay_active")

	// bytes_sent / bytes_received arrive as JSON numbers; json.Unmarshal into
	// map[string]any decodes them as float64.
	for _, field := range []string{"bytes_sent", "bytes_received"} {
		v, ok := r[field]
		if !ok {
			t.Errorf("field %q missing", field)
			continue
		}
		n, ok := v.(float64)
		if !ok || n != 0 {
			t.Errorf("[%q] = %v (%T); want float64(0)", field, v, v)
		}
	}

	// request_token in ad.relay_attempt.completed must match ad.hook.fired.
	hookRow := hookFiredAt(t, before)
	hookTok, _ := hookRow["request_token"].(string)
	relayTok, _ := r["request_token"].(string)
	if hookTok == "" {
		t.Error("ad.hook.fired: request_token missing or empty")
	}
	if relayTok == "" {
		t.Error("ad.relay_attempt.completed: request_token missing or empty")
	}
	if hookTok != relayTok {
		t.Errorf("request_token mismatch: hook.fired=%q relay.completed=%q", hookTok, relayTok)
	}

	if ts, ok := r["ts"].(string); !ok || ts == "" {
		t.Error("ts missing or empty")
	}
}

// TestRelayAttemptCompletedNotEmittedWhenRelayModeOff verifies the CASE B
// relay-mode gate: when AGENT_DIRECTOR_RELAY_MODE is unset ("") or
// explicitly "off", runRelay is never entered and zero
// ad.relay_attempt.completed lines are emitted even though the event is
// PermissionRequest. No stdout envelope is written either (relay must be
// active for the fail-closed path to apply).
func TestRelayAttemptCompletedNotEmittedWhenRelayModeOff(t *testing.T) {
	for _, tc := range []struct {
		name      string
		relayMode string
	}{
		{name: "unset", relayMode: ""},
		{name: "off", relayMode: hook.RelayModeOff},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			before := len(readTrailLines(t, trailFile()))

			st := &flakyRelayStore{}
			var stdout bytes.Buffer
			if err := hook.Handle(
				context.Background(),
				strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
				&stdout,
				st,
				hook.HandleConfig{
					Env: envHook("te-relay-off-"+tc.name, tc.relayMode),
					Cfg: config.Relay{TimeoutSeconds: 1},
				},
				nil,
			); err != nil {
				t.Fatalf("Handle: %v", err)
			}

			all := readTrailLines(t, trailFile())
			for _, row := range all[before:] {
				if row["event"] == "ad.relay_attempt.completed" {
					t.Errorf("relay_mode=%q: unexpected ad.relay_attempt.completed line emitted", tc.relayMode)
				}
			}
			if stdout.Len() != 0 {
				t.Errorf("relay_mode=%q: stdout non-empty (no relay → no envelope): %s",
					tc.relayMode, stdout.String())
			}
		})
	}
}

// TestRelayAttemptCompletedNotEmittedOnUpsertFailure verifies that when
// UpsertOpenPermissionRequest returns an error, the deny envelope is still
// written to stdout (fail-closed per SRD §6.4) but zero
// ad.relay_attempt.completed lines appear in the trail.  The trail-emit
// fires only on the post-upsert success path in runRelay; any early return
// due to upsert failure must not emit it.
func TestRelayAttemptCompletedNotEmittedOnUpsertFailure(t *testing.T) {
	before := len(readTrailLines(t, trailFile()))

	st := &flakyRelayStore{upsertErr: errors.New("db unavailable")}
	var stdout bytes.Buffer
	if err := hook.Handle(
		context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		&stdout,
		st,
		hook.HandleConfig{
			Env: envWith("te-upsert-fail-trail"),
			Cfg: config.Relay{TimeoutSeconds: 1},
		},
		nil,
	); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	assertDenyEnvelope(t, &stdout)

	all := readTrailLines(t, trailFile())
	for _, row := range all[before:] {
		if row["event"] == "ad.relay_attempt.completed" {
			t.Error("ad.relay_attempt.completed must not be emitted when upsert fails")
		}
	}
}

// resumeObservedAfter returns all ad.resume.observed lines written after
// prevCount lines existed in the trail file.
func resumeObservedAfter(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	all := readTrailLines(t, trailFile())
	var out []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.resume.observed" {
			out = append(out, row)
		}
	}
	return out
}

// TestTrailEmitResumeObserved is a table-driven test covering the four
// verdict values (allow / deny / timeout / error) for ad.resume.observed
// per SR-A-2.7. Each sub-test verifies: exactly one line, correct verdict,
// required fields (claude_instance_id, request_token, source, ts,
// elapsed_ms_from_row_open as a numeric JSON value), and that
// request_token matches ad.hook.fired.
func TestTrailEmitResumeObserved(t *testing.T) {
	// Pre-build the read-retry error sequence (function literals cannot
	// appear inside composite literals in Go).
	readRetryRows := make([]store.PermissionRow, 10)
	readRetryErrs := make([]error, 10)
	for i := range readRetryErrs {
		readRetryErrs[i] = errors.New("db error")
	}

	type tc struct {
		name        string
		getRows     []store.PermissionRow
		getErrs     []error
		useClock    bool // advance virtual clock to force timeout
		wantVerdict string
	}
	cases := []tc{
		{
			name:        "allow",
			getRows:     []store.PermissionRow{{Decision: "allow", DecisionReason: "ok"}},
			getErrs:     []error{nil},
			wantVerdict: "allow",
		},
		{
			name:        "deny",
			getRows:     []store.PermissionRow{{Decision: "deny", DecisionReason: "nope"}},
			getErrs:     []error{nil},
			wantVerdict: "deny",
		},
		{
			name:        "timeout",
			getRows:     []store.PermissionRow{{Decision: ""}},
			getErrs:     []error{nil},
			useClock:    true,
			wantVerdict: "timeout",
		},
		{
			name:        "error_read_retry",
			getRows:     readRetryRows,
			getErrs:     readRetryErrs,
			wantVerdict: "error",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			before := len(readTrailLines(t, trailFile()))
			id := "te-resume-obs-" + c.name

			st := &flakyRelayStore{getRows: c.getRows, getErrs: c.getErrs}

			var clock hook.PollClock
			if c.useClock {
				now, restore := setupVirtualClock(t)
				defer restore()
				clock = &advancingClock{now: now}
			}

			var stdout bytes.Buffer
			if err := hook.Handle(
				context.Background(),
				strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
				&stdout,
				st,
				hook.HandleConfig{
					Env:   envWith(id),
					Cfg:   config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0},
					Clock: clock,
				},
				nil,
			); err != nil {
				t.Fatalf("Handle: %v", err)
			}

			lines := resumeObservedAfter(t, before)
			if len(lines) != 1 {
				t.Fatalf("want 1 ad.resume.observed; got %d", len(lines))
			}
			r := lines[0]

			assertStr(t, r, "event", "ad.resume.observed")
			assertStr(t, r, "source", "ad_polling")
			assertStr(t, r, "claude_instance_id", id)
			assertStr(t, r, "verdict", c.wantVerdict)

			if ts, ok := r["ts"].(string); !ok || ts == "" {
				t.Error("ts missing or empty")
			}

			// elapsed_ms_from_row_open must be present as a JSON number.
			v, ok := r["elapsed_ms_from_row_open"]
			if !ok {
				t.Error("field elapsed_ms_from_row_open missing")
			} else if _, ok := v.(float64); !ok {
				t.Errorf("elapsed_ms_from_row_open type = %T; want float64 (JSON number)", v)
			}

			// request_token must match ad.hook.fired for the same invocation.
			hookRow := hookFiredAt(t, before)
			hookTok, _ := hookRow["request_token"].(string)
			resumeTok, _ := r["request_token"].(string)
			if hookTok == "" {
				t.Error("ad.hook.fired: request_token missing or empty")
			}
			if resumeTok == "" {
				t.Error("ad.resume.observed: request_token missing or empty")
			}
			if hookTok != resumeTok {
				t.Errorf("request_token mismatch: hook.fired=%q resume.observed=%q",
					hookTok, resumeTok)
			}
		})
	}
}

// TestTrailEmitResumeObservedElapsedMsRealStore verifies elapsed_ms_from_row_open
// is non-negative on the allow path using a real store (where created_at is an
// actual timestamp, not the zero time). The elapsed value should be small
// (sub-second) for a local SQLite write.
func TestTrailEmitResumeObservedElapsedMsRealStore(t *testing.T) {
	const id = "te-resume-elapsed-real"
	before := len(readTrailLines(t, trailFile()))

	st, _ := storefix.OpenTempStore(t)
	storefix.SeedSpawn(t, st, id)

	// Pre-decide the row so Poll returns immediately on the first read.
	// We INSERT the open row via UpsertOpenPermissionRequest, then decide it.
	const fakeToken = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	if err := st.UpsertOpenPermissionRequest(id, fakeToken, "Bash", "null", 0, "hook"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := st.DecidePermissionRequest(id, fakeToken, "allow", "ok", "decide"); err != nil {
		t.Fatalf("decide: %v", err)
	}

	// Use a flakyRelayStore that delegates GetPermissionRequest to the
	// real store so created_at is populated. We inject the decided row
	// directly into getRows so Poll returns immediately.
	row, err := st.GetPermissionRequest(id, fakeToken)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	fakeSt := &flakyRelayStore{
		getRows: []store.PermissionRow{row},
		getErrs: []error{nil},
	}

	var stdout bytes.Buffer
	if err := hook.Handle(
		context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		&stdout,
		fakeSt,
		hook.HandleConfig{
			Env: envWith(id),
			Cfg: config.Relay{TimeoutSeconds: 5, PollBaseMs: 0, PollJitterMs: 0},
		},
		nil,
	); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	lines := resumeObservedAfter(t, before)
	if len(lines) != 1 {
		t.Fatalf("want 1 ad.resume.observed; got %d", len(lines))
	}
	r := lines[0]
	assertStr(t, r, "verdict", "allow")

	// elapsed_ms must be non-negative and less than 60 000 ms (1 minute).
	// The row was just inserted so actual elapsed is single-digit milliseconds.
	v, ok := r["elapsed_ms_from_row_open"].(float64)
	if !ok {
		t.Fatalf("elapsed_ms_from_row_open type = %T; want float64", r["elapsed_ms_from_row_open"])
	}
	if v < 0 {
		t.Errorf("elapsed_ms_from_row_open = %v; want >= 0", v)
	}
	if v >= 60_000 {
		t.Errorf("elapsed_ms_from_row_open = %v; unexpectedly large (want < 60000)", v)
	}
}

// TestTrailEmitResumeObservedNotEmittedForNonRelay verifies that
// ad.resume.observed is never emitted for state-tracking events
// (where runRelay is never entered). The relay path is scoped to
// PermissionRequest + RELAY_MODE=on.
func TestTrailEmitResumeObservedNotEmittedForNonRelay(t *testing.T) {
	payloads := []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
		`{"hook_event_name":"PostToolUse","tool_name":"Bash"}`,
		`{"hook_event_name":"SessionEnd","reason":"user_quit"}`,
	}
	for _, payload := range payloads {
		payload := payload
		t.Run(payload, func(t *testing.T) {
			before := len(readTrailLines(t, trailFile()))
			st := &flakyRelayStore{}
			if err := hook.Handle(
				context.Background(),
				strings.NewReader(payload),
				io.Discard,
				st,
				hook.HandleConfig{
					Env: envWith("te-norelay-resume"),
					Cfg: config.Relay{TimeoutSeconds: 1},
				},
				nil,
			); err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if lines := resumeObservedAfter(t, before); len(lines) != 0 {
				t.Errorf("non-relay event %q: unexpected %d ad.resume.observed line(s)",
					payload, len(lines))
			}
		})
	}
}

// TestTrailEmitResumeObservedNotEmittedOnUpsertFailure verifies that
// ad.resume.observed is NOT emitted when the upsert fails (runRelay
// returns before Poll is called, so no envelope and no resume event).
func TestTrailEmitResumeObservedNotEmittedOnUpsertFailure(t *testing.T) {
	before := len(readTrailLines(t, trailFile()))
	st := &flakyRelayStore{upsertErr: errors.New("db unavailable")}
	var stdout bytes.Buffer
	if err := hook.Handle(
		context.Background(),
		strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
		&stdout,
		st,
		hook.HandleConfig{
			Env: envWith("te-upsert-fail-resume"),
			Cfg: config.Relay{TimeoutSeconds: 1},
		},
		nil,
	); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertDenyEnvelope(t, &stdout)
	if lines := resumeObservedAfter(t, before); len(lines) != 0 {
		t.Errorf("upsert failure: unexpected %d ad.resume.observed line(s)", len(lines))
	}
}
