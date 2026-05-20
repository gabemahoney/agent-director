package main_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeTmuxBin is the path to the fake-tmux helper compiled by buildFakeTmux.
// Holding it as a package-level var lets each test reach the same binary
// without rebuilding it per test.
var fakeTmuxBin string

// buildFakeTmux compiles test/fake-tmux into a temp dir and returns the
// directory so callers can prepend it to PATH for the spawn-CLI tests.
// Cached across tests via the fakeTmuxBin var.
func buildFakeTmux(t *testing.T) string {
	t.Helper()
	if fakeTmuxBin != "" {
		return filepath.Dir(fakeTmuxBin)
	}
	tmp, err := os.MkdirTemp("", "fake-tmux-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	// The fake binary must be named exactly "tmux" so exec.LookPath finds
	// it ahead of the system tmux on a PATH prepend.
	out := filepath.Join(tmp, "tmux")
	build := exec.Command("go", "build", "-o", out, "../../test/fake-tmux")
	build.Stderr = os.Stderr
	build.Stdout = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build fake-tmux: %v", err)
	}
	fakeTmuxBin = out
	return tmp
}

// runSpawnCLI is a thin wrapper around exec.Command that runs the built
// CLI with PATH manipulated so the fake-tmux binary wins over any
// system-installed tmux. HOME is overridden to t.TempDir() so each test
// has its own ~/.claude-director DB.
func runSpawnCLI(t *testing.T, home, fakeTmuxDir string, args ...string) (string, string, int) {
	t.Helper()
	return runSpawnCLIEnv(t, home, fakeTmuxDir, nil, args...)
}

// runSpawnCLIEnv is the same as runSpawnCLI plus an optional extraEnv
// map appended to the child env. Used by tests that need to inject
// fake-tmux failure-injection vars (e.g. FAKE_TMUX_FAIL_NEWSESSION_NAME)
// without rebuilding the binary.
func runSpawnCLIEnv(t *testing.T, home, fakeTmuxDir string, extraEnv map[string]string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	logPath := filepath.Join(home, "fake-tmux.log")
	env := []string{
		"PATH=" + fakeTmuxDir + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"FAKE_TMUX_LOG=" + logPath,
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("unexpected exec error: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

// spawnResult mirrors api.SpawnResult for the CLI integration tests.
type spawnResult struct {
	ClaudeInstanceID string `json:"claude_instance_id"`
}

func TestSpawnCLIHappyPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	cwd := t.TempDir()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"spawn", "--cwd", cwd, "--label", "role=worker", "--", "--model", "opus")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, stderr)
	}
	var res spawnResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if res.ClaudeInstanceID == "" {
		t.Fatalf("claude_instance_id empty in result %s", stdout)
	}

	// Confirm the row exists by calling `status`.
	statusOut, _, code := runSpawnCLI(t, home, fakeDir,
		"status", "--claude-instance-id", res.ClaudeInstanceID)
	if code != 0 {
		t.Fatalf("status exit = %d", code)
	}
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(statusOut), &st); err != nil {
		t.Fatalf("parse status %q: %v", statusOut, err)
	}
	if st.State != "pending" {
		t.Errorf("state = %q; want pending", st.State)
	}

	// Confirm `get` returns the full row.
	getOut, _, code := runSpawnCLI(t, home, fakeDir,
		"get", "--claude-instance-id", res.ClaudeInstanceID)
	if code != 0 {
		t.Fatalf("get exit = %d", code)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(getOut), &row); err != nil {
		t.Fatalf("parse get %q: %v", getOut, err)
	}
	if row["state"] != "pending" {
		t.Errorf("get.state = %v; want pending", row["state"])
	}
	if row["cwd"] == "" {
		t.Errorf("get.cwd empty")
	}
	if row["relay_mode"] != "off" {
		t.Errorf("get.relay_mode = %v; want off (config default)", row["relay_mode"])
	}
	// claude_args should round-trip the passthrough.
	args, _ := row["claude_args"].([]any)
	if len(args) != 2 || args[0] != "--model" || args[1] != "opus" {
		t.Errorf("get.claude_args = %v; want [--model opus]", args)
	}
	// Labels should round-trip.
	labels, _ := row["labels"].(map[string]any)
	if labels["role"] != "worker" {
		t.Errorf("get.labels.role = %v; want worker", labels["role"])
	}

	// Verify fake-tmux saw a new-session invocation.
	logBytes, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	logContent := string(logBytes)
	if !strings.Contains(logContent, "new-session") {
		t.Errorf("fake-tmux log missing new-session: %s", logContent)
	}
	if !strings.Contains(logContent, "--settings") {
		t.Errorf("fake-tmux log missing --settings: %s", logContent)
	}
	if !strings.Contains(logContent, "CLAUDE_DIRECTOR_INSTANCE_ID="+res.ClaudeInstanceID) {
		t.Errorf("fake-tmux log missing instance-id env: %s", logContent)
	}
	if !strings.Contains(logContent, "CLAUDE_DIRECTOR_LABEL_ROLE=worker") {
		t.Errorf("fake-tmux log missing label env: %s", logContent)
	}
}

