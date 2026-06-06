package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// printResult marshals v to a single-line JSON value and writes it to w
// (intended to be os.Stdout). json.Marshal never embeds newlines in its
// default mode, so the output is always exactly one line followed by a
// newline, suitable for JSON.parse(stdout.trim()) on the Bun side.
//
// Returns an error only when marshaling fails (e.g. a channel or function
// value was inadvertently included in v); callers should treat this as a
// programming error and call printError instead.
func printResult(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("printResult: marshal: %w", err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// printError writes a human-readable error line to w (intended to be
// os.Stderr). stdout is intentionally not touched so the Bun side can
// distinguish success (valid JSON on stdout) from failure (empty stdout +
// message on stderr). The caller is responsible for returning a non-zero
// exit code.
func printError(w io.Writer, err error) {
	fmt.Fprintln(w, "error: "+err.Error()) //nolint:errcheck
}
