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
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// errorEnvelope is the JSON shape emitted on stderr for CLI-level errors.
// Matches SRD §12.2 / §13.1.
type errorEnvelope struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
}

// CLI-internal error names. These are NOT part of the SRD §13.1 API error
// catalogue — that catalogue describes API-surface errors emitted by verbs.
// These names signal startup/dispatch failures of the CLI itself and are
// kept distinct from any future API error names.
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

// handlers maps verb names to their implementations. `help` and `--help`
// route to the same function so their stdout is byte-identical (SRD §12.3).
// client and cfg are captured in closures so each verb sees the same
// already-opened Client — construction is done once in run() via setupClient().
//
// `hook` is intentionally NOT in this table — runHook() short-circuits
// the dispatch loop before setupClient() so hook fires can't be blocked
// by config/store failures (SRD §3.2 fail-open invariant).
//
// The DB-free static-data verbs `help`, `--help`, and `version` remain in this
// table for the unknown-verb-free lookup shape, but run() dispatches them (and
// the no-verb case: help, or nothing for a hook payload on stdin, SR-22.9)
// BEFORE setupClient with a zero-value Client, so on the normal path these
// entries are never reached (SR-4.1/4.2, b.93m). Their
// closures here are the store-backed fallback only and behave identically —
// helpHandler ignores its client and versionHandler consults no store.
func handlers(client *pkgapi.Client, cfg config.Config) map[string]func([]string) error {
	return map[string]func([]string) error{
		"help":           func(args []string) error { return helpHandler(client, args) },
		"--help":         func(args []string) error { return helpHandler(client, args) },
		"version":        func(args []string) error { return versionHandler(client, args) },
		"spawn":          func(args []string) error { return spawnHandlerWith(client, args) },
		"status":         func(args []string) error { return statusHandlerWith(client, args) },
		"get":            func(args []string) error { return getHandlerWith(client, args) },
		"send-keys":      func(args []string) error { return sendKeysHandlerWith(client, args) },
		"read-pane":      func(args []string) error { return readPaneHandlerWith(client, args) },
		"kill":           func(args []string) error { return killHandlerWith(client, args) },
		"pause":          func(args []string) error { return pauseHandlerWith(client, args) },
		"list":           func(args []string) error { return listHandlerWith(client, args) },
		"make-template":  func(args []string) error { return makeTemplateHandlerWith(client, args) },
		"decide":         func(args []string) error { return decideHandlerWith(client, args) },
		"get-permission": func(args []string) error { return getPermissionHandlerWith(client, args) },
		"resume":         func(args []string) error { return resumeHandlerWith(client, args) },
		"find-missing":   func(args []string) error { return findMissingHandlerWith(client, args) },
		"expire":         func(args []string) error { return expireHandlerWith(client, args) },
		"serve":          func(args []string) error { return serveHandlerWith(cfg, args) },
		"trail-emit":     func(args []string) error { return trailEmitHandlerWith(args) },
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
// The function never returns an error; it logs and returns.
func runHook() int {
	logger := newHookLogger()
	// Wire the hook logger into the trail writer so emit failures and
	// ts-substitution warnings reach cfg.Log.ErrorLogPath rather than
	// being silently discarded. SetLogger is safe to call before the
	// first Emit (SR-A-7.6).
	trail.SetLogger(logger)
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
	// With the busy timeout every verb's open uses too (b.c7f).
	st, err := store.OpenOrInitWithBusyTimeout(dbPath, cfg.Store.EffectiveBusyTimeoutMs())
	if err != nil {
		earlyFailClosed(fmt.Sprintf("open store: %v", err))
		return hookExitCode
	}
	defer st.Close()

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
	hc := hook.HandleConfig{
		Env:          hook.OSGetenv,
		Cfg:          cfg.Relay,
		Clock:        hook.DefaultPollClock(),
		ParentPID:    os.Getppid,
		ParentProc:   hookParentProc(),
		Now:          time.Now,
		PendingGrace: cfg.Tmux.EffectivePendingGrace(),
	}
	if err := hook.Handle(context.Background(), bytes.NewReader(stdinRaw), stdout, st, hc, logger); err != nil {
		hookLog(logger, "hook: handle: %v", err)
	}
	return hookExitCode
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
func helpHandler(_ *pkgapi.Client, _ []string) error {
	verbs, err := pkgapi.Help()
	if err != nil {
		// pkg/api.Help never errors today, but if a future implementation
		// changes that, surface it via the dispatch envelope path.
		if werr := writeError(os.Stderr, errJSONMarshal, err.Error()); werr != nil {
			return werr
		}
		return errDispatch
	}
	payload, err := json.Marshal(helpResult{Verbs: verbs})
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

// versionHandler implements the `version` verb. Prints
// {"version": "<stamp>", "commit": "<sha>"} per the manifest. The client's
// Version() never errors; the same envelope path as helpHandler is kept
// for uniformity.
func versionHandler(client *pkgapi.Client, _ []string) error {
	res, err := client.Version()
	if err != nil {
		if werr := writeError(os.Stderr, errJSONMarshal, err.Error()); werr != nil {
			return werr
		}
		return errDispatch
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
	payload, err := json.Marshal(errorEnvelope{ErrName: name, ErrDescription: desc})
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
// b.32k: o carries the --store-path and --tmux-command overrides of the
// global flags run() parsed and applied before dispatch; --home was applied
// there (os.Setenv) BEFORE this function runs, so every "~/" expansion of the
// config and store paths sees the override.
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
// satisfy Epic 1 AC #4 (idempotent dir/file creation) and AC #5
// (ErrSchemaMismatch surfaces). The DB-free verbs below never reach it: help,
// --help, version, the no-verb run, and trail-emit are dispatched before
// setupClient so they neither open nor create the store (SR-4.1/4.2).
// ErrSchemaMismatch now surfaces on a store-opening verb (e.g. `list`), not on
// `help`.
//
// The no-verb run (no verb after the global flags, which counts a run with
// only global flags) prints help, except for a hook from a Claude Code that
// does not run exec-form hooks (SR-22.9): when stdin is not a terminal and
// carries a hook payload (read with a 1 MiB cap and a 1 s deadline), it prints
// nothing, exits 0 and writes one ad.hook.ignored no_exec_form to the trail
// under the (possibly --home) home, with no config load and no store
// (noVerbHookIgnored). `help`, `--help` and `version` never read stdin.
//
// The hook verb is special-cased: it bypasses the normal store-setup-and-
// dispatch path so every failure mode is fail-open per SRD §3.2. The
// branch is keyed off os.Args[1] before anything else so a missing
// config or broken DB cannot block Claude Code's hook fire — including
// the global-flag parser below, which can return an error on a malformed
// `--store-path`/`--home`/`--tmux-command` and would otherwise be a
// blocking failure mode on the hook hot path.
//
// b.32k: after the hook short-circuit, run() pre-scans argv for the three
// global flags (--store-path, --home, --tmux-command) with
// clisetup.ParseGlobalFlags, which agent-director-admin shares (b.vqr), and
// strips them before per-verb dispatch sees argv. GlobalFlags.Apply then sets
// HOME when --home is given, so config.Load's tilde-expansion picks it up —
// the CLI binary is short-lived and single-threaded at startup, so
// process-wide env mutation is safe.
func run() int {
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
	// expansion); overrides carries --store-path and --tmux-command to
	// setupClient. b.32k, b.hvf.
	overrides, err := globals.Apply()
	if err != nil {
		if werr := writeError(os.Stderr, "ErrInvalidFlags", err.Error()); werr != nil {
			fmt.Fprintln(os.Stderr, werr)
		}
		return 1
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

	// help / --help / version / no verb: DB-free static-data verbs —
	// special-cased before setupClient so they never open or create the store
	// (SR-4.1/4.2, t3.93m.nr.om.wq). This closes the path by which the npm
	// client's version probe rewrote the prod DB (b.8dr) and lets every
	// SessionStart hook (`agent-director help`) fire without touching the
	// store. Keyed off stripped argv so global flags still apply
	// (`--home /x help` works and creates no store under /x). helpHandler ignores
	// its client; versionHandler only calls checkClosed + pure data, so a
	// non-nil zero-value &Client{} (closed=false) passes and stdout is
	// byte-identical to the setupClient path. Error mapping mirrors trail-emit.
	//
	// No verb prints help unless stdin carries a hook payload from a Claude
	// Code that does not run exec-form hooks: then nothing on stdout, exit 0,
	// and one trail record (SR-22.9; noVerbHookIgnored). The check runs here,
	// after --home is applied, so the record lands under that home.
	if len(strippedArgv) == 0 ||
		strippedArgv[0] == "help" || strippedArgv[0] == "--help" ||
		strippedArgv[0] == "version" {
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

	if err := dispatch(strippedArgv, handlers(client, cfg)); err != nil {
		if errors.Is(err, errDispatch) {
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run())
}