// TestSpawnCLIPreTrustWritesClaudeJSON pins bug b.f75 at the CLI
// boundary: `claude-director spawn --cwd <fresh>` with no other flags
// writes hasTrustDialogAccepted=true into ~/.claude.json for the
// resolved cwd before exec'ing tmux. HOME is overridden to a per-test
// tmpdir so the operator's real ~/.claude.json is never touched.
func TestSpawnCLIPreTrustWritesClaudeJSON(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	// Seed an empty ~/.claude.json so preTrustCwd has a file to mutate
	// (the missing-file path is the AC#5 case, covered elsewhere).
	claudeJSON := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed claude.json: %v", err)
	}
	cwd := t.TempDir()

	_, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", cwd)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, stderr)
	}

	raw, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read claude.json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse claude.json: %v (raw=%q)", err, string(raw))
	}
	projects, _ := got["projects"].(map[string]any)
	entry, ok := projects[cwd].(map[string]any)
	if !ok {
		t.Fatalf("projects[%q] missing after spawn: %v", cwd, projects)
	}
	if b, _ := entry["hasTrustDialogAccepted"].(bool); !b {
		t.Errorf("hasTrustDialogAccepted = %v; want true", entry["hasTrustDialogAccepted"])
	}
}

// TestSpawnCLINoPreTrustFlagSkipsWrite pins AC #2: --no-pre-trust opts
// out of the workspace-trust pre-write.
func TestSpawnCLINoPreTrustFlagSkipsWrite(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	claudeJSON := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed claude.json: %v", err)
	}
	cwd := t.TempDir()

	_, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", cwd, "--no-pre-trust")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, stderr)
	}

	raw, _ := os.ReadFile(claudeJSON)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse claude.json: %v", err)
	}
	projects, _ := got["projects"].(map[string]any)
	if _, present := projects[cwd]; present {
		t.Errorf("projects[%q] was written despite --no-pre-trust", cwd)
	}
}

func TestSpawnCLIRelativeCwdErrCwdNotAPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"spawn", "--cwd", "./relative")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrCwdNotAPath" {
		t.Errorf("err_name = %q; want ErrCwdNotAPath", env.ErrName)
	}
}

func TestSpawnCLIDeniedFlag(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	cwd := t.TempDir()
	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"spawn", "--cwd", cwd, "--", "--settings={}")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0")
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnDeniedFlag" {
		t.Errorf("err_name = %q; want ErrSpawnDeniedFlag", env.ErrName)
	}
}

func TestSpawnCLIReservedEnvKey(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	cwd := t.TempDir()
	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"spawn", "--cwd", cwd, "--extra-env", "CLAUDE_DIRECTOR_FOO=bar")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0")
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrReservedEnvKey" {
		t.Errorf("err_name = %q; want ErrReservedEnvKey", env.ErrName)
	}
}

func TestStatusCLIErrSpawnNotFound(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	// Bootstrap an empty DB by running help first.
	if _, _, code := runSpawnCLI(t, home, fakeDir, "help"); code != 0 {
		t.Fatalf("help bootstrap failed")
	}
	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"status", "--claude-instance-id", "nonexistent")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0")
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotFound" {
		t.Errorf("err_name = %q; want ErrSpawnNotFound", env.ErrName)
	}
}

