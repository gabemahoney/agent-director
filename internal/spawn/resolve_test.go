package spawn_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// withTemplates seeds templates, by name, under a temp $HOME's
// .config/agent-director/templates/ for the duration of a test.
func withTemplates(t *testing.T, templates map[string]apitest.TemplateFields) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for name, fields := range templates {
		if _, err := apitest.SeedTemplate(filepath.Join(home, ".config", "agent-director", "templates"), name, apitest.TemplateBody(fields)); err != nil {
			t.Fatalf("SeedTemplate(%s): %v", name, err)
		}
	}
}

// TestResolveMergesTemplate pins SRD §7.1's merge: per-call scalars (the agent
// included) replace the template's, maps merge with per-call winning,
// permission arrays concatenate (nil per-call keeps the template's), per-call
// agent_args replace wholesale (nil keeps the template's), and ReuseFinished
// is per-call only (SR-10.1). No template is a passthrough; a resolved value
// shares no state with the template.
func TestResolveMergesTemplate(t *testing.T) {
	withTemplates(t, map[string]apitest.TemplateFields{
		"bare": {CWD: "/tmp"},
		"base": {CWD: "/tmp", RelayMode: "off", Agent: "claude", AgentArgs: []string{"--model", "opus"},
			Labels:      map[string]string{"project": "foo", "env": "dev"},
			ExtraEnv:    map[string]string{"ANTHROPIC_API_KEY": "from-template", "EXTRA": "kept"},
			Permissions: &config.TemplatePermissions{Allow: []string{"A"}, Deny: []string{"X"}}},
	})
	pureTemplate := spawn.SpawnParams{Template: "base", CWD: "/tmp", RelayMode: "off", Agent: "claude", AgentArgs: []string{"--model", "opus"},
		AgentDirectorLabels: map[string]string{"project": "foo", "env": "dev"},
		ExtraEnv:            map[string]string{"ANTHROPIC_API_KEY": "from-template", "EXTRA": "kept"},
		Permissions:         &spawn.Permissions{Allow: []string{"A"}, Deny: []string{"X"}}}
	cases := []struct {
		name string
		p    spawn.SpawnParams
		want spawn.SpawnParams
	}{
		{name: "no template is a passthrough",
			p:    spawn.SpawnParams{CWD: "/tmp", RelayMode: "off", Agent: "claude", ReuseFinished: true},
			want: spawn.SpawnParams{CWD: "/tmp", RelayMode: "off", Agent: "claude", ReuseFinished: true}},
		{name: "pure template", p: spawn.SpawnParams{Template: "base"}, want: pureTemplate},
		{name: "template with no maps or permissions", p: spawn.SpawnParams{Template: "bare"},
			want: spawn.SpawnParams{Template: "bare", CWD: "/tmp"}},
		{name: "per-call values layered on the template",
			p: spawn.SpawnParams{Template: "base", CWD: "/var/data", Agent: "codex", AgentArgs: []string{"--model", "sonnet"}, ReuseFinished: true,
				AgentDirectorLabels: map[string]string{"owner": "alice", "project": "bar"},
				ExtraEnv:            map[string]string{"ANTHROPIC_API_KEY": "from-call", "NEW": "added"},
				Permissions:         &spawn.Permissions{Allow: []string{"B"}, Ask: []string{"Q"}}},
			want: spawn.SpawnParams{Template: "base", CWD: "/var/data", RelayMode: "off", Agent: "codex", AgentArgs: []string{"--model", "sonnet"},
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
			r.AgentDirectorLabels["project"], r.AgentArgs[1], r.Permissions.Allow[0] = "corrupted", "corrupted", "corrupted"
			if again, err := spawn.Resolve(tc.p, config.Default()); err != nil || !reflect.DeepEqual(again.SpawnParams, tc.want) {
				t.Errorf("Resolve after changing the first result = %+v, %v; want %+v", again.SpawnParams, err, tc.want)
			}
		})
	}
}

