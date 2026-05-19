// Command fake-tmux is a test helper that records its argv to a file and
// exits 0. cmd/claude-director's integration tests put this binary on
// PATH ahead of the real tmux so a spawn invocation can be exercised
// end-to-end without launching a real Claude.
//
// Behavior:
//   - argv[0] basename "tmux" + first argv "new-session" → write argv to
//     $FAKE_TMUX_LOG (one argv element per line) and exit 0.
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
	if sub != "new-session" {
		// kill-session / list-panes are no-ops in the fake; they aren't
		// exercised by the Task 5 surface.
		os.Exit(0)
	}
	logPath := os.Getenv("FAKE_TMUX_LOG")
	if logPath == "" {
		// No log target set — exit 0 silently so test runs that don't care
		// about argv don't blow up.
		os.Exit(0)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		// Logging failure must not fail the spawn — production tmux would
		// have succeeded by this point.
		os.Exit(0)
	}
	defer f.Close()
	for _, a := range os.Args {
		_, _ = f.WriteString(a + "\n")
	}
	_, _ = f.WriteString("---\n")
	os.Exit(0)
}
