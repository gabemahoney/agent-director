//go:build darwin

package probe_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// TestDarwinProberFindsLiveID is the darwin counterpart of the Linux
// /proc walker test. Same shape: spawn a child with the env var set,
// poll the prober until it shows up, kill the child on cleanup. The
// build tag ensures this only runs on the macOS CI lane.
func TestDarwinProberFindsLiveID(t *testing.T) {
	const want = "probe-darwin-id-" + "a7e3"

	cmd := exec.Command("sleep", "10")
	cmd.Env = append(os.Environ(), probe.EnvKey+"="+want)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := probe.New().Probe(context.Background())
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if _, ok := got[want]; ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Probe did not find %q within 3s", want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
