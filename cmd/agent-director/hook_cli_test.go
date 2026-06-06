package main_test

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// tsRe is the SR-A-7.9 timestamp regex validated on every ad.hook.fired line.
var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// readTrailLines opens $stateDir/ad-trail.jsonl and returns each line as a
// parsed map. It fails the test immediately on any I/O or parse error.
func readTrailLines(t *testing.T, stateDir string) []map[string]any {
	t.Helper()
	path := filepath.Join(stateDir, "ad-trail.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("readTrailLines: open %s: %v", path, err)
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
	if err := sc.Err(); err != nil {
		t.Fatalf("readTrailLines: scan: %v", err)
	}
	return rows
}

// assertHookFiredLine finds the single ad.hook.fired line in lines, validates
// the SR-A-2.1 required fields (ts, source, relay_mode, session_id, matcher
// shape, upsert_outcome, no tool_input), and returns it for further assertions.
func assertHookFiredLine(t *testing.T, lines []map[string]any) map[string]any {
	t.Helper()
	var fired []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.hook.fired" {
			fired = append(fired, l)
		}
	}
	if len(fired) != 1 {
		t.Fatalf("ad.hook.fired line count = %d; want exactly 1", len(fired))
	}
	row := fired[0]

	// ts: SR-A-7.9 format.
	if ts, ok := row["ts"].(string); !ok || !tsRe.MatchString(ts) {
		t.Errorf("ts %v does not match SR-A-7.9 regex", row["ts"])
	}
	// source: always "ad_hook".
	if row["source"] != "ad_hook" {
		t.Errorf("source = %v; want ad_hook", row["source"])
	}
	// relay_mode: always a string (may be empty when env unset).
	if _, ok := row["relay_mode"].(string); !ok {
		t.Errorf("relay_mode type %T; want string", row["relay_mode"])
	}
	// session_id: always a string (may be empty for non-SessionStart events).
	if _, ok := row["session_id"].(string); !ok {
		t.Errorf("session_id type %T; want string", row["session_id"])
	}
	// matcher: must be a JSON array, never a scalar.
	switch row["matcher"].(type) {
	case []interface{}:
		// OK — serialised from []string{"*"}.
	default:
		t.Errorf("matcher type %T; want []interface{} (JSON array)", row["matcher"])
	}
	// upsert_outcome: must be one of the four valid strings, or nil on early
	// exit (before the store call was reached).
	if outcome := row["upsert_outcome"]; outcome != nil {
		valid := map[string]bool{"inserted": true, "updated": true, "no_change": true, "error": true}
		if s, ok := outcome.(string); !ok || !valid[s] {
			t.Errorf("upsert_outcome = %v; want one of inserted/updated/no_change/error", outcome)
		}
	}
	// tool_input: must NEVER be present (SR-A-7 binding invariant).
	if _, ok := row["tool_input"]; ok {
		t.Errorf("tool_input present in trail line; must be silently dropped")
	}
	return row
}

