package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestHookCLIWakeWritesItsPromptOnStderr (SR-3.4; Task t2.xis.oo.u8, repair
// round 1 F4): busy send's owned FileChanged wake through the built CLI, as a
// child of the row's recorded pane process (this test process), exits 2 with
// exactly the prompt on stderr and nothing on stdout: no diagnostic shares the
// stream Claude Code takes as the agent's input. The stderr the hook was given
// is left non-blocking, so the prompt's write ran under the route's write
// deadline and cannot outlive the process.
func TestHookCLIWakeWritesItsPromptOnStderr(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the hook reads its own identity from /proc")
	}
	h := newGateHome(t)
	const id, session = "id-gate-wake", "7d1e2f3a-4b5c-4d6e-8f70-91a2b3c4d5e6"
	db := stateDB(h.home)
	if _, err := apitest.SeedSpawn(db, id, store.StateWaiting, "", "off", session, true, withTestProcessPane(t)); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	wake := filepath.Join(h.home, "busy-send-wake")
	if err := apitest.SeedBusySendCoverage(db, id, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendRoute(db, id, string(store.BusySendRouteClaudeHooks), wake); err != nil {
		t.Fatal(err)
	}
	if _, err := openDBForRead(t, h.home).AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: id, Text: []byte("hello")}); err != nil {
		t.Fatalf("AdmitBusySend: %v", err)
	}
	payload, err := json.Marshal(map[string]string{"hook_event_name": "FileChanged", "session_id": session,
		"transcript_path": "/x/" + session + ".jsonl", "cwd": "/x", "file_path": wake, "event": "change"})
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := w.Fd() // Fd leaves the write end blocking; only the hook may change that.

	ctx, cancel := context.WithTimeout(context.Background(), gateHookDeadline)
	defer cancel()
	cmd := h.hookCmd(ctx, t, id, "", string(payload))
	var stdout strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, w
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("hook still running after %s", gateHookDeadline)
	}
	flags, flagsErr := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	stderr, readErr := io.ReadAll(r)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || stdout.Len() != 0 || readErr != nil ||
		string(stderr) != "[agent-director: message 1 of 1]\nhello" {
		t.Errorf("hook = %v, stdout %q, stderr %q (%v); want exit 2 with exactly the prompt on stderr", err, stdout.String(), stderr, readErr)
	}
	if flagsErr != nil || flags&unix.O_NONBLOCK == 0 {
		t.Errorf("the hook's stderr flags = %#x, %v; want O_NONBLOCK, so its write deadline applies", flags, flagsErr)
	}
	assertNoTmuxCall(t, h.home)
}
