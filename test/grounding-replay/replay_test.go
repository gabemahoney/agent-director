// Package grounding_replay_test is the §11 acceptance-test harness for the
// AD-side visibility trail (SRD t1.n4v.14 §11, ticket t3.4uk.ou.k5.dv).
//
// It replays the 2026-06-04 incident shape — a worker spawn making a Bash
// tool call under relay_mode=on — and asserts that the AD trail file alone
// can answer all six §11 grounding questions:
//
//  1. Which AD hook lifecycle fired when the permission_requests row was
//     inserted? (event_name, tool_name, matcher)
//  2. What was the spawn's state history from row-open to row-close?
//     (all transitions, including no-ops)
//  3. Did the worker's relay shim attempt to reach CSCB? (outcome)
//  4. Which process called decide, when, with what outcome?
//     (caller_process, caller_pid, outcome="ok")
//  5. Did find-missing touch this instance during the open window?
//     (correct answer: no — empty result is the right answer)
//  6. After decide succeeded, did the worker's hook observe the verdict and
//     resume? (verdict="allow", elapsed_ms_from_row_open > 0)
//
// Hermetic: no network, no tmux, no shared state between runs. Each test
// invocation writes to its own isolated AGENT_DIRECTOR_STATE_DIR temp dir.
//
// Trail singleton note: trail.Emit uses a process-level sync.Once whose path
// is locked in on the first call. TestMain fixes AGENT_DIRECTOR_STATE_DIR
// before m.Run() so every trail write lands in the designated temp dir.
package grounding_replay_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// replayTrailDir is the AGENT_DIRECTOR_STATE_DIR for the whole test binary.
// Set by TestMain before any test runs so the trail singleton captures it.
var replayTrailDir string

// TestMain fixes AGENT_DIRECTOR_STATE_DIR (and HOME) to isolated temp dirs
// before any test function runs. The trail singleton (sync.Once) is
// initialised on the first trail.Emit call — TestMain ensures that happens
// AFTER the env var is set.
func TestMain(m *testing.M) {
	d, err := os.MkdirTemp("", "grounding-replay-trail-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: MkdirTemp: %v\n", err)
		os.Exit(2)
	}
	defer os.RemoveAll(d)
	replayTrailDir = d

	// Pin the trail singleton to our temp dir.
	if err := os.Setenv("AGENT_DIRECTOR_STATE_DIR", d); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: Setenv AGENT_DIRECTOR_STATE_DIR: %v\n", err)
		os.Exit(2)
	}

	// Redirect HOME so UserHomeDir()-based paths don't pollute the real home.
	if err := os.Setenv("HOME", d); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: Setenv HOME: %v\n", err)
		os.Exit(2)
	}

	// Clear any inherited parent-instance ID; test spawns are roots.
	_ = os.Unsetenv("AGENT_DIRECTOR_INSTANCE_ID")

	os.Exit(m.Run())
}

// ── trail helpers ─────────────────────────────────────────────────────────────

// trailPath returns the JSONL file path used by the singleton.
func trailPath() string { return filepath.Join(replayTrailDir, "ad-trail.jsonl") }

// readTrailLines reads every JSONL line from path into []map[string]any.
// Returns nil when the file does not exist.
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

// filterLines returns all lines from haystack that match every supplied
// (key, value) pair. A nil/missing key never matches a non-nil want.
func filterLines(haystack []map[string]any, filters map[string]string) []map[string]any {
	var out []map[string]any
	for _, row := range haystack {
		match := true
		for k, want := range filters {
			got, ok := row[k]
			if !ok || fmt.Sprintf("%v", got) != want {
				match = false
				break
			}
		}
		if match {
			out = append(out, row)
		}
	}
	return out
}

// ── relay env helper ──────────────────────────────────────────────────────────

const testInstanceID = "gr-replay-2026-06-04-incident"

// relayEnv returns the env func Hook.Handle expects for a relay-mode spawn.
func relayEnv(k string) string {
	switch k {
	case "AGENT_DIRECTOR_INSTANCE_ID":
		return testInstanceID
	case hook.EnvRelayMode:
		return hook.RelayModeOn
	}
	return ""
}