// runCLIWithStdin is a variant of runCLI that pipes a payload into stdin.
// Used by the hook tests to deliver synthesized Claude Code event JSON.
func runCLIWithStdin(t *testing.T, home, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"AGENT_DIRECTOR_INSTANCE_ID=" + os.Getenv("AGENT_DIRECTOR_INSTANCE_ID"),
	}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("unexpected exec error: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

func runCLIWithEnv(t *testing.T, home string, env map[string]string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	envArr := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
	}
	for k, v := range env {
		envArr = append(envArr, k+"="+v)
	}
	cmd.Env = envArr
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("unexpected exec error: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

// insertPendingRow uses raw SQL to seed a pending row so the hook test can
// observe the transition. Tests intentionally bypass the api/spawn layer
// here because Task 4's gate is the hook subsystem in isolation; Task 5's
// integration tests will exercise the full spawn → hook round trip.
func insertPendingRow(t *testing.T, dbPath, instanceID string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
        INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode)
        VALUES (?, 'pending', '/tmp', 'cd-test', 'off')
    `, instanceID)
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

// readSpawnRow returns the (state, claude_session_id) of a row. Helper for
// the hook integration tests so each test reads its own observations.
func readSpawnRow(t *testing.T, dbPath, instanceID string) (string, string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var state, sessionID sql.NullString
	err = db.QueryRow(`SELECT state, COALESCE(claude_session_id,'') FROM spawns WHERE claude_instance_id = ?`,
		instanceID).Scan(&state, &sessionID)
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	return state.String, sessionID.String
}

func TestHookCLISessionStartTransitionsToWaiting(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", stateDir)
	// First call: any verb other than `hook` triggers schema bootstrap.
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-1")

	payload := `{"hook_event_name":"SessionStart","transcript_path":"/x/y/abc-uuid.jsonl"}`
	stdout, stderr, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-1",
			"AGENT_DIRECTOR_STATE_DIR":   stateDir,
		},
		payload, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0\nstderr=%s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("hook stdout must be empty (state-tracking fail-open); got %q", stdout)
	}
	state, sessionID := readSpawnRow(t, dbPath, "id-hook-1")
	if state != "waiting" {
		t.Errorf("state = %q; want waiting", state)
	}
	if sessionID != "abc-uuid" {
		t.Errorf("claude_session_id = %q; want abc-uuid", sessionID)
	}

	row := assertHookFiredLine(t, readTrailLines(t, stateDir))
	if row["claude_instance_id"] != "id-hook-1" {
		t.Errorf("claude_instance_id = %v; want id-hook-1", row["claude_instance_id"])
	}
	if row["event_name"] != "SessionStart" {
		t.Errorf("event_name = %v; want SessionStart", row["event_name"])
	}
	if row["session_id"] != "abc-uuid" {
		t.Errorf("session_id = %v; want abc-uuid", row["session_id"])
	}
}

func TestHookCLIMissingEnvExitsZero(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", stateDir)
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
	// No AGENT_DIRECTOR_INSTANCE_ID set — fail-open, exit 0 with no stdout.
	stdout, _, code := runCLIWithEnv(t, home,
		map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir},
		`{"hook_event_name":"SessionStart"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0 (fail-open)", code)
	}
	if stdout != "" {
		t.Fatalf("hook stdout must be empty; got %q", stdout)
	}

	// Trail line must still be emitted (defer fires on all exit paths).
	row := assertHookFiredLine(t, readTrailLines(t, stateDir))
	// claude_instance_id is nil because ResolveInstanceID failed before it was set.
	if row["claude_instance_id"] != nil {
		t.Errorf("claude_instance_id = %v; want nil (missing env)", row["claude_instance_id"])
	}
}

func TestHookCLIPreToolUseAskUserSetsAskUser(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", stateDir)
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-2")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`
	_, stderr, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-2",
			"AGENT_DIRECTOR_STATE_DIR":   stateDir,
		},
		payload, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0\nstderr=%s", code, stderr)
	}
	state, _ := readSpawnRow(t, dbPath, "id-hook-2")
	if state != "ask_user" {
		t.Errorf("state = %q; want ask_user", state)
	}

	row := assertHookFiredLine(t, readTrailLines(t, stateDir))
	if row["claude_instance_id"] != "id-hook-2" {
		t.Errorf("claude_instance_id = %v; want id-hook-2", row["claude_instance_id"])
	}
	if row["event_name"] != "PreToolUse" {
		t.Errorf("event_name = %v; want PreToolUse", row["event_name"])
	}
	if row["tool_name"] != "AskUserQuestion" {
		t.Errorf("tool_name = %v; want AskUserQuestion", row["tool_name"])
	}
}

func TestHookCLISessionEndCompactIsSoftRefresh(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-3")

	// Bump to waiting first so soft-refresh has a non-pending baseline.
	// Use a separate stateDir so its trail line doesn't pollute the assertion.
	stateDir1 := t.TempDir()
	_, _, _ = runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-3",
			"AGENT_DIRECTOR_STATE_DIR":   stateDir1,
		},
		`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`, "hook")
	state, _ := readSpawnRow(t, dbPath, "id-hook-3")
	if state != "waiting" {
		t.Fatalf("baseline state = %q; want waiting", state)
	}

	// Now compact — must NOT change state.
	stateDir2 := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", stateDir2)
	_, _, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-3",
			"AGENT_DIRECTOR_STATE_DIR":   stateDir2,
		},
		`{"hook_event_name":"SessionEnd","reason":"compact"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0", code)
	}
	state, _ = readSpawnRow(t, dbPath, "id-hook-3")
	if state != "waiting" {
		t.Errorf("state after compact = %q; want waiting (soft refresh)", state)
	}

	row := assertHookFiredLine(t, readTrailLines(t, stateDir2))
	if row["claude_instance_id"] != "id-hook-3" {
		t.Errorf("claude_instance_id = %v; want id-hook-3", row["claude_instance_id"])
	}
	if row["event_name"] != "SessionEnd" {
		t.Errorf("event_name = %v; want SessionEnd", row["event_name"])
	}
}

func TestHookCLISessionEndUserQuitIsEnded(t *testing.T) {
	// b.pmn: a `logout` SessionEnd (one of the closed set of terminal causes)
	// transitions to `ended`. Renamed from the older user_quit case — that
	// label no longer matches the post-b.pmn terminal-cause set.
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", stateDir)
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-4")
	_, _, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-4",
			"AGENT_DIRECTOR_STATE_DIR":   stateDir,
		},
		`{"hook_event_name":"SessionEnd","reason":"logout"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0", code)
	}
	state, _ := readSpawnRow(t, dbPath, "id-hook-4")
	if state != "ended" {
		t.Errorf("state = %q; want ended", state)
	}

	row := assertHookFiredLine(t, readTrailLines(t, stateDir))
	if row["claude_instance_id"] != "id-hook-4" {
		t.Errorf("claude_instance_id = %v; want id-hook-4", row["claude_instance_id"])
	}
	if row["event_name"] != "SessionEnd" {
		t.Errorf("event_name = %v; want SessionEnd", row["event_name"])
	}
}

