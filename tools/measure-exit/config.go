package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mode is how a run treats its agents.
type mode string

const (
	// modeReal spawns real Claude Code agents inside the measurement
	// container. It needs the container marker, enforces the 20-sample floor
	// on RN-6/RN-2 cases and the Claude Code version floor.
	modeReal mode = "real"
	// modeDry runs against the stub claude, in the measurement container or
	// the repo's Docker sandbox. Its results always carry the dry-run banner.
	modeDry mode = "dry"
	// modeProbe is the exec-form version probe: real Claude Code binaries
	// with no credentials. It is the only mode exempt from the version floor.
	modeProbe mode = "probe"
)

// minSamples is the per-case sample floor of the SRD's RN-6 and RN-2 methods
// ("at least 20 each"). Real mode refuses fewer for a sampled case; RN-9 and
// the version probe run one agent per scenario and are exempt.
const minSamples = 20

// sampleBuffer is how many samples per case a run takes beyond the floor
// (user, 2026-10-02: L1 takes 22 per case): decide reads every completed
// sample once a case has minSamples, so a flaky sample (a failed spawn, an
// agent that never reported in) does not force a re-run.
const sampleBuffer = 2

// defaultSamples is the per-case sample count when -samples is not given:
// the floor plus the buffer.
const defaultSamples = minSamples + sampleBuffer

// minClaudeCodeVersion is the stated minimum Claude Code: the oldest that
// agent-director supports with exec-form hooks (command + args), as the
// README's Prerequisites state it. Gabe (2026-10-02) set it to 2.1.280, the
// version this fleet's workers run, which L0 showed runs exec-form hooks.
// decide compares the probe's measured minimum against it.
const minClaudeCodeVersion = "2.1.280"

// realModeMinClaudeCode is the oldest Claude Code real mode accepts: the
// version this fleet's workers run (user, 2026-10-01), which L1 and L2
// measure by default. The L0 probe tests it explicitly. Below it, hooks are
// expected to write no_exec_form and no row would report in, so real mode
// refuses it. It equals the stated minimum (minClaudeCodeVersion) but is
// its own constant: the floor is the measured fleet's version, the minimum
// what the README states.
const realModeMinClaudeCode = "2.1.280"

// Defaults for the per-sample bounds. The ceiling must stay well above the
// largest raised SessionEnd budget so a slow but finite exit is measured,
// never cut off.
const (
	defaultSampleCeiling = 120 * time.Second
	defaultReadyTimeout  = 90 * time.Second
	// defaultRaisedHookTimeoutSeconds is the per-hook SessionEnd `timeout`
	// (Claude Code reads it in seconds) of the raised-budget layer.
	defaultRaisedHookTimeoutSeconds = 10
	// defaultRaisedEnvTimeoutMS is CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS in
	// the env-raised variant.
	defaultRaisedEnvTimeoutMS = 10000
	// defaultSlowHookMS is how long the raised-budget layer's own SessionEnd
	// hook runs: above Claude Code's default budget and below the raised one,
	// so raising the budget has an observable effect.
	defaultSlowHookMS = 4000
	// defaultStepTimeout bounds each RN-9 drive step (a turn, a /clear or
	// /compact, an agent team settling) and the probe's wait for its hook.
	defaultStepTimeout = 5 * time.Minute
	// defaultNotificationWait bounds the RN-9 drive's wait for a
	// Notification; Claude Code sends its idle notification after about a
	// minute at the prompt.
	defaultNotificationWait = 90 * time.Second
	// defaultSettleQuiet is how long an agent team must stay silent (no
	// recorded hook) after the lead's Stop before the team counts as settled.
	defaultSettleQuiet = 20 * time.Second
	// defaultInputReadyTimeout bounds an agent team lead's wait for its
	// prompt box (capture.go) before the team prompt is sent; past it the
	// scenario records "input never ready" with the pane text.
	defaultInputReadyTimeout = 60 * time.Second
	// defaultPromptAcceptWait bounds the wait for the lead's own
	// UserPromptSubmit after the team prompt; Claude Code fires it before
	// any model call, so a lead that does not is not taking input, and the
	// scenario stops there with the pane text instead of waiting out
	// -step-timeout.
	defaultPromptAcceptWait = 60 * time.Second
)

// defaultMidTurnPrompt is the mid-turn cases' initial prompt: a long text
// answer that needs no tool and no permission, so the agent is still in its
// turn (state working) when its pane is killed.
const defaultMidTurnPrompt = "Count from 1 to 3000, one number per line, with no other text."

// dryDefaults are the bounds a dry run uses for every flag it was not
// given: a small sample count and short ceilings, with the raised budgets
// still above the stub's default 1.5 s SessionEnd budget and the slow hook
// between the two (c7).
var dryDefaults = map[string]string{
	"samples":               "2",
	"raised-hook-timeout":   "3",
	"raised-env-timeout-ms": "3000",
	"slow-hook-ms":          "2000",
	"sample-ceiling":        "15s",
	"ready-timeout":         "30s",
	"step-timeout":          "30s",
	"notification-wait":     "15s",
	"settle-quiet":          "2s",
	"input-ready-timeout":   "10s",
	"prompt-accept-wait":    "10s",
}

