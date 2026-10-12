package installsh_test

// install.sh's agent presence rule (SR-11.1): at least one of Claude Code and
// Codex must be found, by its [launch] path or on PATH; a host with Codex alone
// installs, skipping every Claude Code step; a host with neither is refused
// (exit 2). The helpers here (a stand-in codex, a PATH without claude and
// codex, a Claude Code stand-in at any path) are shared with launch_path_test.go.
// Assertions read the stand-ins' records, the files and the lines install.sh
// prints, never install.sh's internals.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// agentBare is the shared PATH directory of every tool in the move toolbox but
// claude and codex, so a case puts exactly the agents it wants first on PATH.
var agentBare struct {
	once sync.Once
	dir  string
	err  error
}

func agentBareToolbox(t *testing.T, s *moveShared) string {
	t.Helper()
	agentBare.once.Do(func() {
		agentBare.dir = filepath.Join(s.Root, "toolbox-no-agents")
		if agentBare.err = os.MkdirAll(agentBare.dir, 0o755); agentBare.err != nil {
			return
		}
		entries, err := os.ReadDir(s.Toolbox)
		if err != nil {
			agentBare.err = err
			return
		}
		for _, e := range entries {
			if e.Name() == "claude" || e.Name() == "codex" {
				continue
			}
			if agentBare.err = os.Symlink(filepath.Join(s.Toolbox, e.Name()), filepath.Join(agentBare.dir, e.Name())); agentBare.err != nil {
				return
			}
		}
	})
	if agentBare.err != nil {
		t.Fatalf("toolbox without agents: %v", agentBare.err)
	}
	return agentBare.dir
}

// newAgentHome is newMoveHome with a PATH holding no claude and no codex: the
// case's Tools directory, then every other tool of the toolbox.
func newAgentHome(t *testing.T) *moveHome {
	t.Helper()
	h := newMoveHome(t)
	h.Env["PATH"] = h.Tools + ":" + agentBareToolbox(t, h.S)
	return h
}

// agentCodexScript is a codex that records each call's arguments and, as
// `codex --version` does in the real one (RN-24), writes into the Codex folder.
const agentCodexScript = `printf 'CALL %s\n' "$*" >>"${CODEX_STANDIN_LOG:?}"
mkdir -p "$HOME/.codex/log"
echo "$*" >>"$HOME/.codex/log/standin-ran"
echo "codex-cli 0.99.0 (stand-in)"`

// agentCodex writes the stand-in codex at path and returns path.
func agentCodex(t *testing.T, h *moveHome, path string) string {
	t.Helper()
	h.Write(path, "#!/bin/bash\n"+agentCodexScript+"\n", 0o755)
	h.Env["CODEX_STANDIN_LOG"] = filepath.Join(h.Root, "codex-calls.log")
	return path
}

// agentCodexCalls is the argument lines the stand-in codex recorded.
func agentCodexCalls(t *testing.T, h *moveHome) []string {
	t.Helper()
	b, err := os.ReadFile(h.Env["CODEX_STANDIN_LOG"])
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// agentClaude writes a Claude Code stand-in at path: E3-T2's recording claude
// behind a wrapper that keeps this copy's calls in the log file logName (under
// the case's Root), so copies at two paths are told apart. It returns the log.
func agentClaude(t *testing.T, h *moveHome, path, logName string) string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(ownedClaudeDir, "claude.sh"))
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(h.Root, logName)
	h.Write(path, "#!/bin/bash\nexport CLAUDE_STANDIN_LOG="+moveQuote(log)+"\nexec "+moveQuote(h.S.Bash)+" "+moveQuote(script)+" \"$@\"\n", 0o755)
	capture, err := filepath.Abs(filepath.Join(ownedClaudeDir, ownedSets(t)[0], "none-found-many-others"))
	if err != nil {
		t.Fatal(err)
	}
	h.Env["CLAUDE_STANDIN_CAPTURE"] = capture
	return log
}

// agentClaudeCalls is the calls the stand-in claude recorded in log.
func agentClaudeCalls(t *testing.T, h *moveHome, log string) []ownedCall {
	t.Helper()
	h.Env["CLAUDE_STANDIN_LOG"] = log
	defer delete(h.Env, "CLAUDE_STANDIN_LOG")
	return ownedCalls(t, h)
}

// agentLaunchConfig writes config.toml at path with the [launch] paths given.
func agentLaunchConfig(t *testing.T, path string, keys apitest.ConfigKeys) {
	t.Helper()
	apitest.WriteKeysConfig(t, path, keys)
}

