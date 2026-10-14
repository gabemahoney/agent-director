//go:build linux

package realtmux_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"golang.org/x/sys/unix"
)

// A live ACP peer may stop draining input; termination must stay independently responsive.
func TestCursorBlockedWriteStopAndDeadline(t *testing.T) {
	for _, action := range []string{"write deadline", "local exit", "SIGTERM"} {
		t.Run(action, func(t *testing.T) {
			f, r, launch := newCursorPaneScenario(t, false, "blocked-input")
			sentinel := exec.Command("sleep", "30")
			if err := sentinel.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { sentinel.Process.Kill(); sentinel.Wait() }()
			id := launch.ID

			// Shrink the real empty ACP pipe before releasing session/new, so the
			// escaped initial prompt exceeds its capacity on every Linux host.
			st, err := store.Open(f.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			var binding store.CursorRuntime
			waitFor(t, "registered runtime", func() bool { binding, err = st.ReadCursorRuntime(id); return err == nil }, func() string { return fmt.Sprint(err) })
			st.Close()
			pipe, err := os.OpenFile(fmt.Sprintf("/proc/%d/fd/0", binding.RuntimePID), os.O_WRONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = unix.FcntlInt(pipe.Fd(), unix.F_SETPIPE_SZ, 4096)
			pipe.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(r.Release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "prompt admitted", func() bool { return readRow(t, f.DBPath, id).State == store.StateWorking }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			runtimePID := 0
			if _, err := fmt.Sscan(readFileTrim(t, r.PID), &runtimePID); err != nil {
				t.Fatal(err)
			}
			gatewayPID := int(launch.Row.PanePID.(int64))
			if pidGone(runtimePID) {
				t.Fatal("runtime was already dead before stalled write")
			}
			started := time.Now()
			switch action {
			case "local exit":
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: "/exit"}); err != nil {
					t.Fatal(err)
				}
			case "SIGTERM":
				if err := syscall.Kill(gatewayPID, syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "gateway exits after blocked write", func() bool { return pidGone(gatewayPID) }, func() string { return procState(gatewayPID) })
			if time.Since(started) > 8*time.Second {
				t.Fatalf("blocked transport delayed stop: %s", time.Since(started))
			}
			waitFor(t, "accepting runtime positively gone", func() bool { return pidGone(runtimePID) }, func() string { return procState(runtimePID) })
			if err := sentinel.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unrelated sentinel killed: %v", err)
			}
			row := readRow(t, f.DBPath, id)
			raw, err := os.ReadFile(row.JSONLPath.(string))
			if err != nil {
				t.Fatal(err)
			}
			if action == "write deadline" {
				failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", id)
				if len(failures) != 1 || !strings.Contains(fmt.Sprint(failures[0]["reason"]), "delivery is uncertain") {
					t.Fatalf("stalled write lacked uncertain-delivery failure: %v", failures)
				}
				if strings.Contains(string(raw), `"kind":"session_end"`) || row.State == store.StateEnded {
					t.Fatal("uncertain delivery claimed intentional end")
				}
			} else if row.State != store.StateEnded || !strings.Contains(string(raw), `"runtime_gone":true`) {
				t.Fatalf("intentional stop lacked confirmed end: %+v %s", row, raw)
			}
			for _, frame := range r.Frames(t) {
				if string(frame["method"]) == `"session/prompt"` {
					t.Fatal("nonreading peer unexpectedly consumed or replayed prompt")
				}
			}
		})
	}
}
