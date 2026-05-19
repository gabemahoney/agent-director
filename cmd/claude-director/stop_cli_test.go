package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKillCLIHappyPath(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")
	seedSpawnRow(t, dbPath, "id-kill-1", "cd-kill-1", "waiting", "off")

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir,
		"kill", "--claude-instance-id", "id-kill-1")
	if code != 0 {
		t.Fatalf("kill exit = %d; stderr=%s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "{}" {
		t.Errorf("stdout = %q; want \"{}\"", stdout)
	}

	logBytes, err := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if err != nil {
		t.Fatalf("read fake-tmux log: %v", err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "kill-session") {
		t.Errorf("fake-tmux log missing kill-session: %s", log)
	}
	if !strings.Contains(log, "cd-kill-1") {
		t.Errorf("fake-tmux log missing target session: %s", log)
	}
}

func TestKillCLIErrSpawnNotFound(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"kill", "--claude-instance-id", "absent")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotFound" {
		t.Errorf("err_name = %q; want ErrSpawnNotFound", env.ErrName)
	}
}

func TestKillCLIEndedRowIsNoop(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".claude-director", "state.db")
	seedSpawnRow(t, dbPath, "id-kill-2", "cd-kill-2", "ended", "off")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"kill", "--claude-instance-id", "id-kill-2")
	if code != 0 {
		t.Fatalf("kill on ended must succeed; exit=%d stderr=%s", code, stderr)
	}
	logBytes, _ := os.ReadFile(filepath.Join(home, "fake-tmux.log"))
	if strings.Contains(string(logBytes), "kill-session") {
		t.Errorf("ended row should NOT trigger tmux kill-session: %s", string(logBytes))
	}
}

func TestKillCLIMissingInstanceID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir, "kill")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
	}
}
