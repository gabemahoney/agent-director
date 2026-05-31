// Command agent-director is the CLI entrypoint for the agent-director tool.
//
// This file provides the argv dispatch skeleton and the startup wiring that
// every invocation performs (config load + store open). Real verb handlers
// live in pkg/api; this file marshals their results to JSON per
// SRD §12.3.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
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
	errUnknownVerb     = "ErrUnknownVerb"
	errJSONMarshal     = "ErrJSONMarshal"
	errConfigMalformed = "ErrConfigMalformed"
	errStoreOpen       = "ErrStoreOpen"
)

// errDispatch is a sentinel returned by handlers when they have already
// written a JSON error envelope to stderr. run() uses this to set the exit
// code without double-printing.
var errDispatch = errors.New("dispatch error")

// configPath is the canonical TOML config location.
const configPath = "~/.agent-director/config.toml"

// handlers maps verb names to their implementations. `help` and `--help`
// route to the same function so their stdout is byte-identical (SRD §12.3).
// client and cfg are captured in closures so each verb sees the same
// already-opened Client — construction is done once in run() via setupClient().
//
// `hook` is intentionally NOT in this table — runHook() short-circuits
// the dispatch loop before setupClient() so hook fires can't be blocked
// by config/store failures (SRD §3.2 fail-open invariant).
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
		"delete":         func(args []string) error { return deleteHandlerWith(client, args) },
		"serve":          func(args []string) error { return serveHandlerWith(cfg, args) },
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
// startup path is intentionally bypassed here.
//
// The function never returns an error; it logs and returns.
func runHook() int {
	logger := newHookLogger()
	stdout := os.Stdout
	relayActive := os.Getenv(hook.EnvRelayMode) == hook.RelayModeOn

	// Pre-Handle fail-closed: if relay is active and we can't get far
	// enough to even invoke hook.Handle, emit deny here. Handle itself
	// owns fail-closed past this point.
	earlyFailClosed := func(why string) {
		hookLog(logger, "hook: %s", why)
		if relayActive {
			fmt.Fprintln(stdout, hook.EncodeDecision("deny", ""))
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		earlyFailClosed(fmt.Sprintf("load config: %v", err))
		return hookExitCode
	}
	st, err := store.OpenOrInit(cfg.Store.DbPath)
	if err != nil {
		earlyFailClosed(fmt.Sprintf("open store: %v", err))
		return hookExitCode
	}
	defer st.Close()

	hc := hook.HandleConfig{
		Env:   hook.OSGetenv,
		Cfg:   cfg.Relay,
		Clock: hook.DefaultPollClock(),
	}
	if err := hook.Handle(context.Background(), os.Stdin, stdout, st, hc, logger); err != nil {
		hookLog(logger, "hook: handle: %v", err)
	}
	return hookExitCode
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

// dispatch picks a handler for argv and invokes it. argv is os.Args[1:].
// No-args routes to help (PM call, see Subtask 1.2 spec).
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

// setupClient constructs the pkg/api.Client used by every non-hook verb.
//
// Design pins (see Task 3 spec):
//   - Pin 1 (CreateIfMissing=true): the CLI is the one place that opts in to
//     first-run store creation; library callers get the strict default.
//   - Pin 2 (StorePath omitted by default): leaving StorePath="" lets the
//     three-tier precedence in pkg/api.New honor cfg.Store.DbPath, so users
//     who set a custom [store] db_path in their TOML get that path
//     byte-identical to pre-refactor behavior. When the global --store-path
//     flag is set (gOpts.storePathSet), tier 1 of the precedence kicks in and
//     overrides the config-file value.
//   - Pin 3 (Logger=newRecoveryLogger): SRD §14.6 and §5 WARN messages must
//     reach cfg.Log.ErrorLogPath. We call config.Load once here to build the
//     logger BEFORE calling pkg/api.New, which also loads config internally.
//     The duplicate load is intentional — the alternative would require a
//     circular bootstrap. See Task 3 subtask vk for rationale.
//   - Pin H6 (no Client.Config() accessor): cfg is returned directly so
//     serveHandlerWith can pass it to newMCPLogger without a pkg/api accessor
//     that would leak internal/config.Config into the library's public surface.
//
// b.32k: gOpts carries the three global-flag overrides parsed in run() before
// dispatch. --store-path and --tmux-command thread through to pkgapi.Options;
// --home is handled in run() via os.Setenv BEFORE this function runs, so any
// "~/" expansion below sees the override.
//
// On any error it writes the JSON envelope to stderr and returns errDispatch
// so run() can exit non-zero without double-printing.
func setupClient(gOpts globalOptions) (*pkgapi.Client, config.Config, error) {
	// Preliminary config load to construct the recovery logger (Pin 3).
	// pkg/api.New will load config again internally; this duplicate is
	// acceptable — see Pin 3 comment above.
	cfg, err := config.Load(configPath)
	if err != nil {
		if werr := writeError(os.Stderr, errConfigMalformed, err.Error()); werr != nil {
			return nil, config.Config{}, werr
		}
		return nil, config.Config{}, errDispatch
	}
	logger := newRecoveryLogger(cfg)

	apiOpts := pkgapi.Options{
		ConfigPath:      configPath,
		CreateIfMissing: true, // Pin 1
		// StorePath optionally set from --store-path; otherwise Pin 2 applies.
		Logger: logger, // Pin 3
	}
	if gOpts.storePathSet {
		// pkgapi.New tilde-expands StorePath internally.
		apiOpts.StorePath = gOpts.storePath
	}
	if gOpts.tmuxCommandSet {
		// pkg/api.Client uses opts.TmuxCommand directly when non-empty
		// (see pkg/api/client.go step 5); CLI-side tilde-expand for
		// parity with --store-path so a `~/bin/tmux` argument works.
		apiOpts.TmuxCommand = expandTildeCLI(gOpts.tmuxCommand)
	}

	client, err := pkgapi.New(apiOpts)
	if err != nil {
		name := errStoreOpen
		switch {
		case errors.Is(err, store.ErrSchemaMismatch):
			name = "ErrSchemaMismatch"
		case errors.Is(err, store.ErrStoreNotInitialized):
			name = errStoreOpen
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
// Startup wiring (config + store) runs on every invocation — including
// `help` — to satisfy Epic 1 AC #4 (idempotent dir/file creation) and
// AC #5 (ErrSchemaMismatch surfaces).
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
// global flags (--store-path, --home, --tmux-command) and strips them
// before per-verb dispatch sees argv. When --home is set we os.Setenv
// HOME immediately so config.Load's tilde-expansion picks it up — the CLI
// binary is short-lived and single-threaded at startup, so process-wide
// env mutation is safe.
func run() int {
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		return runHook()
	}

	gOpts, strippedArgv, err := parseGlobalFlags(os.Args[1:])
	if err != nil {
		if werr := writeError(os.Stderr, "ErrInvalidFlags", err.Error()); werr != nil {
			fmt.Fprintln(os.Stderr, werr)
		}
		return 1
	}

	// --home: set process HOME BEFORE config.Load runs. internal/config uses
	// os.UserHomeDir() for tilde-expansion of cfg paths and pkg/api.expandTilde
	// uses user.Current() for opts.StorePath/opts.TmuxCommand; both consult the
	// HOME env var first on POSIX, so an os.Setenv here covers every downstream
	// path-expansion site. Safe because the CLI process is short-lived and not
	// multi-threaded at startup. b.32k.
	if gOpts.homeSet {
		expanded := expandTildeCLI(gOpts.home)
		if err := os.Setenv("HOME", expanded); err != nil {
			if werr := writeError(os.Stderr, "ErrInvalidFlags",
				fmt.Sprintf("set HOME: %v", err)); werr != nil {
				fmt.Fprintln(os.Stderr, werr)
			}
			return 1
		}
	}

	client, cfg, err := setupClient(gOpts)
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

// expandTildeCLI expands a leading "~/" against the current HOME (env, then
// os.UserHomeDir). Used by --store-path / --tmux-command / --home value
// resolution before forwarding to pkg/api. Mirrors pkg/api.expandTilde but
// is duplicated here to keep cmd/agent-director independent of that helper.
func expandTildeCLI(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil || home == "" {
			return p
		}
	}
	return home + p[1:]
}

func main() {
	os.Exit(run())
}