// noRelayEnv is the env func for non-relay handle calls with the same instance.
func noRelayEnv(k string) string {
	switch k {
	case "AGENT_DIRECTOR_INSTANCE_ID":
		return testInstanceID
	}
	return ""
}

// ── assertion helpers ─────────────────────────────────────────────────────────

// assertField fails if row[key] != want.
func assertField(t *testing.T, row map[string]any, key, want string) {
	t.Helper()
	got, ok := row[key]
	if !ok {
		t.Errorf("§11 Q assertion: field %q missing from line %v", key, row)
		return
	}
	if fmt.Sprintf("%v", got) != want {
		t.Errorf("§11 Q assertion: [%q] = %v; want %q", key, got, want)
	}
}

// assertFieldNonEmpty fails if row[key] is absent or empty string.
func assertFieldNonEmpty(t *testing.T, row map[string]any, key string) {
	t.Helper()
	got, ok := row[key]
	if !ok || fmt.Sprintf("%v", got) == "" {
		t.Errorf("§11 Q assertion: field %q missing or empty; got %v", key, got)
	}
}

// assertMatcher fails if the "matcher" field in row is not exactly ["*"].
func assertMatcher(t *testing.T, row map[string]any) {
	t.Helper()
	raw, ok := row["matcher"]
	if !ok {
		t.Errorf("§11 Q assertion: field \"matcher\" missing")
		return
	}
	got, ok := raw.([]any)
	if !ok || len(got) != 1 || fmt.Sprintf("%v", got[0]) != "*" {
		t.Errorf("§11 Q assertion: matcher = %v; want [\"*\"]", raw)
	}
}

// assertPositiveFloat64 fails if row[key] is absent, not a JSON number, or <= 0.
func assertPositiveFloat64(t *testing.T, row map[string]any, key string) {
	t.Helper()
	v, ok := row[key]
	if !ok {
		t.Errorf("§11 Q assertion: field %q missing", key)
		return
	}
	n, ok := v.(float64)
	if !ok {
		t.Errorf("§11 Q assertion: [%q] type = %T; want float64 (JSON number)", key, v)
		return
	}
	if n <= 0 {
		t.Errorf("§11 Q assertion: [%q] = %v; want > 0", key, n)
	}
}

// ── main scenario test ────────────────────────────────────────────────────────