// config is one run's settings, parsed from the run subcommand's flags.
type config struct {
	mode    mode
	samples int
	// cases are the selected case ids, in run order; empty selects every
	// case of the mode in dry and probe mode, and is refused in real mode
	// (selectCases).
	cases []string
	// outDir receives results.json, the table, the run log and the
	// identifiers file. It must exist or be creatable.
	outDir string
	// agentDirector and claude are the binaries the run uses: a path or a
	// name resolved on PATH.
	agentDirector string
	claude        string
	// workDir is the root of the harness-owned per-case working
	// directories (generated .claude/settings.local.json layers go there).
	workDir string
	// mcpConfig is the operator-supplied MCP configuration (a staged
	// read-only copy), passed to the agents as --mcp-config. Empty: none.
	mcpConfig string
	// tmuxBase is the directory the private TMUX_TMPDIR is created in. A
	// short path keeps the socket under the unix-socket length limit.
	tmuxBase string
	// runID names this run in the results and in the identifiers the
	// busy-host guard scans for. The host runner passes its own.
	runID string

	// projectSettings and localSettings are the operator's staged
	// read-only copies of the deployment's project and local layers. Each
	// agent's fresh working directory gets a copy of them as its
	// .claude/settings.json and .claude/settings.local.json; the staged
	// files are never edited. Empty: none.
	projectSettings string
	localSettings   string
	// midTurnPrompt is the mid-turn cases' initial prompt.
	midTurnPrompt string

	raisedHookTimeoutSeconds int
	raisedEnvTimeoutMS       int
	slowHookMS               int
	sampleCeiling            time.Duration
	readyTimeout             time.Duration
	// stepTimeout, notificationWait and settleQuiet bound the RN-9 drive
	// and the version probe (see their defaults).
	stepTimeout      time.Duration
	notificationWait time.Duration
	settleQuiet      time.Duration
	// inputReadyTimeout and promptAcceptWait bound an agent team lead's
	// wait for its prompt box and for its UserPromptSubmit after the team
	// prompt (see their defaults).
	inputReadyTimeout time.Duration
	promptAcceptWait  time.Duration
}

// errUsage marks a flag error; the run subcommand exits exitUsage for it.
var errUsage = errors.New("usage")

// parseRunFlags parses the run subcommand's arguments. Flag errors and
// inconsistent values wrap errUsage; the 20-sample floor and the version
// floor are preflight refusals, not flag errors, because they depend on the
// selected cases and the installed Claude Code.
func parseRunFlags(args []string, stderr io.Writer, newRunID func() string) (config, error) {
	var (
		c        config
		modeRaw  string
		casesRaw string
	)
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&modeRaw, "mode", "", "real, dry or probe (required)")
	fs.IntVar(&c.samples, "samples", defaultSamples, "samples per RN-6/RN-2 case (real mode: at least 20)")
	fs.StringVar(&casesRaw, "cases", "", "comma-separated case ids (required in real mode; dry and probe default to every case of the mode)")
	fs.StringVar(&c.outDir, "out", "", "results directory (required)")
	fs.StringVar(&c.agentDirector, "agent-director", "agent-director", "agent-director binary (path or name on PATH)")
	fs.StringVar(&c.claude, "claude", "claude", "claude binary whose version is recorded (path or name on PATH)")
	fs.StringVar(&c.workDir, "workdir", "", "root of the harness-owned agent working directories (default: $HOME/measure-exit-work)")
	fs.StringVar(&c.mcpConfig, "mcp-config", "", "MCP configuration passed to the MCP cases as --mcp-config")
	fs.StringVar(&c.tmuxBase, "tmux-base", os.TempDir(), "directory the private TMUX_TMPDIR is created in")
	fs.StringVar(&c.runID, "run-id", "", "run id recorded in the results and identifiers file (default: generated)")
	fs.IntVar(&c.raisedHookTimeoutSeconds, "raised-hook-timeout", defaultRaisedHookTimeoutSeconds, "per-hook SessionEnd timeout, seconds, of the raised-budget layer")
	fs.IntVar(&c.raisedEnvTimeoutMS, "raised-env-timeout-ms", defaultRaisedEnvTimeoutMS, "CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS of the env-raised variant")
	fs.IntVar(&c.slowHookMS, "slow-hook-ms", defaultSlowHookMS, "run time of the raised-budget layer's own SessionEnd hook, ms")
	fs.DurationVar(&c.sampleCeiling, "sample-ceiling", defaultSampleCeiling, "per-sample bound after which the sample is recorded as did_not_exit")
	fs.DurationVar(&c.readyTimeout, "ready-timeout", defaultReadyTimeout, "bound on an agent reporting in before the sample fails as did_not_reach_prompt")
	fs.StringVar(&c.projectSettings, "project-settings", "", "staged copy of the deployment's project layer, copied into each agent's working directory")
	fs.StringVar(&c.localSettings, "local-settings", "", "staged copy of the deployment's local layer, copied into each agent's working directory (refused in real mode beside a generated layer)")
	fs.StringVar(&c.midTurnPrompt, "midturn-prompt", defaultMidTurnPrompt, "initial prompt of the mid-turn cases")
	fs.DurationVar(&c.stepTimeout, "step-timeout", defaultStepTimeout, "bound on each RN-9 drive step and the version probe's hook")
	fs.DurationVar(&c.notificationWait, "notification-wait", defaultNotificationWait, "bound on the RN-9 drive's wait for a Notification")
	fs.DurationVar(&c.settleQuiet, "settle-quiet", defaultSettleQuiet, "silence after the lead's Stop before an agent team counts as settled")
	fs.DurationVar(&c.inputReadyTimeout, "input-ready-timeout", defaultInputReadyTimeout, "bound on an agent team lead's prompt box showing before the team prompt is sent")
	fs.DurationVar(&c.promptAcceptWait, "prompt-accept-wait", defaultPromptAcceptWait, "bound on the lead's UserPromptSubmit after the team prompt")
	if err := fs.Parse(args); err != nil {
		return c, fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("%w: unexpected arguments %q", errUsage, fs.Args())
	}
	if mode(modeRaw) == modeDry {
		if err := applyDryDefaults(fs); err != nil {
			return c, err
		}
	}
	c.mode = mode(modeRaw)
	c.cases = splitList(casesRaw)
	if c.runID == "" {
		c.runID = newRunID()
	}
	return c, c.validate()
}

