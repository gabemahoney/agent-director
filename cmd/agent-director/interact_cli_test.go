package main_test

import (
	"database/sql"
	"os"
	"os/exec"
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

// seedOpenPermissionRequest inserts an open permission_requests row
// (decision/decision_reason left NULL) for the given Spawn so the
// get-verb CLI tests can drive the `state=check_permission` happy-path
// and the SR-8.5 absence cases. Tool name and tool_input are pure
// parameters per SR-8.2 — no hardcoded literals inside the helper.
// Caller is responsible for inserting the parent spawn row first; the
// FK on claude_instance_id would reject otherwise.
func seedOpenPermissionRequest(t *testing.T, dbPath, instanceID, toolName, toolInput string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
        INSERT INTO permission_requests (claude_instance_id, tool_name, tool_input)
        VALUES (?, ?, ?)
    `, instanceID, toolName, toolInput); err != nil {
		t.Fatalf("seed permission row: %v", err)
	}
}

// markPermissionRequestDecided flips decision (and optionally
// decision_reason) on the open row, simulating a prior-cycle decision.
// Used by the M1-gating CLI test that asserts api.Get ignores decided
// rows even while the spawn is back at check_permission.
func markPermissionRequestDecided(t *testing.T, dbPath, instanceID, decision string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	res, err := db.Exec(`
        UPDATE permission_requests SET decision = ? WHERE claude_instance_id = ?
    `, decision, instanceID)
	if err != nil {
		t.Fatalf("mark decided: %v", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		t.Fatalf("mark decided affected %d rows; want 1 (row missing?)", n)
	}
}

// bootstrapDB runs `agent-director help` once so the schema is created
// before the test seeds a row directly via raw SQL.
func bootstrapDB(t *testing.T, home string) {
	t.Helper()
	if _, _, code := runCLIWithHome(t, home, "help"); code != 0 {
		t.Fatalf("help bootstrap exit = %d", code)
	}
}

func TestSendKeysCLIHappyPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
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

func TestSendKeysCLIErrSpawnNotInteractive(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
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
	dbPath := filepath.Join(home, ".agent-director", "state.db")
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

func TestReadPaneCLIHappyPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-rp-cli-1", "cd-rp-cli-1", "waiting", "off")

	stdout, stderr, code := runSpawnCLIWithExtraEnv(t, home, fakeDir,
		map[string]string{"FAKE_TMUX_PANE_OUTPUT": "\x1b[31m❯\x1b[0m hi\n"},
		"read-pane", "--claude-instance-id", "id-rp-cli-1")
	if code != 0 {
		t.Fatalf("read-pane exit = %d; stderr=%s", code, stderr)
	}
	// Default mode strips ANSI but preserves the unicode prompt glyph.
	if !strings.Contains(stdout, "❯ hi") {
		t.Errorf("stdout missing stripped pane content: %q", stdout)
	}
	if strings.Contains(stdout, "\\u001b[") {
		t.Errorf("stdout contains escape codes; default should strip: %q", stdout)
	}

	// fake-tmux logs the capture-pane invocation with -S -25 default.
	logBytes, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "capture-pane") {
		t.Errorf("fake-tmux log missing capture-pane: %s", log)
	}
	if !strings.Contains(log, "-25") {
		t.Errorf("fake-tmux log missing -S -25 default: %s", log)
	}
	if !strings.Contains(log, "cd-rp-cli-1:0.0") {
		t.Errorf("fake-tmux log missing pane target: %s", log)
	}
}

func TestReadPaneCLIANSIPreservesEscapes(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-rp-cli-2", "cd-rp-cli-2", "waiting", "off")

	stdout, stderr, code := runSpawnCLIWithExtraEnv(t, home, fakeDir,
		map[string]string{"FAKE_TMUX_PANE_OUTPUT": "\x1b[31mred\x1b[0m text"},
		"read-pane", "--claude-instance-id", "id-rp-cli-2", "--ansi")
	if code != 0 {
		t.Fatalf("read-pane exit = %d; stderr=%s", code, stderr)
	}
	// JSON encodes the escape byte (0x1b) as . Both forms prove
	// the raw bytes survived.
	if !strings.Contains(stdout, "\\u001b[31m") {
		t.Errorf("stdout missing raw escape codes under --ansi: %q", stdout)
	}
	// b.s12: tmux only emits ANSI escapes when called with `-e`. Verify
	// the underlying capture-pane invocation included it under --ansi.
	logBytes, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "\n-e\n") {
		t.Errorf("fake-tmux log missing -e flag under --ansi (tmux strips escapes without it): %s", log)
	}
}

func TestReadPaneCLIDefaultOmitsDashE(t *testing.T) {
	// Companion to TestReadPaneCLIANSIPreservesEscapes. Default mode
	// (no --ansi) must NOT pass -e — agent-director strips residuals
	// at the verb layer.
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-rp-cli-default-e", "cd-rp-cli-de", "waiting", "off")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"read-pane", "--claude-instance-id", "id-rp-cli-default-e")
	if code != 0 {
		t.Fatalf("read-pane exit = %d; stderr=%s", code, stderr)
	}
	logBytes, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	if strings.Contains(string(logBytes), "\n-e\n") {
		t.Errorf("fake-tmux log unexpectedly contains -e under default (no --ansi): %s", string(logBytes))
	}
}

func TestReadPaneCLICustomLineCount(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	seedSpawnRow(t, dbPath, "id-rp-cli-3", "cd-rp-cli-3", "waiting", "off")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"read-pane", "--claude-instance-id", "id-rp-cli-3", "--n-lines", "100")
	if code != 0 {
		t.Fatalf("read-pane exit = %d; stderr=%s", code, stderr)
	}
	logBytes, _ := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if !strings.Contains(string(logBytes), "-100") {
		t.Errorf("fake-tmux log missing -S -100 override: %s", string(logBytes))
	}
}

func TestReadPaneCLIErrSpawnNotFound(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"read-pane", "--claude-instance-id", "absent")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotFound" {
		t.Errorf("err_name = %q; want ErrSpawnNotFound", env.ErrName)
	}
}

// runSpawnCLIWithExtraEnv extends runSpawnCLI by injecting additional env
// vars (used by the read-pane tests to swap fake-tmux's stub pane output).
// The PATH / HOME / FAKE_TMUX_LOG vars are still set, identical to
// runSpawnCLI; the extras are appended.
func runSpawnCLIWithExtraEnv(t *testing.T, home, fakeTmuxDir string, extras map[string]string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	env := []string{
		"PATH=" + fakeTmuxDir + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"FAKE_TMUX_LOG=" + filepath.Join(home, "fake-tmux.log"),
	}
	for k, v := range extras {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	var stdout, stderr strings.Builder
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
