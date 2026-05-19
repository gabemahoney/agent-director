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
	"os"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/config"
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
func handlers() map[string]func([]string) error {
	return map[string]func([]string) error{
		"help":   helpHandler,
		"--help": helpHandler,
	}
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

// setupStore loads config and opens the store, applying Epic 1 AC #4
// (idempotent dir/file creation at 0700/0600) and AC #5
// (ErrSchemaMismatch surfaces as JSON on stderr).
//
// On any error it writes the JSON envelope to stderr and returns
// errDispatch so run() can exit non-zero without double-printing.
func setupStore() (*store.Store, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		if werr := writeError(os.Stderr, errConfigMalformed, err.Error()); werr != nil {
			return nil, werr
		}
		return nil, errDispatch
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
			return nil, werr
		}
		return nil, errDispatch
	}
	return st, nil
}

// run is the testable body of main. Returning an int lets main use
// os.Exit(run()) so deferred cleanup in run() still executes.
//
// Startup wiring (config + store) runs on every invocation — including
// `help` — to satisfy Epic 1 AC #4 (idempotent dir/file creation) and
// AC #5 (ErrSchemaMismatch surfaces).
func run() int {
	st, err := setupStore()
	if err != nil {
		if errors.Is(err, errDispatch) {
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()

	if err := dispatch(os.Args[1:], handlers()); err != nil {
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