// validate checks the values that do not depend on the environment.
func (c config) validate() error {
	switch c.mode {
	case modeReal, modeDry, modeProbe:
	case "":
		return fmt.Errorf("%w: -mode is required (real, dry or probe)", errUsage)
	default:
		return fmt.Errorf("%w: -mode %q: want real, dry or probe", errUsage, c.mode)
	}
	if c.outDir == "" {
		return fmt.Errorf("%w: -out is required", errUsage)
	}
	if c.samples < 1 {
		return fmt.Errorf("%w: -samples %d: want at least 1", errUsage, c.samples)
	}
	for _, f := range []struct{ name, path string }{
		{"mcp-config", c.mcpConfig}, {"project-settings", c.projectSettings}, {"local-settings", c.localSettings},
	} {
		if f.path != "" && !filepath.IsAbs(f.path) {
			return fmt.Errorf("%w: -%s %q: want an absolute path", errUsage, f.name, f.path)
		}
	}
	if strings.TrimSpace(c.midTurnPrompt) == "" {
		return fmt.Errorf("%w: -midturn-prompt must not be empty", errUsage)
	}
	if c.stepTimeout <= 0 || c.notificationWait < 0 || c.settleQuiet <= 0 {
		return fmt.Errorf("%w: -step-timeout and -settle-quiet must be positive, -notification-wait not negative", errUsage)
	}
	if c.inputReadyTimeout <= 0 || c.promptAcceptWait <= 0 {
		return fmt.Errorf("%w: -input-ready-timeout and -prompt-accept-wait must be positive", errUsage)
	}
	if c.raisedHookTimeoutSeconds < 1 || c.raisedEnvTimeoutMS < 1 || c.slowHookMS < 1 {
		return fmt.Errorf("%w: the raised budgets and the slow hook's run time must be positive", errUsage)
	}
	hook, env := c.raisedBudgets()
	if slow := time.Duration(c.slowHookMS) * time.Millisecond; slow >= hook || slow >= env {
		return fmt.Errorf("%w: -slow-hook-ms %d must stay below both raised budgets", errUsage, c.slowHookMS)
	}
	if largest := max(hook, env); c.sampleCeiling <= largest {
		return fmt.Errorf("%w: -sample-ceiling %s must exceed the largest raised budget %s", errUsage, c.sampleCeiling, largest)
	}
	if c.readyTimeout <= 0 {
		return fmt.Errorf("%w: -ready-timeout must be positive", errUsage)
	}
	return nil
}

// raisedBudgets returns the two raised SessionEnd budgets: the per-hook
// timeout and the CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS value.
func (c config) raisedBudgets() (hook, env time.Duration) {
	return time.Duration(c.raisedHookTimeoutSeconds) * time.Second,
		time.Duration(c.raisedEnvTimeoutMS) * time.Millisecond
}

// applyDryDefaults sets every dryDefaults flag the command line did not
// give, so a plain dry run is small and fast while an explicit flag still
// wins.
func applyDryDefaults(fs *flag.FlagSet) error {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	for name, value := range dryDefaults {
		if given[name] {
			continue
		}
		if err := fs.Set(name, value); err != nil {
			return fmt.Errorf("%w: dry default -%s %s: %v", errUsage, name, value, err)
		}
	}
	return nil
}

// splitList splits a comma-separated flag value, dropping empty items.
func splitList(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
