package envelope_diff

import (
	"testing"
)

// TestBuildCLIEnvOverride verifies that when AGENT_DIRECTOR_TEST_BINARY is set,
// buildCLI returns that exact path without invoking go build.
//
// The env-var check in buildCLI fires before the sync.Once, so this test is
// independent of whether cliOnce has already fired in the current process.
func TestBuildCLIEnvOverride(t *testing.T) {
	sentinel := "/tmp/sentinel-agent-director-override"
	t.Setenv("AGENT_DIRECTOR_TEST_BINARY", sentinel)

	got := buildCLI(t)
	if got != sentinel {
		t.Errorf("buildCLI with env override: got %q; want %q", got, sentinel)
	}
}

// TestBuildFakeTmuxEnvOverride verifies that when AGENT_DIRECTOR_FAKE_TMUX_DIR
// is set, buildFakeTmux returns that exact directory without invoking go build.
func TestBuildFakeTmuxEnvOverride(t *testing.T) {
	sentinelDir := "/tmp/sentinel-faketmux-dir"
	t.Setenv("AGENT_DIRECTOR_FAKE_TMUX_DIR", sentinelDir)

	got := buildFakeTmux(t)
	if got != sentinelDir {
		t.Errorf("buildFakeTmux with env override: got %q; want %q", got, sentinelDir)
	}
}
