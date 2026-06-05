package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// trailEmitHandlerWith implements `agent-director trail-emit`. It dispatches
// on the first positional argument (sub-verb) — currently only "relay-attempt"
// is defined. Called from run() before setupClient so the verb works even when
// state.db is missing or corrupted (SR-A-2.3 / ticket t3.4uk.nz.j9.2k).
func trailEmitHandlerWith(args []string) error {
	if len(args) == 0 {
		return writeApiErrorAndDispatch("ErrInvalidFlags",
			"trail-emit requires a sub-verb; try: trail-emit relay-attempt")
	}
	switch args[0] {
	case "relay-attempt":
		return trailEmitRelayAttemptHandler(args[1:])
	default:
		return writeApiErrorAndDispatch("ErrInvalidFlags",
			fmt.Sprintf("unknown trail-emit sub-verb %q; try: trail-emit relay-attempt", args[0]))
	}
}

// trailEmitRelayAttemptHandler implements `agent-director trail-emit relay-attempt`.
//
// NOT fail-open: malformed flags produce a non-zero exit, no trail line is
// written, and the ErrInvalidFlags envelope is written to stderr.
func trailEmitRelayAttemptHandler(args []string) error {
	var (
		token         string
		endpoint      string
		outcomeStr    string
		bytesSent     int
		bytesReceived int
		instanceID    string
	)
	fs := flag.NewFlagSet("trail-emit relay-attempt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&token, "token", "", "request_token (required)")
	fs.StringVar(&endpoint, "endpoint", "", "target_endpoint URL or socket path (required)")
	fs.StringVar(&outcomeStr, "outcome", "", "3-digit HTTP status code (100-599) or named class: connection_refused, timeout, dns_failure (required)")
	fs.IntVar(&bytesSent, "bytes-sent", 0, "bytes sent")
	fs.IntVar(&bytesReceived, "bytes-received", 0, "bytes received")
	fs.StringVar(&instanceID, "instance-id", "", "claude_instance_id (required)")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}

	// Required-flag guard — no emit until all flags pass.
	if token == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--token is required")
	}
	if endpoint == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--endpoint is required")
	}
	if outcomeStr == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--outcome is required")
	}
	if instanceID == "" {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--instance-id is required")
	}

	// outcome is int for HTTP status codes, string for named classes.
	outcome, err := parseRelayOutcome(outcomeStr)
	if err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}

	fields := map[string]any{
		"claude_instance_id": instanceID,
		"request_token":      token,
		"target_endpoint":    endpoint,
		"outcome":            outcome, // int or string per spec
		"bytes_sent":         bytesSent,
		"bytes_received":     bytesReceived,
		"source":             "relay_hook", // SR-A-1.4
	}
	if emitErr := trail.Emit(context.Background(), "ad.relay_attempt.completed", fields); emitErr != nil {
		return writeApiErrorAndDispatch("ErrTrailWrite", emitErr.Error())
	}
	return writeJSON(os.Stdout, struct{}{})
}

// parseRelayOutcome validates the --outcome flag value. Returns an int for
// HTTP status codes in the range 100-599, a string for the three named error
// classes, and an error for anything else.
func parseRelayOutcome(s string) (any, error) {
	// Named error classes take precedence so "200" is unambiguously numeric.
	switch s {
	case "connection_refused", "timeout", "dns_failure":
		return s, nil
	}
	n, convErr := strconv.Atoi(s)
	if convErr == nil && n >= 100 && n <= 599 {
		return n, nil
	}
	return nil, fmt.Errorf(
		"--outcome %q is not a valid HTTP status code (100-599) or named class (connection_refused, timeout, dns_failure)", s)
}
