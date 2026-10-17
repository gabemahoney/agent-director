// Command agent-director is the CLI entrypoint for the agent-director tool.
//
// This file provides the argv dispatch skeleton and the startup wiring that
// every invocation performs (config load + store open). Real verb handlers
// live in pkg/api; this file marshals their results to JSON per
// SRD §12.3.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// errorEnvelope is the JSON shape emitted on stderr for CLI-level errors.
// Matches SRD §12.2 / §13.1. ErrDetails is the optional err_details object
// (b.146 rule 15, decision 8 A): the facts of a refusal that carries them
// (pkgapi.ErrDetails), omitted otherwise; a caller that does not know the
// key ignores it.
type errorEnvelope struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
	ErrDetails     any    `json:"err_details,omitempty"`
}

// CLI-internal error names. These signal dispatch and output failures of the
// CLI itself, not errors a verb returns, so no verb lists them:
// errUnknownVerb for a verb the CLI does not know, errJSONMarshal only for a
// json.Marshal failure of a verb's result. They are in
// pkg/api/errnames.Catalog (with internal/clisetup's sentinels, b.cm7) so
// the TS client knows them.
const (
	errUnknownVerb = "ErrUnknownVerb"
	errJSONMarshal = "ErrJSONMarshal"
)

// errDispatch is a sentinel returned by handlers when they have already
// written a JSON error envelope to stderr. run() uses this to set the exit
// code without double-printing.
var errDispatch = errors.New("dispatch error")

// configPath is the canonical TOML config location.
const configPath = clisetup.ConfigPath

// verbAliases maps each alias of a verb to that verb: --help and -h run help,
// --version and -v run version (b.fv2), the flags most CLIs accept, so people
// and scripts try them first. run() replaces an alias with its verb when it is
// the first argument after the global flags, before any dispatch, so an alias
// runs exactly as its verb does (same stdout, same exit code, no store); an
// alias anywhere else is no alias. They are no verbs of their own: no manifest
// entry and no handlers() entry names them, and help's and version's
// manifest descriptions list them.
var verbAliases = map[string]string{
	"--help":    "help",
	"-h":        "help",
	"--version": "version",
	"-v":        "version",
}

// handlers maps verb names to their implementations. Verb aliases (--help,
// -h, --version, -v) are not in this table: run() has already replaced them
// with their verbs (verbAliases). client and cfg are captured in closures so
// each verb sees the same already-opened Client — construction is done once
// in run() via setupClient(). o is the run's --store-path, --tmux-command and
// --create-if-missing overrides setupClient opened client with; serve builds
// its separate MCP Client from them too (b.wb7, b.78b).
//
// `hook` is intentionally NOT in this table — runHook() short-circuits
// the dispatch loop before setupClient() so hook fires can't be blocked
// by config/store failures (SRD §3.2 fail-open invariant).
//
// The DB-free static-data verbs `help` and `version` remain in this
// table for the unknown-verb-free lookup shape, but run() dispatches them (and
// the no-verb case: help, or nothing for a hook payload on stdin, SR-22.9)
// BEFORE setupClient with a zero-value Client, so on the normal path these
// entries are never reached (SR-4.1/4.2, b.93m). Their
// closures here are the store-backed fallback only and behave identically —
// helpHandler ignores its client and versionHandler consults no store.
func handlers(client *pkgapi.Client, cfg config.Config, o clisetup.Overrides) map[string]func([]string) error {
	return map[string]func([]string) error{
		"help":               func(args []string) error { return helpHandler(client, args) },
		"cursor-host":        cursorHostHandler,
		"version":            func(args []string) error { return versionHandler(client, args) },
		"spawn":              func(args []string) error { return spawnHandlerWith(client, args) },
		"status":             func(args []string) error { return statusHandlerWith(client, args) },
		"get":                func(args []string) error { return getHandlerWith(client, args) },
		"send-keys":          func(args []string) error { return sendKeysHandlerWith(client, args) },
		"read-pane":          func(args []string) error { return readPaneHandlerWith(client, args) },
		"kill":               func(args []string) error { return killHandlerWith(client, args) },
		"pause":              func(args []string) error { return pauseHandlerWith(client, args) },
		"list":               func(args []string) error { return listHandlerWith(client, args) },
		"list-subagents":     func(args []string) error { return listSubagentsHandlerWith(client, args) },
		"make-template":      func(args []string) error { return makeTemplateHandlerWith(client, args) },
		"decide":             func(args []string) error { return decideHandlerWith(client, args) },
		"get-permission":     func(args []string) error { return getPermissionHandlerWith(client, args) },
		"record-pane-answer": func(args []string) error { return recordPaneAnswerHandlerWith(client, args) },
		"resume":             func(args []string) error { return resumeHandlerWith(client, args) },
		"find-missing":       func(args []string) error { return findMissingHandlerWith(client, args) },
		"expire":             func(args []string) error { return expireHandlerWith(client, args) },
		"serve":              func(args []string) error { return serveHandlerWith(cfg, o, pkgapi.New, args) },
		"trail-emit":         func(args []string) error { return trailEmitHandlerWith(args) },
	}
}