// TestResolveAgentAndArgsOwnership pins SR-1.2 and SR-2.1: the per-call agent
// beats the template's, which beats none; the template's agent_args are
// inherited only through a nil per-call list (any supplied list, an empty one
// included, replaces them and is never refused for ownership), and Validate
// refuses inherited arguments of another agent with ErrAgentArgsMismatch.
func TestResolveAgentAndArgsOwnership(t *testing.T) {
	withTemplates(t, map[string]apitest.TemplateFields{
		"claude-args": {CWD: "/tmp", Agent: "claude", AgentArgs: []string{"--model", "opus"}},
		"claude-bare": {CWD: "/tmp", Agent: "claude"},
		"codex-args":  {CWD: "/tmp", Agent: "codex", AgentArgs: []string{"--profile", "x"}},
		"no-agent":    {CWD: "/tmp"},
	})
	type outcome struct {
		agent     string
		args      []string
		inherited string // Resolved.InheritedArgsAgent
		invalid   error  // Validate's verdict on the resolved call
	}
	cases := []struct {
		name string
		p    spawn.SpawnParams
		want outcome
	}{
		{"per-call agent, no template", spawn.SpawnParams{CWD: "/tmp", Agent: "claude"}, outcome{agent: "claude"}},
		{"neither names an agent", spawn.SpawnParams{CWD: "/tmp"}, outcome{invalid: spawn.ErrAgentMissing}},
		{"template names none, call names one", spawn.SpawnParams{Template: "no-agent", Agent: "codex"}, outcome{agent: "codex"}},
		{"neither the template nor the call names one", spawn.SpawnParams{Template: "no-agent"}, outcome{invalid: spawn.ErrAgentMissing}},
		{"the template's agent is used", spawn.SpawnParams{Template: "claude-bare"}, outcome{agent: "claude"}},
		{"per-call agent overrides the template's", spawn.SpawnParams{Template: "claude-bare", Agent: "codex"}, outcome{agent: "codex"}},
		{"per-call claude overrides a codex template, own list", spawn.SpawnParams{Template: "codex-args", Agent: "claude", AgentArgs: []string{"--model", "x"}},
			outcome{agent: "claude", args: []string{"--model", "x"}}},
		{"nil list inherits when the template's agent is the spawn's", spawn.SpawnParams{Template: "claude-args"},
			outcome{agent: "claude", args: []string{"--model", "opus"}, inherited: "claude"}},
		{"nil list inherits when the call names the same agent", spawn.SpawnParams{Template: "claude-args", Agent: "claude"},
			outcome{agent: "claude", args: []string{"--model", "opus"}, inherited: "claude"}},
		{"supplied list replaces, whatever the agent", spawn.SpawnParams{Template: "claude-args", Agent: "codex", AgentArgs: []string{"--profile", "y"}},
			outcome{agent: "codex", args: []string{"--profile", "y"}}},
		{"explicit empty list replaces and is not refused for ownership", spawn.SpawnParams{Template: "claude-args", Agent: "codex", AgentArgs: []string{}},
			outcome{agent: "codex", args: []string{}}},
		{"explicit empty list replaces for the same agent", spawn.SpawnParams{Template: "claude-args", AgentArgs: []string{}},
			outcome{agent: "claude", args: []string{}}},
		{"nil list of another agent's template is refused", spawn.SpawnParams{Template: "claude-args", Agent: "codex"},
			outcome{agent: "codex", args: []string{"--model", "opus"}, inherited: "claude", invalid: spawn.ErrAgentArgsMismatch}},
		{"nil list of a codex template is refused for claude", spawn.SpawnParams{Template: "codex-args", Agent: "claude"},
			outcome{agent: "claude", args: []string{"--profile", "x"}, inherited: "codex", invalid: spawn.ErrAgentArgsMismatch}},
		{"no arguments to inherit, any agent", spawn.SpawnParams{Template: "claude-bare", Agent: "codex"}, outcome{agent: "codex"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := spawn.Resolve(tc.p, config.Default())
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			got := outcome{agent: r.Agent, args: r.AgentArgs, inherited: r.InheritedArgsAgent, invalid: spawn.Validate(&r)}
			if got.agent != tc.want.agent || !reflect.DeepEqual(got.args, tc.want.args) || got.inherited != tc.want.inherited ||
				!errors.Is(got.invalid, tc.want.invalid) || (tc.want.invalid == nil) != (got.invalid == nil) {
				t.Errorf("Resolve + Validate = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// TestResolveCarriesDisableAskUser: the question-tool setting reaches Validate
// through Resolve (with or without a template), so a codex spawn's argument
// that turns the question tool back on is refused while the setting is on, and
// a claude spawn is not affected (SR-2.3, Q1).
func TestResolveCarriesDisableAskUser(t *testing.T) {
	withTemplates(t, map[string]apitest.TemplateFields{
		"codex-question": {CWD: "/tmp", Agent: "codex", AgentArgs: []string{"--enable", "default_mode_request_user_input"}},
	})
	enable := []string{"--enable", "default_mode_request_user_input"}
	cases := []struct {
		name string
		p    spawn.SpawnParams
		on   bool
		want error
	}{
		{"setting on refuses a codex call's enable", spawn.SpawnParams{CWD: "/tmp", Agent: "codex", AgentArgs: enable}, true, spawn.ErrSpawnDeniedFlag},
		{"setting off passes it", spawn.SpawnParams{CWD: "/tmp", Agent: "codex", AgentArgs: enable}, false, nil},
		{"setting on refuses an inherited template's enable", spawn.SpawnParams{Template: "codex-question"}, true, spawn.ErrSpawnDeniedFlag},
		{"setting off passes an inherited template's enable", spawn.SpawnParams{Template: "codex-question"}, false, nil},
		{"setting on does not touch claude", spawn.SpawnParams{CWD: "/tmp", Agent: "claude", AgentArgs: enable}, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Defaults.DisableAskUser = tc.on
			r, err := spawn.Resolve(tc.p, cfg)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got := spawn.Validate(&r); !errors.Is(got, tc.want) || (tc.want == nil) != (got == nil) {
				t.Errorf("Validate = %v; want %v", got, tc.want)
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

// Switching drops selectors only after template argument ownership and replacement resolve.
func TestResolveCursorSwitchSelectorsAndTemplateOwnership(t *testing.T) {
	withTemplates(t, map[string]apitest.TemplateFields{
		"cursor-select": {CWD: "/tmp", Agent: "cursor", AgentArgs: []string{"--resume", "old", "--model", "sonnet", "hello"}},
		"claude-select": {CWD: "/tmp", Agent: "claude", AgentArgs: []string{"--resume", "old"}},
	})
	for _, tc := range []struct {
		name, template    string
		args, want, drops []string
		refused           error
	}{
		{"inherit matching template", "cursor-select", nil, []string{"--model", "sonnet", "hello"}, []string{"--resume", "old"}, nil},
		{"explicit empty replaces foreign args", "claude-select", []string{}, []string{}, nil, nil},
		{"own list replaces foreign args", "claude-select", []string{"--continue", "hello"}, []string{"hello"}, []string{"--continue"}, nil},
		{"foreign inherited args refused before drop", "claude-select", nil, []string{"--resume", "old"}, nil, spawn.ErrAgentArgsMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := spawn.Resolve(spawn.SpawnParams{Template: tc.template, Agent: "cursor", AgentArgs: tc.args, ReuseFinished: true}, config.Default())
			if err != nil {
				t.Fatal(err)
			}
			if tc.args != nil && len(tc.args) == 0 && r.InheritedArgsAgent != "" {
				t.Fatal("explicit empty call inherited foreign template arguments")
			}
			r.DropResumeArgs = true
			err = spawn.Validate(&r)
			if !errors.Is(err, tc.refused) || (err == nil) != (tc.refused == nil) || !slices.Equal(r.AgentArgs, tc.want) || !slices.Equal(r.DroppedArgs, tc.drops) {
				t.Fatalf("resolved switch args=%q drops=%q error=%v; want %q / %q / %v", r.AgentArgs, r.DroppedArgs, err, tc.want, tc.drops, tc.refused)
			}
		})
	}
}