func TestStatusCLIMissingFlag(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	_, stderr, code := runSpawnCLI(t, home, fakeDir, "status")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0")
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
	}
}

// TestSpawnCLITmuxSessionNameHappyPath pins SR-1.1 + SR-3.1 + SR-4.1
// end-to-end: --tmux-session-name <name> reaches the persisted row
// verbatim and the fake-tmux log shows the same name on `-s`.
func TestSpawnCLITmuxSessionNameHappyPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	cwd := t.TempDir()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"spawn", "--cwd", cwd, "--tmux-session-name", "bot-claude-status")
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, stderr)
	}
	var res spawnResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	getOut, _, code := runSpawnCLI(t, home, fakeDir,
		"get", "--claude-instance-id", res.ClaudeInstanceID)
	if code != 0 {
		t.Fatalf("get exit = %d", code)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(getOut), &row); err != nil {
		t.Fatalf("parse get %q: %v", getOut, err)
	}
	if row["tmux_session_name"] != "bot-claude-status" {
		t.Errorf("get.tmux_session_name = %v; want bot-claude-status", row["tmux_session_name"])
	}
	logBytes, _ := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if !strings.Contains(string(logBytes), "\nbot-claude-status\n") {
		t.Errorf("fake-tmux log missing the user-supplied session name: %s", logBytes)
	}
}

