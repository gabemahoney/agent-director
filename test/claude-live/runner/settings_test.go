package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/google/uuid"
)

func TestClaudeLiveInstallLifecycle(t *testing.T) {
	kit := lifecycleKit(t)
	tools := t.TempDir()
	// Only this stand-in can answer Claude calls, including install.sh's version
	// summary. Any attempt to start a provider exits unsuccessfully.
	claude := filepath.Join(tools, "claude")
	putFixture(t, claude, []byte(`#!/bin/sh
case "$*" in
  'auth status --json') printf '%s\n' '{"loggedIn":true,"subscriptionType":"test","account":"synthetic-auth-secret"}' ;;
  '--version') printf '%s\n' '0.0.0 (synthetic Claude fixture)' ;;
  *) exit 91 ;;
esac
`))
	if err := os.Chmod(claude, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

	t.Run("install-launch", func(t *testing.T) {
		// b.1jw: SQLite's default backup mode must not depend on a private umask.
		// Umask is process-wide; keep this test serial and restore it on return.
		previousUmask := syscall.Umask(0022)
		defer syscall.Umask(previousUmask)
		scope := t.TempDir()
		r := scopedRunner(t, scope, kit, "install")
		cfg := filepath.Join(r.Home, ".config/agent-director")
		settingsPath := filepath.Join(r.Home, ".claude/settings.json")
		initial := []byte(`{"permissions":{"allow":["Read"],"deny":["Bash"]},"env":{"PRIVATE_TEST":"synthetic-settings-secret"},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"user-hook"}]}]}}`)
		putFixture(t, settingsPath, initial)
		putFixture(t, filepath.Join(cfg, "config.toml"), []byte("# retained installation\n"))
		dbPath, old := seedRunner(t, r, "ended", true)
		before, err := Observe(context.Background(), dbPath, old.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		backup := filepath.Join(scope, "backup")
		assertBackup := func() {
			t.Helper()
			st, err := os.Stat(filepath.Join(backup, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if got := st.Mode().Perm(); got != 0600 {
				t.Fatalf("backup store mode = %04o, want 0600", got)
			}
			if got := string(readFixture(t, filepath.Join(backup, "install/config.toml"))); got != "# retained installation\n" {
				t.Fatalf("original installation backup: %q", got)
			}
			saved, err := Observe(context.Background(), filepath.Join(backup, "state.db"), old.InstanceID)
			if err != nil || !reflect.DeepEqual(saved.Tables, before.Tables) {
				t.Fatalf("consistent backup missing or changed: %v", err)
			}
			if st, err := os.Stat(backup); err != nil || st.Mode().Perm() != 0700 {
				t.Fatalf("backup is not private: %v", err)
			}
		}
		installed := false
		r.Commands.Execute = func(ctx context.Context, argv, environment []string, out, errout io.Writer) (int, error) {
			if argv[0] == "bash" {
				want := []string{"bash", filepath.Join(kit, "skills/install-agent-director/install.sh"), "--binary", filepath.Join(kit, "payload/agent-director-linux-"+runtime.GOARCH), "--admin-binary", filepath.Join(kit, "payload/agent-director-admin-linux-"+runtime.GOARCH), "--no-hooks", "--keep-prior", "--symlink-dir", filepath.Join(r.Home, ".local/bin")}
				if !reflect.DeepEqual(argv, want) {
					t.Fatalf("unsupported installer argv: %q", argv)
				}
				assertBackup() // At entry, before any installer command can mutate state.
				proof := readJSON[map[string]any](t, filepath.Join(r.Evidence.Root, "raw/backup.json"))
				if proof["path"] != backup || !r.Manifest.RecordingHook.Installed {
					t.Fatal("installer started without backup evidence and recorder")
				}
				registered := readFixture(t, settingsPath)
				if !bytes.Contains(registered, []byte(r.Manifest.RunID)) || !bytes.Contains(registered, []byte("user-hook")) {
					t.Fatal("recorder or unrelated hook absent at install")
				}
				code, err := Exec(ctx, argv, environment, out, errout)
				if !bytes.Equal(registered, readFixture(t, settingsPath)) {
					t.Fatal("supported installation changed recorder or user settings")
				}
				installed = code == 0 && err == nil
				return code, err
			}
			return Exec(ctx, argv, environment, out, errout)
		}
		v, err := r.ExecutePhase(context.Background(), func(ctx context.Context, run *Run) error {
			return run.Install(ctx, backup, false, true)
		}, nil)
		if err != nil || v.Status != "pass" || !installed {
			t.Fatalf("install phase: %+v %v", v, err)
		}
		if !equalJSON(initial, readFixture(t, settingsPath)) || !r.Manifest.RecordingHook.Removed {
			t.Fatal("install cleanup did not remove only its recorder")
		}
		if _, err := os.Stat(r.runtimeDir); !os.IsNotExist(err) {
			t.Fatalf("completed install retained runtime: %v", err)
		}
		assertBackup()

		launch := scopedRunner(t, scope, kit, "launch")
		t.Setenv("TMUX", launch.Socket+",0,0")
		if filepath.Dir(launch.Socket) != launch.runtimeDir || filepath.Dir(launch.runtimeDir) != scope || strings.HasPrefix(launch.Socket, launch.Evidence.Root+string(os.PathSeparator)) {
			t.Fatal("constructor did not isolate runtime from evidence")
		}
		if st, err := os.Stat(launch.runtimeDir); err != nil || st.Mode().Perm() != 0700 {
			t.Fatalf("constructor runtime is not private: %v", err)
		}
		rec := tmuxfix.NewRecorder()
		id := "live-" + launch.Manifest.RunID + "-" + uuid.NewString()
		launch.Manifest.Owned = []Owned{{InstanceID: id, CWD: launch.Home, Socket: launch.Socket, Started: launch.Now()}}
		launch.Commands.Execute = func(ctx context.Context, argv, environment []string, out, errout io.Writer) (int, error) {
			if !reflect.DeepEqual(environment, launch.Commands.Environment) {
				t.Fatal("launch lost its private command environment")
			}
			if argv[1] == "spawn" {
				want := []string{filepath.Join(cfg, "bin/agent-director"), "spawn", "--agent", "claude", "--instance-id", id, "--cwd", launch.Home, "--no-pre-trust"}
				if !reflect.DeepEqual(argv, want) {
					t.Fatalf("controlled launch argv: %q", argv)
				}
				s, err := store.Open(dbPath)
				if err != nil {
					return 1, err
				}
				defer s.Close()
				// Exercise real launch composition/store writes with the shared tmux
				// and process fixtures; no provider or session process is started.
				_, _, err = spawn.Launch(s, rec, procfix.New(), spawn.Resolved{SpawnParams: spawn.SpawnParams{
					Agent: store.AgentClaude, InstanceID: id, TmuxSessionName: id, CWD: launch.Home, RelayMode: "off", NoPreTrust: true,
				}}, false, config.Default(), launch.Now, nil)
				if err != nil {
					return 1, err
				}
				return 0, json.NewEncoder(out).Encode(map[string]string{"instance_id": id})
			}
			want := []string{filepath.Join(cfg, "bin/agent-director"), argv[1], "--instance-id", id}
			if argv[1] == "read-pane" {
				want = append(want, "--n-lines", "80")
			}
			if !reflect.DeepEqual(argv, want) || (argv[1] != "read-pane" && argv[1] != "get" && argv[1] != "kill") {
				t.Fatalf("cleanup escaped owned launch: %q", argv)
			}
			return 0, nil // Successful command answers alone cannot establish exit.
		}
		v, err = launch.ExecutePhase(context.Background(), func(ctx context.Context, run *Run) error {
			if err := run.StartRecorder([]string{"SessionStart", "FileChanged", "PostToolBatch", "UserPromptSubmit", "Stop"}); err != nil {
				return err
			}
			registered := readFixture(t, settingsPath)
			if _, _, err := run.AD(ctx, "controlled-spawn", "spawn", "--agent", "claude", "--instance-id", id, "--cwd", run.Home, "--no-pre-trust"); err != nil {
				return err
			}
			creates := rec.SocketCallsOf(tmux.CallCreate)
			if len(creates) != 1 || creates[0].Socket != run.Socket || len(creates[0].Command) < 3 || creates[0].Command[1] != "--settings" {
				return fmt.Errorf("controlled Claude launch did not compose its settings: %+v", creates)
			}
			if !bytes.Equal(registered, readFixture(t, settingsPath)) {
				t.Fatal("launch changed recorder or unrelated settings")
			}
			obj := readJSON[map[string]json.RawMessage](t, settingsPath)
			obj["userEdit"] = json.RawMessage(`true`)
			b, _ := json.Marshal(obj)
			putFixture(t, settingsPath, b)
			return nil
		}, nil)
		if err != nil || v.Status != "not_completed" || launch.Manifest.Owned[0].Exited {
			t.Fatalf("unverified fixture process exit passed: %+v %v", v, err)
		}
		found := false
		for _, result := range v.Results {
			found = found || result.SignalID == "runtime-cleanup" && result.Status == "not_completed"
		}
		if _, err := os.Stat(launch.runtimeDir); err != nil || !found {
			t.Fatalf("unverified exit lost private runtime: %v", err)
		}
		var want map[string]json.RawMessage
		if err := json.Unmarshal(initial, &want); err != nil {
			t.Fatal(err)
		}
		want["userEdit"] = json.RawMessage(`true`)
		wantJSON, _ := json.Marshal(want)
		if !equalJSON(wantJSON, readFixture(t, settingsPath)) || !launch.Manifest.RecordingHook.Removed {
			t.Fatal("launch cleanup removed unrelated settings or kept its recorder")
		}
		after, err := Observe(context.Background(), dbPath, old.InstanceID)
		if err != nil || !reflect.DeepEqual(before.Tables, after.Tables) {
			t.Fatalf("install/launch/cleanup changed unrelated row: %v", err)
		}
		assertBackup()
		for _, run := range []*Run{r, launch} {
			if err := filepath.WalkDir(run.Evidence.Root, func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					b := readFixture(t, path)
					for _, secret := range []string{"synthetic-auth-secret", "synthetic-settings-secret"} {
						if bytes.Contains(b, []byte(secret)) {
							t.Errorf("exported %s contains private canary", path)
						}
					}
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := os.Stat(filepath.Join(kit, "kit.json")); err != nil {
			t.Fatalf("candidate removed: %v", err)
		}
	})

	for _, scenario := range []string{"backup-outside", "backup-evidence", "backup-existing", "backup-link", "recorder-link", "recorder-owned"} {
		t.Run(scenario, func(t *testing.T) {
			scope := t.TempDir()
			r := scopedRunner(t, scope, kit, "install")
			backup := filepath.Join(scope, "backup")
			foreign := filepath.Join(t.TempDir(), "settings.json")
			putFixture(t, foreign, []byte(`{"retained":true}`))
			switch scenario {
			case "backup-outside":
				backup = filepath.Join(t.TempDir(), "backup")
			case "backup-evidence":
				backup = r.Evidence.Root
			case "backup-existing":
				putFixture(t, filepath.Join(backup, "retained"), []byte("private"))
			case "backup-link":
				if err := os.Symlink(filepath.Dir(foreign), backup); err != nil {
					t.Fatal(err)
				}
			case "recorder-link":
				if err := os.Symlink(filepath.Dir(foreign), filepath.Join(r.Home, ".claude")); err != nil {
					t.Fatal(err)
				}
			case "recorder-owned":
				owner, err := installRecorderAt(scope, r.Home, filepath.Join(kit, "bin/claude-live"), r.Evidence.Root, "other-run", []string{"Stop"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := owner.Remove(); err != nil {
						t.Error(err)
					}
				})
			}
			invoked := false
			r.Commands.Execute = func(ctx context.Context, argv, environment []string, out, errout io.Writer) (int, error) {
				if argv[0] == "bash" {
					invoked = true
					return 1, fmt.Errorf("installer reached after unsafe setup")
				}
				return Exec(ctx, argv, environment, out, errout)
			}
			v, err := r.ExecutePhase(context.Background(), func(ctx context.Context, run *Run) error {
				return run.Install(ctx, backup, false, true)
			}, nil)
			if err != nil || v.Status != "not_completed" || invoked {
				t.Fatalf("unsafe lifecycle was not refused: %+v %v; installer=%v", v, err, invoked)
			}
			if string(readFixture(t, foreign)) != `{"retained":true}` {
				t.Fatal("refusal changed foreign settings")
			}
			if scenario == "backup-existing" && string(readFixture(t, filepath.Join(backup, "retained"))) != "private" {
				t.Fatal("refusal changed existing backup")
			}
		})
	}
}

func TestClaudeLiveScopedConstructorKeepsPublicConfinement(t *testing.T) {
	scope := t.TempDir()
	r := scopedRunner(t, scope, "", "setup")
	if err := ValidateHome(r.Home); err == nil {
		t.Fatal("public home validation accepted temporary root")
	}
	if _, err := NewRun(r.Home, "", filepath.Join(scope, "public-evidence"), "install", 0); err == nil {
		t.Fatal("public constructor accepted temporary root")
	}
	literal := &Run{Home: r.Home}
	if err := literal.Preflight(context.Background()); err == nil {
		t.Fatal("literal Run opted into temporary-root seam")
	}
	for _, scenario := range []string{"foreign-config", "nested-evidence", "symlink-home"} {
		t.Run(scenario, func(t *testing.T) {
			home, evidence := r.Home, filepath.Join(scope, scenario)
			switch scenario {
			case "foreign-config":
				t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			case "nested-evidence":
				evidence = filepath.Join(r.Home, "evidence")
			case "symlink-home":
				if err := os.Rename(home, home+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(home+"-original", home); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := newRunAt(scope, home, "", evidence, "install", 0); err == nil {
				t.Fatal("unsafe scoped constructor accepted")
			}
			if _, err := os.Lstat(evidence); !os.IsNotExist(err) {
				t.Fatalf("refused constructor created evidence: %v", err)
			}
		})
	}
}

func TestClaudeLiveSettingsLifecyclePreservesUserState(t *testing.T) {
	for _, initial := range []string{"", `{}`, `{"permissions":{"allow":["Read"],"deny":["Bash"]},"env":{"PRIVATE_TEST":"synthetic-secret-canary"},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"user-hook"}]}]}}`} {
		t.Run(initial, func(t *testing.T) {
			r, _ := runnerFixture(t)
			path := filepath.Join(r.Home, ".claude/settings.json")
			if initial != "" {
				putFixture(t, path, []byte(initial))
			}
			s, err := installRecorderFile(path, "/synthetic/recorder", r.Evidence.Root, r.Manifest.RunID, []string{"Stop", "SessionStart"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			obj := readJSON[map[string]json.RawMessage](t, path)
			var hooks map[string][]json.RawMessage
			if err := json.Unmarshal(obj["hooks"], &hooks); err != nil {
				t.Fatal(err)
			}
			for _, event := range []string{"Stop", "SessionStart"} {
				var entry struct {
					Hooks []struct {
						Type, Command string
						Args          []string
						Timeout       int
					}
				}
				if err := json.Unmarshal(hooks[event][len(hooks[event])-1], &entry); err != nil {
					t.Fatal(err)
				}
				if len(entry.Hooks) != 1 || entry.Hooks[0].Type != "command" || entry.Hooks[0].Command != "/synthetic/recorder" || entry.Hooks[0].Timeout != 3 || !reflect.DeepEqual(entry.Hooks[0].Args, []string{"record-hook", "--evidence", r.Evidence.Root, "--run-id", r.Manifest.RunID, "--event", event}) {
					t.Fatalf("registration changed: %+v", entry)
				}
			}
			ledger := readFixture(t, path+".claude-live-ledger")
			if bytes.Contains(ledger, []byte("synthetic-secret-canary")) {
				t.Fatal("private settings copied to recorder ledger")
			}
			// Another writer edits settings after setup; cleanup must retain it.
			obj["userEdit"] = json.RawMessage(`{"retained":true}`)
			hooks["Stop"] = append(hooks["Stop"], json.RawMessage(`{"hooks":[{"type":"command","command":"later-user-hook"}]}`))
			obj["hooks"], _ = json.Marshal(hooks)
			updated, _ := json.Marshal(obj)
			putFixture(t, path, updated)
			if err := s.Remove(); err != nil {
				t.Fatal(err)
			}
			want := map[string]json.RawMessage{}
			if initial != "" {
				if err := json.Unmarshal([]byte(initial), &want); err != nil {
					t.Fatal(err)
				}
			}
			want["userEdit"] = obj["userEdit"]
			var kept map[string][]json.RawMessage
			if want["hooks"] == nil {
				kept = map[string][]json.RawMessage{}
			} else if err := json.Unmarshal(want["hooks"], &kept); err != nil {
				t.Fatal(err)
			}
			kept["Stop"] = append(kept["Stop"], hooks["Stop"][len(hooks["Stop"])-1])
			want["hooks"], _ = json.Marshal(kept)
			wantJSON, _ := json.Marshal(want)
			if !equalJSON(readFixture(t, path), wantJSON) {
				t.Fatalf("cleanup lost unrelated settings: %s", readFixture(t, path))
			}
			if _, err := os.Stat(path + ".claude-live-ledger"); !os.IsNotExist(err) {
				t.Fatalf("ledger retained after clean removal: %v", err)
			}
			v, err := r.ExecutePhase(context.Background(), func(context.Context, *Run) error { return nil }, nil)
			if err != nil || v.Status != "pass" {
				t.Fatalf("finalization: %+v %v", v, err)
			}
			if err := filepath.WalkDir(r.Evidence.Root, func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && bytes.Contains(readFixture(t, path), []byte("synthetic-secret-canary")) {
					t.Error("settings secret exported")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClaudeLiveSettingsAbsentAndOwnershipRefusal(t *testing.T) {
	r, _ := runnerFixture(t)
	path := filepath.Join(r.Home, ".claude/settings.json")
	s, err := installRecorderFile(path, "/synthetic/recorder", r.Evidence.Root, r.Manifest.RunID, []string{"Stop"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	before := readFixture(t, path)
	if other, err := installRecorderFile(path, "/synthetic/other", r.Evidence.Root, "other", []string{"Stop"}); err == nil {
		other.Close()
		t.Fatal("competing recorder acquired ownership")
	}
	if !bytes.Equal(before, readFixture(t, path)) {
		t.Fatal("refused setup changed settings")
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("originally absent settings remains: %v", err)
	}
	// Literal condition-clear follow: releasing the owner permits another run.
	s, err = installRecorderFile(path, "/synthetic/recorder", r.Evidence.Root, "next", []string{"Stop"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.Close() // Interrupted run leaves a private recovery ledger.
	if other, err := installRecorderFile(path, "/synthetic/other", r.Evidence.Root, "other", []string{"Stop"}); err == nil {
		other.Close()
		t.Fatal("stale ledger did not refuse a new run")
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeLiveSettingsRefusesChangedRegistration(t *testing.T) {
	r, _ := runnerFixture(t)
	path := filepath.Join(r.Home, ".claude/settings.json")
	s, err := installRecorderFile(path, "/synthetic/recorder", r.Evidence.Root, r.Manifest.RunID, []string{"Stop"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	putFixture(t, path, []byte(`{"hooks":{"Stop":[]},"user":"retained"}`))
	before := readFixture(t, path)
	r.Settings = s
	v, err := r.ExecutePhase(context.Background(), func(context.Context, *Run) error { return nil }, nil)
	if err != nil || v.Status != "not_completed" {
		t.Fatalf("unresolved settings cleanup passed: %+v %v", v, err)
	}
	if !bytes.Equal(before, readFixture(t, path)) {
		t.Fatal("failed cleanup changed user state")
	}
	if _, err := os.Stat(path + ".claude-live-ledger"); err != nil {
		t.Fatalf("lost recovery ledger: %v", err)
	}
}

func TestClaudeLiveSettingsRejectsUnsafeLiveHome(t *testing.T) {
	r, _ := runnerFixture(t)
	if s, err := InstallRecorder(r.Home, "/synthetic/recorder", r.Evidence.Root, r.Manifest.RunID, []string{"Stop"}); err == nil {
		s.Close()
		t.Fatal("live entry point accepted arbitrary HOME")
	}
	if err := r.Install(context.Background(), filepath.Join(t.TempDir(), "backup"), false, false); err == nil {
		t.Fatal("install accepted missing maintenance assertion")
	}
}
