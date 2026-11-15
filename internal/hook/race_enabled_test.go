//go:build race

package hook_test

// raceDetector reports whether this test binary runs under the race detector,
// whose SQLite slowdown makes the single-goroutine 1,000-Message backlog
// (TestClaudeWakeDrainsBacklog) run for many minutes while adding no check.
const raceDetector = true
