// Package toolguard owns the side-effect-free PreToolUse decision boundary.
// Must use: all Guard rules run on decodePayload's validated fields through
// Run, sharing its deadline and fixed reasons. The CLI only writes the returned
// outcome; decision workers must never receive an output writer.
package toolguard

import (
	"context"
	"io"
	"path/filepath"
	"time"
)

// decisionTimeout initially reserves 55 of the hook's 60 seconds for startup,
// scheduling and exit: five seconds bounds stdin plus all decision work. This
// is a conservative starting budget, not a runtime measurement; Task
// t2.jo7.dj.z3 measures the finished path/Execute rules and sets the final value.
const decisionTimeout = 5 * time.Second

// maxPayloadBytes allows 64 MiB of JSON: two 5 MiB Edit strings can each expand
// sixfold as JSON escapes (60 MiB), leaving 4 MiB for paths and the envelope.
// This also accommodates a 10 MiB Create or ApplyPatch string at worst-case
// escaping. It is an explicit large-text allowance, not the unrelated lifecycle
// hook's 1 MiB cap or a claim about the provider's maximum. Larger input blocks.
const maxPayloadBytes = 64 << 20

// Result is the complete Guard outcome. No decision has exit 0 and empty
// Stderr; a block has exit 2 and one fixed line. There is never stdout output.
type Result struct {
	ExitCode int
	Stderr   string
}

// reason is a closed set: input, errors and tool names cannot become output.
type reason uint8

const (
	noDecision reason = iota
	checkFailed
	callDisabled
	secretPath
)

// outcome is the only formatter, including for unknown/internal decisions.
func outcome(r reason) Result {
	if r == noDecision {
		return Result{}
	}
	message := "agent-director could not check it"
	switch r {
	case callDisabled:
		message = "this call is disabled"
	case secretPath:
		message = "this call accesses a protected path"
	}
	return Result{ExitCode: 2, Stderr: "agent-director blocked this call: " + message + "\n"}
}

// Run accepts the literal arguments following tool-guard and one stdin payload.
// The form `tool-guard -- <absolute path> ...` must survive executable upgrades:
// running launches keep invoking the installed program with these arguments.
// Run reads no configuration or state and writes nothing. Its caller must exit
// after writing the result: an uninterruptible stdin/metadata operation may
// remain in the isolated worker until process exit, but cannot publish output.
func Run(args []string, stdin io.Reader) (result Result) {
	result = outcome(checkFailed)
	defer func() {
		if recover() != nil {
			result = outcome(checkFailed)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), decisionTimeout)
	defer cancel()
	return run(ctx, args, stdin, decide)
}

// run's context and rule function are causal seams for in-package timeout and
// failure tests. No global clock or exported test service is needed. The budget
// begins before argument validation or stdin ingestion, and never resets.
func run(ctx context.Context, args []string, stdin io.Reader, check func(context.Context, payload, []string) reason) (result Result) {
	result = outcome(checkFailed)
	defer func() {
		if recover() != nil {
			result = outcome(checkFailed)
		}
	}()
	if ctx.Err() != nil {
		return result
	}
	done := make(chan reason, 1)
	go func() {
		r := checkFailed
		defer func() {
			if recover() != nil {
				r = checkFailed
			}
			// Buffered and private: a late worker cannot block on publication
			// or overwrite the outcome already returned at the deadline.
			done <- r
		}()
		entries, ok := pathEntries(args)
		if !ok {
			return
		}
		p, ok := decodePayload(stdin)
		if !ok || ctx.Err() != nil {
			return
		}
		r = check(ctx, p, entries)
	}()
	select {
	case r := <-done:
		// If both become ready, expiry wins regardless of select's choice.
		if ctx.Err() == nil {
			return outcome(r)
		}
	case <-ctx.Done():
	}
	return result
}

func pathEntries(args []string) ([]string, bool) {
	if len(args) < 2 || args[0] != "--" {
		return nil, false
	}
	for _, entry := range args[1:] {
		if !filepath.IsAbs(entry) {
			return nil, false
		}
	}
	return append([]string(nil), args[1:]...), true
}

// decide is the one rule entry point. Later path/Execute rules must consume p,
// reuse entries and ctx, and return a fixed reason without I/O to any writer.
func decide(ctx context.Context, p payload, entries []string) reason {
	if ctx.Err() != nil {
		return checkFailed
	}
	if p.tool == "AskUser" {
		return callDisabled
	}
	switch p.tool {
	case "Read", "Create", "Edit", "ApplyPatch", "LS", "Grep", "Glob":
		return decideFiles(p, entries, newPathResolver(ctx))
	}
	return noDecision
}
