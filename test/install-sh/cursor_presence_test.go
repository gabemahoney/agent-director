package installsh_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// agentCursor records any accidental runtime, version or authentication probe.
func agentCursor(t *testing.T, h *moveHome, path string) string {
	t.Helper()
	log := filepath.Join(h.Root, "cursor-ran")
	h.Write(path, "#!/bin/bash\nprintf '%s\\n' \"$*\" >>"+moveQuote(log)+"\nexit 73\n", 0o755)
	return log
}

func TestAgentCursorPresence(t *testing.T) {
	for _, tc := range []struct {
		name, configured                              string
		onPath, editor, upgrade, absent, hooks, codex bool
	}{
		{name: "path", onPath: true},
		{name: "hooks-merge", onPath: true, hooks: true},
		{name: "mixed-codex-cursor", onPath: true, codex: true},
		{name: "configured", configured: "executable"},
		{name: "whole-path", configured: `agent with "quotes" --flag`},
		{name: "home-path", configured: "home"},
		{name: "empty-is-unset", configured: "empty", onPath: true},
		{name: "missing-discovery-only", configured: "missing", onPath: true},
		{name: "nonexec-discovery-only", configured: "nonexec", onPath: true},
		{name: "directory-discovery-only", configured: "directory", onPath: true},
		{name: "editor-is-not-agent", editor: true, absent: true},
		{name: "missing", configured: "missing", absent: true},
		{name: "nonexec", configured: "nonexec", absent: true},
		{name: "directory", configured: "directory", absent: true},
		{name: "old-layout", configured: "executable", upgrade: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newAgentHome(t)
			h.Flags = []string{"--no-symlink", "--no-hooks"}
			if tc.hooks {
				h.Flags = []string{"--no-symlink"}
			}
			if tc.codex {
				agentCodex(t, h, filepath.Join(h.Tools, "codex"))
			}
			configured := filepath.Join(h.Root, "programs", "agent")
			keys := apitest.ConfigKeys{}
			var logs []string
			switch tc.configured {
			case "empty":
				keys.WriteEmptyCursorPath = true
			case "":
			case "home":
				configured = filepath.Join(h.Home, "bin", "agent")
				logs = append(logs, agentCursor(t, h, configured))
				keys.CursorPath = "~/bin/agent"
			case "missing", "nonexec", "directory":
				keys.CursorPath = configured
				if tc.configured == "nonexec" {
					h.Write(configured, "must not run\n", 0o644)
				} else if tc.configured == "directory" {
					if err := os.MkdirAll(configured, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			default:
				if tc.configured != "executable" {
					configured = filepath.Join(h.Root, tc.configured)
				}
				keys.CursorPath = configured
				logs = append(logs, agentCursor(t, h, configured))
			}
			selected := configured
			if tc.onPath {
				selected = filepath.Join(h.Tools, "agent")
				logs = append(logs, agentCursor(t, h, selected))
			}
			if tc.editor {
				logs = append(logs, agentCursor(t, h, filepath.Join(h.Tools, "cursor")))
			}
			configPath := filepath.Join(h.New, "config.toml")
			if tc.upgrade {
				h.BuildOld(withMoveNoStore(), withMoveConfig(func(*moveHome) string { return agentLaunchConfigText(t, keys) }))
				configPath = filepath.Join(h.Old, "config.toml")
			} else {
				agentLaunchConfig(t, configPath, keys)
			}
			if tc.name == "whole-path" {
				// The shell reader accepts a literal string; the encoder uses escapes.
				h.Write(configPath, "[launch]\ncursor_path = '"+keys.CursorPath+"'\n", 0o600)
			}
			configBefore, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			before := h.Snapshot()
			res := h.Install("--register-mcp")
			want := moveWant{}
			if tc.absent {
				want.Code = 2
				want.Stderr = []string{"no supported agent (Claude Code, Codex or Cursor)", "  Cursor     :", "Nothing was installed or changed."}
			} else {
				want.Stdout = []string{agentNoClaude, agentSkipHook, agentSkipMCP, "  cursor  : " + selected + " (presence only; version and compatibility not checked; agent was not run)"}
				if tc.onPath && keys.CursorPath != "" {
					want.Stdout = append(want.Stdout, "agent found on PATH for installation only (configured launch path unchanged)")
				}
			}
			want.Check(t, h, res)
			if tc.absent {
				if d := moveDiff(before, h.Snapshot()); d != "" {
					t.Errorf("refusal changed HOME:\n%s", d)
				}
			} else {
				after, err := os.ReadFile(filepath.Join(h.New, "config.toml"))
				if err != nil || (!tc.hooks && !bytes.Equal(after, configBefore)) || (tc.hooks && (!bytes.HasPrefix(after, configBefore) || !bytes.Contains(after, []byte("inject_help_hook = true")))) {
					t.Errorf("launch config changed: %q %v; want %q", after, err, configBefore)
				}
			}
			for _, log := range logs {
				if data, err := os.ReadFile(log); !os.IsNotExist(err) {
					t.Errorf("Cursor executed: %q %v", data, err)
				}
			}
			if tc.codex && len(agentCodexCalls(t, h)) != 0 {
				t.Fatal("Codex discovery executed runtime")
			}
		})
	}
}