// TestSpawnCLITmuxSessionNameOmittedDefaults pins SR-1.1: bare-omit of
// --tmux-session-name keeps today's composeSessionName behavior.
func TestSpawnCLITmuxSessionNameOmittedDefaults(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	cwd := t.TempDir()
	stdout, _, code := runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", cwd)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var res spawnResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse stdout: %v", err)
	}
	getOut, _, _ := runSpawnCLI(t, home, fakeDir,
		"get", "--claude-instance-id", res.ClaudeInstanceID)
	var row map[string]any
	if err := json.Unmarshal([]byte(getOut), &row); err != nil {
		t.Fatalf("parse get: %v", err)
	}
	name, _ := row["tmux_session_name"].(string)
	// <basename(cwd)>-<id[:8]>. cwd basename is the tempdir's leaf
	// (varies per run); assert via shape regex.
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+-[0-9a-f]{8}$`).MatchString(name) {
		t.Errorf("tmux_session_name = %q; want <basename>-<id[:8]> shape", name)
	}
}

// TestSpawnCLITmuxSessionNameValidationFailures covers each new sentinel
// driven by parseEnvelope's err_name. Reserved char, control char,
// >64 bytes, explicit empty, non-UTF-8.
func TestSpawnCLITmuxSessionNameValidationFailures(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cwd := t.TempDir()
	cases := []struct {
		name    string
		value   string
		argEq   bool // pass as --tmux-session-name=<value> to allow explicit empty
		wantErr string
	}{
		{"reserved colon", "bad:name", false, "ErrTmuxSessionNameInvalid"},
		{"reserved dot", "bad.name", false, "ErrTmuxSessionNameInvalid"},
		{"reserved hash", "bad#name", false, "ErrTmuxSessionNameInvalid"},
		{"control SOH", "bad\x01name", false, "ErrTmuxSessionNameInvalid"},
		// NUL (\x00) cannot be passed through exec on linux; unit test
		// covers that branch (TestValidateTmuxSessionName).
		{"control DEL", "bad\x7fname", false, "ErrTmuxSessionNameInvalid"},
		{"non-UTF-8", string([]byte{0xff, 0xfe, 0x80}), false, "ErrTmuxSessionNameInvalid"},
		{"too long", strings.Repeat("a", 65), false, "ErrTmuxSessionNameTooLong"},
		{"explicit empty", "", true, "ErrTmuxSessionNameEmpty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			var args []string
			if tc.argEq {
				args = []string{"spawn", "--cwd", cwd, "--tmux-session-name="}
			} else {
				args = []string{"spawn", "--cwd", cwd, "--tmux-session-name", tc.value}
			}
			_, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			if code == 0 {
				t.Fatalf("expected non-zero exit; stderr=%q", stderr)
			}
			env := parseEnvelope(t, stderr)
			if env.ErrName != tc.wantErr {
				t.Errorf("err_name = %q; want %q (stderr=%q)", env.ErrName, tc.wantErr, stderr)
			}
		})
	}
}

// TestSpawnCLITmuxSessionNameLiveCollision pins SR-2.4 + SR-4.1: when
// tmux refuses new-session because the name is already live, the wrapped
// tmux error surfaces — NO ErrTmuxSessionNameTaken sentinel. The
// fake-tmux fixture is configured via FAKE_TMUX_FAIL_NEWSESSION_NAME so
// no real tmux is needed.
func TestSpawnCLITmuxSessionNameLiveCollision(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	cwd := t.TempDir()
	_, stderr, code := runSpawnCLIEnv(t, home, fakeDir,
		map[string]string{"FAKE_TMUX_FAIL_NEWSESSION_NAME": "bot-claude-status"},
		"spawn", "--cwd", cwd, "--tmux-session-name", "bot-claude-status")
	if code == 0 {
		t.Fatalf("expected non-zero exit; stderr=%q", stderr)
	}
	if strings.Contains(stderr, "ErrTmuxSessionNameTaken") {
		t.Errorf("collision must not surface ErrTmuxSessionNameTaken: %q", stderr)
	}
	env := parseEnvelope(t, lastJSONLine(stderr))
	// The wrapped tmux error is classified via the existing tmux
	// sentinel catalog — pin the exact name so a future re-classification
	// surfaces here.
	if env.ErrName != "ErrTmuxSessionCreate" {
		t.Errorf("err_name = %q; want ErrTmuxSessionCreate (wrapped tmux error)", env.ErrName)
	}
}

// lastJSONLine returns the last non-empty line of s that begins with `{`.
// Used to skip soft warning lines (e.g. pre-trust skipped) that the spawn
// path may emit on stderr ahead of the JSON envelope.
func lastJSONLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		if strings.HasPrefix(ln, "{") {
			return ln
		}
	}
	return s
}

// TestSpawnCLICwdMissing covers the bare-required-flag case: no --cwd
// at all → ErrCwdMissing (NOT ErrInvalidFlags — the empty-string is
// passed through to Validate which produces the typed error).
func TestSpawnCLICwdMissing(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	_, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0")
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrCwdMissing" {
		t.Errorf("err_name = %q; want ErrCwdMissing", env.ErrName)
	}
}

// TestGetCLICheckPermissionOpenRow pins SR-8.3 case 1: spawn at
// check_permission with an open permission_requests row → `get`
// response carries a populated permission_request sub-object with all
// four documented fields. tool_input must round-trip as the raw JSON
// string byte-for-byte (no parse/re-emit per req-review m2).
func TestGetCLICheckPermissionOpenRow(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")

	const id = "id-gp-1"
	const toolName = "Read"
	const toolInput = `{"file":"/tmp/x","mode":"rw"}`
	seedSpawnRow(t, dbPath, id, "cd-gp-1", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, id, toolName, toolInput)

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"get", "--claude-instance-id", id)
	if code != 0 {
		t.Fatalf("get exit = %d; stderr=%s", code, stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	pr, ok := m["permission_request"].(map[string]any)
	if !ok {
		t.Fatalf("permission_request missing or not an object: %v (raw=%s)", m["permission_request"], stdout)
	}
	if got, _ := pr["tool_name"].(string); got != toolName {
		t.Errorf("tool_name = %v; want %q", pr["tool_name"], toolName)
	}
	if got, _ := pr["tool_input"].(string); got != toolInput {
		t.Errorf("tool_input = %v; want %q (raw JSON string, no parse/re-emit)", pr["tool_input"], toolInput)
	}
	if _, ok := pr["requested_at"].(string); !ok {
		t.Errorf("requested_at missing or not a string: %v", pr["requested_at"])
	}
	// request_id JSON-unmarshals to float64 for any[]; just assert non-zero.
	if rid, _ := pr["request_id"].(float64); rid == 0 {
		t.Errorf("request_id = %v; want non-zero", pr["request_id"])
	}
}

// TestGetCLICheckPermissionNoRow pins SR-8.3 case 2: spawn at
// check_permission with NO permission_requests row → `get` response
// omits the permission_request key entirely. Also pins SR-8.5 +
// req-review nit n1: key absence is asserted via map unmarshal, not
// substring match. A future regression that emits
// `"permission_request": null` would FAIL this test (null unmarshals
// to a present key with a nil value, so `_, ok := m["..."]; ok` is
// true).
func TestGetCLICheckPermissionNoRowOmitsField(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")

	const id = "id-gp-2"
	seedSpawnRow(t, dbPath, id, "cd-gp-2", "check_permission", "on")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"get", "--claude-instance-id", id)
	if code != 0 {
		t.Fatalf("get exit = %d; stderr=%s", code, stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if _, present := m["permission_request"]; present {
		t.Errorf("permission_request key present in JSON; want absent (omitempty must drop a nil pointer, not emit null). raw=%s", stdout)
	}
}

// TestGetCLICheckPermissionDecidedRowOmitsField pins SR-8.3 case 3 +
// req-review MAJOR M1: even though the spawn is at check_permission
// and a permission_requests row exists, a non-empty decision means
// the row was decided in a prior cycle and the verb MUST treat it as
// absent. If api.Get is regressed to gate only on sql.ErrNoRows, this
// test fails.
func TestGetCLICheckPermissionDecidedRowOmitsField(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")

	const id = "id-gp-3"
	seedSpawnRow(t, dbPath, id, "cd-gp-3", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, id, "Bash", `{"cmd":"ls"}`)
	markPermissionRequestDecided(t, dbPath, id, "allow")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"get", "--claude-instance-id", id)
	if code != 0 {
		t.Fatalf("get exit = %d; stderr=%s", code, stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if _, present := m["permission_request"]; present {
		t.Errorf("permission_request key present despite decided row; want absent (M1: gate on pr.Decision == \"\"). raw=%s", stdout)
	}
}

// TestGetCLINonCheckPermissionStateOmitsField pins SR-8.3 case 4: when
// state != check_permission and no permission row exists, the existing
// SpawnRow assertions still hold and permission_request is absent.
func TestGetCLINonCheckPermissionStateOmitsField(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")

	const id = "id-gp-4"
	seedSpawnRow(t, dbPath, id, "cd-gp-4", "waiting", "on")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"get", "--claude-instance-id", id)
	if code != 0 {
		t.Fatalf("get exit = %d; stderr=%s", code, stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if _, present := m["permission_request"]; present {
		t.Errorf("permission_request key present for state=waiting; want absent. raw=%s", stdout)
	}
	// Existing SpawnRow assertions still pass.
	if m["state"] != "waiting" {
		t.Errorf("state = %v; want waiting", m["state"])
	}
	if m["tmux_session_name"] != "cd-gp-4" {
		t.Errorf("tmux_session_name = %v; want cd-gp-4", m["tmux_session_name"])
	}
}

// TestGetCLINonCheckPermissionStateWithStaleRowOmitsField pins SR-8.3
// case 5: even when an open permission_requests row coincidentally
// exists (e.g. residue from a prior cycle), the verb MUST gate on
// STATE, not on row presence. If api.Get regresses to call
// GetPermissionRequest unconditionally, this test fails because the
// stale row would surface.
func TestGetCLINonCheckPermissionStateWithStaleRowOmitsField(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")

	const id = "id-gp-5"
	seedSpawnRow(t, dbPath, id, "cd-gp-5", "waiting", "on")
	seedOpenPermissionRequest(t, dbPath, id, "Read", `{"file":"/etc/hosts"}`)

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"get", "--claude-instance-id", id)
	if code != 0 {
		t.Fatalf("get exit = %d; stderr=%s", code, stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if _, present := m["permission_request"]; present {
		t.Errorf("permission_request key present for state=waiting with stale open row; want absent (verb gates on state, not row presence). raw=%s", stdout)
	}
}
