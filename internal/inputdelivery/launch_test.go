package inputdelivery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPreparePinsDiagnosticsToFreshLaunch(t *testing.T) {
	// Serial: launch provisioning resolves HOME and configuration directories.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, agent := range []string{"codex", "claude"} {
		for _, mode := range []string{"supported", "unsupported version", "custom bindings", "unsafe args"} {
			t.Run(agent+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				version := "codex-cli 0.159.2"
				if agent == "claude" {
					version = "2.1.280 (Claude Code)"
				}
				if mode == "unsupported version" {
					version += "-future"
				}
				program := filepath.Join(root, "agent")
				if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0700); err != nil {
					t.Fatal(err)
				}
				configDir := filepath.Join(root, "config")
				if err := os.Mkdir(configDir, 0700); err != nil {
					t.Fatal(err)
				}
				if mode == "custom bindings" {
					writeEvidence(t, filepath.Join(configDir, "keybindings.json"), []byte("[]"))
				}
				a, err := NewAttempt(Identity{}, "token", nil)
				if err != nil {
					t.Fatal(err)
				}
				id := Identity{StoreID: "0123456789abcdef", Token: a.RequestID[:16], InstanceID: "launch", Agent: agent, Socket: "/own/socket", CWD: "/work/project"}
				env := map[string]string{"CLAUDE_CONFIG_DIR": configDir, "KEEP": "value"}
				if agent == "codex" {
					env["CODEX_HOME"] = configDir
					if mode == "custom bindings" {
						writeEvidence(t, filepath.Join(configDir, "config.toml"), []byte("[tui]\nvim_mode_default=true\n[tui.keymap.composer]\nsubmit=['x']\n[tui.keymap.editor]\ninsert_newline=['enter']\n"))
					}
				}
				originalEnvLen := len(env)
				cmd, gotEnv, err := Prepare(id, []string{program}, env, mode != "unsafe args")
				if err != nil {
					t.Fatal(err)
				}
				if len(env) != originalEnvLen || gotEnv["KEEP"] != "value" {
					t.Fatal("launch environment mutated")
				}
				dir, err := launchDir(id.StoreID, id.Token)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(dir, "launch.json"))
				if err != nil {
					t.Fatal(err)
				}
				var m manifest
				if err = json.Unmarshal(data, &m); err != nil {
					t.Fatal(err)
				}
				if m.Token != id.Token || m.InstanceID != id.InstanceID || m.CWD != id.CWD || m.Socket != id.Socket || m.Created.IsZero() {
					t.Fatalf("missing launch ownership: %+v", m)
				}
				joined := strings.Join(cmd, " ")
				if mode == "supported" || mode == "custom bindings" {
					if agent == "codex" {
						if !strings.Contains(joined, "log_dir=") || !strings.Contains(joined, dir) || gotEnv["RUST_LOG"] != "off,codex_tui=info" {
							t.Fatalf("unbound diagnostics: %q %+v", cmd, gotEnv)
						}
						var pinned map[string]any
						var overrides []string
						for i := 1; i+1 < len(cmd); i++ {
							if cmd[i] == "-c" {
								overrides = append(overrides, cmd[i+1])
							}
						}
						if _, err := toml.Decode(strings.Join(overrides, "\n"), &pinned); err != nil {
							t.Fatal(err)
						}
						assertPinnedCodexComposer(t, pinned)
						if !m.KeymapPinned {
							t.Fatal("launch did not record pinned composer ownership")
						}
					} else {
						if m.SessionID == "" || !strings.Contains(joined, "--session-id "+m.SessionID) || !strings.HasSuffix(m.Transcript, m.SessionID+".jsonl") || !strings.Contains(joined, filepath.Join(dir, "claude-debug.log")) {
							t.Fatalf("unbound Claude evidence: %q %+v", cmd, m)
						}
						if mode == "custom bindings" && m.Safe {
							t.Fatal("custom bindings enabled automatic input")
						}
					}
				} else if m.Version != "" || len(cmd) != 1 {
					t.Fatalf("unsupported launch enabled diagnostics: %q %+v", cmd, m)
				}
				if _, _, err := Prepare(id, []string{program}, env, true); err == nil {
					t.Fatal("reused stale launch diagnostic directory")
				}
			})
		}
	}
}

