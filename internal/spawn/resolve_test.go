package spawn_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
)

// withTemplates seeds TOML templates, by name, under a temp $HOME's
// .agent-director/templates/ for the duration of a test.
func withTemplates(t *testing.T, bodies map[string]string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".agent-director", "templates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range bodies {
		if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
			t.Fatalf("write template: %v", err)
		}
	}
}

// TestResolveMergesTemplate pins SRD §7.1's merge: per-call scalars replace
// the template's, maps merge with per-call winning, permission arrays
// concatenate (nil per-call keeps the template's), per-call claude_args
// replace wholesale (nil keeps the template's), and ReuseFinished is per-call
// only (SR-10.1). No template is a passthrough; a resolved value shares no
// state with the template.
func TestResolveMergesTemplate(t *testing.T) {
	withTemplates(t, map[string]string{"bare": `cwd = "/tmp"`, "base": `
cwd = "/tmp"
relay_mode = "off"
claude_args = ["--model", "opus"]

[labels]
project = "foo"
env = "dev"

[extra_env]
ANTHROPIC_API_KEY = "from-template"
EXTRA = "kept"

[permissions]
allow = ["A"]
deny = ["X"]
`})
	pureTemplate := spawn.SpawnParams{Template: "base", CWD: "/tmp", RelayMode: "off", ClaudeArgs: []string{"--model", "opus"},
		AgentDirectorLabels: map[string]string{"project": "foo", "env": "dev"},
		ExtraEnv:            map[string]string{"ANTHROPIC_API_KEY": "from-template", "EXTRA": "kept"},
		Permissions:         &spawn.Permissions{Allow: []string{"A"}, Deny: []string{"X"}}}
	cases := []struct {
		name string
		p    spawn.SpawnParams
		want spawn.SpawnParams
	}{
		{name: "no template is a passthrough",
			p:    spawn.SpawnParams{CWD: "/tmp", RelayMode: "off", ReuseFinished: true},
			want: spawn.SpawnParams{CWD: "/tmp", RelayMode: "off", ReuseFinished: true}},
		{name: "pure template", p: spawn.SpawnParams{Template: "base"}, want: pureTemplate},
		{name: "template with no maps or permissions", p: spawn.SpawnParams{Template: "bare"},
			want: spawn.SpawnParams{Template: "bare", CWD: "/tmp"}},
		{name: "per-call values layered on the template",
			p: spawn.SpawnParams{Template: "base", CWD: "/var/data", ClaudeArgs: []string{"--model", "sonnet"}, ReuseFinished: true,
				AgentDirectorLabels: map[string]string{"owner": "alice", "project": "bar"},
				ExtraEnv:            map[string]string{"ANTHROPIC_API_KEY": "from-call", "NEW": "added"},
				Permissions:         &spawn.Permissions{Allow: []string{"B"}, Ask: []string{"Q"}}},
			want: spawn.SpawnParams{Template: "base", CWD: "/var/data", RelayMode: "off", ClaudeArgs: []string{"--model", "sonnet"},
				ReuseFinished:       true,
				AgentDirectorLabels: map[string]string{"project": "bar", "env": "dev", "owner": "alice"},
				ExtraEnv:            map[string]string{"ANTHROPIC_API_KEY": "from-call", "EXTRA": "kept", "NEW": "added"},
				Permissions:         &spawn.Permissions{Allow: []string{"A", "B"}, Deny: []string{"X"}, Ask: []string{"Q"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := spawn.Resolve(tc.p, config.Default())
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !reflect.DeepEqual(r.SpawnParams, tc.want) {
				t.Fatalf("Resolve = %+v (permissions %+v)\nwant      %+v (permissions %+v)", r.SpawnParams, r.Permissions, tc.want, tc.want.Permissions)
			}
			if tc.name != "pure template" {
				return
			}
			r.AgentDirectorLabels["project"], r.ClaudeArgs[1], r.Permissions.Allow[0] = "corrupted", "corrupted", "corrupted"
			if again, err := spawn.Resolve(tc.p, config.Default()); err != nil || !reflect.DeepEqual(again.SpawnParams, tc.want) {
				t.Errorf("Resolve after changing the first result = %+v, %v; want %+v", again.SpawnParams, err, tc.want)
			}
		})
	}
}

func TestResolveTemplateNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := spawn.Resolve(spawn.SpawnParams{Template: "nope"}, config.Default()); !errors.Is(err, config.ErrTemplateNotFound) {
		t.Fatalf("err = %v; want ErrTemplateNotFound", err)
	}
}