// agentLaunchConfigText is the text agentLaunchConfig writes, for a case that
// builds it into an old install.
func agentLaunchConfigText(t *testing.T, keys apitest.ConfigKeys) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	agentLaunchConfig(t, p, keys)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// agentSubtree is the entries of tree at prefix or under it.
func agentSubtree(tree moveTree, prefix string) moveTree {
	out := moveTree{}
	for p, v := range tree {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			out[p] = v
		}
	}
	return out
}

// agentUnchanged fails the case when a path at or under any prefix differs
// between two snapshots.
func agentUnchanged(t *testing.T, before, after moveTree, prefixes ...string) {
	t.Helper()
	for _, p := range prefixes {
		if d := moveDiff(agentSubtree(before, p), agentSubtree(after, p)); d != "" {
			t.Errorf("%s changed:\n%s", p, d)
		}
	}
}

const (
	agentNoClaude = "  claude  : not installed (claude is not on PATH, and [launch] claude_path is not set)"
	agentSkipHook = "  hooks   : skipped because Claude Code is not installed (settings.json is not touched)"
	agentSkipMCP  = "  mcp     : --register-mcp skipped because Claude Code is not installed"
)

// TestAgentCodexOnly: a host with Codex alone (a stand-in codex on PATH, no
// claude) installs with hooks on or off, with or without --register-mcp, and
// touches nothing of Claude Code or Codex. The skip line wins over --no-hooks'.
func TestAgentCodexOnly(t *testing.T) {
	for _, tc := range []struct {
		name            string
		noHooks, regMCP bool
	}{
		{"hooks", false, false},
		{"hooks-register-mcp", false, true},
		{"no-hooks", true, false},
		{"no-hooks-register-mcp", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newAgentHome(t)
			codex := agentCodex(t, h, filepath.Join(h.Tools, "codex"))
			codexUnchanged := uninstallSeedCodex(t, h.Home)
			h.Flags = []string{"--no-symlink"}
			var args []string
			if tc.noHooks {
				h.Flags = append(h.Flags, "--no-hooks")
			}
			if tc.regMCP {
				args = append(args, "--register-mcp")
			}
			before := h.Snapshot()
			res := h.Install(args...)
			want := moveWant{
				Stdout:   []string{agentNoClaude, agentSkipHook, "  codex   : " + codex + " (version not checked: "},
				NoStdout: []string{"hooks   : skipped (--no-hooks)", "found from this directory"},
			}
			if tc.regMCP {
				want.Stdout = append(want.Stdout, agentSkipMCP)
			} else {
				want.NoStdout = append(want.NoStdout, "  mcp     :")
			}
			want.Check(t, h, res)
			after := h.Snapshot()

			if calls := agentCodexCalls(t, h); len(calls) != 0 {
				t.Errorf("the stand-in codex was called: %q", calls)
			}
			codexUnchanged()
			agentUnchanged(t, before, after, ".codex")
			if _, err := os.Lstat(filepath.Join(h.Home, ".claude", "settings.json")); !os.IsNotExist(err) {
				t.Errorf("~/.claude/settings.json after a Codex-only install: %v, want none made", err)
			}
			cfg, err := os.ReadFile(filepath.Join(h.New, "config.toml"))
			if tc.noHooks {
				if err == nil && strings.Contains(string(cfg), "inject_help_hook") {
					t.Errorf("config.toml holds inject_help_hook after --no-hooks:\n%s", cfg)
				}
			} else if err != nil || !strings.Contains(string(cfg), "inject_help_hook = true") {
				t.Errorf("config.toml = %q, %v; want inject_help_hook = true merged", cfg, err)
			}
		})
	}
}

// TestAgentCodexOnlySymlinks: with Claude Code absent a dangling settings.json
// link is not checked (exit 0, link as it was); config.toml's link check runs
// as it does with Claude Code, a dangling one refused (exit 5).
func TestAgentCodexOnlySymlinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		link string // the link, relative to HOME
		want moveWant
	}{
		{"settings-json-dangling-link", ".claude/settings.json", moveWant{Stdout: []string{agentSkipHook}}},
		{"config-toml-dangling-link", ".config/agent-director/config.toml", moveWant{
			Code: 5, Err: "ErrConfigMalformed",
			Stderr:   []string{"install.sh: cannot merge into {HOME}/.config/agent-director/config.toml through its symlink; refusing to install."},
			NoStdout: []string{"kept"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newAgentHome(t)
			agentCodex(t, h, filepath.Join(h.Tools, "codex"))
			h.Flags = []string{"--no-symlink"}
			h.Link(filepath.Join(h.Home, "dotfiles", "missing", "target"), filepath.Join(h.Home, tc.link))
			before := h.Snapshot()
			res := h.Install()
			tc.want.Check(t, h, res)
			after := h.Snapshot()
			if tc.want.Code == 0 {
				agentUnchanged(t, before, after, tc.link)
			} else if d := moveDiff(before, after); d != "" {
				t.Errorf("a refused install changed HOME:\n%s", d)
			}
		})
	}
}

