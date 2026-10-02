package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// hookSpec is one hook of a settings-layer fixture; timeout nil is none.
type hookSpec struct {
	event, command string
	timeout        any
}

// layerJSON builds a settings layer with an optional env block.
func layerJSON(env map[string]any, hooks ...hookSpec) string {
	doc := map[string]any{}
	if env != nil {
		doc["env"] = env
	}
	byEvent := map[string][]any{}
	for _, h := range hooks {
		entry := map[string]any{"type": "command", "command": h.command}
		if h.timeout != nil {
			entry["timeout"] = h.timeout
		}
		byEvent[h.event] = append(byEvent[h.event], map[string]any{"hooks": []any{entry}})
	}
	if len(byEvent) > 0 {
		doc["hooks"] = byEvent
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// layerSet is one agent's layer files in a temp tree, and agent-director's
// layer in a fake /proc.
type layerSet struct {
	home, cwd, procRoot, managed string
	generated                    bool
}

func newLayerSet(t *testing.T) *layerSet {
	t.Helper()
	dir := t.TempDir()
	return &layerSet{home: filepath.Join(dir, "home"), cwd: filepath.Join(dir, "cwd"),
		procRoot: filepath.Join(dir, "proc"), managed: filepath.Join(dir, "managed-settings.json")}
}

// put writes a layer: kind is managed, local, project or user.
func (s *layerSet) put(t *testing.T, kind, content string) {
	t.Helper()
	path := map[string]string{
		"managed": s.managed,
		"local":   filepath.Join(s.cwd, ".claude", "settings.local.json"),
		"project": filepath.Join(s.cwd, ".claude", "settings.json"),
		"user":    filepath.Join(s.home, ".claude", "settings.json"),
	}[kind]
	writeFile(t, path, content)
}

// cmdline writes pid 77's argv into the fake /proc.
func (s *layerSet) cmdline(t *testing.T, args ...string) {
	t.Helper()
	writeFile(t, filepath.Join(s.procRoot, "77", "cmdline"), strings.Join(args, "\x00")+"\x00")
}

// layers lists the agent's layers, with the managed layer moved into the tree.
func (s *layerSet) layers() []settingsLayer {
	l := agentLayers(s.home, s.cwd, s.generated, agentDirectorLayer(s.procRoot, 77))
	l[0].path = s.managed
	return l
}

// hookLines renders reported hooks as "layer:program:timeout".
func hookLines(br budgetReport) []string {
	out := []string{}
	for _, h := range br.SessionEndHooks {
		out = append(out, string(h.Layer)+":"+h.Program+":"+h.Timeout)
	}
	return out
}

func TestCaptureBudgets(t *testing.T) {
	adLayer := layerJSON(nil, hookSpec{"SessionEnd", "/usr/local/bin/agent-director", nil}, hookSpec{"SessionStart", "/usr/local/bin/agent-director", 600})
	budget := func(v string) map[string]any { return map[string]any{sessionEndBudgetEnv: v} }
	tests := []struct {
		name          string
		setup         func(t *testing.T, s *layerSet)
		spawn, cont   map[string]string
		hooks         []string
		effective     string
		warningAbouts []string
	}{
		{name: "no layers", hooks: []string{}, effective: "unset"},
		{name: "a SessionEnd hook without timeout", setup: func(t *testing.T, s *layerSet) {
			s.put(t, "user", layerJSON(nil, hookSpec{"SessionEnd", "notify", nil}))
		}, hooks: []string{"user:notify:default"}, effective: "unset"},
		{name: "a per-hook timeout", setup: func(t *testing.T, s *layerSet) {
			s.put(t, "project", layerJSON(nil, hookSpec{"SessionEnd", "cleanup --fast", 10}))
		}, hooks: []string{"project:cleanup:10 s"}, effective: "unset"},
		{name: "several layers, in precedence order", setup: func(t *testing.T, s *layerSet) {
			s.put(t, "user", layerJSON(nil, hookSpec{"SessionEnd", "u", 2}))
			s.put(t, "local", layerJSON(nil, hookSpec{"SessionEnd", "l", nil}))
			s.put(t, "managed", layerJSON(nil, hookSpec{"SessionEnd", "m", 5}))
		}, hooks: []string{"managed:m:5 s", "local:l:default", "user:u:2 s"}, effective: "unset"},
		{name: "other events ignored", setup: func(t *testing.T, s *layerSet) {
			s.put(t, "user", layerJSON(nil, hookSpec{"SessionStart", "s", 600}, hookSpec{"Stop", "x", 1}))
		}, hooks: []string{}, effective: "unset"},
		{name: "agent-director's --settings layer", setup: func(t *testing.T, s *layerSet) {
			s.cmdline(t, "claude", "--settings", adLayer)
		}, hooks: []string{"agent-director --settings:agent-director:default"}, effective: "unset"},
		{name: "agent-director's --settings= layer", setup: func(t *testing.T, s *layerSet) {
			s.cmdline(t, "claude", "--settings="+adLayer, "prompt")
		}, hooks: []string{"agent-director --settings:agent-director:default"}, effective: "unset"},
		{name: "agent-director's layer missing from argv", setup: func(t *testing.T, s *layerSet) {
			s.cmdline(t, "claude", "prompt")
		}, hooks: []string{}, effective: "unset", warningAbouts: []string{"agent-director --settings"}},
		{name: "the generated layer", setup: func(t *testing.T, s *layerSet) {
			s.generated = true
			s.put(t, "local", layerJSON(nil, hookSpec{"SessionEnd", "/usr/bin/sleep", 3}))
		}, hooks: []string{"generated (local):sleep:3 s"}, effective: "unset"},
		{name: "budget in the container environment", cont: map[string]string{sessionEndBudgetEnv: "3000"},
			hooks: []string{}, effective: "3000 (container environment)"},
		{name: "spawn extra env over the container", spawn: map[string]string{sessionEndBudgetEnv: "7000"},
			cont: map[string]string{sessionEndBudgetEnv: "3000"}, hooks: []string{}, effective: "7000 (spawn --extra-env)"},
		{name: "a layer env block over both", setup: func(t *testing.T, s *layerSet) {
			s.put(t, "user", layerJSON(budget("9000")))
		}, spawn: map[string]string{sessionEndBudgetEnv: "7000"}, cont: map[string]string{sessionEndBudgetEnv: "3000"},
			hooks: []string{}, effective: "9000 (user layer env)"},
		{name: "a malformed layer warns", setup: func(t *testing.T, s *layerSet) {
			s.put(t, "project", "{not json")
			s.put(t, "user", layerJSON(nil, hookSpec{"SessionEnd", "u", nil}))
		}, hooks: []string{"user:u:default"}, effective: "unset", warningAbouts: []string{"project layer"}},
		{name: "an unreadable layer warns", setup: func(t *testing.T, s *layerSet) {
			if err := os.MkdirAll(filepath.Join(s.home, ".claude", "settings.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, hooks: []string{}, effective: "unset", warningAbouts: []string{"user layer"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newLayerSet(t)
			s.cmdline(t, "claude", "--settings", "{}")
			if tc.setup != nil {
				tc.setup(t, s)
			}
			br := captureBudgets(s.layers(), tc.cont, tc.spawn, "2.1.285")
			if got := hookLines(br); !reflect.DeepEqual(got, tc.hooks) {
				t.Errorf("hooks %q, want %q", got, tc.hooks)
			}
			if br.EnvEffective != tc.effective {
				t.Errorf("effective %q, want %q", br.EnvEffective, tc.effective)
			}
			if len(br.Warnings) != len(tc.warningAbouts) {
				t.Fatalf("warnings %q", br.Warnings)
			}
			for i, w := range tc.warningAbouts {
				if !strings.Contains(br.Warnings[i], w) {
					t.Errorf("warning %q does not name %q", br.Warnings[i], w)
				}
			}
		})
	}
}

func TestBudgetsReportNamesNotValues(t *testing.T) {
	s := newLayerSet(t)
	s.cmdline(t, "claude", "--settings", "{}")
	s.put(t, "user", layerJSON(map[string]any{"API_TOKEN": "layer-env-secret", sessionEndBudgetEnv: 5000},
		hookSpec{"SessionEnd", "/opt/bin/notify --token=hook-arg-secret", nil}))
	br := captureBudgets(s.layers(), nil, nil, "2.1.285")
	if !reflect.DeepEqual(br.EnvKeys["user"], []string{"API_TOKEN", sessionEndBudgetEnv}) {
		t.Errorf("env keys %v", br.EnvKeys)
	}
	b, _ := json.Marshal(br)
	assertAbsent(t, "budget report", string(b), "layer-env-secret", "hook-arg-secret")
	if !strings.Contains(string(b), `"program":"notify"`) {
		t.Errorf("report %s", b)
	}
}

func TestMCPAndHookProgramChecks(t *testing.T) {
	dir := t.TempDir()
	mcp := filepath.Join(dir, "mcp.json")
	writeFile(t, mcp, `{"mcpServers": {
		"zeta": {"command": "/nonexistent/bin/zeta-server", "args": ["--key", "arg-secret"], "env": {"TOKEN": "mcp-env-secret"}},
		"alpha": {"command": "true"},
		"remote": {"type": "http", "url": "https://mcp.invalid"}}}`)
	names, err := mcpServerNames(mcp)
	if err != nil || !reflect.DeepEqual(names, []string{"alpha", "remote", "zeta"}) {
		t.Fatalf("names %v, %v", names, err)
	}
	notes, err := mcpMissingCommands(mcp, os.Getenv("PATH"))
	if err != nil || !reflect.DeepEqual(notes, []string{"MCP server zeta: command zeta-server not found in the container"}) {
		t.Errorf("mcp notes %q, %v", notes, err)
	}
	writeFile(t, filepath.Join(dir, "bad.json"), "{")
	if _, err := mcpServerNames(filepath.Join(dir, "bad.json")); err == nil {
		t.Error("a malformed MCP configuration gave no error")
	}

	s := newLayerSet(t)
	s.cmdline(t, "claude", "--settings", "{}")
	s.put(t, "project", `{"hooks": {
		"SessionEnd": [{"hooks": [{"type": "command", "command": "/nonexistent/bin/slow", "args": ["x"]}]}],
		"Stop": [{"hooks": [{"type": "command", "command": "true --ok"}, {"type": "command", "command": "missing-tool --secret-arg"}]}]}}`)
	got := hookProgramsMissing(s.layers(), os.Getenv("PATH"))
	want := []string{"project layer: SessionEnd hook program slow not found in the container",
		"project layer: Stop hook program missing-tool not found in the container"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hook notes\n got %q\nwant %q", got, want)
	}
}
