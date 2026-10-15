//go:build linux

package inputdelivery

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

func TestPrepareTimedOutProbeStopsLauncherChild(t *testing.T) {
	assertPrepareStopsProbeChild(t, "\"$PROBE_CHILD\"\n", 3*time.Second, false)
}

func TestPrepareExitedProbeStopsLauncherChild(t *testing.T) {
	for _, tc := range []struct {
		name      string
		redirect  string
		supported bool
	}{
		{name: "redirected stdout", redirect: " >/dev/null", supported: true},
		{name: "inherited stdout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launcher := "\"$PROBE_CHILD\"" + tc.redirect + " &\n" +
				"while [ ! -s \"$PROBE_CHILD_PID_FILE\" ]; do sleep 0.01; done\n" +
				"printf '%s\\n' '2.1.280 (Claude Code)'\n"
			assertPrepareStopsProbeChild(t, launcher, time.Second, tc.supported)
		})
	}
}

func assertPrepareStopsProbeChild(t *testing.T, launcher string, limit time.Duration, supported bool) {
	t.Helper()
	// Serial: launch provisioning resolves HOME, which this test isolates.
	root := t.TempDir()
	t.Setenv("HOME", root)
	pidFile := filepath.Join(root, "child.pid")
	child := filepath.Join(root, "child")
	wrapper := filepath.Join(root, "claude")
	for path, script := range map[string]string{
		child:   "#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$PROBE_CHILD_PID_FILE\"\nexec sleep 3600\n",
		wrapper: "#!/bin/sh\n" + launcher,
	} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			return
		}
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	})
	id := Identity{StoreID: "0123456789abcdef", Token: "fedcba9876543210", Agent: "claude"}
	env := map[string]string{
		"PROBE_CHILD": child, "PROBE_CHILD_PID_FILE": pidFile,
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "config"),
	}
	started := time.Now()
	command, _, err := Prepare(id, []string{wrapper}, env, true)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > limit {
		t.Fatalf("version probe took %v; want at most %v", elapsed, limit)
	}
	if gotSupported := len(command) > 1; gotSupported != supported {
		t.Fatalf("prepared command %q: native support=%v, want %v", command, gotSupported, supported)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("probe child pid=%q err=%v", data, err)
	}
	checker := probe.NewProcChecker()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		_, alive, known := checker.StartTime(pid)
		if known && !alive {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("version probe left launcher child %d alive=%v known=%v", pid, alive, known)
		}
	}
}