// hookExitCode is the exit code the hook verb always uses.
//
// State-tracking hooks fail-open per SRD §3.2: any internal failure logs
// to the error log and the process exits 0 with no stdout. The relay-mode
// permission decision envelope (Epic 10) will branch off this contract;
// for Epic 3 every hook event takes the state-tracking fail-open path.
const hookExitCode = 0

// runHook is the hook-verb entry point. It is invoked directly from run()
// before the normal store-setup-and-dispatch path so that *every* failure
// mode (missing config, store open failure, malformed payload, DB
// unreachable) yields exit 0 with empty stdout (state-tracking) or a
// deny envelope (relay-on per SRD §6.4 fail-closed boundary).
//
// The relay-active determination is made FROM ENV (AGENT_DIRECTOR_RELAY_MODE)
// before any disk I/O — SRD §6.5 — so even a store-open failure on a
// relay-on Spawn still emits a valid deny envelope.
//
// SRD §3.2 EXEMPTION: runHook retains its own config.Load + store.Open calls
// and does NOT go through setupClient. This is required by SRD §3.2 fail-open:
// hook fires must never be blocked by config or store failures. The pkg/api.Client
// startup path is intentionally bypassed here; its store-path resolution is
// not: both take the store from config.Store.EffectiveDbPath (b.8up), and its
// busy timeout from config.Store.EffectiveBusyTimeoutMs (b.c7f).
//
// The relay hook's kill instant is its start plus the per-hook timeout spawn
// wrote for it (b.146 rule 4), and the main agent's Stop or idle-prompt
// Notification proves gone only the requests recorded before its start
// (b.omt), so the start is read first, and the timeout comes from the hook's
// --timeout argument (parseHookArgs).
//
// The function never returns an error; it logs and returns.
func runHook() int {
	start := time.Now()
	stdout := os.Stdout
	relayActive := os.Getenv(hook.EnvRelayMode) == hook.RelayModeOn

	// Peek the hook event name before any other I/O so the early
	// fail-closed path can gate its envelope write on event type
	// (b.45p). Stdin is consumed here and passed to Handle via a
	// bytes.Reader so Handle's own ReadPayload still works.
	//
	// A stdin-read failure leaves stdinRaw empty and eventName "",
	// which forces earlyFailClosed into silent mode — fail-open is
	// the correct b.45p-safe default when we don't know what the
	// event was. Handle's own ReadPayload will then also fail and
	// log, exiting silently.
	stdinRaw, _ := io.ReadAll(io.LimitReader(os.Stdin, hook.MaxPayloadBytes+1))
	eventName := hook.PeekEventName(stdinRaw)

	// The old-layout refusal comes before the logger, which creates
	// errors.log, and before the trail and the store: a refused hook creates
	// nothing. It fails open (exit 0, one diagnostic line on stderr) and a
	// relayed PermissionRequest still gets its deny, as for any failure
	// before Handle.
	if err := hookOldLayout(); err != nil {
		hookLog(log.New(os.Stderr, "agent-director-hook ", log.LstdFlags), "hook: %v", err)
		if relayActive && eventName == hook.EventNamePermissionRequest {
			fmt.Fprintln(stdout, hook.EncodeDecision(eventName, "deny", ""))
		}
		return hookExitCode
	}

	logger := newHookLogger()
	// Wire the hook logger into the trail writer so emit failures and
	// ts-substitution warnings reach cfg.Log.ErrorLogPath rather than
	// being silently discarded. SetLogger is safe to call before the
	// first Emit (SR-A-7.6).
	trail.SetLogger(logger)

	// Pre-Handle fail-closed: if relay is active AND we know this is
	// a PermissionRequest event AND we can't get far enough to even
	// invoke hook.Handle, emit deny here. Handle itself owns
	// fail-closed past this point. The eventName gate is critical
	// for b.45p — emitting a deny envelope from a non-PermissionRequest
	// process causes Claude Code to apply the deny to the in-flight
	// tool, racing the legitimate PermissionRequest sibling process.
	earlyFailClosed := func(why string) {
		hookLog(logger, "hook: %s", why)
		if relayActive && eventName == hook.EventNamePermissionRequest {
			fmt.Fprintln(stdout, hook.EncodeDecision(eventName, "deny", ""))
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		earlyFailClosed(fmt.Sprintf("load config: %v", err))
		return hookExitCode
	}
	// The same store every verb opens: EffectiveDbPath is pkg/api.New's
	// tiers 2 and 3 (the hook takes no --store-path), so an empty db_path
	// gives the default store, never a file in Claude's cwd (b.8up).
	dbPath, err := cfg.Store.EffectiveDbPath()
	if err != nil {
		earlyFailClosed(fmt.Sprintf("resolve store path: %v", err))
		return hookExitCode
	}
	// Cursor's owned pane carries the actual opened store path, including a
	// per-client override. This is only routing; the direct-parent, launch
	// and runtime gates still prove authority independently.
	if parseHookArgs(os.Args[2:]).agent == store.AgentCursor {
		if path := os.Getenv("AGENT_DIRECTOR_CURSOR_STORE_PATH"); path != "" {
			dbPath = path
		}
	}
	// With the busy timeout every verb's open uses too (b.c7f).
	var st *store.Store
	if parseHookArgs(os.Args[2:]).agent == store.AgentCursor {
		st, err = store.OpenWithBusyTimeout(dbPath, cfg.Store.EffectiveBusyTimeoutMs())
	} else {
		st, err = store.OpenOrInitWithBusyTimeout(dbPath, cfg.Store.EffectiveBusyTimeoutMs())
	}
	if err != nil {
		earlyFailClosed(fmt.Sprintf("open store: %v", err))
		return hookExitCode
	}
	defer st.Close()
	// The settle time this hook's state write judges the value it replaces
	// with (b.3ie), from the config loaded above: part of the hook's one
	// UPDATE, so no extra read, statement or wait.
	st.SetSettleMs(int64(cfg.State.EffectiveSettleMs()))

	// The hook gate's parent process (SR-22.9): with exec-form hooks
	// getppid() is the agent process; its start time comes from the
	// start-time reader, its command name (for ad.hook.ignored and
	// ad.hook.pane_is_grandparent only, SR-14) from the command-name reader,
	// and its parent pid (for ad.hook.pane_is_grandparent's check only,
	// b.9n6, b.zde) from the parent-pid reader. Now and the loaded config's
	// effective pending grace period bound SessionStart's wait for its
	// launch's identity write, which the hook also caps at 540 s from its
	// start on Now's monotonic reading (SR-22.9, SR-13.4; WD 2026-09-30c):
	// time.Now is passed as is, never stripped by .UTC or .Round(0).
	//
	// The relay hook's timing and identity (b.146 rules 2, 4 and 14): Start,
	// read above before anything else, and RelayTimeout, the --timeout spawn
	// wrote, give its kill instant; Start also cuts a Stop's or idle prompt's
	// turn_end proof (b.omt); Self reads its own pid, start time and
	// pid namespace once, when it records its request. RelayHookGone judges a
	// request's relay hook for the PostToolUse close (b.146 rule 13), with
	// the same readers and relay window the verbs judge it with.
	args := parseHookArgs(os.Args[2:])
	hc := hook.HandleConfig{
		Agent:                args.agent,
		Env:                  hook.OSGetenv,
		Cfg:                  cfg.Relay,
		Clock:                hook.DefaultPollClock(),
		ParentPID:            os.Getppid,
		ParentProc:           hookParentProc(),
		VerifyCursor:         hookCursorVerifier(cfg),
		VerifyCursorRotation: hookCursorRotationVerifier(cfg),
		Now:                  time.Now,
		PendingGrace:         cfg.Tmux.EffectivePendingGrace(),
		Start:                start,
		RelayTimeout:         args.timeout,
		Self:                 hookSelf(probe.NewProcChecker()),
		RelayHookGone:        hookRelayHookGone(cfg.Relay),
	}
	if err := hook.Handle(context.Background(), bytes.NewReader(stdinRaw), stdout, st, hc, logger); err != nil {
		hookLog(logger, "hook: handle: %v", err)
	}
	return hookExitCode
}

// hookAgentNoValue is the agent value runHook hands Handle when --agent is
// given with no value or an empty one: no agent, so an invalid one (a hook
// failure), where an absent --agent reads "" and means claude.
const hookAgentNoValue = "(no value)"

// agentFlag is the hook verb's --agent value: the string as given, and
// whether the flag was given at all.
type agentFlag struct {
	val string
	set bool
}

func (a *agentFlag) String() string {
	if a == nil {
		return ""
	}
	return a.val
}

func (a *agentFlag) Set(s string) error {
	a.val, a.set = s, true
	return nil
}

// lenientValue wraps a flag.Value whose Set never fails the parse: a value it
// rejects leaves the wrapped value as it was.
type lenientValue struct{ flag.Value }

func (l lenientValue) Set(s string) error {
	_ = l.Value.Set(s)
	return nil
}

// isAgentFlagArg reports whether arg is the --agent flag by name, in either
// dash form and with no value attached (-agent, --agent).
func isAgentFlagArg(arg string) bool {
	return strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-") == "agent" && strings.HasPrefix(arg, "-")
}

// hookArgs is the hook verb's parsed command line.
type hookArgs struct {
	// agent is the raw --agent value handed to Handle: "" when the flag is
	// absent (claude), hookAgentNoValue when it has no value or an empty one.
	agent string
	// timeout is --timeout's per-hook timeout; 0 when absent, unparseable or
	// not positive (Handle then reads the loaded config's relay window).
	timeout time.Duration
}

// parseHookArgs parses the hook verb's arguments in one pass, --agent and
// --timeout in either order (b.146 rule 4; SR-6.1): --agent claude|codex|cursor names
// the agent whose hook table applies and --timeout N is the per-hook timeout in
// seconds spawn writes on the relay hook's settings entry. The hook path never
// fails on its arguments (SRD §3.2): --timeout absent, unparseable or not
// positive gives 0, and an argument it does not know, a positional one
// included, is skipped without costing --agent or --timeout its value, which
// the rest of the line still gives. --agent's value is not checked here:
// Handle rejects one other than claude, codex or cursor, as a hook failure.
func parseHookArgs(args []string) hookArgs {
	var seconds int
	var agent agentFlag
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// An unparseable --timeout value is no parse error: it leaves seconds 0, so
	// the only parse errors are an unknown flag, a flag with no value left to
	// take and a malformed flag.
	fs.Var(lenientValue{newDecimalInt(&seconds, 0)}, "timeout", "the per-hook timeout in seconds spawn wrote for this hook (the relay hook's kill instant is its start plus it)")
	fs.Var(&agent, "agent", "the agent whose hook this is, claude, codex or cursor (absent: claude)")
	rest := args
	for len(rest) > 0 {
		err := fs.Parse(rest)
		after := fs.Args()
		switch {
		case err == nil && len(after) > 0:
			// Parse stopped at a positional argument: skip it.
			rest = after[1:]
			continue
		case err != nil && len(after) == len(rest):
			// A malformed argument (---x, -=x) fails without being consumed:
			// skip it, so every pass advances.
			rest = rest[1:]
			continue
		case err != nil && len(after) == 0 && isAgentFlagArg(rest[len(rest)-1]):
			// The last argument is --agent, with no value left to take.
			agent.set, agent.val = true, ""
		}
		// A parse error leaves in after what follows the argument that failed
		// (an unknown flag, or a flag and its value); carry on from there.
		rest = after
	}
	out := hookArgs{agent: agent.val}
	if agent.set && agent.val == "" {
		out.agent = hookAgentNoValue
	}
	if seconds > 0 && seconds <= config.MaxRelayTimeoutSeconds {
		out.timeout = time.Duration(seconds) * time.Second
	}
	return out
}

// hookSelf returns the hook's reader of its own identity (b.146 rules 2 and
// 14): its pid, its start time through pc (field 22 of /proc/<pid>/stat on
// Linux, the form proc_starttime records) and its pid namespace
// (probe.SelfPIDNamespace). When either the start time or the namespace
// cannot be read it returns the zero identity, which records none: readers
// then fall back to the request's settle instant.
func hookSelf(pc probe.ProcChecker) func() store.ProcessIdentity {
	return func() store.ProcessIdentity {
		pid := os.Getpid()
		start, alive, known := pc.StartTime(pid)
		ns, nsKnown := probe.SelfPIDNamespace()
		if !alive || !known || start == "" || !nsKnown {
			return store.ProcessIdentity{}
		}
		return store.ProcessIdentity{PID: pid, Starttime: start, PIDNamespace: ns}
	}
}

// hookRelayHookGone returns the hook's judge of a permission request's relay
// hook (b.146 rules 5, 13 and 14): pkgapi.RelayHookGone with the per-OS
// start-time reader, this process's pid namespace, time.Now and relay's
// effective window, the view every verb judges relay hooks with.
func hookRelayHookGone(relay config.Relay) func(store.PermissionRow) bool {
	view := pkgapi.RelayView{
		Procs:        probe.NewProcChecker(),
		PIDNamespace: probe.SelfPIDNamespace,
		Now:          time.Now,
		Window:       time.Duration(relay.EffectiveTimeoutSeconds()) * time.Second,
	}
	return func(pr store.PermissionRow) bool { return pkgapi.RelayHookGone(view, pr) }
}

// hookParentProc is the hook's parent-process reader: the per-OS start-time
// reader (the gate, SR-22.9), command-name reader (ad.hook.ignored's
// parent_command, SR-14, and ad.hook.pane_is_grandparent's commands only) and
// parent-pid reader (ad.hook.pane_is_grandparent's check only, b.9n6, b.zde).
// runHook and a no-verb run's hook check (noVerbHookIgnored) share it.
func hookParentProc() hook.ParentProc {
	return struct {
		probe.ProcChecker
		probe.CommandNameReader
		probe.ParentPIDReader
	}{probe.NewProcChecker(), probe.NewCommandNameReader(), probe.NewParentPIDReader()}
}

// hookOldLayout is the hook's old-layout check: store.ErrOldLayout (wrapped)
// when the hook is about to open the default store (the config loads and its
// file sets no [store] db_path), no store exists there and the old directory
// is present, nil otherwise. A config that fails to load is not judged here:
// the hook's own load reports it. It creates nothing.
func hookOldLayout() error {
	if parseHookArgs(os.Args[2:]).agent == store.AgentCursor && os.Getenv("AGENT_DIRECTOR_CURSOR_STORE_PATH") != "" {
		return nil
	}
	cfg, err := config.Load(configPath)
	if err != nil || cfg.Store.DbPathSet {
		return nil
	}
	return store.CheckOldLayout()
}

// newHookLogger opens the configured error_log_path (best-effort) and
// returns a *log.Logger writing to it. On any open failure it falls back
// to stderr — the hook MUST still log somewhere because diagnostic
// silence on the hot path is harder to debug than a stderr blast.
//
// SRD §3.2 EXEMPTION (second exempt site): newHookLogger calls config.Load
// independently so the hook-path logger can be constructed before any other
// disk I/O fails. This is intentional and must not be collapsed into
// setupClient's load.
func newHookLogger() *log.Logger {
	cfg, err := config.Load(configPath)
	if err != nil {
		return log.New(os.Stderr, "agent-director-hook ", log.LstdFlags)
	}
	if cfg.Log.ErrorLogPath == "" {
		return log.New(os.Stderr, "agent-director-hook ", log.LstdFlags)
	}
	f, err := os.OpenFile(cfg.Log.ErrorLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return log.New(os.Stderr, "agent-director-hook ", log.LstdFlags)
	}
	// Best-effort: the file is intentionally leaked for the lifetime of
	// the hook fire (short-lived process; the OS reclaims the fd on exit).
	return log.New(f, "agent-director-hook ", log.LstdFlags)
}

// hookLog is a small wrapper so the hook code path uses one log-line
// format consistently. Callers pass nil for logger only in tests; in
// production newHookLogger always returns a non-nil *log.Logger.
func hookLog(logger *log.Logger, format string, args ...any) {
	if logger == nil {
		return
	}
	logger.Printf(format, args...)
}

// helpResult is the top-level JSON envelope for the help verb. The single
// "verbs" field mirrors the manifest's ResultFields for the help verb.
type helpResult struct {
	Verbs []pkgapi.VerbSummary `json:"verbs"`
}

// helpHandler implements the help verb. `help` is Callable:false in the
// manifest — it does NOT route through the Client facade. It calls
// pkg/api.Help() directly.
//
// It prints the plain form: the help JSON on one line. Only when the caller is
// a Codex agent's SessionStart hook (codexSessionStartHook) does it print the
// structured form Codex accepts as SessionStart output, with the plain form as
// its additionalContext. Every other case prints the plain form, unchanged.
func helpHandler(_ *pkgapi.Client, _ []string) error {
	verbs, err := pkgapi.Help()
	if err != nil {
		// pkg/api.Help never errors today, but if a future implementation
		// changes that, name the error as every verb handler does
		// (errnames.Classify: its Catalog name, else ErrInternal), never
		// ErrJSONMarshal, which is only for the json.Marshal below (b.3jc).
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	payload, err := json.Marshal(helpResult{Verbs: verbs})
	if err != nil {
		if werr := writeError(os.Stderr, errJSONMarshal, err.Error()); werr != nil {
			return werr
		}
		return errDispatch
	}
	out := string(payload)
	if codexSessionStartHook(os.Getenv, os.Stdin, noVerbStdinDeadline) {
		structured, merr := json.Marshal(codexHelpOutput{HookSpecificOutput: codexHelpSpecific{
			HookEventName:     "SessionStart",
			AdditionalContext: out,
		}})
		if merr == nil {
			out = string(structured)
		}
	}
	if _, err := fmt.Fprintln(os.Stdout, out); err != nil {
		return err
	}
	return nil
}

// codexHelpOutput is the structured SessionStart output of `help` for a Codex
// agent: {"hookSpecificOutput":{"hookEventName":"SessionStart",
// "additionalContext":"<the plain help output>"}}.
type codexHelpOutput struct {
	HookSpecificOutput codexHelpSpecific `json:"hookSpecificOutput"`
}

// codexHelpSpecific is the hookSpecificOutput object of codexHelpOutput.
type codexHelpSpecific struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// codexSessionStartHook reports whether `help` was run by a Codex agent's
// SessionStart hook: the launch's signal (spawn.EnvAgent equal to
// spawn.EnvAgentCodex, set on every Codex launch and never on a Claude one)
// AND a hook payload on stdin whose hook_event_name is SessionStart, any
// source included (compact too). A Codex agent that runs `agent-director help`
// in its own shell has the signal but no such payload, so it gets the plain
// form. It reads stdin only when the signal is set, through the bounded reader
// (not a terminal, hook.MaxPayloadBytes, deadline), so it never blocks past
// the deadline and writes nothing. Every failure (a terminal, empty input,
// input held open past the deadline, not JSON, another event, no signal)
// reports false: the caller prints the plain form.
func codexSessionStartHook(getenv func(string) string, stdin *os.File, deadline time.Duration) bool {
	if getenv(spawn.EnvAgent) != spawn.EnvAgentCodex {
		return false
	}
	raw, ok := readNoVerbStdin(stdin, deadline)
	if !ok {
		return false
	}
	event, ok := hook.PayloadEventName(raw)
	return ok && event == "SessionStart"
}

// versionHandler implements the `version` verb. Prints
// {"version": "<stamp>", "commit": "<sha>", "config_dir": "<dir>"} per the
// manifest. The client's
// Version() fails only on a closed Client, and no caller passes one; a
// failure is named as every verb handler names one
// (errnames.Classify: its Catalog name, else ErrInternal), never
// ErrJSONMarshal, which is only for the json.Marshal below (b.3jc).
func versionHandler(client *pkgapi.Client, _ []string) error {
	res, err := client.Version()
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	payload, err := json.Marshal(res)
	if err != nil {
		if werr := writeError(os.Stderr, errJSONMarshal, err.Error()); werr != nil {
			return werr
		}
		return errDispatch
	}
	if _, err := fmt.Fprintln(os.Stdout, string(payload)); err != nil {
		return err
	}
	return nil
}

// writeError marshals an error envelope as JSON to w with a trailing newline.
func writeError(w io.Writer, name, desc string) error {
	return writeEnvelope(w, errorEnvelope{ErrName: name, ErrDescription: desc})
}

// writeEnvelope marshals env as JSON to w with a trailing newline.
func writeEnvelope(w io.Writer, env errorEnvelope) error {
	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(payload))
	return err
}

// dispatch picks a handler for argv and invokes it. argv is the verb and its
// arguments, global flags stripped. run() handles the no-verb case itself
// before setupClient (help, or nothing for a hook payload on stdin, SR-22.9),
// so an empty argv reaches dispatch only from a direct caller; it routes to
// help (PM call, see Subtask 1.2 spec).
//
// On unknown verb it writes the JSON envelope to stderr and returns
// errDispatch so the caller can set a non-zero exit code without
// re-printing the message.
func dispatch(argv []string, table map[string]func([]string) error) error {
	if len(argv) == 0 {
		return table["help"](nil)
	}
	verb := argv[0]
	handler, ok := table[verb]
	if !ok {
		if werr := writeError(os.Stderr, errUnknownVerb,
			fmt.Sprintf("unknown verb %q; try 'agent-director help'", verb)); werr != nil {
			return werr
		}
		return errDispatch
	}
	return handler(argv[1:])
}

// setupClient constructs the pkg/api.Client used by every non-hook verb,
// through clisetup.Open (whose comment holds the design pins), which
// agent-director-admin shares so both binaries open the store alike.
//
// b.32k: o carries the --store-path, --tmux-command and --create-if-missing
// overrides of the global flags run() parsed and applied before dispatch;
// --home was applied there (os.Setenv) BEFORE this function runs, so every
// "~/" expansion of the config and store paths sees the override. With
// --create-if-missing false a missing store is refused here (ErrStoreOpen)
// before any verb runs, and none of it is created (b.78b). run() also hands o
// to serve, whose MCP Client opens with the same overrides (b.wb7).
//
// On any error it writes the JSON envelope to stderr and returns errDispatch
// so run() can exit non-zero without double-printing.
func setupClient(o clisetup.Overrides) (*pkgapi.Client, config.Config, error) {
	client, cfg, err := clisetup.Open(o)
	if err != nil {
		name := "ErrStoreOpen"
		var oe *clisetup.OpenError
		if errors.As(err, &oe) {
			name = oe.Name
		}
		if werr := writeError(os.Stderr, name, err.Error()); werr != nil {
			return nil, config.Config{}, werr
		}
		return nil, config.Config{}, errDispatch
	}
	return client, cfg, nil
}

// run is the testable body of main. Returning an int lets main use
// os.Exit(run()) so deferred cleanup in run() still executes.
//
// Startup wiring (config + store) runs on every STORE-BACKED invocation to
// satisfy Epic 1 AC #4 (idempotent dir/file creation, which
// --create-if-missing false turns off, b.78b) and AC #5
// (ErrSchemaMismatch surfaces). The DB-free verbs below never reach it: help,
// version (and their aliases --help, -h, --version and -v, b.fv2), the no-verb
// run, and trail-emit are dispatched before setupClient so they neither open
// nor create the store (SR-4.1/4.2).
// ErrSchemaMismatch now surfaces on a store-opening verb (e.g. `list`), not on
// `help`.
//
// The no-verb run (no verb after the global flags, which counts a run with
// only global flags) prints help, except for a hook from a Claude Code that
// does not run exec-form hooks (SR-22.9): when stdin is not a terminal and
// carries a hook payload (read with a 1 MiB cap and a 1 s deadline), it prints
// nothing, exits 0 and writes one ad.hook.ignored no_exec_form to the trail
// under the (possibly --home) home, with no config load and no store
// (noVerbHookIgnored). `version` and its aliases never read stdin. `help` and
// its aliases read it only when AGENT_DIRECTOR_AGENT is codex (a Codex launch's
// environment), with the same bounded reader, to print a Codex SessionStart
// hook's structured form (codexSessionStartHook); otherwise they never read it.
//
// A verb alias (verbAliases: --help, -h, --version, -v) as the first argument
// after the global flags is replaced with its verb right after the global
// flags are applied, so everything below sees only the verb (b.fv2).
//
// The hook verb is special-cased: it bypasses the normal store-setup-and-
// dispatch path so every failure mode is fail-open per SRD §3.2. The
// branch is keyed off os.Args[1] before anything else so a missing
// config or broken DB cannot block Claude Code's hook fire — including
// the global-flag parser below, which can return an error on a malformed
// `--store-path`/`--home`/`--tmux-command`/`--create-if-missing` and would
// otherwise be a blocking failure mode on the hook hot path.
//
// b.32k: after the hook short-circuit, run() pre-scans argv for the four
// global flags (--store-path, --home, --tmux-command, --create-if-missing)
// with clisetup.ParseGlobalFlags, which agent-director-admin shares (b.vqr),
// and strips them before per-verb dispatch sees argv. GlobalFlags.Apply then sets
// HOME when --home is given, so config.Load's tilde-expansion picks it up —
// the CLI binary is short-lived and single-threaded at startup, so
// process-wide env mutation is safe.
func run() int {
	if len(os.Args) > 1 && os.Args[1] == "cursor-host" {
		if err := cursorHostHandler(os.Args[2:]); err != nil {
			return 1
		}
		return 0
	}
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		return runHook()
	}

	globals, strippedArgv, err := clisetup.ParseGlobalFlags(os.Args[1:])
	if err != nil {
		if werr := writeError(os.Stderr, "ErrInvalidFlags", err.Error()); werr != nil {
			fmt.Fprintln(os.Stderr, werr)
		}
		return 1
	}

	// --home: set process HOME BEFORE config.Load runs (clisetup's
	// GlobalFlags.Apply says why that covers every "~/" store/config path
	// expansion); overrides carries --store-path, --tmux-command and
	// --create-if-missing to setupClient. b.32k, b.hvf, b.78b.
	overrides, err := globals.Apply()
	if err != nil {
		if werr := writeError(os.Stderr, "ErrInvalidFlags", err.Error()); werr != nil {
			fmt.Fprintln(os.Stderr, werr)
		}
		return 1
	}

	// A verb alias is honoured only as the first argument after the global
	// flags (b.fv2). strippedArgv is ParseGlobalFlags' own copy, never os.Args.
	if len(strippedArgv) > 0 {
		if verb, ok := verbAliases[strippedArgv[0]]; ok {
			strippedArgv[0] = verb
		}
	}

	// trail-emit: DB-free verb — special-cased before setupClient so it works
	// even when state.db is missing or corrupted (SR-A-2.3, t3.4uk.nz.j9.2k).
	if len(strippedArgv) > 0 && strippedArgv[0] == "trail-emit" {
		if err := trailEmitHandlerWith(strippedArgv[1:]); err != nil {
			if errors.Is(err, errDispatch) {
				return 1
			}
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	// help / version / no verb: DB-free static-data verbs (their aliases are
	// already replaced with them above) — special-cased before setupClient so
	// they never open or create the store (SR-4.1/4.2, t3.93m.nr.om.wq).
	// This closes the path by which the npm client's version probe rewrote the
	// prod DB (b.8dr) and lets every SessionStart hook (`agent-director help`)
	// fire without touching the store. Keyed off stripped argv so global flags still apply
	// (`--home /x help` works and creates no store under /x). helpHandler ignores
	// its client; versionHandler only calls checkClosed + pure data, so a
	// non-nil zero-value &Client{} (closed=false) passes and stdout is
	// byte-identical to the setupClient path. Error mapping mirrors trail-emit.
	//
	// No verb prints help unless stdin carries a hook payload from a Claude
	// Code that does not run exec-form hooks: then nothing on stdout, exit 0,
	// and one trail record (SR-22.9; noVerbHookIgnored). The check runs here,
	// after --home is applied, so the record lands under that home.
	if len(strippedArgv) == 0 || strippedArgv[0] == "help" || strippedArgv[0] == "version" {
		var derr error
		switch {
		case len(strippedArgv) == 0:
			if noVerbHookIgnored() {
				return 0
			}
			derr = helpHandler(&pkgapi.Client{}, nil)
		case strippedArgv[0] == "version":
			derr = versionHandler(&pkgapi.Client{}, strippedArgv[1:])
		default:
			derr = helpHandler(&pkgapi.Client{}, strippedArgv[1:])
		}
		if derr != nil {
			if errors.Is(derr, errDispatch) {
				return 1
			}
			fmt.Fprintln(os.Stderr, derr)
			return 1
		}
		return 0
	}

	client, cfg, err := setupClient(overrides)
	if err != nil {
		if errors.Is(err, errDispatch) {
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer client.Close()

	if err := dispatch(strippedArgv, handlers(client, cfg, overrides)); err != nil {
		if errors.Is(err, errDispatch) {
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func main() {
	code := run()
	// Verb deadlines end at the result. Process teardown has a separate,
	// fixed allowance within the CLI's existing one-second return reserve:
	// accepted memory-resident audit events must get a chance to reach fsync
	// before os.Exit kills the worker. An expired drain is still unconfirmed.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	if err := trail.Default().Close(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "agent-director: audit shutdown:", err)
	}
	cancel()
	os.Exit(code)
}
