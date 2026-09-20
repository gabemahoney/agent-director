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

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	_ "modernc.org/sqlite"
)

// linuxProcStarttimeRe is the canonical Linux proc_starttime FORM: field 22 of
// /proc/<pid>/stat is a decimal clock-ticks-since-boot integer, stored verbatim
// (no unit conversion). The end-to-end SessionStart test asserts the recorded
// value matches this shape rather than a fixed value — the real starttime is the
// live hook process's, which is not knowable in advance. The FORM is cross-checked
// against apitest.LinuxProcStarttime (the shared canonical fixture) below so the
// two never silently diverge.
var linuxProcStarttimeRe = regexp.MustCompile(`^[0-9]+$`)

// tsRe is the SR-A-7.9 timestamp regex validated on every ad.hook.fired line.
var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// readTrailLines opens <home>/.agent-director/ad-trail.jsonl and returns each
// line as a parsed map. The trail always lands under the process HOME, so tests
// isolate their trail by giving each subprocess its own HOME. It fails the test
// immediately on any I/O or parse error.
func readTrailLines(t *testing.T, home string) []map[string]any {
	t.Helper()
	path := filepath.Join(home, ".agent-director", "ad-trail.jsonl")
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

// hookFiredLines returns every ad.hook.fired line in lines, preserving order.
// Used both by assertHookFiredLine (exactly-one case) and by tests that share a
// single HOME trail across invocations and measure their contribution as a
// checkpoint/delta line count (SR-12.1).
func hookFiredLines(lines []map[string]any) []map[string]any {
	var fired []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.hook.fired" {
			fired = append(fired, l)
		}
	}
	return fired
}

