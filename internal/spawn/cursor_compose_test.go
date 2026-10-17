package spawn

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

func installCompositionGateway(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCursorFreshComposition(t *testing.T) {
	for _, path := range []string{"", "/opt/cursor with space/agent", `/opt/q"uote's/agent --flag`} {
		t.Run(path, func(t *testing.T) {
			e := newLaunchEnv(t)
			installCompositionGateway(t)
			e.r.Agent = store.AgentCursor
			e.r.AgentArgs = []string{"--model", "model-x", "first\n多行 🦋"}
			e.cfg.Launch.CursorPath = path
			c, err := ComposeLaunch(e.r, e.cfg)
			if err != nil {
				t.Fatal(err)
			}
			argv := c.Create.Command
			if len(argv) != 8 || argv[1] != "cursor-host" || argv[2] != "--runtime" {
				t.Fatalf("gateway command %q", argv)
			}
			want := path
			if want == "" {
				want = "agent"
			}
			if argv[3] != want {
				t.Fatalf("runtime path %q; want whole %q", argv[3], want)
			}
			var args []string
			if err = json.Unmarshal([]byte(argv[5]), &args); err != nil || !reflect.DeepEqual(args, []string{"--model", "model-x"}) {
				t.Fatalf("runtime args %q %v", args, err)
			}
			if argv[7] != "first\n多行 🦋" || c.Row.Agent != store.AgentCursor {
				t.Fatalf("prompt/agent %q %q", argv[7], c.Row.Agent)
			}
			if strings.Contains(strings.Join(argv, " "), "--settings") {
				t.Fatal("Claude settings in Cursor launch")
			}
		})
	}
}

func TestCursorInstalledGatewaySelection(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "missing", "nonexecutable", "dangling", "directory", "symlink nonexecutable"} {
		t.Run(kind, func(t *testing.T) {
			e := newLaunchEnv(t)
			path := installCompositionGateway(t)
			want := path
			if kind != "regular" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "symlink", "symlink nonexecutable":
					want = filepath.Join(t.TempDir(), "gateway target")
					mode := os.FileMode(0700)
					if kind == "symlink nonexecutable" {
						mode = 0600
					}
					if err := os.WriteFile(want, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(want, path); err != nil {
						t.Fatal(err)
					}
				case "nonexecutable":
					if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				case "dangling":
					if err := os.Symlink(path+"-missing", path); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			e.r.Agent = store.AgentCursor
			e.r.AgentArgs = nil
			before, _ := json.Marshal(e.r)
			c, err := ComposeLaunch(e.r, e.cfg)
			valid := kind == "regular" || kind == "symlink"
			if valid {
				if err != nil || c.Create.Command[0] != want {
					t.Fatalf("gateway=%+v err=%v want=%s", c.Create, err, want)
				}
			} else if err == nil {
				t.Fatal("unavailable installed gateway accepted")
			}
			if !valid {
				if _, _, err := e.launch(); err == nil {
					t.Fatal("fresh launch accepted unavailable gateway")
				}
				if row, err := e.s.GetSpawn(e.r.InstanceID); !errors.Is(err, store.ErrSpawnNotFound) || row.InstanceID != "" {
					t.Fatalf("fresh refusal wrote row: %+v %v", row, err)
				}
			}
			after, _ := json.Marshal(e.r)
			if string(before) != string(after) {
				t.Fatal("composition mutated inputs")
			}
			if len(e.rec.SocketCalls()) != 0 {
				t.Fatal("composition touched tmux")
			}
		})
	}
}
func TestCursorArgsSupportedAndRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in, want []string
		prompt   string
		refuse   bool
	}{
		{"no prompt", nil, nil, "", false},
		{"split model", []string{"-m", "x"}, []string{"--model", "x"}, "", false},
		{"equals model", []string{"--model=x", "hello"}, []string{"--model", "x"}, "hello", false},
		{"short equals opaque model", []string{"-m=account/模型:β", "first\n多行 🦋"}, []string{"--model", "account/模型:β"}, "first\n多行 🦋", false},
		{"repeated model", []string{"-m", "first", "--model=second"}, []string{"--model", "first", "--model", "second"}, "", false},
		{"literal command", []string{"--", "acp"}, nil, "acp", false},
		{"operand before empty delimiter", []string{"acp", "--"}, nil, "", true},
		{"mode and operand before empty delimiter", []string{"chat", "--mode", "plan", "--"}, nil, "", true},
		{"empty delimiter", []string{"--"}, nil, "", true},
		{"owned delimiter", []string{"--model", "--"}, nil, "", true},
		{"policy force", []string{"--force"}, nil, "", true},
		{"policy yolo", []string{"--yolo"}, nil, "", true},
		{"policy sandbox", []string{"--sandbox", "disabled"}, nil, "", true},
		{"policy trust", []string{"--trust"}, nil, "", true},
		{"unknown alias", []string{"-p"}, nil, "", true},
		{"explicit prompt", []string{"--", "--looks-like-flag"}, nil, "--looks-like-flag", false},
		{"session", []string{"--resume", "s"}, nil, "", true},
		{"resume picker", []string{"--resume"}, nil, "", true},
		{"continue", []string{"--continue"}, nil, "", true},
		{"resume command", []string{"resume"}, nil, "", true},
		{"hidden new session", []string{"--new-session-id", "new"}, nil, "", true},
		{"remote", []string{"--remote", "wss://x"}, nil, "", true},
		{"auth", []string{"--api-key", "synthetic"}, nil, "", true},
		{"mode", []string{"acp"}, nil, "", true},
		{"two prompts", []string{"one", "two"}, nil, "", true},
		{"empty model", []string{"--model="}, nil, "", true},
		{"invalid UTF8", []string{string([]byte{0xff})}, nil, "", true},
		{"NUL", []string{"x\x00y"}, nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.in)
			args, prompt, err := CursorArgs(tc.in)
			if !slices.Equal(tc.in, before) {
				t.Fatal("argument parsing mutated caller input")
			}
			if tc.refuse {
				if !errors.Is(err, ErrSpawnDeniedFlag) {
					t.Fatalf("expected refusal: %v", err)
				}
				return
			}
			if err != nil || prompt != tc.prompt || !reflect.DeepEqual(args, tc.want) {
				t.Fatalf("CursorArgs = %q %q %v", args, prompt, err)
			}
		})
	}
}

