package main_test

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// runCLIWithStdin is a variant of runCLI that pipes a payload into stdin.
// Used by the hook tests to deliver synthesized Claude Code event JSON.
func runCLIWithStdin(t *testing.T, home, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"CLAUDE_DIRECTOR_INSTANCE_ID=" + os.Getenv("CLAUDE_DIRECTOR_INSTANCE_ID"),
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
	// First call: any verb other than `hook` triggers schema bootstrap.
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-1")

	payload := `{"hook_event_name":"SessionStart","transcript_path":"/x/y/abc-uuid.jsonl"}`
	stdout, stderr, code := runCLIWithEnv(t, home,
		map[string]string{"CLAUDE_DIRECTOR_INSTANCE_ID": "id-hook-1"},
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
}

func TestHookCLIMissingEnvExitsZero(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
	// No CLAUDE_DIRECTOR_INSTANCE_ID set — fail-open, exit 0 with no stdout.
	stdout, _, code := runCLIWithEnv(t, home, nil,
		`{"hook_event_name":"SessionStart"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0 (fail-open)", code)
	}
	if stdout != "" {
		t.Fatalf("hook stdout must be empty; got %q", stdout)
	}
}

func TestHookCLIPreToolUseAskUserSetsAskUser(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-2")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`
	_, stderr, code := runCLIWithEnv(t, home,
		map[string]string{"CLAUDE_DIRECTOR_INSTANCE_ID": "id-hook-2"},
		payload, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0\nstderr=%s", code, stderr)
	}
	state, _ := readSpawnRow(t, dbPath, "id-hook-2")
	if state != "ask_user" {
		t.Errorf("state = %q; want ask_user", state)
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
	_, _, _ = runCLIWithEnv(t, home,
		map[string]string{"CLAUDE_DIRECTOR_INSTANCE_ID": "id-hook-3"},
		`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`, "hook")
	state, _ := readSpawnRow(t, dbPath, "id-hook-3")
	if state != "waiting" {
		t.Fatalf("baseline state = %q; want waiting", state)
	}
	// Now compact — must NOT change state.
	_, _, code := runCLIWithEnv(t, home,
		map[string]string{"CLAUDE_DIRECTOR_INSTANCE_ID": "id-hook-3"},
		`{"hook_event_name":"SessionEnd","reason":"compact"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0", code)
	}
	state, _ = readSpawnRow(t, dbPath, "id-hook-3")
	if state != "waiting" {
		t.Errorf("state after compact = %q; want waiting (soft refresh)", state)
	}
}

func TestHookCLISessionEndUserQuitIsEnded(t *testing.T) {
	home := t.TempDir()
	if _, _, code := runCLIWithStdin(t, home, "", "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	insertPendingRow(t, dbPath, "id-hook-4")
	_, _, code := runCLIWithEnv(t, home,
		map[string]string{"CLAUDE_DIRECTOR_INSTANCE_ID": "id-hook-4"},
		`{"hook_event_name":"SessionEnd","reason":"user_quit"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit = %d; want 0", code)
	}
	state, _ := readSpawnRow(t, dbPath, "id-hook-4")
	if state != "ended" {
		t.Errorf("state = %q; want ended", state)
	}
}
