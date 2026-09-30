package spawn

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// Exec-form hooks (SR-22.9, SR-20.6) and the launch argv of at least two
// elements (SR-3.8): the hook's parent is the Claude process itself.

// execFormEvents is the eight events every spawn registers; relay events
// carry matcher "*" and the inner relay timeout.
var execFormEvents = map[string]bool{
	"SessionStart": false, "UserPromptSubmit": false, "PreToolUse": true, "PostToolUse": false,
	"Stop": false, "Notification": false, "SessionEnd": false, "PermissionRequest": true,
}

// assertExecFormSettings checks settingsJSON: each event has exactly one
// exec-form agent-director hook for exe; SessionStart also carries the
// shell-form help entry wantHelp when it is non-empty.
func assertExecFormSettings(t *testing.T, settingsJSON, exe, wantHelp string, cfg config.Config) {
	t.Helper()
	if strings.Contains(settingsJSON, ` hook"`) {
		t.Errorf("settings contain a shell-form %q suffix: %s", " hook", settingsJSON)
	}
	var top struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(settingsJSON), &top); err != nil {
		t.Fatalf("Unmarshal settings: %v\n%s", err, settingsJSON)
	}
	var gotEvents []string
	for evt := range top.Hooks {
		gotEvents = append(gotEvents, evt)
	}
	sort.Strings(gotEvents)
	var wantEvents []string
	for evt := range execFormEvents {
		wantEvents = append(wantEvents, evt)
	}
	sort.Strings(wantEvents)
	if !slices.Equal(gotEvents, wantEvents) {
		t.Errorf("hook events = %v; want %v", gotEvents, wantEvents)
	}

	timeout := float64(cfg.Relay.EffectiveTimeoutSeconds())
	for evt, relay := range execFormEvents {
		var wantMatcher any
		if relay {
			wantMatcher = "*"
		}
		var ours, help []map[string]any
		for _, entry := range top.Hooks[evt] {
			inner, _ := entry["hooks"].([]any)
			if len(inner) != 1 {
				t.Errorf("%s: entry %v has %d inner commands; want 1", evt, entry, len(inner))
				continue
			}
			cmd, _ := inner[0].(map[string]any)
			if cmd["command"] == exe {
				ours = append(ours, cmd)
				if entry["matcher"] != wantMatcher {
					t.Errorf("%s: matcher = %v; want %v", evt, entry["matcher"], wantMatcher)
				}
			} else {
				help = append(help, cmd)
			}
		}
		if len(ours) != 1 {
			t.Errorf("%s: %d agent-director hooks with command %q; want exactly 1 (entries %v)", evt, len(ours), exe, top.Hooks[evt])
			continue
		}
		want := map[string]any{"type": "command", "command": exe, "args": []any{"hook"}}
		if relay {
			want["timeout"] = timeout
		}
		if !reflect.DeepEqual(ours[0], want) {
			t.Errorf("%s: hook = %v; want exec form %v", evt, ours[0], want)
		}

		var wantOthers []map[string]any
		if evt == "SessionStart" && wantHelp != "" {
			wantOthers = []map[string]any{{"type": "command", "command": wantHelp}}
		}
		if !reflect.DeepEqual(help, wantOthers) {
			t.Errorf("%s: other hooks = %v; want %v (help entry stays shell form)", evt, help, wantOthers)
		}
	}
}

// TestHookExecFormSettings: every agent-director hook is exec form with the
// path verbatim (never quoted); the help entry stays shell form.
func TestHookExecFormSettings(t *testing.T) {
	cases := []struct {
		name, exe, helpBin, wantHelp string
		injectHelp                   bool
	}{
		{name: "plain path", exe: "/opt/ad/bin/agent-director"},
		{name: "path with a space is verbatim", exe: "/opt/with space/agent-director"},
		{name: "help entry shell form", exe: "/opt/ad/bin/agent-director", injectHelp: true,
			helpBin: "/home/op/.agent-director/bin/agent-director", wantHelp: "/home/op/.agent-director/bin/agent-director help"},
		{name: "help entry quoted, hooks verbatim", exe: "/opt/with space/agent-director", injectHelp: true,
			helpBin: "/opt/help dir/agent-director", wantHelp: `"/opt/help dir/agent-director" help`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStubExe(t, tc.exe)
			withStubHelpBin(t, tc.helpBin)
			cfg := config.Default()
			cfg.Defaults.InjectHelpHook = tc.injectHelp
			got, err := synthesizeSettings(Resolved{SpawnParams: SpawnParams{ClaudeInstanceID: "id-exec"}}, cfg)
			if err != nil {
				t.Fatalf("synthesizeSettings: %v", err)
			}
			assertExecFormSettings(t, got, tc.exe, tc.wantHelp, cfg)
		})
	}
}

// TestHookExecFormLaunchArgv: a spawn's and a resume relaunch's create argv
// has at least two elements, starts with the Claude binary and carries
// exec-form settings.
func TestHookExecFormLaunchArgv(t *testing.T) {
	cases := []struct {
		name   string
		create func(t *testing.T) ([]string, config.Config)
	}{
		{name: "spawn", create: func(t *testing.T) ([]string, config.Config) {
			e := newLaunchEnv(t)
			e.r.ClaudeArgs = nil
			e.mustLaunch()
			return e.onlyCreate().Command, e.cfg
		}},
		{name: "resume relaunch", create: func(t *testing.T) ([]string, config.Config) {
			e := newRelaunchEnv(t)
			e.row.ClaudeArgs = nil
			if out := e.relaunch("session-exec-1"); out.Kind != CreateLabelled {
				t.Fatalf("Relaunch kind = %v (cause %v); want CreateLabelled", out.Kind, out.Cause)
			}
			return e.onlyCreate().Command, e.cfg
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv, cfg := tc.create(t)
			if len(argv) < 2 || argv[0] != claudeBinary {
				t.Fatalf("create argv = %v; want at least 2 elements starting with %q", argv, claudeBinary)
			}
			i := slices.Index(argv, "--settings")
			if i < 0 || i+1 >= len(argv) {
				t.Fatalf("create argv = %v; want --settings <json>", argv)
			}
			assertExecFormSettings(t, argv[i+1], "/bin/agent-director", "", cfg)
		})
	}
}
