// Command fake-tmux is a test helper that records its argv to a file and
// exits 0. cmd/claude-director's integration tests put this binary on
// PATH ahead of the real tmux so a spawn / send-keys / read-pane
// invocation can be exercised end-to-end without launching a real Claude.
//
// Behavior:
//   - argv[0] basename "tmux" + first argv in {"new-session", "send-keys",
//     "capture-pane"} → write argv to $FAKE_TMUX_LOG (one argv element per
//     line, then a "---" record separator) and exit 0.
//   - "capture-pane" additionally writes $FAKE_TMUX_PANE_OUTPUT (or a
//     deterministic stub when unset) to stdout so callers can exercise
//     read-pane's bytes-back path.
//   - "has-session" → exit 1 (matches tmux's "no such session" exit so
//     spawn's HasSession-then-create flow doesn't trip a false positive).
//   - anything else → exit 0 with no side effects.
//
// The implementation is deliberately stripped down: no JSON output, no
// validation, no claims about argument legality. The integration tests
// inspect the log file to assert what the production code would have
// handed to real tmux.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		// `tmux` with no subcommand; nothing to record. Exit 0 so the
		// caller's `command -v tmux` probes still succeed.
		os.Exit(0)
	}
	sub := os.Args[1]
	if sub == "has-session" {
		// Production code calls HasSession as a precondition probe. The
		// real tmux exits non-zero for absent sessions; mirror that so a
		// "session already exists" branch isn't accidentally taken.
		os.Exit(1)
	}

	switch sub {
	case "new-session", "send-keys":
		logArgv()
	case "capture-pane":
		logArgv()
		// Emit stub pane bytes on stdout. The default stub is
		// deterministic so output-shape assertions in the read-pane CLI
		// test can pin against it. Callers wanting a specific corpus set
		// $FAKE_TMUX_PANE_OUTPUT to whatever bytes they want returned.
		if override := os.Getenv("FAKE_TMUX_PANE_OUTPUT"); override != "" {
			fmt.Fprint(os.Stdout, override)
		} else {
			fmt.Fprint(os.Stdout, "fake pane line one\nfake pane line two\n")
		}
	}
	os.Exit(0)
}

// logArgv appends the current argv (one element per line, followed by a
// "---" separator) to $FAKE_TMUX_LOG. Silent no-op when the env var is
// unset so tests that don't care about argv don't blow up.
func logArgv() {
	logPath := os.Getenv("FAKE_TMUX_LOG")
	if logPath == "" {
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		// Logging failure must not fail the call — production tmux would
		// have succeeded by this point.
		return
	}
	defer f.Close()
	for _, a := range os.Args {
		_, _ = f.WriteString(a + "\n")
	}
	_, _ = f.WriteString("---\n")
}
