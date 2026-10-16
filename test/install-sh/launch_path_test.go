package installsh_test

// install.sh's wiring of [launch] claude_path and codex_path (SR-5.0, SR-11.1):
// presence by launch path or PATH and the summary lines each case gives, the
// Claude Code program a launch path names running every Claude Code call, the
// pre-flight refusals of a [launch] line it cannot read, and the table never
// being written. Well-formed [launch] values are written with
// apitest.WriteKeysConfig (SR-14.3); the stand-ins (agent_presence_test.go)
// record the calls, so a PATH program and a launch-path program are told apart.
// The reader's own line-form variants are the dbpathfix guard's.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const launchClaudeVersion = "2.1.999 (Claude Code stand-in)"

// launchCapture makes the stand-in claudes replay the capture set/name for
// `mcp get agent-director`.
func launchCapture(t *testing.T, h *moveHome, set, name string) {
	t.Helper()
	capture, err := filepath.Abs(filepath.Join(ownedClaudeDir, set, name))
	if err != nil {
		t.Fatal(err)
	}
	h.Env["CLAUDE_STANDIN_CAPTURE"] = capture
}

// launchCalls is the lines of the calls the stand-in claude recorded in log.
func launchCalls(t *testing.T, h *moveHome, log string) []string {
	t.Helper()
	return ownedLines(agentClaudeCalls(t, h, log))
}

// TestLaunchPresenceSummary is the presence rule through each way an agent is
// found or not, with the summary lines it gives and which claude ran.
func TestLaunchPresenceSummary(t *testing.T) {
	const (
		fromLaunch = "launch" // claude_path names the stand-in at programs/claude
		missing    = "none"   // claude_path names a file that is not there
	)
	version := func(prog string) string {
		return "  claude  : " + launchClaudeVersion + " (program: " + prog + ")"
	}
	probes := []string{"--version", "mcp get agent-director"}
	for _, tc := range []struct {
		name string
		// claudePath and codexPath say what [launch] names (fromLaunch, missing or
		// "" for unset, "empty" for an empty value, "home" for a ~/ path); claudeOnPath
		// and codexOnPath put the agent on PATH too.
		claudePath, codexPath     string
		claudeOnPath, codexOnPath bool
		// want builds the summary lines from the case's programs/ and tools/ paths.
		want func(programs, tools, home string) []string
		// ran names the log of the claude that must have been called, "" for none.
		ran string
	}{
		{name: "codex-by-codex-path-alone", codexPath: fromLaunch,
			want: func(p, tools, home string) []string {
				return []string{agentNoClaude, "  codex   : " + p + "/codex (version not checked: "}
			}},
		{name: "codex-path-names-nothing-and-no-codex-on-path", claudeOnPath: true, codexPath: missing, ran: "path.log",
			want: func(p, tools, home string) []string {
				return []string{version(tools + "/claude"),
					`  codex   : not installed ([launch] codex_path = "` + p + `/nowhere/codex" names no executable file, and codex is not on PATH)`}
			}},
		{name: "claude-by-claude-path-alone", claudePath: fromLaunch, ran: "launch.log",
			want: func(p, tools, home string) []string {
				return []string{version(p + "/claude"), "  codex   : not installed (codex is not on PATH, and [launch] codex_path is not set)"}
			}},
		{name: "claude-path-in-home", claudePath: "home", ran: "launch.log",
			want: func(p, tools, home string) []string {
				return []string{version(home + "/bin/claude")}
			}},
		{name: "claude-path-names-nothing-falls-back-to-path", claudePath: missing, claudeOnPath: true, ran: "path.log",
			want: func(p, tools, home string) []string {
				return []string{
					`  claude  : [launch] claude_path = "` + p + `/nowhere/claude" names no executable file; using claude from PATH`,
					version(tools + "/claude")}
			}},
		{name: "codex-path-names-nothing-falls-back-to-path", claudeOnPath: true, codexOnPath: true, codexPath: missing, ran: "path.log",
			want: func(p, tools, home string) []string {
				return []string{
					`  codex   : [launch] codex_path = "` + p + `/nowhere/codex" names no executable file; using codex from PATH`,
					"  codex   : " + tools + "/codex (version not checked: "}
			}},
		{name: "empty-values-count-as-unset", claudePath: "empty", codexPath: "empty", claudeOnPath: true, codexOnPath: true, ran: "path.log",
			want: func(p, tools, home string) []string {
				return []string{version(tools + "/claude"), "  codex   : " + tools + "/codex (version not checked: "}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newAgentHome(t)
			programs := filepath.Join(h.Root, "programs")
			// The launch-path stand-in is written even where claude_path does not
			// name it, so a case that runs it by mistake shows in its log.
			launchAt := filepath.Join(programs, "claude")
			if tc.claudePath == "home" {
				launchAt = filepath.Join(h.Home, "bin", "claude")
			}
			logs := map[string]string{"launch.log": agentClaude(t, h, launchAt, "launch.log")}
			if tc.claudeOnPath {
				logs["path.log"] = agentClaude(t, h, filepath.Join(h.Tools, "claude"), "path.log")
			}
			agentCodex(t, h, filepath.Join(programs, "codex"))
			if tc.codexOnPath {
				agentCodex(t, h, filepath.Join(h.Tools, "codex"))
			}
			keys := apitest.ConfigKeys{}
			switch tc.claudePath {
			case fromLaunch:
				keys.ClaudePath = filepath.Join(programs, "claude")
			case missing:
				keys.ClaudePath = filepath.Join(programs, "nowhere", "claude")
			case "home":
				keys.ClaudePath = "~/bin/claude"
			case "empty":
				keys.WriteEmptyClaudePath = true
			}
			switch tc.codexPath {
			case fromLaunch:
				keys.CodexPath = filepath.Join(programs, "codex")
			case missing:
				keys.CodexPath = filepath.Join(programs, "nowhere", "codex")
			case "empty":
				keys.WriteEmptyCodexPath = true
			}
			agentLaunchConfig(t, filepath.Join(h.New, "config.toml"), keys)

			res := h.Install()

			moveWant{Stdout: tc.want(programs, h.Tools, h.Home)}.Check(t, h, res)
			for name, log := range logs {
				want := []string(nil)
				if name == tc.ran {
					want = probes
				}
				if got := launchCalls(t, h, log); !slices.Equal(got, want) {
					t.Errorf("claude calls of %s = %q, want %q", name, got, want)
				}
			}
			if calls := agentCodexCalls(t, h); len(calls) != 0 {
				t.Errorf("a stand-in codex was called: %q", calls)
			}
		})
	}
}

