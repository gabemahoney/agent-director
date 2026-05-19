package main_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	cmd := exec.Command(binaryPath, args...)
	logPath := filepath.Join(home, "fake-tmux.log")
	cmd.Env = []string{
		"PATH=" + fakeTmuxDir + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"FAKE_TMUX_LOG=" + logPath,
	}
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
