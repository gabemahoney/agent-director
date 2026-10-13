package spawn

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/config"
)

// EnvAgent is the environment variable every Codex launch sets to
// EnvAgentCodex, and no Claude launch sets (SR-10.1). `agent-director help`
// reads it, with a SessionStart payload on stdin, to print Codex's structured
// SessionStart form (RN-2). The AGENT_DIRECTOR_ prefix is reserved, so no
// caller's extra_env can set or spoof it.
const (
	EnvAgent      = "AGENT_DIRECTOR_AGENT"
	EnvAgentCodex = "codex"
)

// codexBinary is the program tmux launches for a Codex agent when
// [launch] codex_path is not set. Held as a var so tests can swap it.
var codexBinary = "codex"

// codexProgram is the program a Codex launch (spawn, reuse, resume) puts first
// in the pane command: [launch] codex_path from the config in force at this
// launch when set, as one unchanged argument, else the program named codex,
// found on the pane's PATH (SR-5.0). Nothing about it is stored on the row.
func codexProgram(cfg config.Config) string {
	if cfg.Launch.CodexPath != "" {
		return cfg.Launch.CodexPath
	}
	return codexBinary
}

// codexSettingsBlock is the per-launch settings a Codex launch adds to args,
// the caller's (or the row's replayed) pass-through arguments: CodexSettingsArgs
// for the running binary, the installed path (only with inject_help_hook), the
// effective relay window, the config's inject_help_hook and disable_ask_user,
// cwd and the carried pre-trust outcome. An argument agent-director would add
// that args already carries before its first `--` is left out of the block
// (--no-daemon, --dangerously-bypass-hook-trust, --enable write_stdin_approval;
// a copy after `--` is the prompt and does not count). The only failures are a
// binary path that cannot be resolved, as for Claude's settings.
func codexSettingsBlock(args []string, cwd string, preTrust PreTrustOutcome, cfg config.Config) ([]string, error) {
	exe, err := executablePath()
	if err != nil {
		return nil, fmt.Errorf("resolve agent-director path: %w", err)
	}
	in := codexSettingsInput{
		Exe:         exe,
		Relay:       cfg.Relay.EffectiveTimeoutSeconds(),
		InjectHelp:  cfg.Defaults.InjectHelpHook,
		QuestionOff: cfg.Defaults.DisableAskUser,
		CWD:         cwd,
		PreTrustOK:  preTrust == PreTrustOK,
	}
	if in.InjectHelp {
		in.HelpBin, err = helpHookBinPath()
		if err != nil {
			return nil, fmt.Errorf("resolve help-hook binary path: %w", err)
		}
	}
	read := ReadCodexArgs(args)
	in.OmitNoDaemon = read.Has(codexOptNoDaemon)
	in.OmitBypass = read.Has(codexOptBypassHookTrust)
	in.OmitWriteStdin = read.HasEnable(codexFeatureWriteStdin)
	return CodexSettingsArgs(in), nil
}

// codexArgvTail returns args with block inserted after the last argument
// before the first `--`, or at the end when there is no `--` (the arguments
// from the `--` on, the prompt Codex reads, follow the block). Every setting
// of the block therefore comes after every pass-through option, so it wins
// (Codex applies the last -c of a key, and --enable/--disable after every -c,
// RN-13). The result is a new slice; args is not changed.
func codexArgvTail(args, block []string) []string {
	cut := len(args)
	if dd := ReadCodexArgs(args).DoubleDash; dd >= 0 {
		cut = dd
	}
	out := make([]string, 0, len(args)+len(block))
	out = append(out, args[:cut]...)
	out = append(out, block...)
	return append(out, args[cut:]...)
}

// composeCodexCommand composes the argv of a plain spawn's or a reuse's Codex
// launch (SR-5.1):
//
//	<codex> [pass-through before --] <settings block> [-- and what follows]
//
// with the settings block of codexSettingsBlock.
func composeCodexCommand(r Resolved, preTrust PreTrustOutcome, cfg config.Config) ([]string, error) {
	block, err := codexSettingsBlock(r.AgentArgs, r.CWD, preTrust, cfg)
	if err != nil {
		return nil, err
	}
	return append([]string{codexProgram(cfg)}, codexArgvTail(r.AgentArgs, block)...), nil
}

// composeCodexResumeCommand composes the argv of a Codex resume (SR-7.4,
// RN-20):
//
//	<codex> resume <session id> [replayed args before --] <settings block> [-- and what follows]
//
// Nothing of agent-director's stands before `resume` (Codex ignores settings
// there) and `-i` of the replayed arguments comes only after the id. The
// block is last before any `--`, so agent-director's question-tool setting
// wins over stored arguments that turn the tool on. ComposeRelaunch first
// extracts parsed session selectors and their owned values while retaining
// the original prompt token after --; this composer adds no spawn validation.
func composeCodexResumeCommand(sessionID string, args []string, cwd string, preTrust PreTrustOutcome, cfg config.Config) ([]string, error) {
	block, err := codexSettingsBlock(args, cwd, preTrust, cfg)
	if err != nil {
		return nil, err
	}
	cmd := []string{codexProgram(cfg), "resume", sessionID}
	return append(cmd, codexArgvTail(args, block)...), nil
}
