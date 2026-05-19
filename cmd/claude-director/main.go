// Command claude-director is the CLI entrypoint for the claude-director tool.
//
// This file provides the argv dispatch skeleton. Real verb handlers land in
// later Tasks. The dispatch table is a map[string]func([]string) error so new
// verbs become one-line additions.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// errorEnvelope is the JSON shape emitted on stderr for CLI-level errors.
// Matches SRD §12.2 / §13.1.
type errorEnvelope struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
}

// errUnknownVerb is an internal CLI-dispatch error name. It is intentionally
// NOT part of the SRD §13.1 API error catalogue — that catalogue describes
// API-surface errors, while this name signals a bad invocation of the CLI
// itself. Keep it distinct from any future API error names.
const errUnknownVerb = "ErrUnknownVerb"

// errDispatch is a sentinel returned by dispatch when it has already written
// a JSON error envelope to stderr. run() uses this to set the exit code
// without double-printing.
var errDispatch = errors.New("dispatch error")

// handlers maps verb names to their implementations. Help is wired here so
// `help` and `--help` route to a single function. Future verbs are added as
// one-line entries.
func handlers() map[string]func([]string) error {
	return map[string]func([]string) error{
		"help":   helpHandler,
		"--help": helpHandler,
	}
}

// helpHandler is a stub. Real help output lands in Task 5.
func helpHandler(_ []string) error {
	// Emit a minimal valid JSON object so callers can parse it today.
	_, err := fmt.Fprintln(os.Stdout, "{}")
	return err
}

// writeError marshals an error envelope as JSON to w.
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

// run is the testable body of main. Returning an int lets main use
// os.Exit(run()) so deferred cleanup in run() still executes.
func run() int {
	err := dispatch(os.Args[1:], handlers())
	if err == nil {
		return 0
	}
	if errors.Is(err, errDispatch) {
		return 1
	}
	// Unexpected error from a handler or writeError itself. Surface it.
	fmt.Fprintln(os.Stderr, err)
	return 1
}

func main() {
	os.Exit(run())
}
