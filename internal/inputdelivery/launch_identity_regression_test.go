package inputdelivery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareProbeOmitsHookIdentityButLaunchRetainsIt(t *testing.T) {
	// Serial: the test supplies inherited caller identity and launch configuration.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for key, value := range map[string]string{
		"AGENT_DIRECTOR_INSTANCE_ID":    "caller-row",
		"AGENT_DIRECTOR_INHERITED_ONLY": "caller-hook-value",
		"TMUX":                          "/caller/socket,10,0",
		"TMUX_PANE":                     "%caller",
	} {
		t.Setenv(key, value)
	}
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			version := "codex-cli 0.159.2"
			if agent == "claude" {
				version = "2.1.280 (Claude Code)"
			}
			program := filepath.Join(root, "agent")
			script := "#!/bin/sh\n" +
				"if [ \"$1\" = --version ]; then\n" +
				"  env > \"$CAPTURE_DIR/probe.env\"\n" +
				"  printf '%s\\n' '" + version + "'\n" +
				"else\n  env > \"$CAPTURE_DIR/launch.env\"\nfi\n"
			if err := os.WriteFile(program, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{
				"CAPTURE_DIR":                 root,
				"AGENT_DIRECTOR_INSTANCE_ID":  "new-row",
				"AGENT_DIRECTOR_LAUNCH_TOKEN": "new-token",
				"TMUX":                        "/new/socket,20,0",
				"TMUX_PANE":                   "%new",
				"CLAUDE_CONFIG_DIR":           filepath.Join(root, "claude"),
				"CODEX_HOME":                  filepath.Join(root, "codex"),
			}
			id := Identity{StoreID: "0123456789abcdef", Token: "fedcba9876543210", InstanceID: "new-row", Agent: agent, Socket: "/new/socket", CWD: root}
			command, launchEnv, err := Prepare(id, []string{program}, env, true)
			if err != nil {
				t.Fatal(err)
			}
			probe := readCapturedEnvironment(t, filepath.Join(root, "probe.env"))
			for key := range probe {
				if strings.HasPrefix(key, "AGENT_DIRECTOR_") || key == "TMUX" || key == "TMUX_PANE" {
					t.Errorf("version probe retained hook identity %s", key)
				}
			}
			if probe["CAPTURE_DIR"] != root || probe["CODEX_HOME"] != env["CODEX_HOME"] || probe["CLAUDE_CONFIG_DIR"] != env["CLAUDE_CONFIG_DIR"] {
				t.Fatalf("probe lost non-identity launch configuration: %+v", probe)
			}
			if len(command) <= 1 {
				t.Fatal("identity-free version probe failed to select supported diagnostics")
			}
			launch := exec.Command(command[0], command[1:]...)
			launch.Env = os.Environ()
			for key, value := range launchEnv {
				launch.Env = append(launch.Env, key+"="+value)
			}
			if output, err := launch.CombinedOutput(); err != nil {
				t.Fatalf("launch: %v: %s", err, output)
			}
			actual := readCapturedEnvironment(t, filepath.Join(root, "launch.env"))
			for key, want := range env {
				if actual[key] != want || launchEnv[key] != want {
					t.Errorf("launch %s=%q prepared=%q; want %q", key, actual[key], launchEnv[key], want)
				}
			}
			if actual["AGENT_DIRECTOR_INHERITED_ONLY"] != "caller-hook-value" {
				t.Fatal("identity filtering escaped the version probe and altered the real launch")
			}
		})
	}
}

func readCapturedEnvironment(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		env[key] = value
	}
	return env
}
