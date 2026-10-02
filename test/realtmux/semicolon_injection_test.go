package realtmux_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Bug b.ukw: tmux splits its argv into commands at every element ending in
// ";", even after "--". A caller-supplied element ending in ";" (a claude
// argument, a session name, a cwd, an -e value) must reach tmux as written:
// it must neither start a tmux command of the caller's choosing nor break the
// create. Spawn and resume run through the production pkg/api client on real
// tmux.

// argvClaude puts first on PATH a stand-in claude that writes its argv
// (NUL-separated) to the returned file, then execs sleep.
func argvClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > '" + file + "'\nexec sleep 3600\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write argv-recording claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return file
}

// injections are the tmux commands that follow "x;" in the claude args: each
// would run on the caller's server if "x;" ended the create.
var injections = []struct {
	name string
	cmd  func(marker string) []string
}{
	{"kill-server", func(string) []string { return []string{"kill-server"} }},
	{"run-shell", func(marker string) []string { return []string{"run-shell", "touch " + marker} }},
}

// injectionFix is the spawn fixture with the argv-recording claude, a
// bystander session on the socket spawn resolves, and the file an injected
// run-shell would create.
type injectionFix struct {
	*spawnFix
	ArgvFile  string
	Bystander rawSession
	Marker    string
}

// newInjectionFix sets PATH before starting the bystander, so the server's
// panes find the argv-recording claude.
func newInjectionFix(t *testing.T) *injectionFix {
	t.Helper()
	f := newSpawnFix(t)
	argv := argvClaude(t)
	return &injectionFix{spawnFix: f, ArgvFile: argv, Bystander: f.startSession(t, ""),
		Marker: filepath.Join(t.TempDir(), "injected")}
}

// assertNoSecondCommand checks no injected command ran: the bystander session
// and its server still run, and the run-shell marker does not exist.
func (f *injectionFix) assertNoSecondCommand(t *testing.T) {
	t.Helper()
	if !f.hasSession(t, f.Bystander.ID) || pidGone(f.Bystander.ServerPID) {
		t.Errorf("bystander session %s on server pid %d is gone (%s): an injected kill-server ran",
			f.Bystander.ID, f.Bystander.ServerPID, procState(f.Bystander.ServerPID))
	}
	if _, err := os.Stat(f.Marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s: %v; want no such file (an injected run-shell ran)", f.Marker, err)
	}
}

