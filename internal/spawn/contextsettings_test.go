package spawn

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

func TestClaudeContextStatuslineComposition(t *testing.T) {
	for _, name := range []string{"none", "user", "project overrides user", "local overrides project", "selected sources", "jsonc", "unresolved", "ancestor local", "inherited config", "explicit empty config", "explicit config override"} {
		t.Run(name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			exe := "/opt/a d/'agent-director"
			withStubExe(t, exe)
			r := Resolved{SpawnParams: SpawnParams{CWD: root}}
			files := map[string][]byte{}
			write := func(path, body string) {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				files[path] = []byte(body)
			}
			command := "printf '%s\\n' \"quotes ' and \\\" dollar $HOME\"\nexit 17"
			fields := map[string]any{"type": "command", "command": command, "padding": float64(3), "refreshInterval": float64(123)}
			b, err := json.Marshal(map[string]any{"statusLine": fields, "subagentStatusLine": fields})
			if err != nil {
				t.Fatal(err)
			}
			if name != "none" {
				write(filepath.Join(home, ".claude", "settings.json"), string(b))
			}
			var configCommands map[string]string
			if name == "inherited config" || name == "explicit empty config" || name == "explicit config override" {
				inherited, override := t.TempDir(), t.TempDir()
				t.Setenv("CLAUDE_CONFIG_DIR", inherited)
				selected := inherited
				if name == "explicit empty config" {
					r.ExtraEnv = map[string]string{"CLAUDE_CONFIG_DIR": ""}
					selected = filepath.Join(home, ".claude")
				} else if name == "explicit config override" {
					r.ExtraEnv = map[string]string{"CLAUDE_CONFIG_DIR": override}
					selected = override
				}
				for label, dir := range map[string]string{"home": filepath.Join(home, ".claude"), "inherited": inherited, "override": override} {
					settings := map[string]any{}
					commands := map[string]string{}
					for _, key := range []string{"statusLine", "subagentStatusLine"} {
						callback := map[string]any{}
						for k, v := range fields {
							callback[k] = v
						}
						commands[key] = label + " " + key + " " + command
						callback["command"] = commands[key]
						settings[key] = callback
					}
					data, err := json.Marshal(settings)
					if err != nil {
						t.Fatal(err)
					}
					write(filepath.Join(dir, "settings.json"), string(data))
					if dir == selected {
						configCommands = commands
					}
				}
			}
			if name == "project overrides user" || name == "local overrides project" {
				write(filepath.Join(root, ".claude", "settings.json"), `{"statusLine":{"command":"project command","padding":7}}`)
				command = "project command"
			}
			if name == "local overrides project" {
				write(filepath.Join(root, ".claude", "settings.local.json"), `{"statusLine":{"command":"local command"}}`)
				command = "local command"
			}
			if name == "selected sources" {
				r.AgentArgs = []string{"--setting-sources", "project"}
				command = ""
			}
			if name == "jsonc" {
				write(filepath.Join(home, ".claude", "settings.json"), "{ // native JSONC\n\"statusLine\":{}\n}")
			}
			if name == "unresolved" {
				r.CWD = "relative"
			}
			if name == "ancestor local" {
				if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(root, ".claude", "settings.local.json"), string(b))
				r.CWD = filepath.Join(root, "nested")
				if err := os.Mkdir(r.CWD, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var top map[string]any
			if err := json.Unmarshal([]byte(synthSettings(t, r, config.Default())), &top); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"statusLine", "subagentStatusLine"} {
				if name == "jsonc" || name == "unresolved" || name == "ancestor local" {
					if _, exists := top[key]; exists {
						t.Errorf("unsafe settings resolution overrode native %s", key)
					}
					continue
				}
				got, ok := top[key].(map[string]any)
				if !ok {
					t.Fatalf("missing %s: %+v", key, top)
				}
				wrapper, _ := got["command"].(string)
				if !strings.HasPrefix(wrapper, "exec '/opt/a d/'\"'\"'agent-director' hook --context-source "+key) {
					t.Errorf("callback does not retain native direct parent/quoted executable: %q", wrapper)
				}
				expected := command
				if key == "subagentStatusLine" && name != "none" && name != "selected sources" {
					expected = fields["command"].(string)
				}
				if configCommands != nil {
					expected = configCommands[key]
				}
				parts := strings.Split(wrapper, " --context-forward ")
				if name == "none" || name == "selected sources" {
					if len(parts) != 1 {
						t.Errorf("unexpected forward: %s", wrapper)
					}
				} else {
					if len(parts) != 2 {
						t.Fatalf("missing original command: %s", wrapper)
					}
					decoded, err := base64.RawStdEncoding.DecodeString(parts[1])
					if err != nil || string(decoded) != expected {
						t.Errorf("original command changed bytes: %q %v; want %q", decoded, err, expected)
					}
					wantPadding := float64(3)
					if key == "statusLine" && (name == "project overrides user" || name == "local overrides project") {
						wantPadding = 7
					}
					if got["padding"] != wantPadding || got["refreshInterval"] != float64(123) {
						t.Errorf("display settings changed: %+v", got)
					}
				}
			}
			for path, want := range files {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != string(want) {
					t.Fatalf("native settings modified: %s %v", path, err)
				}
			}
		})
	}
}