// TestLaunchProgramRunsEveryClaudeCall: with claude_path naming a program and
// a different claude first on PATH, the launch-path program makes the summary's
// --version, `mcp get`, `mcp add` (--register-mcp) and, on an upgrade that finds
// the old directory's registration, `mcp remove` and `mcp add`; the PATH claude
// is never called. The upgrade reads claude_path from the old directory's
// config.toml.
func TestLaunchProgramRunsEveryClaudeCall(t *testing.T) {
	for _, tc := range []struct {
		name    string
		upgrade bool   // an old-layout install with an old-path registration to re-point
		capture string // the `mcp get` capture the stand-ins replay
		calls   func(canon string) []string
		stdout  string
	}{
		{name: "fresh-install-registers", capture: "none-found-many-others",
			calls: func(canon string) []string {
				return []string{"--version", "mcp get agent-director", "mcp add agent-director -- " + canon + " serve --stdio"}
			}, stdout: "  mcp     : registered with claude mcp"},
		{name: "upgrade-re-points-the-old-registration", upgrade: true, capture: "old-plain-user",
			calls: func(canon string) []string {
				return []string{"--version", "mcp get agent-director", "mcp remove agent-director -s user",
					"mcp add agent-director -s user -- " + canon + " serve --stdio"}
			}, stdout: "  mcp     : re-registered with claude mcp (scope user)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newAgentHome(t)
			program := filepath.Join(h.Root, "programs", "claude")
			launchLog := agentClaude(t, h, program, "launch.log")
			pathLog := agentClaude(t, h, filepath.Join(h.Tools, "claude"), "path.log")
			launchCapture(t, h, ownedSets(t)[0], tc.capture)
			keys := apitest.ConfigKeys{ClaudePath: program}
			if tc.upgrade {
				h.BuildOld(withMoveNoStore(), withMoveConfig(func(*moveHome) string { return agentLaunchConfigText(t, keys) }))
			} else {
				agentLaunchConfig(t, filepath.Join(h.New, "config.toml"), keys)
			}

			res := h.Install("--register-mcp")

			moveWant{Stdout: []string{"  claude  : " + launchClaudeVersion + " (program: " + program + ")", tc.stdout}}.Check(t, h, res)
			if got, want := launchCalls(t, h, launchLog), tc.calls(filepath.Join(h.New, "bin", "agent-director")); !slices.Equal(got, want) {
				t.Errorf("claude_path program's calls = %q, want %q", got, want)
			}
			if got := launchCalls(t, h, pathLog); len(got) != 0 {
				t.Errorf("the PATH claude was called: %q", got)
			}
		})
	}
}

