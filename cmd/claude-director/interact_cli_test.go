package main_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// seedSpawnRow inserts a row at the requested state and relay_mode so
// the interact-verb CLI tests can drive state-precondition cases without
// going through the full spawn pipeline.
func seedSpawnRow(t *testing.T, dbPath, instanceID, sessionName, state, relayMode string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
        INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode)
        VALUES (?, ?, '/tmp', ?, ?)
    `, instanceID, state, sessionName, relayMode)
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

// bootstrapDB runs `claude-director help` once so the schema is created
// before the test seeds a row directly via raw SQL.
func bootstrapDB(t *testing.T, home string) {
	t.Helper()
	if _, _, code := runCLI(t, "help"); code != 0 {
		// runCLI uses its own t.TempDir; we need a HOME-stable run.
	}
	if _, _, code := runCLIWithHome(t, home, "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
}

func TestSendKeysCLIHappyPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")
	seedSpawnRow(t, dbPath, "id-sk-1", "cd-sk-1", "waiting", "off")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", "id-sk-1", "--text", "hello world")
	if code != 0 {
		t.Fatalf("send-keys exit = %d; stderr=%s", code, stderr)
	}
	// Body is currently an empty struct; the CLI prints "{}".
	if strings.TrimSpace(stdout) != "{}" {
		t.Errorf("stdout = %q; want \"{}\"", stdout)
	}

	logBytes, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	log := string(logBytes)
	// Exactly two send-keys invocations: the text, then Enter.
	if got := strings.Count(log, "send-keys"); got != 2 {
		t.Errorf("send-keys invocation count = %d; want 2 (text + Enter)\nlog=%s", got, log)
	}
	if !strings.Contains(log, "hello world") {
		t.Errorf("fake-tmux log missing the text argv: %s", log)
	}
	if !strings.Contains(log, "\nEnter\n") {
		t.Errorf("fake-tmux log missing the Enter token: %s", log)
	}
	if !strings.Contains(log, "cd-sk-1:0.0") {
		t.Errorf("fake-tmux log missing the pane target: %s", log)
	}
}

func TestSendKeysCLINoEnter(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")
	seedSpawnRow(t, dbPath, "id-sk-2", "cd-sk-2", "waiting", "off")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", "id-sk-2", "--text", "draft", "--no-enter")
	if code != 0 {
		t.Fatalf("send-keys exit = %d; stderr=%s", code, stderr)
	}
	logBytes, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	log := string(logBytes)
	// One send-keys call only — the text. No Enter.
	if got := strings.Count(log, "send-keys"); got != 1 {
		t.Errorf("send-keys invocation count = %d; want 1 (no Enter)\nlog=%s", got, log)
	}
	if strings.Contains(log, "\nEnter\n") {
		t.Errorf("fake-tmux log contained an Enter token under --no-enter: %s", log)
	}
}

func TestSendKeysCLIErrSpawnNotInteractive(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")
	seedSpawnRow(t, dbPath, "id-sk-3", "cd-sk-3", "ended", "off")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", "id-sk-3", "--text", "hi")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotInteractive" {
		t.Errorf("err_name = %q; want ErrSpawnNotInteractive", env.ErrName)
	}
}

func TestSendKeysCLIErrSpawnNotFound(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", "absent", "--text", "hi")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotFound" {
		t.Errorf("err_name = %q; want ErrSpawnNotFound", env.ErrName)
	}
}

func TestSendKeysCLIErrSendKeysWhileRelayed(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")
	seedSpawnRow(t, dbPath, "id-sk-4", "cd-sk-4", "check_permission", "on")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--claude-instance-id", "id-sk-4", "--text", "1")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSendKeysWhileRelayed" {
		t.Errorf("err_name = %q; want ErrSendKeysWhileRelayed", env.ErrName)
	}
}

func TestSendKeysCLIMissingInstanceID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"send-keys", "--text", "hi")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
	}
}
