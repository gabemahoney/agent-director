package spawn

import (
	"errors"
	"strings"
	"testing"
)

const (
	whyHooks     = "agent-director sets the hooks and their trust for every Codex launch"
	whyPaste     = "agent-director sets the typing setting for every Codex launch"
	whyFeatHooks = "agent-director needs Codex's hooks on for every launch"
	whyQuestion  = "disable_ask_user keeps Codex's question tool off"
	whyRemote    = "it attaches the agent to a remote or shared service"
)

// codexSubcommandWhy is the description of a refused subcommand, word for word.
func codexSubcommandWhy(name string) string {
	return "codex subcommand " + name + ": a spawn launches the interactive agent, and this subcommand picks or resumes another session, runs non-interactively or attaches to a remote or shared service"
}

// TestCheckCodexArgs pins SR-2.3 and SR-2.4 as amended by RN-13: one row per
// refused form (the description word for word, after "ErrSpawnDeniedFlag: ")
// and per pass-through control. A row with want "" passes; off is the
// question-tool setting (disable_ask_user).
func TestCheckCodexArgs(t *testing.T) {
	type argCase struct {
		name string
		args []string
		off  bool
		want string
	}
	var cases []argCase
	add := func(name, want string, off bool, args ...string) {
		cases = append(cases, argCase{name, args, off, want})
	}

	// Subcommands in command position, each of the amended list.
	for _, sub := range []string{"agents", "exec", "e", "review", "login", "logout", "mcp", "plugin",
		"app-server", "remote-control", "completion", "update", "doctor", "sandbox", "debug", "apply", "a",
		"resume", "fork", "queue", "archive", "delete", "migrate-rollouts", "unarchive", "cloud",
		"cloud-tasks", "exec-server", "features", "help", "tcp-tunnel", "execpolicy",
		"responses-api-proxy", "stdio-to-uds", "app"} {
		add("subcommand "+sub, codexSubcommandWhy(sub), false, sub)
	}
	add("mcp-server is not a subcommand", "", false, "mcp-server")
	add("subcommand with its own arguments", codexSubcommandWhy("resume"), false, "resume", "--last")
	add("subcommand after a flag", codexSubcommandWhy("exec"), false, "--no-daemon", "exec")
	add("subcommand after a flag cluster", codexSubcommandWhy("exec"), false, "-hV", "exec")
	add("subcommand after -m's value", codexSubcommandWhy("exec"), false, "-m", "opus", "exec")
	add("subcommand after --sandbox's value", codexSubcommandWhy("resume"), false, "--sandbox", "read-only", "resume")
	add("subcommand after -c's value", codexSubcommandWhy("exec"), false, "-c", "tui.theme=dark", "exec")
	add("subcommand after --image=FILE", codexSubcommandWhy("exec"), false, "--image=a", "exec")
	add("subcommand after -iFILE", codexSubcommandWhy("exec"), false, "-ia", "exec")
	add("subcommand after a prompt word is prompt text", "", false, "hello", "exec")
	add("-m takes the subcommand name as its value", "", false, "-m", "exec")
	add("-i takes a word up to the next flag", "", false, "-i", "a.png", "exec")
	add("-i takes several words", "", false, "-i", "a", "b", "exec")
	add("-i stops at the next flag", codexSubcommandWhy("exec"), false, "-i", "a", "--no-daemon", "exec")
	add("subcommand name after --", "", false, "--", "exec")
	add("two words after --", "", false, "--", "exec", "resume")
	add("prompt before -- then a subcommand name after", "", false, "hello", "--", "exec")

	// --remote and --remote-auth-token-env, every form.
	for _, opt := range []string{"--remote", "--remote-auth-token-env"} {
		add(opt+" next argument", opt+": "+whyRemote, false, opt, "x")
		add(opt+"=", opt+": "+whyRemote, false, opt+"=x")
		add(opt+" outranks a later subcommand", opt+": "+whyRemote, false, opt, "x", "exec")
	}
	add("--remote after --", "--remote: "+whyRemote, false, "--", "--remote", "ws://h")
	add("--remote after a prompt word", "--remote: "+whyRemote, false, "hello", "--remote=ws://h")

	// -c / --config key paths, each in the next-argument, = and attached forms.
	type keyCase struct{ key, why, shown string }
	for _, k := range []keyCase{
		{"hooks.Stop=[]", whyHooks, "hooks.Stop"},
		{"hooks.state={}", whyHooks, "hooks.state"},
		{"hooks={}", whyHooks, "hooks"},
		{"hooks.PreToolUse.0=1", whyHooks, "hooks.PreToolUse.0"},
		{"tui.disable_paste_burst=false", whyPaste, "tui.disable_paste_burst"},
		{"tui={disable_paste_burst=false}", whyPaste, "tui"},
		{"features.hooks=false", whyFeatHooks, "features.hooks"},
		{"features={hooks=false}", whyFeatHooks, "features"},
		{" hooks.Stop = []", whyHooks, "hooks.Stop"},
	} {
		forms := map[string][]string{
			"-c " + k.key:       {"-c", k.key},
			"--config " + k.key: {"--config", k.key},
			"-c" + k.key:        {"-c" + k.key},
			"-c=" + k.key:       {"-c=" + k.key},
			"--config=" + k.key: {"--config=" + k.key},
		}
		for name, args := range forms {
			bare := "-c"
			if strings.HasPrefix(name, "--config") {
				bare = "--config"
			}
			add("override "+name, bare+" "+k.shown+": "+k.why, false, args...)
		}
	}
	add("override after --", "-c hooks.Stop: "+whyHooks, false, "--", "-c", "hooks.Stop=[]")
	add("override after a prompt word", "-c hooks.Stop: "+whyHooks, false, "hello", "-c", "hooks.Stop=[]")
	add("sibling keys sharing a prefix string pass", "", false,
		"-c", "tui.theme=dark", "-c", "features.fast_mode=true", "-c", "hooksx=1", "-c", "tui.disable_paste_burst_x=1",
		"-c", "features.hooks2=1", "-c", "tools.web_search=live")
	add("projects passes", "", false, "-c", `projects={"/a"={trust_level="trusted"}}`)
	add("-c with no value passes", "", false, "-c")

	// --enable and --disable.
	for _, form := range [][]string{{"--disable", "hooks"}, {"--disable=hooks"}} {
		add(strings.Join(form, " "), "--disable hooks: "+whyFeatHooks, false, form...)
	}
	add("--disable hooks after --", "--disable hooks: "+whyFeatHooks, false, "--", "--disable", "hooks")
	add("--enable hooks passes", "", false, "--enable", "hooks")
	add("--disable of another feature passes", "", false, "--disable", "fast_mode")
	add("--enable write_stdin_approval passes", "", false, "--enable", "write_stdin_approval")
	add("--disable write_stdin_approval passes", "", false, "--disable", "write_stdin_approval")
	add("--disable=write_stdin_approval passes", "", false, "--disable=write_stdin_approval")

	// The question-tool forms: refused only while disable_ask_user is on.
	for _, off := range []bool{true, false} {
		tag := "setting off: "
		if off {
			tag = "setting on: "
		}
		why := func(s string) string {
			if off {
				return s
			}
			return ""
		}
		add(tag+"--enable the question tool", why("--enable default_mode_request_user_input: "+whyQuestion), off,
			"--enable", "default_mode_request_user_input")
		add(tag+"--enable= the question tool", why("--enable default_mode_request_user_input: "+whyQuestion), off,
			"--enable=default_mode_request_user_input")
		add(tag+"--enable the question tool after --", why("--enable default_mode_request_user_input: "+whyQuestion), off,
			"--", "--enable", "default_mode_request_user_input")
		add(tag+"--disable the question tool passes", "", off, "--disable", "default_mode_request_user_input")
		for _, key := range []string{
			"features.default_mode_request_user_input=true",
			"features.default_mode_request_user_input=false",
			"tools={}",
			"tools.experimental_request_user_input=1",
			"tools.experimental_request_user_input.enabled=true",
		} {
			shown, _, _ := strings.Cut(key, "=")
			add(tag+"-c "+key, why("-c "+shown+": "+whyQuestion), off, "-c", key)
			add(tag+"--config= "+key, why("--config "+shown+": "+whyQuestion), off, "--config="+key)
		}
		add(tag+"tools.web_search passes", "", off, "-c", "tools.web_search=live")
	}

	// The earliest refused argument is the one named.
	add("hooks before a subcommand", "-c hooks.Stop: "+whyHooks, false, "-c", "hooks.Stop=[]", "exec")
	add("a subcommand before hooks", codexSubcommandWhy("exec"), false, "exec", "-c", "hooks.Stop=[]")
	add("a subcommand before remote", codexSubcommandWhy("exec"), false, "-m", "x", "exec", "--remote", "y")
	add("paste before hooks", "-c tui.disable_paste_burst: "+whyPaste, false,
		"-c", "tui.disable_paste_burst=true", "-c", "hooks.x=1")
	add("--disable hooks before remote", "--disable hooks: "+whyFeatHooks, false, "--disable", "hooks", "--remote", "x")
	add("remote before the question tool", "--remote: "+whyRemote, true, "--remote", "x", "--enable", "default_mode_request_user_input")
	add("refused option before -- and after it", "-c hooks.a: "+whyHooks, false, "-c", "hooks.a=1", "--", "--remote", "x")

	// Everything else passes.
	add("no arguments", "", false)
	add("a positional prompt", "", false, "fix the bug")
	add("-s", "", false, "-s", "workspace-write")
	add("-a", "", false, "-a", "never")
	add("--add-dir", "", false, "--add-dir", "/tmp/x")
	add("--dangerously-bypass-approvals-and-sandbox", "", false, "--dangerously-bypass-approvals-and-sandbox")
	add("--approve-for-me", "", false, "--approve-for-me")
	add("--dangerously-bypass-hook-trust", "", false, "--dangerously-bypass-hook-trust")
	add("-C", "", false, "-C", "/tmp/x")
	add("-p", "", false, "-p", "work")
	add("-h and -V", "", false, "-h", "-V")
	add("-hV cluster", "", false, "-hV")
	add("--no-daemon", "", false, "--no-daemon")
	add("--model", "", false, "--model", "gpt-5", "hello")
	add("a prompt after --", "", false, "--", "hello world")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCodexArgs(tc.args, tc.off)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("CheckCodexArgs(%q, %v) = %v; want it to pass", tc.args, tc.off, err)
				}
				return
			}
			if !errors.Is(err, ErrSpawnDeniedFlag) {
				t.Fatalf("CheckCodexArgs(%q, %v) = %v; want ErrSpawnDeniedFlag", tc.args, tc.off, err)
			}
			if got := strings.TrimPrefix(err.Error(), "ErrSpawnDeniedFlag: "); got != tc.want {
				t.Errorf("CheckCodexArgs(%q, %v) description = %q; want %q", tc.args, tc.off, got, tc.want)
			}
		})
	}
}