// TestAgentNeitherFound: with no agent found the install is refused (exit 2,
// naming both) and changes nothing, whether [launch] is absent, names no codex
// or names a claude that is no executable file.
func TestAgentNeitherFound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		keys    func(h *moveHome) *apitest.ConfigKeys
		claude  string
		codex   string
		nonExec bool // make claude_path a file that is not executable
	}{
		{name: "no-launch-keys", claude: "claude is not on PATH, and [launch] claude_path is not set",
			codex: "codex is not on PATH, and [launch] codex_path is not set"},
		{name: "codex-path-names-nothing", keys: func(h *moveHome) *apitest.ConfigKeys {
			return &apitest.ConfigKeys{CodexPath: filepath.Join(h.Root, "nowhere", "codex")}
		}, claude: "claude is not on PATH, and [launch] claude_path is not set",
			codex: `[launch] codex_path = "{ROOT}/nowhere/codex" names no executable file, and codex is not on PATH`},
		{name: "claude-path-is-not-executable", nonExec: true, keys: func(h *moveHome) *apitest.ConfigKeys {
			return &apitest.ConfigKeys{ClaudePath: filepath.Join(h.Root, "programs", "claude")}
		}, claude: `[launch] claude_path = "{ROOT}/programs/claude" names no executable file, and claude is not on PATH`,
			codex: "codex is not on PATH, and [launch] codex_path is not set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newAgentHome(t)
			if tc.nonExec {
				h.Write(filepath.Join(h.Root, "programs", "claude"), "#!/bin/bash\necho not run\n", 0o644)
			}
			if tc.keys != nil {
				agentLaunchConfig(t, filepath.Join(h.New, "config.toml"), *tc.keys(h))
			}
			h.Vars["ROOT"] = h.Root
			before := h.Snapshot()
			res := h.Install()
			moveWant{
				Code: 2,
				Stderr: []string{
					"install.sh: neither Claude Code nor Codex was found; refusing to install.",
					"  Claude Code: " + tc.claude,
					"  Codex      : " + tc.codex,
					"  Nothing was installed or changed. Re-run this install after the change.",
				},
				NoStdout: []string{"kept", "  claude  :"},
			}.Check(t, h, res)
			if d := moveDiff(before, h.Snapshot()); d != "" {
				t.Errorf("a refused install changed HOME:\n%s", d)
			}
		})
	}
}

// TestAgentCodexOnlyUpgradeLeavesClaudeCodeAlone: an old-layout upgrade with
// Codex alone and --register-mcp moves the directory but touches no Claude
// Code entry: settings.json keeps its old-path help hooks byte for byte, none
// is printed, and the MCP step reports its skip.
func TestAgentCodexOnlyUpgradeLeavesClaudeCodeAlone(t *testing.T) {
	h := newAgentHome(t)
	agentCodex(t, h, filepath.Join(h.Tools, "codex"))
	h.Flags = []string{"--no-symlink"}
	h.BuildOld()
	hook := func(event string) string {
		return `"` + event + `": [{"matcher": "compact", "hooks": [{"type": "command", "command": "` + h.Old + `/bin/agent-director help"}]}]`
	}
	h.Write(filepath.Join(h.Home, ".claude", "settings.json"), `{"theme": "dark", "hooks": {`+hook("SessionStart")+`, `+hook("SessionEnd")+`}}`+"\n", 0o600)
	before := h.Snapshot()

	res := h.Install("--register-mcp")

	moveWant{
		Stdout:   []string{agentNoClaude, agentSkipHook, agentSkipMCP},
		NoStdout: []string{"old directory's binary", "found from this directory", "registered with claude mcp"},
	}.Check(t, h, res)
	after := h.Snapshot()
	agentUnchanged(t, before, after, ".claude")
	if _, err := os.Stat(filepath.Join(h.New, "bin", "agent-director")); err != nil {
		t.Errorf("the upgrade did not install the binary in the new directory: %v", err)
	}
	if calls := agentCodexCalls(t, h); len(calls) != 0 {
		t.Errorf("the stand-in codex was called: %q", calls)
	}
}
