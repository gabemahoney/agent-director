// Package launchfix holds the fixture values of a launch's identity that
// several test-support packages share (SR-20.3): the test socket and the
// default pane identity of a live row.
//
// This is a LEAF test-support package, like procstarttimefix: it imports
// nothing from internal/store (or any other agent-director package), so
// pkg/api/apitest (which re-exports it) and internal/testsupport/tmuxfix can
// both use it without an import cycle. Tests reference these constants rather
// than duplicating the literals.
package launchfix

// TestSocket is the tmux socket SeedSpawn records on every row unless an
// option overrides it. It is a fixed test value, never derived from the
// environment (tmux.ResolveSocket reads the environment), and no tmux server
// is expected to listen on it: the in-process Recorder answers for it.
const TestSocket = "/tmp/agent-director-test-tmux/default"

// TestPaneID and TestPanePID are the pane identity SeedSpawn records on a
// live row unless an option overrides it (SR-20.3); the Recorder's row
// session seed (tmuxfix) gives the row's session this pane. The pid is above
// Linux's PID_MAX_LIMIT (4194304), so every process-start-time reader reads
// the pane process as gone. No pane start time is recorded (NULL).
const (
	TestPaneID  = "%42"
	TestPanePID = 4194305
)
