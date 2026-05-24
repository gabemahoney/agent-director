package envelope_diff

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuildCLIOnce verifies that the sync.Once callback inside buildCLI fires
// exactly once no matter how many times buildCLI is called in the same process.
func TestBuildCLIOnce(t *testing.T) {
	// Call buildCLI three times; the Once should fire at most once.
	for i := 0; i < 3; i++ {
		path := buildCLI(t)
		if path == "" {
			t.Fatal("buildCLI returned empty path")
		}
	}

	// When the env override is active the Once never fires (no build needed);
	// skip the count assertion in that case.
	if os.Getenv("AGENT_DIRECTOR_TEST_BINARY") != "" {
		t.Skip("AGENT_DIRECTOR_TEST_BINARY set; skipping Once-count assertion")
	}
	if cliOnceBuildCount != 1 {
		t.Errorf("buildCLI Once callback fired %d time(s); want exactly 1", cliOnceBuildCount)
	}
}

// TestBuildFakeTmuxNamedTmux verifies that the binary produced by buildFakeTmux
// is literally named "tmux" so PATH-prepend resolution finds it.
func TestBuildFakeTmuxNamedTmux(t *testing.T) {
	dir := buildFakeTmux(t)
	if dir == "" {
		t.Fatal("buildFakeTmux returned empty dir")
	}
	binPath := filepath.Join(dir, "tmux")
	if filepath.Base(binPath) != "tmux" {
		t.Errorf("fake-tmux binary base = %q; want \"tmux\"", filepath.Base(binPath))
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Errorf("fake-tmux binary not present at %s: %v", binPath, err)
	}
}
