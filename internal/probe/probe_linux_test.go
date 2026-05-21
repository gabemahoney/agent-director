//go:build linux

package probe_test

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// TestLinuxProberFindsLiveID launches a child shell with the env var
// set and asserts the prober sees the new value. The child sleeps a
// known interval; the test kills it on cleanup. This is a real-/proc
// test — no mocks. Skipped on non-linux GOOS via the build tag.
func TestLinuxProberFindsLiveID(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc not mounted; skipping")
	}

	const want = "probe-test-id-" + "f9c1b2d3"

	cmd := exec.Command("sleep", "10")
	cmd.Env = append(os.Environ(), probe.EnvKey+"="+want)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Give the kernel a moment to expose /proc/<pid>/environ.
	deadline := time.Now().Add(2 * time.Second)
	var got map[string]struct{}
	for {
		var err error
		got, err = probe.New().Probe(context.Background())
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if _, ok := got[want]; ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Probe did not find %q within 2s; pid=%s",
				want, strconv.Itoa(cmd.Process.Pid))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestLinuxProberIgnoresUnrelatedEnv proves the walker filters by the
// exact AGENT_DIRECTOR_INSTANCE_ID prefix. A child with a similar-
// looking-but-different env var must NOT show up in the probe set.
func TestLinuxProberIgnoresUnrelatedEnv(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc not mounted; skipping")
	}

	const unrelated = "probe-test-unrelated"
	const sentinel = "probe-test-positive-sentinel"

	cmd := exec.Command("sleep", "10")
	cmd.Env = append(os.Environ(),
		"AGENT_DIRECTOR_INSTANCE_IDX="+unrelated, // off-by-one suffix
		"AGENT_DIRECTOR_NOT_INSTANCE_ID="+unrelated,
		"AGENT_DIRECTOR_INSTANCE_ID="+sentinel, // matching prefix; positive control
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Poll until the kernel has exposed /proc/<pid>/environ for the
	// child (asserted by the positive-sentinel id appearing in the
	// probe's output). Without the poll, a vacuous "probe hasn't seen
	// anything yet" result would silently pass the negative check.
	deadline := time.Now().Add(2 * time.Second)
	var got map[string]struct{}
	for {
		var err error
		got, err = probe.New().Probe(context.Background())
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if _, ok := got[sentinel]; ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe never saw sentinel %q; child env not exposed?", sentinel)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := got[unrelated]; ok {
		t.Errorf("Probe wrongly returned %q for an off-prefix env var", unrelated)
	}
}
