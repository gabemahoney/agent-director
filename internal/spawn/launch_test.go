package spawn

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/store"
)

// captureTmux is a TmuxClient test double — records the argv that
// Launch would have handed tmux, and returns a programmable error.
type captureTmux struct {
	got struct {
		name    string
		cwd     string
		envs    map[string]string
		command []string
		called  bool
	}
	err error
}

func (c *captureTmux) NewSession(name, cwd string, envs map[string]string, command []string) error {
	c.got.called = true
	c.got.name = name
	c.got.cwd = cwd
	c.got.envs = envs
	c.got.command = command
	return c.err
}

// newStoreAndLaunchInputs builds a Resolved that has already passed
// validation + defaults. Centralizing the boilerplate keeps each test
// focused on the behavior it pins.
func newStoreAndLaunchInputs(t *testing.T) (*store.Store, Resolved, config.Config) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cwd := t.TempDir()
	r := Resolved{SpawnParams: SpawnParams{
		CWD:              cwd,
		ClaudeInstanceID: "id-launch-1",
		TmuxSessionName:  "cd-launch-1",
		RelayMode:        "off",
		ClaudeArgs:       []string{"--model", "opus"},
		ClaudeDirectorLabels: map[string]string{
			"role": "worker",
		},
	}}
	return s, r, config.Default()
}

func TestLaunchInsertsPendingAndCallsTmux(t *testing.T) {
	withStubExe(t, "/bin/claude-director")
	s, r, cfg := newStoreAndLaunchInputs(t)
	tmux := &captureTmux{}

	id, err := Launch(s, tmux, r, cfg)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if id != "id-launch-1" {
		t.Errorf("Launch returned %q; want id-launch-1", id)
	}

	// Pending row written.
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if row.State != store.StatePending {
		t.Errorf("State = %q; want pending", row.State)
	}
	if row.CWD != r.CWD {
		t.Errorf("CWD = %q; want %q", row.CWD, r.CWD)
	}
	if row.TmuxSessionName != "cd-launch-1" {
		t.Errorf("TmuxSessionName = %q", row.TmuxSessionName)
	}
	if !reflect.DeepEqual(row.ClaudeArgs, []string{"--model", "opus"}) {
		t.Errorf("ClaudeArgs = %v", row.ClaudeArgs)
	}
	if row.Labels["role"] != "worker" {
		t.Errorf("Labels = %v", row.Labels)
	}

	// tmux call observed.
	if !tmux.got.called {
		t.Fatal("tmux.NewSession not called")
	}
	if tmux.got.name != "cd-launch-1" {
		t.Errorf("session name = %q", tmux.got.name)
	}
	if tmux.got.cwd != r.CWD {
		t.Errorf("cwd = %q; want %q", tmux.got.cwd, r.CWD)
	}
	if len(tmux.got.command) < 3 || tmux.got.command[0] != "claude" || tmux.got.command[1] != "--settings" {
		t.Errorf("command argv prefix = %v", tmux.got.command[:3])
	}
	if tmux.got.command[len(tmux.got.command)-2] != "--model" || tmux.got.command[len(tmux.got.command)-1] != "opus" {
		t.Errorf("user claude_args missing from command tail: %v", tmux.got.command)
	}

	// Env vars composed correctly.
	if tmux.got.envs["CLAUDE_DIRECTOR_INSTANCE_ID"] != "id-launch-1" {
		t.Errorf("env CLAUDE_DIRECTOR_INSTANCE_ID = %q", tmux.got.envs["CLAUDE_DIRECTOR_INSTANCE_ID"])
	}
	if tmux.got.envs["CLAUDE_DIRECTOR_RELAY_MODE"] != "off" {
		t.Errorf("env CLAUDE_DIRECTOR_RELAY_MODE = %q", tmux.got.envs["CLAUDE_DIRECTOR_RELAY_MODE"])
	}
	if tmux.got.envs["CLAUDE_DIRECTOR_LABEL_ROLE"] != "worker" {
		t.Errorf("env CLAUDE_DIRECTOR_LABEL_ROLE = %q", tmux.got.envs["CLAUDE_DIRECTOR_LABEL_ROLE"])
	}
}

func TestLaunchTmuxFailureLeavesRowPending(t *testing.T) {
	withStubExe(t, "/bin/claude-director")
	s, r, cfg := newStoreAndLaunchInputs(t)
	tmux := &captureTmux{err: errors.New("tmux: name collision")}

	_, err := Launch(s, tmux, r, cfg)
	if err == nil {
		t.Fatal("expected error from Launch when tmux fails")
	}

	row, getErr := s.GetSpawn("id-launch-1")
	if getErr != nil {
		t.Fatalf("GetSpawn: %v", getErr)
	}
	if row.State != store.StatePending {
		t.Errorf("row should remain pending after tmux failure; got %q", row.State)
	}
}

func TestLaunchSecondInsertSurfacesCollision(t *testing.T) {
	withStubExe(t, "/bin/claude-director")
	s, r, cfg := newStoreAndLaunchInputs(t)
	tmux := &captureTmux{}
	if _, err := Launch(s, tmux, r, cfg); err != nil {
		t.Fatalf("first Launch: %v", err)
	}
	tmux2 := &captureTmux{}
	_, err := Launch(s, tmux2, r, cfg)
	if !errors.Is(err, ErrInstanceIdCollision) {
		t.Fatalf("second Launch err = %v; want ErrInstanceIdCollision", err)
	}
	if tmux2.got.called {
		t.Error("tmux.NewSession should not be called on collision")
	}
}
