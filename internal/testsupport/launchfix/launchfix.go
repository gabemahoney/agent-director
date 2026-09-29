// Package launchfix holds the fixture values of a launch's identity that
// several test-support packages share (SR-20.3): today the test socket.
//
// This is a LEAF test-support package, like procstarttimefix: it imports
// nothing from internal/store (or any other agent-director package), so
// pkg/api/apitest (which re-exports it) and internal/testsupport/tmuxfix can
// both use it without an import cycle. Tests reference these constants rather
// than duplicating the literals.
package launchfix

// TestSocket is the tmux socket path SeedSpawn records on every row unless an
// option overrides it. It is a fixed test value, never derived from the
// environment (tmux.ResolveSocket reads the environment), and no tmux server
// is expected to listen on it: the in-process Recorder answers for it.
const TestSocket = "/tmp/agent-director-test-tmux/default"