func TestCursorRequestedModeComposition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		mode   string
		refuse bool
	}{
		{"split agent", []string{"--mode", "agent"}, "agent", false},
		{"equals ask", []string{"--mode=ask"}, "ask", false},
		{"plan alias", []string{"--plan"}, "plan", false},
		{"same mode repeats", []string{"--plan", "--mode", "plan", "--mode=plan"}, "plan", false},
		{"conflict", []string{"--plan", "--mode", "ask"}, "", true},
		{"conflicting explicit modes", []string{"--mode=agent", "--mode=plan"}, "", true},
		{"missing", []string{"--mode"}, "", true},
		{"empty", []string{"--mode="}, "", true},
		{"unsupported", []string{"--mode", "sensitive-mode"}, "", true},
		{"alias value", []string{"--plan=true"}, "", true},
		{"owned selector", []string{"--mode", "--continue"}, "", true},
		{"owned delimiter", []string{"--mode", "--"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newLaunchEnv(t)
			installCompositionGateway(t)
			e.r.Agent = store.AgentCursor
			e.r.AgentArgs = append(slices.Clone(tc.args), "--model=account/模型:β", "--", "--resume\n多行 🦋")
			before := slices.Clone(e.r.AgentArgs)
			err := Validate(&e.r)
			if tc.refuse {
				if !errors.Is(err, ErrSpawnDeniedFlag) || strings.Contains(err.Error(), "sensitive-mode") {
					t.Fatalf("static mode refusal: %v", err)
				}
				if _, _, launchErr := e.launch(); !errors.Is(launchErr, ErrSpawnDeniedFlag) {
					t.Fatalf("launch refusal: %v", launchErr)
				}
				if _, rowErr := e.s.GetSpawn(e.r.InstanceID); !errors.Is(rowErr, store.ErrSpawnNotFound) || len(e.rec.SocketCalls()) != 0 {
					t.Fatalf("refusal mutated runtime/store: %v", rowErr)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				c, err := ComposeLaunch(e.r, e.cfg)
				want := []string{"cursor-host", "--runtime", "agent", "--runtime-args", `["--model","account/模型:β"]`, "--initial-prompt", "--resume\n多行 🦋", "--requested-mode", tc.mode}
				if err != nil || !slices.Equal(c.Create.Command[1:], want) {
					t.Fatalf("private mode composition=%q err=%v; want %q", c.Create.Command, err, want)
				}
			}
			if !slices.Equal(e.r.AgentArgs, before) {
				t.Fatal("mode validation changed saved/caller args")
			}
		})
	}
}