// trailLifecycleCase parameterizes TestHookCLITrailLifecycles.
type trailLifecycleCase struct {
	name              string
	payload           string
	instanceID        string
	seedRow           bool
	wantEvent         string
	wantTool          string // non-empty: assert tool_name equals this value
	wantSession       string // expected session_id value (default "")
	checkRequestToken bool   // assert request_token key is present (PermissionRequest)
}

// TestHookCLITrailLifecycles is a table-driven test covering every hook
// lifecycle event. Each sub-test asserts exactly one ad.hook.fired line with
// the correct SR-A-2.1 shape.
func TestHookCLITrailLifecycles(t *testing.T) {
	cases := []trailLifecycleCase{
		{
			name:        "SessionStart",
			payload:     `{"hook_event_name":"SessionStart","transcript_path":"/x/tl-uuid.jsonl"}`,
			instanceID:  "id-tl-1",
			seedRow:     true,
			wantEvent:   "SessionStart",
			wantSession: "tl-uuid",
		},
		{
			name:       "UserPromptSubmit",
			payload:    `{"hook_event_name":"UserPromptSubmit"}`,
			instanceID: "id-tl-2",
			seedRow:    true,
			wantEvent:  "UserPromptSubmit",
		},
		{
			name:       "PreToolUse",
			payload:    `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
			instanceID: "id-tl-3",
			seedRow:    true,
			wantEvent:  "PreToolUse",
			wantTool:   "Bash",
		},
		{
			name:       "PostToolUse",
			payload:    `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`,
			instanceID: "id-tl-4",
			seedRow:    true,
			wantEvent:  "PostToolUse",
			wantTool:   "Bash",
		},
		{
			name:              "PermissionRequest",
			payload:           `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`,
			instanceID:        "id-tl-5",
			seedRow:           true,
			wantEvent:         "PermissionRequest",
			wantTool:          "Bash",
			checkRequestToken: true,
		},
		{
			name:       "Notification",
			payload:    `{"hook_event_name":"Notification"}`,
			instanceID: "id-tl-6",
			seedRow:    true,
			wantEvent:  "Notification",
		},
		{
			name:       "SessionEnd_user_quit",
			payload:    `{"hook_event_name":"SessionEnd","reason":"user_quit"}`,
			instanceID: "id-tl-7",
			seedRow:    true,
			wantEvent:  "SessionEnd",
		},
		{
			name:       "SessionEnd_compact",
			payload:    `{"hook_event_name":"SessionEnd","reason":"compact"}`,
			instanceID: "id-tl-8",
			seedRow:    true,
			wantEvent:  "SessionEnd",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			stateDir := t.TempDir()
			t.Setenv("AGENT_DIRECTOR_STATE_DIR", stateDir)

			if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
				t.Fatalf("bootstrap exit = %d", code)
			}
			if tc.seedRow {
				dbPath := filepath.Join(home, ".agent-director", "state.db")
				insertPendingRow(t, dbPath, tc.instanceID)
			}

			_, stderr, code := runCLIWithEnv(t, home,
				map[string]string{
					"AGENT_DIRECTOR_INSTANCE_ID": tc.instanceID,
					"AGENT_DIRECTOR_STATE_DIR":   stateDir,
				},
				tc.payload, "hook")
			if code != 0 {
				t.Fatalf("hook exit = %d; want 0\nstderr=%s", code, stderr)
			}

			row := assertHookFiredLine(t, readTrailLines(t, stateDir))

			if row["claude_instance_id"] != tc.instanceID {
				t.Errorf("claude_instance_id = %v; want %q", row["claude_instance_id"], tc.instanceID)
			}
			if row["event_name"] != tc.wantEvent {
				t.Errorf("event_name = %v; want %q", row["event_name"], tc.wantEvent)
			}
			if tc.wantTool != "" && row["tool_name"] != tc.wantTool {
				t.Errorf("tool_name = %v; want %q", row["tool_name"], tc.wantTool)
			}
			if s, _ := row["session_id"].(string); s != tc.wantSession {
				t.Errorf("session_id = %q; want %q", s, tc.wantSession)
			}
			if tc.checkRequestToken {
				if _, ok := row["request_token"]; !ok {
					t.Errorf("request_token key missing for PermissionRequest event")
				}
			}
		})
	}
}

// TestHookCLINoOpUpsert invokes the hook twice with the same payload against
// an instance that has no pre-seeded row. Both emissions land in separate
// trail files; the second upsert_outcome must be "no_change".
func TestHookCLINoOpUpsert(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("bootstrap exit = %d", code)
	}
	// No insertPendingRow — both UPDATEs will find zero rows → no_change.
	payload := `{"hook_event_name":"UserPromptSubmit"}`
	instanceID := "id-noop-1"

	stateDir1 := t.TempDir()
	_, _, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": instanceID,
			"AGENT_DIRECTOR_STATE_DIR":   stateDir1,
		},
		payload, "hook")
	if code != 0 {
		t.Fatalf("first hook exit = %d; want 0", code)
	}
	row1 := assertHookFiredLine(t, readTrailLines(t, stateDir1))
	if row1["upsert_outcome"] != "no_change" {
		t.Errorf("first upsert_outcome = %v; want no_change (no matching row)", row1["upsert_outcome"])
	}

	stateDir2 := t.TempDir()
	_, _, code = runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": instanceID,
			"AGENT_DIRECTOR_STATE_DIR":   stateDir2,
		},
		payload, "hook")
	if code != 0 {
		t.Fatalf("second hook exit = %d; want 0", code)
	}
	row2 := assertHookFiredLine(t, readTrailLines(t, stateDir2))
	if row2["upsert_outcome"] != "no_change" {
		t.Errorf("second upsert_outcome = %v; want no_change", row2["upsert_outcome"])
	}
}

// TestHookCLIFailOpenEmitsLine asserts that a fail-open early exit (missing
// AGENT_DIRECTOR_INSTANCE_ID) still emits exactly one ad.hook.fired line.
// The trail event is the observable proof that the defer fired.
func TestHookCLIFailOpenEmitsLine(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", stateDir)
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("bootstrap exit = %d", code)
	}

	// No AGENT_DIRECTOR_INSTANCE_ID → ResolveInstanceID fails → early exit.
	_, _, code := runCLIWithEnv(t, home,
		map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir},
		`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0 (fail-open)", code)
	}

	// Line must exist; upsert_outcome may be nil (store call was never reached).
	row := assertHookFiredLine(t, readTrailLines(t, stateDir))
	// claude_instance_id stays nil because resolve failed before it was set.
	if row["claude_instance_id"] != nil {
		t.Errorf("claude_instance_id = %v; want nil on early-exit path", row["claude_instance_id"])
	}
}

// TestHookCLITrailWriteFailureExitsZero points AGENT_DIRECTOR_STATE_DIR at a
// directory the process cannot create (0o500 parent). The hook must still exit
// 0 — trail write failures are fail-soft (SR-A-7). Asserting the meta-event
// in the operational log is not straightforward from CLI-level tests, so only
// exit-0 is verified here.
func TestHookCLITrailWriteFailureExitsZero(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-twf-1")

	// Create a read-only parent so MkdirAll inside the trail writer fails.
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	readOnlyStateDir := filepath.Join(parent, "state")

	_, _, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-twf-1",
			"AGENT_DIRECTOR_STATE_DIR":   readOnlyStateDir,
		},
		`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0 (trail write failure must not kill the hook)", code)
	}
	// Trail file cannot be created — no readTrailLines assertion. The meta-event
	// lands in the operational log (not verifiable at the CLI surface).
}
