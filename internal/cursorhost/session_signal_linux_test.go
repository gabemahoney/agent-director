//go:build linux

package cursorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/trail"
	"golang.org/x/sys/unix"
)

func TestCursorSignalsRemainCapturedThroughTerminalRestore(t *testing.T) {
	// Serial: b.jf1 shutdown regression isolates actual SIGINT in a subprocess.
	if testing.Short() {
		t.Skip("isolated signal teardown regression")
	}
	if os.Getenv("AD_TEST_CURSOR_SIGNAL_TEARDOWN") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCursorSignalsRemainCapturedThroughTerminalRestore$", "-test.v")
		cmd.Env = append(os.Environ(), "AD_TEST_CURSOR_SIGNAL_TEARDOWN=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated gateway teardown: %v\n%s", err, out)
		}
		if !bytes.Contains(out, []byte("terminal restored; sending repeated SIGINT")) {
			t.Fatalf("restore boundary not exercised: %s", out)
		}
		return
	}

	starter := func(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
		restore := terminalLoop(in, out, emit, admit, choices...)
		return func() {
			restore()
			term, err := unix.IoctlGetTermios(int(in.Fd()), unix.TCGETS)
			if err != nil || term.Lflag&unix.ISIG == 0 {
				t.Errorf("terminal signal handling not restored: term=%+v err=%v", term, err)
			}
			// This blocks Run after real restore, before cleanup and failure emission.
			t.Log("terminal restored; sending repeated SIGINT")
			for n := 0; n < 3; n++ {
				if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
					t.Errorf("signal isolated gateway: %v", err)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
	c := beginCursorSessionWithInput(t, "", openTranscript, starter)
	c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	c.keys(t, "\xff")
	if err := c.awaitFailure(t, 5*time.Second); !strings.Contains(err.Error(), "Cursor pane input closed") {
		t.Fatalf("wrong shutdown failure: %v", err)
	}
	raw, err := os.ReadFile(trail.Path())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var record struct {
			Event    string `json:"event"`
			Instance string `json:"instance_id"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Event == "ad.cursor.failed" && record.Instance == c.e.binding.InstanceID {
			count++
			if !strings.Contains(record.Reason, "Cursor pane input closed") {
				t.Errorf("failure event lost cause: %+v", record)
			}
		}
	}
	if count != 1 {
		t.Fatalf("failure events after repeated teardown signals=%d want 1; trail=%s", count, raw)
	}
}