func assertPinnedCodexComposer(t *testing.T, config map[string]any) {
	t.Helper()
	tui, ok := config["tui"].(map[string]any)
	if !ok || tui["vim_mode_default"] != false {
		t.Fatalf("Vim was not disabled: %+v", config)
	}
	keymap, ok := tui["keymap"].(map[string]any)
	if !ok || len(keymap) != 12 {
		t.Fatalf("incomplete keymap contexts: %+v", keymap)
	}
	actions := 0
	for context, value := range keymap {
		bindings, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("missing explicit bindings for %s", context)
		}
		actions += len(bindings)
	}
	if actions != 154 {
		t.Fatalf("pinned %d actions, want all 154 in supported Codex", actions)
	}
	for _, context := range []string{"global", "composer"} {
		bindings := keymap[context].(map[string]any)
		submit, _ := bindings["submit"].([]any)
		if len(submit) != 1 || submit[0] != "enter" {
			t.Fatalf("%s Enter binding=%+v", context, submit)
		}
	}
	newline := keymap["editor"].(map[string]any)["insert_newline"].([]any)
	lfIsNewline := false
	for _, key := range newline {
		lfIsNewline = lfIsNewline || key == "ctrl-j"
	}
	if !lfIsNewline {
		t.Fatalf("LF lost its input-newline binding: %+v", newline)
	}
}

func TestPrepareClaudeForwardsInspectedEnvironment(t *testing.T) {
	// Serial: launch provisioning reads and forwards caller configuration.
	for _, mode := range []string{"inherited config", "explicit config", "default home", "explicit home", "relative config", "custom inherited bindings"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			program := filepath.Join(root, "claude")
			writeEvidence(t, program, []byte("#!/bin/sh\nprintf '%s\\n' '2.1.280 (Claude Code)'\n"))
			if err := os.Chmod(program, 0700); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{}
			home, config := root, filepath.Join(root, ".claude")
			switch mode {
			case "inherited config", "custom inherited bindings":
				config = filepath.Join(root, "inherited")
				t.Setenv("CLAUDE_CONFIG_DIR", config)
			case "explicit config":
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "unselected"))
				config = filepath.Join(root, "selected")
				env["CLAUDE_CONFIG_DIR"] = config
			case "explicit home":
				home = filepath.Join(root, "selected-home")
				config = filepath.Join(home, ".claude")
				env["HOME"] = home
			case "relative config":
				config = "relative-config"
				env["CLAUDE_CONFIG_DIR"] = config
			}
			if mode == "custom inherited bindings" {
				if err := os.MkdirAll(config, 0700); err != nil {
					t.Fatal(err)
				}
				writeEvidence(t, filepath.Join(config, "keybindings.json"), []byte("[]"))
			}
			id := Identity{StoreID: "0123456789abcdef", Token: "fedcba9876543210", Agent: "claude", CWD: "/work", Socket: "/own/socket", InstanceID: "own"}
			_, got, err := Prepare(id, []string{program}, env, true)
			if err != nil {
				t.Fatal(err)
			}
			if got["HOME"] != home || got["CLAUDE_CONFIG_DIR"] != config {
				t.Fatalf("launch environment=%+v want HOME=%s config=%s", got, home, config)
			}
			dir, err := launchDir(id.StoreID, id.Token)
			if err != nil {
				t.Fatal(err)
			}
			var m manifest
			data, err := os.ReadFile(filepath.Join(dir, "launch.json"))
			if err != nil || json.Unmarshal(data, &m) != nil {
				t.Fatalf("cannot read launch manifest: %v", err)
			}
			wantSafe := mode != "relative config" && mode != "custom inherited bindings"
			if m.Safe != wantSafe || !strings.HasPrefix(m.Transcript, filepath.Join(config, "projects")+string(filepath.Separator)) {
				t.Fatalf("inspected configuration not bound to launch: %+v", m)
			}
		})
	}
}

func TestManagedCodexComposerOverridesAreRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		config string
		unsafe bool
	}{
		{"model='ordinary'", false},
		{"[tui]\nnotifications=false", false},
		{"[tui.keymap.composer]\nsubmit=['x']", true},
		{"[tui]\nvim_mode_default=false", true},
		{"[profiles.work.tui.keymap.editor]\ninsert_newline=['enter']", true},
	} {
		t.Run(tc.config, func(t *testing.T) {
			var config map[string]any
			if _, err := toml.Decode(tc.config, &config); err != nil {
				t.Fatal(err)
			}
			if hasComposerSettings(config) != tc.unsafe {
				t.Fatalf("managed composer settings classification=%v", !tc.unsafe)
			}
		})
	}
}
