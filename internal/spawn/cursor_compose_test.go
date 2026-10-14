package spawn

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

func TestCursorFreshComposition(t *testing.T) {
	for _, path := range []string{"", "/opt/cursor with space/agent", `/opt/q"uote's/agent --flag`} {
		t.Run(path, func(t *testing.T) {
			e := newLaunchEnv(t)
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
		{"explicit prompt", []string{"--", "--looks-like-flag"}, nil, "--looks-like-flag", false},
		{"session", []string{"--resume", "s"}, nil, "", true},
		{"remote", []string{"--remote", "wss://x"}, nil, "", true},
		{"auth", []string{"--api-key", "synthetic"}, nil, "", true},
		{"mode", []string{"acp"}, nil, "", true},
		{"two prompts", []string{"one", "two"}, nil, "", true},
		{"empty model", []string{"--model="}, nil, "", true},
		{"invalid UTF8", []string{string([]byte{0xff})}, nil, "", true},
		{"NUL", []string{"x\x00y"}, nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, prompt, err := CursorArgs(tc.in)
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