// assertAgentArgv checks the launch is on the bystander's server and the
// agent's argv is prefix, the --settings value, then args exactly.
func (f *injectionFix) assertAgentArgv(t *testing.T, s spawned, prefix, args []string) {
	t.Helper()
	if srv := s.rt.formatInt(t, s.SessionID, "#{pid}"); srv != f.Bystander.ServerPID {
		t.Errorf("session %s is on server pid %d, want the bystander's %d", s.SessionID, srv, f.Bystander.ServerPID)
	}
	waitExeced(t, s.rt.formatInt(t, s.Row.PaneID.(string), "#{pane_pid}"), "sleep")
	data, err := os.ReadFile(f.ArgvFile)
	if err != nil {
		t.Fatalf("read the stand-in claude's argv: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	if len(got) <= len(prefix) {
		t.Fatalf("claude argv = %q, want %q, the settings, then %q", got, prefix, args)
	}
	if want := slices.Concat(prefix, got[len(prefix):len(prefix)+1], args); !slices.Equal(got, want) {
		t.Errorf("claude argv = %q, want %q", got, want)
	}
}

// TestSpawnClaudeArgsEndingInSemicolonRunNoTmuxCommand spawns with claude
// args "x;" then a tmux command: the create runs no second command and the
// agent receives the args literally.
func TestSpawnClaudeArgsEndingInSemicolonRunNoTmuxCommand(t *testing.T) {
	for _, tc := range injections {
		t.Run(tc.name, func(t *testing.T) {
			f := newInjectionFix(t)
			args := append([]string{"x;"}, tc.cmd(f.Marker)...)
			id, err := f.spawnWith(api.SpawnParams{ClaudeArgs: args})
			f.assertNoSecondCommand(t)
			if err != nil {
				t.Fatalf("spawn: %s", describe(err))
			}
			f.assertAgentArgv(t, f.launched(t, id), []string{"--settings"}, args)
		})
	}
}

// TestResumeClaudeArgsEndingInSemicolonRunNoTmuxCommand resumes an ended row
// whose stored claude args are "x;" then a tmux command: the relaunch runs no
// second command and the agent receives the args literally.
func TestResumeClaudeArgsEndingInSemicolonRunNoTmuxCommand(t *testing.T) {
	for _, tc := range injections {
		t.Run(tc.name, func(t *testing.T) {
			f := newInjectionFix(t)
			args := append([]string{"x;"}, tc.cmd(f.Marker)...)
			id, sessionID := f.seedResumable(t, args)
			_, err := f.API.Resume(api.ResumeParams{ClaudeInstanceID: id})
			f.assertNoSecondCommand(t)
			if err != nil {
				t.Fatalf("resume: %s", describe(err))
			}
			f.assertAgentArgv(t, f.launched(t, id), []string{"--resume", sessionID, "--settings"}, args)
		})
	}
}

// seedResumable seeds an ended row in f.CWD with args as its stored claude
// args, the socket spawn resolves, the pre-trust opt-out and a transcript on
// disk; it returns the row's id and session id.
func (f *injectionFix) seedResumable(t *testing.T, args []string) (id, sessionID string) {
	t.Helper()
	id, sessionID = newInstanceID("resume-semi"), newInstanceID("sess")
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode claude args: %v", err)
	}
	if _, err := apitest.SeedSpawn(f.DBPath, id, store.StateEnded, f.CWD, "", sessionID, false,
		apitest.WithRawClaudeArgs(string(encoded)), apitest.WithTmuxSocket(f.Socket), apitest.WithNoPreTrust(),
		apitest.WithJsonlPath(apitest.SeedJsonl(t, f.CWD, sessionID))); err != nil {
		t.Fatalf("SeedSpawn %s: %v", id, err)
	}
	return id, sessionID
}

// TestSpawnValuesEndingInSemicolonAreLiteral spawns with a session name, a
// cwd, an extra env value or an instance id ending in ";": the spawn succeeds,
// labelled as usual, and tmux and the pane hold each value as written.
func TestSpawnValuesEndingInSemicolonAreLiteral(t *testing.T) {
	cases := []struct {
		name   string
		params func(t *testing.T) api.SpawnParams
	}{
		{"session name", func(*testing.T) api.SpawnParams {
			return api.SpawnParams{TmuxSessionName: uniqueName() + ";", TmuxSessionNameSupplied: true}
		}},
		{"cwd", func(t *testing.T) api.SpawnParams {
			dir := filepath.Join(t.TempDir(), "d;")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatalf("make cwd: %v", err)
			}
			return api.SpawnParams{CWD: dir}
		}},
		{"extra env value", func(*testing.T) api.SpawnParams {
			return api.SpawnParams{ExtraEnv: map[string]string{"AD_SEMI": "v;"}}
		}},
		{"instance id", func(*testing.T) api.SpawnParams {
			return api.SpawnParams{ClaudeInstanceID: newInstanceID("semi") + ";"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFix(t)
			p := tc.params(t)
			s := f.mustLaunchWith(t, p)
			f.assertSpawned(t, s, f.Socket, 1) // label, instance id env and name as recorded
			if p.TmuxSessionNameSupplied && s.Row.TmuxSessionName != p.TmuxSessionName {
				t.Errorf("tmux_session_name = %v, want %q", s.Row.TmuxSessionName, p.TmuxSessionName)
			}
			panePID := s.rt.formatInt(t, s.Row.PaneID.(string), "#{pane_pid}")
			wantCwd := p.CWD
			if wantCwd == "" {
				wantCwd = f.CWD
			}
			wantCwd, _ = filepath.EvalSymlinks(wantCwd)
			if got, err := os.Readlink("/proc/" + strconv.Itoa(panePID) + "/cwd"); err != nil || got != wantCwd {
				t.Errorf("pane cwd = %q (%v), want %q", got, err, wantCwd)
			}
			env := procEnviron(t, panePID)
			for k, v := range p.ExtraEnv {
				if got, ok := envValue(env, k); got != v {
					t.Errorf("pane %s = %q (set %v), want %q", k, got, ok, v)
				}
			}
		})
	}
}
