package spawn

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// composeContextStatuslines preserves each selected user command and its display
// options. Claude starts the shell command with exec, retaining the native
// direct parent for the capacity-only hook. No settings file is written.
// If settings cannot be resolved safely, leave the native settings untouched.
func composeContextStatuslines(r Resolved, exe string, top map[string]any) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	configDir, explicit := r.ExtraEnv["CLAUDE_CONFIG_DIR"]
	if !explicit {
		configDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if configDir == "" {
		configDir = filepath.Join(home, ".claude")
	}
	root := r.CWD
	explicitRoot := false
	sources := map[string]bool{"user": true, "project": true, "local": true}
	for _, o := range ReadClaudeArgs(r.AgentArgs).Options {
		if len(o.Values) == 0 {
			continue
		}
		switch o.Canonical {
		case "--project-config-root":
			root = o.Values[len(o.Values)-1]
			explicitRoot = true
		case "--setting-sources":
			sources = map[string]bool{}
			for _, name := range strings.Split(o.Values[len(o.Values)-1], ",") {
				sources[name] = true
			}
		}
	}
	if !filepath.IsAbs(configDir) || !filepath.IsAbs(root) {
		return
	}
	// Native local settings can canonicalize to an ancestor Git root. Until
	// that configuration is qualified, don't override its native callbacks.
	_, rootGitErr := os.Stat(filepath.Join(root, ".git"))
	for dir := filepath.Dir(root); !explicitRoot && rootGitErr != nil && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.local.json")); err == nil && sources["local"] {
				return
			}
			break
		}
	}
	selected := map[string]map[string]any{}
	for _, source := range []struct{ name, path string }{{"user", filepath.Join(configDir, "settings.json")}, {"project", filepath.Join(root, ".claude", "settings.json")}, {"local", filepath.Join(root, ".claude", "settings.local.json")}} {
		if !sources[source.name] {
			continue
		}
		data, err := os.ReadFile(source.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || len(data) > 1<<20 {
			return
		}
		var settings map[string]json.RawMessage
		if json.Unmarshal(data, &settings) != nil {
			return
		}
		for _, key := range []string{"statusLine", "subagentStatusLine"} {
			value, exists := settings[key]
			if !exists {
				continue
			}
			var fields map[string]any
			if json.Unmarshal(value, &fields) != nil || fields == nil {
				return
			}
			if selected[key] == nil {
				selected[key] = map[string]any{}
			}
			for k, v := range fields {
				selected[key][k] = v
			}
		}
	}
	for _, key := range []string{"statusLine", "subagentStatusLine"} {
		fields := selected[key]
		if fields == nil {
			fields = map[string]any{"type": "command"}
		}
		forward := ""
		if command, ok := fields["command"]; ok {
			var valid bool
			forward, valid = command.(string)
			if !valid {
				continue
			}
		}
		command := "exec " + statusShellQuote(exe) + " hook --context-source " + key
		if forward != "" {
			command += " --context-forward " + base64.RawStdEncoding.EncodeToString([]byte(forward))
		}
		fields["type"], fields["command"] = "command", command
		top[key] = fields
	}
}

func statusShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
