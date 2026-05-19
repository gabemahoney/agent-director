// Command claude-director is the CLI entrypoint for the claude-director tool.
//
// This file provides the argv dispatch skeleton and the startup wiring that
// every invocation performs (config load + store open). Real verb handlers
// live in internal/api; this file marshals their results to JSON per
// SRD §12.3.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/hook"
	"github.com/gabemahoney/claude-director/internal/store"
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
const configPath = "~/.claude-director/config.toml"

// handlers maps verb names to their implementations. `help` and `--help`
// route to the same function so their stdout is byte-identical (SRD §12.3).
// st + cfg are captured in closures so each verb sees the same already-
// opened store / loaded config — opening twice would be wasteful and risk
// divergence between run()'s startup and a verb's view of the world.
//
// `hook` is intentionally NOT in this table — runHook() short-circuits
// the dispatch loop before setupStore() so hook fires can't be blocked
// by config/store failures (SRD §3.2 fail-open invariant).
func handlers(st *store.Store, cfg config.Config) map[string]func([]string) error {
	return map[string]func([]string) error{
		"help":      helpHandler,
		"--help":    helpHandler,
		"spawn":     func(args []string) error { return spawnHandlerWith(st, cfg, args) },
		"status":    func(args []string) error { return statusHandlerWith(st, args) },
		"get":       func(args []string) error { return getHandlerWith(st, args) },
		"send-keys": func(args []string) error { return sendKeysHandlerWith(st, args) },
		"read-pane": func(args []string) error { return readPaneHandlerWith(st, args) },
		"kill":      func(args []string) error { return killHandlerWith(st, args) },
		"pause":     func(args []string) error { return pauseHandlerWith(st, cfg, args) },
		"list":          func(args []string) error { return listHandlerWith(st, args) },
		"make-template": func(args []string) error { return makeTemplateHandlerWith(args) },
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
// unreachable) yields exit 0 with empty stdout. State-tracking hooks
// MUST NOT block Claude Code.
//
// The function never returns an error; it logs and returns.
func runHook() int {
	logger := newHookLogger()

	cfg, err := config.Load(configPath)
	if err != nil {
		// Fail-open: log and exit 0. Config malformed cannot block Claude.
		hookLog(logger, "hook: load config: %v", err)
		return hookExitCode
	}
	st, err := store.Open(cfg.Store.DbPath)
	if err != nil {
		hookLog(logger, "hook: open store: %v", err)
		return hookExitCode
	}
	defer st.Close()

	if err := hook.Handle(os.Stdin, hook.OSGetenv, st, logger); err != nil {
		hookLog(logger, "hook: handle: %v", err)
	}
	return hookExitCode
}

// newHookLogger opens the configured error_log_path (best-effort) and
// returns a *log.Logger writing to it. On any open failure it falls back
// to stderr — the hook MUST still log somewhere because diagnostic
// silence on the hot path is harder to debug than a stderr blast.
func newHookLogger() *log.Logger {
	cfg, err := config.Load(configPath)
	if err != nil {
		return log.New(os.Stderr, "claude-director-hook ", log.LstdFlags)
	}
	if cfg.Log.ErrorLogPath == "" {
		return log.New(os.Stderr, "claude-director-hook ", log.LstdFlags)
	}
	f, err := os.OpenFile(cfg.Log.ErrorLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return log.New(os.Stderr, "claude-director-hook ", log.LstdFlags)
	}
	// Best-effort: the file is intentionally leaked for the lifetime of
	// the hook fire (short-lived process; the OS reclaims the fd on exit).
	return log.New(f, "claude-director-hook ", log.LstdFlags)
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
	Verbs []api.VerbSummary `json:"verbs"`
}

// helpHandler implements the help verb. It calls api.Help, wraps the
// result in {"verbs": [...]}, marshals to single-line JSON (SRD §12.3
// "exactly one JSON object on stdout"), and writes it to stdout with a
// trailing newline.
func helpHandler(_ []string) error {
	verbs, err := api.Help()
	if err != nil {
		// api.Help never errors today, but if a future implementation
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
			fmt.Sprintf("unknown verb %q; try 'claude-director help'", verb)); werr != nil {
			return werr
		}
		return errDispatch
	}
	return handler(argv[1:])
}

// setupStoreAndCfg loads config and opens the store, applying Epic 1
// AC #4 (idempotent dir/file creation at 0700/0600) and AC #5
// (ErrSchemaMismatch surfaces as JSON on stderr). The Config is
// returned alongside so verb handlers can see the same view startup
// observed.
//
// On any error it writes the JSON envelope to stderr and returns
// errDispatch so run() can exit non-zero without double-printing.
func setupStoreAndCfg() (*store.Store, config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		if werr := writeError(os.Stderr, errConfigMalformed, err.Error()); werr != nil {
			return nil, config.Config{}, werr
		}
		return nil, config.Config{}, errDispatch
	}

	// config.Load fully resolves path fields (SRD §11), so DbPath is already
	// $HOME-expanded — no further tilde handling needed at the CLI layer.
	st, err := store.Open(cfg.Store.DbPath)
	if err != nil {
		name := errStoreOpen
		if errors.Is(err, store.ErrSchemaMismatch) {
			name = "ErrSchemaMismatch"
		}
		if werr := writeError(os.Stderr, name, err.Error()); werr != nil {
			return nil, config.Config{}, werr
		}
		return nil, config.Config{}, errDispatch
	}
	return st, cfg, nil
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
// config or broken DB cannot block Claude Code's hook fire.
func run() int {
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		return runHook()
	}

	st, cfg, err := setupStoreAndCfg()
	if err != nil {
		if errors.Is(err, errDispatch) {
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()

	if err := dispatch(os.Args[1:], handlers(st, cfg)); err != nil {
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