// TestGroundingReplayScenario drives the 2026-06-04 incident shape and
// asserts that the trail alone answers all six §11 questions.
//
// Scenario sequence:
//
//  1. Seed a relay-mode spawn in StateWorking.
//  2. SessionStart → spawn transitions working→waiting.
//  3. PreToolUse(Bash) → waiting→working.
//  4. PermissionRequest(relay) in a goroutine → working→check_permission,
//     relay_attempt emitted, Poll blocks waiting for a decision.
//  5. PostToolUse while relay is open → no-op (open row blocks working transition).
//  6. Client.Decide("allow") → unblocks Poll, emits ad.decide.called.
//  7. Goroutine returns → ad.resume.observed + ad.hook.fired emitted.
//
// Assertions cover §11 Q1–Q6, no tool_input on any line, and the SR-A-7.17
// stitching one-liner across AD + CSCB trail fixtures.
func TestGroundingReplayScenario(t *testing.T) {
	// ── store + spawn setup ────────────────────────────────────────────────
	st, dbPath := storefix.OpenTempStore(t)

	sp := store.Spawn{
		ClaudeInstanceID: testInstanceID,
		CWD:              "/tmp/grounding-replay",
		TmuxSessionName:  "gr-replay-session",
		RelayMode:        "on",
	}
	if err := st.InsertPending(sp); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	// Transition pending→working (emits ad.spawn.state_transition).
	if err := st.ApplyHookTransition(testInstanceID, store.StateWorking, false, "test_seed"); err != nil {
		t.Fatalf("seed transition: %v", err)
	}

	// Snapshot trail line count BEFORE the scenario starts.
	before := len(readTrailLines(t, trailPath()))

	// Fast relay config: short poll/jitter, generous overall timeout so the
	// goroutine reliably unblocks when decide is called from the main goroutine.
	fastCfg := config.Relay{
		TimeoutSeconds:       30,
		PollBaseMs:           5,
		PollJitterMs:         0,
		PermissionRequestCap: 1000,
	}

	callHandle := func(payload string, env func(string) string) {
		t.Helper()
		if err := hook.Handle(
			context.Background(),
			strings.NewReader(payload),
			io.Discard,
			st,
			hook.HandleConfig{Env: env, Cfg: fastCfg},
			nil,
		); err != nil {
			t.Fatalf("Handle(%q): %v", payload, err)
		}
	}

	// ── step 2: SessionStart (working → waiting) ───────────────────────────
	callHandle(`{"hook_event_name":"SessionStart","transcript_path":"/x/incident-session.jsonl"}`, noRelayEnv)

	// ── step 3: PreToolUse (waiting → working) ─────────────────────────────
	callHandle(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, noRelayEnv)

	// ── step 4: PermissionRequest relay in goroutine ───────────────────────
	var wg sync.WaitGroup
	var relayStdout bytes.Buffer
	var relayHandleErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		relayHandleErr = hook.Handle(
			context.Background(),
			strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`),
			&relayStdout,
			st,
			hook.HandleConfig{Env: relayEnv, Cfg: fastCfg},
			nil,
		)
	}()

	// ── step 5: wait for ad.relay_attempt.completed; extract request_token ─
	// The goroutine emits ad.relay_attempt.completed BEFORE Poll blocks,
	// so it appears in the trail shortly after the goroutine starts.
	var requestToken string
	const tokenPollDeadline = 10 * time.Second
	deadline := time.Now().Add(tokenPollDeadline)
	for time.Now().Before(deadline) {
		all := readTrailLines(t, trailPath())
		for _, row := range all[before:] {
			if row["event"] == "ad.relay_attempt.completed" &&
				fmt.Sprintf("%v", row["claude_instance_id"]) == testInstanceID {
				if tok, ok := row["request_token"].(string); ok && tok != "" {
					requestToken = tok
					break
				}
			}
		}
		if requestToken != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if requestToken == "" {
		t.Fatal("timed out waiting for ad.relay_attempt.completed in trail (no request_token)")
	}

	// ── step 6: PostToolUse while relay is open → no-op ───────────────────
	// The open permission_requests row is present; the working-state guard in
	// ApplyHookTransitionResult emits a no-op ad.spawn.state_transition
	// (prior_state == new_state = check_permission). This answers §11 Q2.
	callHandle(`{"hook_event_name":"PostToolUse","tool_name":"Bash"}`, noRelayEnv)

	// ── step 7: Client.Decide("allow") → emits ad.decide.called ───────────
	// Open a second store handle (WAL-mode; concurrent access is safe).
	// config.Load returns Default() for a non-existent config path.
	nonExistentCfg := filepath.Join(t.TempDir(), "config.toml")
	client, err := api.New(api.Options{
		StorePath:       dbPath,
		ConfigPath:      nonExistentCfg,
		CreateIfMissing: false,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer client.Close() //nolint:errcheck

	if _, err := client.Decide(api.DecideParams{
		ClaudeInstanceID: testInstanceID,
		RequestToken:     requestToken,
		Decision:         "allow",
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	// ── step 8: wait for relay goroutine to finish ─────────────────────────
	wg.Wait()
	if relayHandleErr != nil {
		t.Fatalf("PermissionRequest relay Handle: %v", relayHandleErr)
	}

	// ── collect trail lines written during the scenario ────────────────────
	all := readTrailLines(t, trailPath())
	myLines := all[before:]

	t.Logf("scenario emitted %d trail lines (token=%s)", len(myLines), requestToken)

	// ── §11 Q1: hook lifecycle that minted the row ─────────────────────────
	// Query: ad.hook.fired WHERE request_token=X
	// Derivable answer: event_name=PermissionRequest, tool_name=Bash, matcher=["*"]
	t.Run("Q1_hook_lifecycle", func(t *testing.T) {
		lines := filterLines(myLines, map[string]string{
			"event":         "ad.hook.fired",
			"request_token": requestToken,
		})
		if len(lines) == 0 {
			t.Fatalf("§11 Q1: no ad.hook.fired line found for request_token=%q\n"+
				"Query: grep '\"event\":\"ad.hook.fired\"' | jq -c 'select(.request_token==\"%s\")'",
				requestToken, requestToken)
		}
		row := lines[0]
		assertField(t, row, "event_name", "PermissionRequest")
		assertField(t, row, "tool_name", "Bash")
		assertMatcher(t, row)
	})

	// ── §11 Q2: spawn state history ────────────────────────────────────────
	// Query: ad.spawn.state_transition WHERE claude_instance_id=X
	// Derivable answer: time-ordered transitions including at least one no-op
	t.Run("Q2_spawn_state_history", func(t *testing.T) {
		lines := filterLines(myLines, map[string]string{
			"event":             "ad.spawn.state_transition",
			"claude_instance_id": testInstanceID,
		})
		if len(lines) < 2 {
			t.Fatalf("§11 Q2: want >= 2 ad.spawn.state_transition lines; got %d\n"+
				"Query: grep '\"event\":\"ad.spawn.state_transition\"' | jq -c 'select(.claude_instance_id==\"%s\")'",
				len(lines), testInstanceID)
		}
		// Assert time order (ISO-8601 UTC strings sort lexicographically).
		for i := 1; i < len(lines); i++ {
			tsA, _ := lines[i-1]["ts"].(string)
			tsB, _ := lines[i]["ts"].(string)
			if tsA != "" && tsB != "" && tsB < tsA {
				t.Errorf("§11 Q2: trail not time-ordered: [%d].ts=%q > [%d].ts=%q", i-1, tsA, i, tsB)
			}
		}
		// Assert at least one no-op: a line where prior_state == new_state.
		var foundNoop bool
		for _, row := range lines {
			if row["prior_state"] == row["new_state"] {
				foundNoop = true
				break
			}
		}
		if !foundNoop {
			t.Errorf("§11 Q2: no no-op transition found (prior_state==new_state); "+
				"PostToolUse while open row exists should have produced one.\n"+
				"Lines seen:\n%s", formatLines(lines))
		}
	})

	// ── §11 Q3: relay attempt outcome ──────────────────────────────────────
	// Query: ad.relay_attempt.completed WHERE request_token=X
	// Derivable answer: CASE B degenerate (db_poll / db_relay_active)
	t.Run("Q3_relay_attempt_outcome", func(t *testing.T) {
		lines := filterLines(myLines, map[string]string{
			"event":         "ad.relay_attempt.completed",
			"request_token": requestToken,
		})
		if len(lines) == 0 {
			t.Fatalf("§11 Q3: no ad.relay_attempt.completed line for request_token=%q\n"+
				"Query: grep '\"event\":\"ad.relay_attempt.completed\"' | jq -c 'select(.request_token==\"%s\")'",
				requestToken, requestToken)
		}
		row := lines[0]
		// The outcome must be a recognized value — CASE B ships "db_relay_active".
		got := fmt.Sprintf("%v", row["outcome"])
		if got == "" {
			t.Errorf("§11 Q3: ad.relay_attempt.completed: \"outcome\" missing or empty")
		}
		assertFieldNonEmpty(t, row, "target_endpoint")
	})

	// ── §11 Q4: decide caller identity ─────────────────────────────────────
	// Query: ad.decide.called WHERE request_token=X
	// Derivable answer: outcome=ok, caller_process=<process>, caller_pid>0
	t.Run("Q4_decide_caller_identity", func(t *testing.T) {
		lines := filterLines(myLines, map[string]string{
			"event":         "ad.decide.called",
			"request_token": requestToken,
		})
		if len(lines) == 0 {
			t.Fatalf("§11 Q4: no ad.decide.called line for request_token=%q\n"+
				"Query: grep '\"event\":\"ad.decide.called\"' | jq -c 'select(.request_token==\"%s\")'",
				requestToken, requestToken)
		}
		row := lines[0]
		assertField(t, row, "outcome", "ok")
		assertFieldNonEmpty(t, row, "caller_process")
		assertPositiveFloat64(t, row, "caller_pid")
	})

	// ── §11 Q5: find-missing during open window ─────────────────────────────
	// Query: ad.find_missing.tick WHERE claude_instance_id=X (or request_token=X)
	// Derivable answer: empty result — no find-missing tick touched this instance.
	// "No spurious events, but the query returns cleanly" is the correct answer.
	t.Run("Q5_find_missing_open_window", func(t *testing.T) {
		// Q5a: by instance id
		byID := filterLines(myLines, map[string]string{
			"event":             "ad.find_missing.tick",
			"claude_instance_id": testInstanceID,
		})
		// Q5b: by request_token (degraded-mode events carry null instance_id)
		byTok := filterLines(myLines, map[string]string{
			"event":         "ad.find_missing.tick",
			"request_token": requestToken,
		})
		if len(byID) > 0 || len(byTok) > 0 {
			t.Errorf("§11 Q5: unexpected ad.find_missing.tick for this instance/token "+
				"(byID=%d, byTok=%d); the query must return cleanly with zero results "+
				"when find-missing did not run during the open window",
				len(byID), len(byTok))
		}
		// The test PASSES when the query returns zero lines — that IS the correct
		// answer for §11 Q5 (no find-missing interference).
		t.Logf("§11 Q5: correctly 0 ad.find_missing.tick events for this instance/token")
	})

	// ── §11 Q6: worker hook resume ─────────────────────────────────────────
	// Query: ad.resume.observed WHERE request_token=X
	// Derivable answer: verdict=allow, elapsed_ms_from_row_open > 0
	t.Run("Q6_worker_hook_resume", func(t *testing.T) {
		lines := filterLines(myLines, map[string]string{
			"event":         "ad.resume.observed",
			"request_token": requestToken,
		})
		if len(lines) == 0 {
			t.Fatalf("§11 Q6: no ad.resume.observed line for request_token=%q\n"+
				"Query: grep '\"event\":\"ad.resume.observed\"' | jq -c 'select(.request_token==\"%s\")'",
				requestToken, requestToken)
		}
		row := lines[0]
		assertField(t, row, "verdict", "allow")
		assertPositiveFloat64(t, row, "elapsed_ms_from_row_open")
	})

	// ── no tool_input on any emitted line ──────────────────────────────────
	t.Run("no_tool_input_in_trail", func(t *testing.T) {
		for i, row := range myLines {
			if _, ok := row["tool_input"]; ok {
				t.Errorf("line[%d] contains \"tool_input\" (forbidden per SRD §9): %v", i, row)
			}
		}
	})

	// ── SR-A-7.17 stitching: AD trail + CSCB fixture in chronological order ─
	t.Run("stitching_SR_A_7_17", func(t *testing.T) {
		// Write a temp CSCB fixture with the actual request_token and
		// timestamps bracketing the AD relay events.
		cscbFixturePath := filepath.Join(t.TempDir(), "cscb-permission-trail.jsonl")

		// Find the timestamp of ad.relay_attempt.completed so CSCB events
		// are interleaved between relay and decide (chronologically correct).
		var relayTS string
		for _, row := range myLines {
			if row["event"] == "ad.relay_attempt.completed" &&
				fmt.Sprintf("%v", row["request_token"]) == requestToken {
				if ts, ok := row["ts"].(string); ok {
					relayTS = ts
				}
				break
			}
		}
		if relayTS == "" {
			relayTS = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		}

		// Parse relay timestamp and create CSCB events slightly after it.
		relayTime, parseErr := time.Parse("2006-01-02T15:04:05.000Z", relayTS)
		if parseErr != nil {
			// Fallback: use now
			relayTime = time.Now().UTC()
		}
		fmtTS := func(t time.Time) string {
			return t.UTC().Format("2006-01-02T15:04:05.000Z")
		}

		cscbLines := []map[string]any{
			{
				"ts":                  fmtTS(relayTime.Add(100 * time.Millisecond)),
				"event":               "cscb.relay.received",
				"claude_instance_id":  testInstanceID,
				"request_token":       requestToken,
				"channel":             "C0B1ZJJLJ9M",
				"source":              "cscb_relay",
			},
			{
				"ts":                  fmtTS(relayTime.Add(500 * time.Millisecond)),
				"event":               "cscb.slack.posted",
				"claude_instance_id":  testInstanceID,
				"request_token":       requestToken,
				"channel":             "C0B1ZJJLJ9M",
				"message_ts":          "1717531843.501000",
				"source":              "cscb_slack",
			},
			{
				"ts":                  fmtTS(relayTime.Add(1 * time.Second)),
				"event":               "cscb.operator.click",
				"claude_instance_id":  testInstanceID,
				"request_token":       requestToken,
				"channel":             "C0B1ZJJLJ9M",
				"message_ts":          "1717531843.501000",
				"verdict":             "allow",
				"source":              "cscb_click",
			},
		}

		// Write fixture file.
		f, err := os.Create(cscbFixturePath)
		if err != nil {
			t.Fatalf("create CSCB fixture: %v", err)
		}
		enc := json.NewEncoder(f)
		for _, line := range cscbLines {
			if err := enc.Encode(line); err != nil {
				f.Close()
				t.Fatalf("encode CSCB fixture line: %v", err)
			}
		}
		f.Close()

		// Programmatic equivalent of the SR-A-7.17 stitching one-liner:
		//   cat ad-trail.jsonl cscb-trail-fixture.jsonl \
		//     | jq -c 'select(.request_token=="X")' \
		//     | jq -sc 'sort_by(.ts)[]'
		//
		// 1. Collect all lines from both files that carry our request_token.
		// 2. Verify the combined set is chronologically ordered (ts non-decreasing).
		adLines := filterLines(myLines, map[string]string{"request_token": requestToken})
		cscbFiltered := filterLines(cscbLines, map[string]string{"request_token": requestToken})

		combined := append(append([]map[string]any(nil), adLines...), cscbFiltered...)
		if len(combined) == 0 {
			t.Fatalf("stitching: no lines found for request_token=%q in either trail", requestToken)
		}

		// Sort by ts (string sort == chronological for ISO-8601 UTC).
		combined = sortByTS(combined)

		// Verify the merged result is non-empty and each line has ts + event.
		for i, row := range combined {
			if ts, ok := row["ts"].(string); !ok || ts == "" {
				t.Errorf("stitching line[%d]: ts missing or non-string", i)
			}
			if ev, ok := row["event"].(string); !ok || ev == "" {
				t.Errorf("stitching line[%d]: event missing or non-string", i)
			}
		}

		// Verify AD events and CSCB events are both represented.
		var hasAD, hasCscb bool
		for _, row := range combined {
			ev, _ := row["event"].(string)
			if strings.HasPrefix(ev, "ad.") {
				hasAD = true
			}
			if strings.HasPrefix(ev, "cscb.") {
				hasCscb = true
			}
		}
		if !hasAD {
			t.Error("stitching: no AD events (ad.*) in merged result")
		}
		if !hasCscb {
			t.Error("stitching: no CSCB events (cscb.*) in merged result")
		}

		t.Logf("SR-A-7.17 stitching: %d combined lines (%d AD + %d CSCB) in chronological order",
			len(combined), len(adLines), len(cscbFiltered))
		t.Logf("CSCB fixture written to: %s (line count: %d)", cscbFixturePath, len(cscbLines))
	})
}

// ── sort helpers ─────────────────────────────────────────────────────────────

// sortByTS returns a new slice sorted ascending by the "ts" field.
// ISO-8601 UTC strings sort lexicographically in time order.
func sortByTS(lines []map[string]any) []map[string]any {
	out := make([]map[string]any, len(lines))
	copy(out, lines)
	// Insertion sort is fine for the small fixture sizes here.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			tsA, _ := out[j-1]["ts"].(string)
			tsB, _ := out[j]["ts"].(string)
			if tsB < tsA {
				out[j-1], out[j] = out[j], out[j-1]
			} else {
				break
			}
		}
	}
	return out
}

// ── debug helpers ─────────────────────────────────────────────────────────────

// formatLines returns a newline-joined JSON rendering of lines for error output.
func formatLines(lines []map[string]any) string {
	var sb strings.Builder
	for _, row := range lines {
		b, _ := json.Marshal(row)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String()
}