// assertHookFiredLine finds the single ad.hook.fired line in lines, validates
// the SR-A-2.1 required fields (ts, source, relay_mode, session_id, matcher
// shape, upsert_outcome, no tool_input), and returns it for further assertions.
func assertHookFiredLine(t *testing.T, lines []map[string]any) map[string]any {
	t.Helper()
	fired := hookFiredLines(lines)
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

// spawnIdentity is the schema-v3 identity/transcript triplet the SessionStart
// write site populates: pid, proc_starttime, jsonl_path. Each field carries
// its own SQL NULL flag so the caller can assert non-NULL explicitly rather
// than conflating NULL with a zero/empty value.
type spawnIdentity struct {
	pid           sql.NullInt64
	procStarttime sql.NullString
	jsonlPath     sql.NullString
}

// readSpawnIdentity reads the pid, proc_starttime, and jsonl_path columns raw
// (no COALESCE) so the end-to-end SessionStart test can distinguish NULL from a
// present value. This is the observable proof that the CLI's hook verb wrote
// the tracked identity onto the spawn row.
func readSpawnIdentity(t *testing.T, dbPath, instanceID string) spawnIdentity {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var id spawnIdentity
	err = db.QueryRow(`SELECT pid, proc_starttime, jsonl_path FROM spawns WHERE claude_instance_id = ?`,
		instanceID).Scan(&id.pid, &id.procStarttime, &id.jsonlPath)
	if err != nil {
		t.Fatalf("read identity row: %v", err)
	}
	return id
}

// runCLIWithEnvPID is runCLIWithEnv plus the started subprocess's OS pid. The
// SessionStart end-to-end test needs the pid because — with
// AGENT_DIRECTOR_INSTANCE_ID set ONLY on the hook subprocess (never on the
// go-test parent) — the probe resolver's topmost matching ancestor is the hook
// process itself, so the recorded spawn-row pid must equal this exact value.
// exec.Cmd exposes it via cmd.Process.Pid once Start/Run has run.
func runCLIWithEnvPID(t *testing.T, home string, env map[string]string, stdin string, args ...string) (string, string, int, int) {
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
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("unexpected exec error: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exitCode, pid
}

func TestHookCLISessionStartTransitionsToWaiting(t *testing.T) {
	home := t.TempDir()
	// First call: a store-opening verb (`list`) triggers schema bootstrap.
	if _, _, code := runCLIWithStdin(t, home, "", "list"); code != 0 {
		t.Fatalf("list bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-1")

	payload := `{"hook_event_name":"SessionStart","transcript_path":"/x/y/abc-uuid.jsonl"}`
	stdout, stderr, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-1",
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

	row := assertHookFiredLine(t, readTrailLines(t, home))
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

// TestHookCLISessionStartRecordsIdentityAndTranscript is the Epic's sprint-demo
// acceptance, exercised end-to-end against the REAL binary subprocess (not an
// in-process Handle): seed a spawn, fire a SessionStart hook carrying a
// transcript_path, and assert the row shows the tracked identity — non-NULL pid
// (exact, equal to the subprocess's own pid), a canonical-form proc_starttime,
// and jsonl_path exactly equal to the payload transcript_path. A subsequent
// non-SessionStart hook invocation must leave all three unchanged (no-clobber).
//
// pid is asserted for EXACT equality — not merely > 0 — because it is feasible
// and preferred here: AGENT_DIRECTOR_INSTANCE_ID is set ONLY on the hook
// subprocess's env (runCLIWithEnvPID injects it via cmd.Env, never on the
// go-test parent), the binary does no re-exec, and the probe resolver walks from
// the hook process UP to the TOPMOST ancestor carrying the var. The go-test
// parent does not carry it, so the walk's topmost match is the hook process
// itself → the recorded pid equals cmd.Process.Pid deterministically. This holds
// under sandbox --pid=host: the assertion is anchored to the subprocess's own
// pid, never an absolute host-pid value, and the walk tolerates foreign-uid
// ancestors above it (their environ is unreadable → non-match, walk continues).
//
// proc_starttime is a live value (the hook process's actual field-22 ticks), so
// it is asserted by FORM (^[0-9]+$), cross-checked against the shared canonical
// fixture apitest.LinuxProcStarttime rather than by value equality.
func TestHookCLISessionStartRecordsIdentityAndTranscript(t *testing.T) {
	// Guard: the canonical-form regex must accept the shared fixture constant.
	// This anchors the FORM the test asserts to apitest's re-exported canonical
	// value so a future change to the canonical proc_starttime shape trips here.
	if !linuxProcStarttimeRe.MatchString(apitest.LinuxProcStarttime) {
		t.Fatalf("canonical-form regex rejects apitest.LinuxProcStarttime %q — form/fixture drift",
			apitest.LinuxProcStarttime)
	}

	home := t.TempDir()
	// list bootstrap: opens the store and runs schema migration to v3.
	if _, _, code := runCLIWithStdin(t, home, "", "list"); code != 0 {
		t.Fatalf("list bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-e2e-1")

	transcript := "/home/agent/.claude/projects/e2e/session-e2e-uuid.jsonl"
	payload := `{"hook_event_name":"SessionStart","transcript_path":"` + transcript + `"}`
	_, stderr, code, pid := runCLIWithEnvPID(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-e2e-1",
		},
		payload, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0\nstderr=%s", code, stderr)
	}
	if pid <= 0 {
		t.Fatalf("subprocess pid = %d; want > 0 (cmd.Process.Pid unavailable)", pid)
	}

	id := readSpawnIdentity(t, dbPath, "id-e2e-1")

	// pid: non-NULL and exactly the hook subprocess's pid (topmost matching
	// ancestor is the hook process itself, per the doc-comment above).
	if !id.pid.Valid {
		t.Errorf("pid is NULL; want non-NULL == subprocess pid %d", pid)
	} else if id.pid.Int64 != int64(pid) {
		t.Errorf("pid = %d; want %d (hook subprocess is the topmost AGENT_DIRECTOR_INSTANCE_ID ancestor)",
			id.pid.Int64, pid)
	}

	// proc_starttime: non-NULL and in the canonical Linux form (decimal ticks).
	if !id.procStarttime.Valid {
		t.Errorf("proc_starttime is NULL; want a canonical decimal string")
	} else if !linuxProcStarttimeRe.MatchString(id.procStarttime.String) {
		t.Errorf("proc_starttime = %q; want canonical form %s", id.procStarttime.String, linuxProcStarttimeRe)
	}

	// jsonl_path: non-NULL and EXACTLY the payload transcript_path (no basename
	// extraction, no COALESCE surprise).
	if !id.jsonlPath.Valid {
		t.Errorf("jsonl_path is NULL; want %q", transcript)
	} else if id.jsonlPath.String != transcript {
		t.Errorf("jsonl_path = %q; want %q (exact payload transcript_path)", id.jsonlPath.String, transcript)
	}

	// No-clobber: a subsequent non-SessionStart hook (PreToolUse) must NOT touch
	// pid / proc_starttime / jsonl_path — the write site is gated on the
	// SessionStart event, and non-SessionStart events never reach the resolver.
	_, stderr2, code2 := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-e2e-1",
		},
		`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, "hook")
	if code2 != 0 {
		t.Fatalf("second hook exit = %d; want 0\nstderr=%s", code2, stderr2)
	}
	after := readSpawnIdentity(t, dbPath, "id-e2e-1")
	if after.pid != id.pid {
		t.Errorf("pid changed after non-SessionStart event: %v -> %v", id.pid, after.pid)
	}
	if after.procStarttime != id.procStarttime {
		t.Errorf("proc_starttime changed after non-SessionStart event: %v -> %v",
			id.procStarttime, after.procStarttime)
	}
	if after.jsonlPath != id.jsonlPath {
		t.Errorf("jsonl_path changed after non-SessionStart event: %v -> %v",
			id.jsonlPath, after.jsonlPath)
	}
}

func TestHookCLIMissingEnvExitsZero(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "list"); code != 0 {
		t.Fatalf("list bootstrap exit = %d", code)
	}
	// No AGENT_DIRECTOR_INSTANCE_ID set — fail-open, exit 0 with no stdout.
	stdout, _, code := runCLIWithEnv(t, home,
		map[string]string{},
		`{"hook_event_name":"SessionStart"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0 (fail-open)", code)
	}
	if stdout != "" {
		t.Fatalf("hook stdout must be empty; got %q", stdout)
	}

	// Trail line must still be emitted (defer fires on all exit paths).
	row := assertHookFiredLine(t, readTrailLines(t, home))
	// claude_instance_id is nil because ResolveInstanceID failed before it was set.
	if row["claude_instance_id"] != nil {
		t.Errorf("claude_instance_id = %v; want nil (missing env)", row["claude_instance_id"])
	}
}

func TestHookCLIPreToolUseAskUserSetsAskUser(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "list"); code != 0 {
		t.Fatalf("list bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-2")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`
	_, stderr, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-2",
		},
		payload, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0\nstderr=%s", code, stderr)
	}
	state, _ := readSpawnRow(t, dbPath, "id-hook-2")
	if state != "ask_user" {
		t.Errorf("state = %q; want ask_user", state)
	}

	row := assertHookFiredLine(t, readTrailLines(t, home))
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
	if _, _, code := runCLIWithStdin(t, home, "", "list"); code != 0 {
		t.Fatalf("list bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-3")

	// Bump to waiting first so soft-refresh has a non-pending baseline. Both
	// invocations share this HOME (and its state.db + trail), so the bump's
	// ad.hook.fired line is isolated from the compact assertion via a
	// checkpoint/delta line-count against the shared trail (SR-12.1) rather
	// than a separate trail file.
	_, _, _ = runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-3",
		},
		`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`, "hook")
	state, _ := readSpawnRow(t, dbPath, "id-hook-3")
	if state != "waiting" {
		t.Fatalf("baseline state = %q; want waiting", state)
	}
	// Checkpoint: the bump has already emitted one ad.hook.fired line; the
	// compact's contribution is measured as the delta past this point.
	checkpoint := len(hookFiredLines(readTrailLines(t, home)))

	// Now compact — must NOT change state.
	_, _, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-3",
		},
		`{"hook_event_name":"SessionEnd","reason":"compact"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0", code)
	}
	state, _ = readSpawnRow(t, dbPath, "id-hook-3")
	if state != "waiting" {
		t.Errorf("state after compact = %q; want waiting (soft refresh)", state)
	}

	fired := hookFiredLines(readTrailLines(t, home))
	if got := len(fired) - checkpoint; got != 1 {
		t.Fatalf("ad.hook.fired delta = %d; want 1 (total %d, checkpoint %d)", got, len(fired), checkpoint)
	}
	row := fired[len(fired)-1]
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
	if _, _, code := runCLIWithStdin(t, home, "", "list"); code != 0 {
		t.Fatalf("list bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-4")
	_, _, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-hook-4",
		},
		`{"hook_event_name":"SessionEnd","reason":"logout"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0", code)
	}
	state, _ := readSpawnRow(t, dbPath, "id-hook-4")
	if state != "ended" {
		t.Errorf("state = %q; want ended", state)
	}

	row := assertHookFiredLine(t, readTrailLines(t, home))
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

			if _, _, code := runCLIWithStdin(t, home, "", "list"); code != 0 {
				t.Fatalf("bootstrap exit = %d", code)
			}
			if tc.seedRow {
				dbPath := filepath.Join(home, ".agent-director", "state.db")
				insertPendingRow(t, dbPath, tc.instanceID)
			}

			_, stderr, code := runCLIWithEnv(t, home,
				map[string]string{
					"AGENT_DIRECTOR_INSTANCE_ID": tc.instanceID,
				},
				tc.payload, "hook")
			if code != 0 {
				t.Fatalf("hook exit = %d; want 0\nstderr=%s", code, stderr)
			}

			row := assertHookFiredLine(t, readTrailLines(t, home))

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
	// No insertPendingRow — both UPDATEs will find zero rows → no_change.
	// Neither invocation depends on the other's DB state, so each gets its own
	// HOME; that keeps their trail lines in separate files (one ad.hook.fired
	// line apiece) without a shared-trail checkpoint/delta.
	payload := `{"hook_event_name":"UserPromptSubmit"}`
	instanceID := "id-noop-1"

	home1 := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home1, "", "list"); code != 0 {
		t.Fatalf("first bootstrap exit = %d", code)
	}
	_, _, code := runCLIWithEnv(t, home1,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": instanceID,
		},
		payload, "hook")
	if code != 0 {
		t.Fatalf("first hook exit = %d; want 0", code)
	}
	row1 := assertHookFiredLine(t, readTrailLines(t, home1))
	if row1["upsert_outcome"] != "no_change" {
		t.Errorf("first upsert_outcome = %v; want no_change (no matching row)", row1["upsert_outcome"])
	}

	home2 := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home2, "", "list"); code != 0 {
		t.Fatalf("second bootstrap exit = %d", code)
	}
	_, _, code = runCLIWithEnv(t, home2,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": instanceID,
		},
		payload, "hook")
	if code != 0 {
		t.Fatalf("second hook exit = %d; want 0", code)
	}
	row2 := assertHookFiredLine(t, readTrailLines(t, home2))
	if row2["upsert_outcome"] != "no_change" {
		t.Errorf("second upsert_outcome = %v; want no_change", row2["upsert_outcome"])
	}
}

// TestHookCLIFailOpenEmitsLine asserts that a fail-open early exit (missing
// AGENT_DIRECTOR_INSTANCE_ID) still emits exactly one ad.hook.fired line.
// The trail event is the observable proof that the defer fired.
func TestHookCLIFailOpenEmitsLine(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "list"); code != 0 {
		t.Fatalf("bootstrap exit = %d", code)
	}

	// No AGENT_DIRECTOR_INSTANCE_ID → ResolveInstanceID fails → early exit.
	_, _, code := runCLIWithEnv(t, home,
		map[string]string{},
		`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0 (fail-open)", code)
	}

	// Line must exist; upsert_outcome may be nil (store call was never reached).
	row := assertHookFiredLine(t, readTrailLines(t, home))
	// claude_instance_id stays nil because resolve failed before it was set.
	if row["claude_instance_id"] != nil {
		t.Errorf("claude_instance_id = %v; want nil on early-exit path", row["claude_instance_id"])
	}
}

// TestHookCLITrailWriteFailureExitsZero makes the isolated HOME's
// ~/.agent-director directory read-only (0o500) after the store is fully
// warmed, so the trail writer's O_CREATE of a fresh ad-trail.jsonl in that
// directory genuinely fails with EACCES. The hook must still exit 0 — trail
// write failures are fail-soft (SR-A-7). The trail resolves to
// <home>/.agent-director/ad-trail.jsonl (os.UserHomeDir), so making that
// directory unwritable is the HOME-based equivalent of the former
// read-only-state-dir provocation.
func TestHookCLITrailWriteFailureExitsZero(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "list"); code != 0 {
		t.Fatalf("bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	// Seed while the directory is still writable — this opens state.db RW and
	// materializes the WAL/-shm sidecars, so the hook's own store open below
	// needs no new files in the soon-to-be-read-only directory.
	insertPendingRow(t, dbPath, "id-twf-1")

	// Freeze ~/.agent-director read-only so the trail append (a fresh-file
	// O_CREATE) fails, while the pre-existing state.db + sidecars stay openable.
	adDir := filepath.Join(home, ".agent-director")
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })

	_, _, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-twf-1",
		},
		`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0 (trail write failure must not kill the hook)", code)
	}
	// Trail file cannot be created — no readTrailLines assertion. The meta-event
	// lands in the operational log (not verifiable at the CLI surface).
}