// TestLaunchRefusals: a [launch] line install.sh cannot read stops the install
// before anything runs or changes: exit 5, ErrConfigMalformed, HOME as it was
// and no call to either agent.
func TestLaunchRefusals(t *testing.T) {
	const headline = "install.sh: cannot tell which programs agent-director launches (the [launch] table); refusing to install."
	for _, tc := range []struct {
		name string
		raw  string              // a config.toml written by hand (lines ConfigKeys cannot express)
		keys *apitest.ConfigKeys // or by ConfigKeys
		line string              // the line the refusal names
	}{
		{name: "header-in-capitals", raw: "[Launch]\nclaude_path = \"/opt/claude\"\n", line: "line 1  : [Launch]"},
		{name: "key-in-capitals", raw: "[launch]\nCODEX_PATH = \"/opt/codex\"\n", line: "line 2  : CODEX_PATH"},
		{name: "second-launch-header", raw: "[launch]\nclaude_path = \"/opt/claude\"\n[launch]\ncodex_path = \"/opt/codex\"\n", line: "line 3  : [launch]"},
		{name: "dotted-key-before-any-header", raw: "launch.codex_path = \"/opt/codex\"\n", line: "line 1  : launch.codex_path"},
		{name: "inline-table-before-any-header", raw: "launch = { codex_path = \"/opt/codex\" }\n", line: "line 1  : launch = "},
		{name: "value-with-an-escape", keys: &apitest.ConfigKeys{ClaudePath: "/opt/cl\taude"}, line: "line 2  :   claude_path"},
		{name: "relative-value", keys: &apitest.ConfigKeys{CodexPath: "bin/codex"}, line: "line 2  :   codex_path"},
		{name: "relative-cursor", keys: &apitest.ConfigKeys{CursorPath: "bin/agent"}, line: "line 2  :   cursor_path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newAgentHome(t)
			pathLog := agentClaude(t, h, filepath.Join(h.Tools, "claude"), "path.log")
			agentCodex(t, h, filepath.Join(h.Tools, "codex"))
			config := filepath.Join(h.New, "config.toml")
			if tc.keys != nil {
				agentLaunchConfig(t, config, *tc.keys)
			} else {
				h.Write(config, tc.raw, 0o600)
			}
			before := h.Snapshot()

			res := h.Install()

			moveWant{Code: 5, Err: "ErrConfigMalformed", Stderr: []string{headline, "  config  : " + config, tc.line}, NoStdout: []string{"kept"}}.Check(t, h, res)
			if d := moveDiff(before, h.Snapshot()); d != "" {
				t.Errorf("a refused install changed HOME:\n%s", d)
			}
			if got := launchCalls(t, h, pathLog); len(got) != 0 {
				t.Errorf("claude was called: %q", got)
			}
			if got := agentCodexCalls(t, h); len(got) != 0 {
				t.Errorf("codex was called: %q", got)
			}
		})
	}
}

// TestLaunchNeverWritten: install.sh reads [launch] and never writes it. With
// hooks on it merges inject_help_hook into config.toml and leaves an existing
// [launch] table byte for byte; with --no-hooks config.toml is left alone.
func TestLaunchNeverWritten(t *testing.T) {
	for _, tc := range []struct {
		name            string
		withLaunch      bool
		noHooks         bool
		wantLaunchCount int
	}{
		{"no-config-hooks", false, false, 0},
		{"no-config-no-hooks", false, true, 0},
		{"launch-table-hooks", true, false, 1},
		{"launch-table-no-hooks", true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newAgentHome(t)
			program := agentCodex(t, h, filepath.Join(h.Tools, "codex"))
			agentClaude(t, h, filepath.Join(h.Tools, "claude"), "path.log")
			config := filepath.Join(h.New, "config.toml")
			before := ""
			if tc.withLaunch {
				agentLaunchConfig(t, config, apitest.ConfigKeys{ClaudePath: filepath.Join(h.Tools, "claude"), CodexPath: program})
				b, err := os.ReadFile(config)
				if err != nil {
					t.Fatal(err)
				}
				before = string(b)
			}
			h.Flags = []string{"--no-symlink"}
			if tc.noHooks {
				h.Flags = append(h.Flags, "--no-hooks")
			}

			res := h.Install()

			moveWant{}.Check(t, h, res)
			b, err := os.ReadFile(config)
			after := string(b)
			switch {
			case tc.noHooks && !tc.withLaunch:
				if err == nil {
					t.Errorf("config.toml was made with --no-hooks:\n%s", after)
				}
			case err != nil:
				t.Fatal(err)
			case tc.noHooks && after != before:
				t.Errorf("config.toml changed with --no-hooks:\n--- before\n%s--- after\n%s", before, after)
			case !tc.noHooks && !strings.Contains(after, "inject_help_hook = true"):
				t.Errorf("config.toml lacks the merged inject_help_hook:\n%s", after)
			case !strings.HasPrefix(after, before):
				t.Errorf("the [launch] table's bytes changed:\n--- before\n%s--- after\n%s", before, after)
			}
			if n := strings.Count(after, "[launch]"); n != tc.wantLaunchCount {
				t.Errorf("config.toml holds %d [launch] tables, want %d:\n%s", n, tc.wantLaunchCount, after)
			}
		})
	}
}