func TestCursorResumeComposition(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		args       []string
		prompt     string
		noPreTrust bool
	}{
		{"configured", `/opt/current cursor/agent 'quoted'`, []string{"--resume", "OLDER", "--model", "model-x", "saved\n多行 🦋"}, "saved\n多行 🦋", false},
		{"default path", "", []string{"--continue", "-m=model-x"}, "", false},
		{"opt out", "/opt/new/agent", []string{"resume", "--new-session-id=NEW", "--model=model-x", "--", "--resume"}, "--resume", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelaunchEnv(t)
			exe := installCompositionGateway(t)
			e.row.Agent = store.AgentCursor
			e.row.SessionID = "LATEST"
			e.row.JSONLPath = "/synthetic/protected/transcript.jsonl"
			e.row.AgentArgs = tc.args
			e.row.NoPreTrust = tc.noPreTrust
			e.row.CWD = "/saved/project with spaces"
			e.row.TmuxSessionName = `$saved\name`
			e.row.ExtraEnv = map[string]string{"CURSOR_GROUP": "saved-group", "SAVED_VALUE": "spaces and 'quotes'"}
			e.cfg.Launch.CursorPath = tc.path
			before, err := json.Marshal(e.row)
			if err != nil {
				t.Fatal(err)
			}
			c, err := ComposeRelaunch(RelaunchInput{Row: e.row, SessionID: "LATEST", Socket: e.socket, Token: relaunchToken, StoreID: relaunchStoreID}, e.cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := tc.path
			if path == "" {
				path = "agent"
			}
			want := []string{exe, "cursor-host", "--runtime", path, "--runtime-args", `["--model","model-x"]`, "--initial-prompt", tc.prompt, "--load-session", "LATEST"}
			if !slices.Equal(c.Create.Command, want) {
				t.Fatalf("command %q; want %q", c.Create.Command, want)
			}
			wantEnv := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": e.row.InstanceID, "AGENT_DIRECTOR_RELAY_MODE": e.row.RelayMode, "AGENT_DIRECTOR_AGENT": "cursor", "AGENT_DIRECTOR_LABEL_ROLE": "worker", "CURSOR_GROUP": "saved-group", "SAVED_VALUE": "spaces and 'quotes'"}
			if !reflect.DeepEqual(c.Create.Env, wantEnv) {
				t.Fatalf("env %v; want %v", c.Create.Env, wantEnv)
			}
			if c.Create.CWD != e.row.CWD || c.Create.Name != e.row.TmuxSessionName || c.Create.Socket != e.socket || c.Create.Token != relaunchToken || c.Create.StoreID != relaunchStoreID || c.Create.InstanceID != e.row.InstanceID {
				t.Fatalf("create identity/config %+v", c.Create)
			}
			wantTrust := PreTrustFailed
			if tc.noPreTrust {
				wantTrust = PreTrustSkipped
			}
			if c.PreTrust != wantTrust {
				t.Fatalf("pretrust %q; want %q", c.PreTrust, wantTrust)
			}
			after, err := json.Marshal(e.row)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("composition mutated saved row")
			}
			c.Create.Env["SAVED_VALUE"] = "changed"
			c.Create.Command[0] = "changed"
			if e.row.ExtraEnv["SAVED_VALUE"] != "spaces and 'quotes'" || !slices.Equal(e.row.AgentArgs, tc.args) {
				t.Fatal("composition aliases saved configuration")
			}
		})
	}
}

func TestCursorResumeRefusesUnavailableLatest(t *testing.T) {
	for _, tc := range []struct{ name, saved, target, transcript string }{
		{"missing latest", "", "", "/synthetic/transcript"},
		{"older target", "LATEST", "OLDER", "/synthetic/transcript"},
		{"missing protected path", "LATEST", "LATEST", ""},
		{"whitespace", " LATEST", " LATEST", "/synthetic/transcript"},
		{"newline", "LATEST\n", "LATEST\n", "/synthetic/transcript"},
		{"nul", "LATEST\x00", "LATEST\x00", "/synthetic/transcript"},
		{"oversize", strings.Repeat("s", 513), strings.Repeat("s", 513), "/synthetic/transcript"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelaunchEnv(t)
			e.row.Agent = store.AgentCursor
			e.row.SessionID, e.row.JSONLPath = tc.saved, tc.transcript
			before, _ := json.Marshal(e.row)
			got, err := ComposeRelaunch(RelaunchInput{Row: e.row, SessionID: tc.target}, e.cfg)
			if !errors.Is(err, ErrSpawnDeniedFlag) || !reflect.DeepEqual(got, ComposedRelaunch{}) {
				t.Fatalf("composition %+v, %v; want empty refusal", got, err)
			}
			after, _ := json.Marshal(e.row)
			if string(before) != string(after) {
				t.Fatal("refusal mutated saved row")
			}
		})
	}
}
func TestCursorValidationRefusesBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		perms     *Permissions
		inherited string
		want      error
	}{
		{"fresh", nil, nil, "", nil},
		{"allow", nil, &Permissions{Allow: []string{"Bash"}}, "", ErrPermissionsUnsupported},
		{"deny", nil, &Permissions{Deny: []string{"Bash"}}, "", ErrPermissionsUnsupported},
		{"ask", nil, &Permissions{Ask: []string{"Bash"}}, "", ErrPermissionsUnsupported},
		{"empty permissions", nil, &Permissions{}, "", nil},
		{"inherited ownership", []string{"--model", "x"}, nil, "claude", ErrAgentArgsMismatch},
		{"denied arg before permissions", []string{"--resume=x"}, &Permissions{Ask: []string{"Bash"}}, "", ErrSpawnDeniedFlag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: SpawnParams{Agent: store.AgentCursor, CWD: t.TempDir(), AgentArgs: tc.args, Permissions: tc.perms}, InheritedArgsAgent: tc.inherited}
			err := Validate(&r)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate = %v; want %v", err, tc.want)
			}
		})
	}
}
